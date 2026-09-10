package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/wire"
)

// rateLimitedRelayStats tracks how a mockRateLimitedRelay resolved each
// EVENT frame it received.
type rateLimitedRelayStats struct {
	accepted int32 // atomic
	rejected int32 // atomic
	inFlight int32 // atomic, current concurrent "processing" count
}

func (s *rateLimitedRelayStats) Accepted() int { return int(atomic.LoadInt32(&s.accepted)) }
func (s *rateLimitedRelayStats) Rejected() int { return int(atomic.LoadInt32(&s.rejected)) }

// newRateLimitedMockRelay simulates nmilat's storeLimiter (relay/session.go):
// up to maxConcurrent EVENT submissions may be "processing" (in flight,
// unacked) at once; a frame arriving while that many are already outstanding
// is rejected immediately with the exact NIP-01 `rate-limited:` prefix
// nmilat itself sends (relay/session.go's ErrRateLimited -> packet.go's
// OkSubscriptionResponse{Accepted:false}), matching client/stream.go's own
// isDuplicatedEvent/isEphemeralAck convention of checking a relay-supplied
// message prefix.
//
// processDelay stands in for a real store commit: it must be long enough
// that a fast burst of sends actually overlaps in flight server-side --
// an instantly-acking mock would never observe any concurrency at all,
// since each frame would resolve before the next one arrives.
//
// Each received frame is dispatched to its own goroutine (mirroring
// nmilat's executeStoreTask: acquire-or-reject, then process
// asynchronously) so the read loop never blocks on processing and keeps
// accepting new frames while earlier ones are still outstanding -- exactly
// the condition an unpaced client-side burst is reported to trigger.
func newRateLimitedMockRelay(t *testing.T, maxConcurrent int, processDelay time.Duration) (*httptest.Server, *rateLimitedRelayStats) {
	t.Helper()
	stats := &rateLimitedRelayStats{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		var writeMu sync.Mutex
		safeWrite := func(msg string) {
			writeMu.Lock()
			defer writeMu.Unlock()
			_ = conn.WriteMessage(websocket.TextMessage, []byte(msg))
		}

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}

			id := extractIDField(string(msg))
			if id == "" {
				continue
			}

			current := atomic.AddInt32(&stats.inFlight, 1)
			if current > int32(maxConcurrent) {
				atomic.AddInt32(&stats.inFlight, -1)
				atomic.AddInt32(&stats.rejected, 1)
				safeWrite(fmt.Sprintf(`["OK", "%s", false, "rate-limited: too many concurrent tasks"]`, id))
				continue
			}

			go func(id string) {
				time.Sleep(processDelay)
				atomic.AddInt32(&stats.inFlight, -1)
				atomic.AddInt32(&stats.accepted, 1)
				safeWrite(fmt.Sprintf(`["OK", "%s", true, ""]`, id))
			}(id)
		}
	}))

	t.Cleanup(server.Close)
	return server, stats
}

// extractIDField is the same naive "id":"..." scan used elsewhere in this
// package's tests (recovery_test.go, integration_test.go) -- fragile JSON
// parsing, but sufficient for a controlled test fixture that only ever
// sends well-formed EVENT frames.
func extractIDField(s string) string {
	target := "\"id\":\""
	idStart := -1
	for i := 0; i < len(s)-len(target); i++ {
		if s[i:i+len(target)] == target {
			idStart = i + len(target)
			break
		}
	}
	if idStart == -1 {
		return ""
	}
	idEnd := idStart
	for idEnd < len(s) && s[idEnd] != '"' {
		idEnd++
	}
	return s[idStart:idEnd]
}

// newPublishConcurrencyTestStream wires a real Stream (whitebox, same
// pattern as TestRateLimitRecovery in integration_test.go) with a single
// FlOW_REMOTE destination pointed at relayURL, and returns the destination's
// FlowContext for direct event injection plus its stat for progress
// polling.
func newPublishConcurrencyTestStream(t *testing.T, ctx context.Context, relayURL string, writeConcurrency int) (*Stream, tui.FlowStat, *FlowContext) {
	t.Helper()

	u, err := url.Parse(relayURL)
	if err != nil {
		t.Fatalf("invalid relay URL %s: %v", relayURL, err)
	}

	spec := &StreamSpec{
		To: []*FlowSpec{
			{Type: FlOW_REMOTE, Relay: relayURL, Trusted: true, WriteConcurrency: writeConcurrency, relayURI: u},
		},
		Filters: []*FilterSpec{{}},
	}
	spec.filters = nip01.NewSubscriptionFilterGroup()

	stream, err := NewStream(spec, false)
	if err != nil {
		t.Fatalf("NewStream failed: %v", err)
	}
	t.Cleanup(stream.Close)

	upStats, _ := stream.Sync(ctx)
	if len(upStats) != 1 {
		t.Fatalf("expected 1 destination flow, got %d", len(upStats))
	}

	deadline := time.Now().Add(2 * time.Second)
	var dstFC *FlowContext
	for dstFC == nil {
		stream.sc.subscribersMu.RLock()
		for _, fc := range stream.sc.subscribers {
			dstFC = fc
		}
		stream.sc.subscribersMu.RUnlock()
		if dstFC != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("destination flow context never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}

	return stream, upStats[0], dstFC
}

// injectBurst pushes numEvents synthetic events into the stream as fast as
// this goroutine can loop, simulating apply stream's reported fan-out
// pattern: many events arriving well within the destination relay's own
// processing window, with nothing pacing how fast they're handed to the
// destination.
func injectBurst(ctx context.Context, sc *StreamChannel, numEvents int, idOffset int) {
	srcStat := tui.NewInboundMetrics(1, "src", func() {})
	srcFC := NewFlowContext(nip01.NewSubscriptionFilterGroup(), srcStat, true, nil)
	for i := 0; i < numEvents; i++ {
		sc.handleEvent(ctx, srcFC, newRegressionTestEvent(idOffset+i))
	}
}

// waitForResolvedCount polls a destination stat until events+failures
// (accepted+rejected) reaches want, or fails the test after timeout.
func waitForResolvedCount(t *testing.T, stat tui.FlowStat, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		row := stat.FlatRow()
		if row[1]+row[2] >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("destination did not resolve %d events within %s (events=%d failures=%d)", want, timeout, row[1], row[2])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRemoteDestinationBurstWithoutCapHitsRateLimiter reproduces the exact
// symptom in ncli/issue.md ("apply stream has no backfill-aware pacing --
// initial sync reliably rate-limits the destination"): with no
// WriteConcurrency configured (0 = unbounded, the only option before this
// fix), a burst of events comfortably exceeds a modestly-sized destination's
// concurrency guard and gets rejected with the relay's own `rate-limited:`
// text -- not because the events are individually invalid, purely because
// nothing paces how many are outstanding at once.
func TestRemoteDestinationBurstWithoutCapHitsRateLimiter(t *testing.T) {
	const maxConcurrent = 10
	const numEvents = 200

	server, stats := newRateLimitedMockRelay(t, maxConcurrent, 150*time.Millisecond)
	relayURL := mockRelayWSURL(server)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, dstStat, _ := newPublishConcurrencyTestStream(t, ctx, relayURL, 0 /* unbounded, today's only behavior */)

	injectBurst(ctx, stream.sc, numEvents, 0)
	waitForResolvedCount(t, dstStat, numEvents, 10*time.Second)

	if stats.Rejected() == 0 {
		t.Fatalf("expected the unpaced burst to exceed the destination's concurrency guard (max=%d) and produce rate-limit rejections -- got 0 rejected, %d accepted; the reported incident did not reproduce", maxConcurrent, stats.Accepted())
	}
	t.Logf("unpaced burst against maxConcurrent=%d: accepted=%d rejected=%d", maxConcurrent, stats.Accepted(), stats.Rejected())
}

// TestRemoteDestinationPublishConcurrencyCapPreventsRateLimiting is the fix
// side of the same scenario: with WriteConcurrency set below the
// destination's own concurrency guard, the exact same burst against the
// exact same mock relay must produce zero rate-limit rejections, because
// the client now never has more than writeConcurrency events outstanding to
// this destination at once.
func TestRemoteDestinationPublishConcurrencyCapPreventsRateLimiting(t *testing.T) {
	const maxConcurrent = 10
	const writeConcurrency = 6 // comfortably under maxConcurrent
	const numEvents = 200

	server, stats := newRateLimitedMockRelay(t, maxConcurrent, 150*time.Millisecond)
	relayURL := mockRelayWSURL(server)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, dstStat, _ := newPublishConcurrencyTestStream(t, ctx, relayURL, writeConcurrency)

	injectBurst(ctx, stream.sc, numEvents, 0)
	waitForResolvedCount(t, dstStat, numEvents, 15*time.Second)

	if stats.Rejected() != 0 {
		t.Errorf("expected the %d-cap to keep the destination under its %d-concurrent guard entirely, got %d rejections (accepted=%d)",
			writeConcurrency, maxConcurrent, stats.Rejected(), stats.Accepted())
	}
	if stats.Accepted() != numEvents {
		t.Errorf("expected all %d events to eventually be accepted, got %d", numEvents, stats.Accepted())
	}
}

// TestFlowContextInFlightSlotAcquireReleaseCycle is a fast, network-free unit
// test of the semaphore mechanics themselves: a bounded FlowContext's
// in-flight slot enforces its capacity, and handleFlow's processing of a
// genuinely correlated ack (found in fc.dispatched) releases it.
func TestFlowContextInFlightSlotAcquireReleaseCycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sc := NewStreamChannel(1, nil)
	stat := tui.NewOutboundMetrics(1, "dest", func() {})
	fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, true, nil)
	fc.setPublishConcurrency(1)

	// handleFlow itself calls open() on entry (every (re)connect cycle
	// resets pauseCh/inFlight together -- see open()'s doc comment); it must
	// be the only caller here; a second, separate open() call from the test
	// would replace fc.inFlight with a fresh, different channel out from
	// under it, which is exactly the "recreated per cycle" behavior open()
	// is documented to have, not a bug -- but it means this test must read
	// inFlightSlots() only after handleFlow's own open() has already run.
	go sc.handleFlow(ctx, fc)

	var sem chan struct{}
	deadline := time.Now().Add(2 * time.Second)
	for sem == nil {
		sem = fc.inFlightSlots()
		if time.Now().After(deadline) {
			t.Fatal("handleFlow never materialized an in-flight semaphore")
		}
	}
	if cap(sem) != 1 {
		t.Fatalf("expected capacity 1, got %d", cap(sem))
	}

	select {
	case sem <- struct{}{}:
	default:
		t.Fatal("expected to acquire the single free slot")
	}

	acquired := make(chan struct{})
	go func() {
		sem <- struct{}{} // must block until the release below
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("second acquire succeeded before any release -- capacity not enforced")
	case <-time.After(100 * time.Millisecond):
	}

	ev := newRegressionTestEvent(1)
	fc.pending.add(ev)
	fc.dispatched.add(ev.ID) // Write's real invariant: only add here once a slot is actually held
	fc.receive(&wire.OkSubscriptionResponse{EventID: ev.ID, Accepted: true})

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire did not unblock after handleFlow processed the matching ack -- slot never released")
	}
}

// TestFlowContextUnmatchedAckDoesNotDeadlockSlotRelease guards the hazard
// releaseSlot's doc comment calls out explicitly: a naive unconditional
// release on every ack (rather than gating on fc.pending actually having
// tracked it) would call <-sem for an unmatched ack too. With another send
// legitimately still holding the only slot, that would either free a slot
// that isn't actually free yet (corrupting the count -- a second send could
// start while the first is still outstanding, exceeding the destination's
// configured cap) or, in the general case, block forever on an empty
// channel if nothing were held at all. This proves neither happens: an
// unmatched ack leaves a genuinely-held slot untouched, and handleFlow
// keeps making progress afterward, correctly releasing it once its real,
// correlated ack arrives.
func TestFlowContextUnmatchedAckDoesNotDeadlockSlotRelease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sc := NewStreamChannel(1, nil)
	stat := tui.NewOutboundMetrics(1, "dest", func() {})
	fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, true, nil)
	fc.setPublishConcurrency(1)

	// handleFlow calls open() itself on entry -- see the sibling test's
	// comment for why this test must not also call it directly.
	go sc.handleFlow(ctx, fc)

	var sem chan struct{}
	deadline := time.Now().Add(2 * time.Second)
	for sem == nil {
		sem = fc.inFlightSlots()
		if time.Now().After(deadline) {
			t.Fatal("handleFlow never materialized an in-flight semaphore")
		}
	}

	// Simulate RemoteSubscription.Write having acquired the (only) slot for
	// a genuine send -- production always holds this invariant: an event
	// only ever enters fc.dispatched with a slot already held for it (Write
	// acquires the slot, then conn.Send succeeds, then dispatched.add).
	// Skipping this step is exactly what made an earlier version of this
	// test release a slot nothing had acquired, hanging on the empty
	// channel itself -- a test bug, not a reason to weaken the invariant
	// being asserted here.
	select {
	case sem <- struct{}{}:
	default:
		t.Fatal("expected to acquire the single free slot")
	}
	ev := newRegressionTestEvent(1)
	fc.pending.add(ev)
	fc.dispatched.add(ev.ID)

	// Unmatched: no pending entry was ever registered for this other ID --
	// the same shape as a relay re-sending/late-sending an OK for something
	// this destination already moved past.
	unknownID := fmt.Sprintf("%064x", 999999)
	fc.receive(&wire.OkSubscriptionResponse{EventID: unknownID, Accepted: true})

	// No positive signal to wait on for "nothing happened" -- give handleFlow
	// a moment to actually process the unmatched ack above.
	time.Sleep(200 * time.Millisecond)

	// ev's slot must still be exactly held: a second acquire must still
	// block, proving the unmatched ack above didn't free it.
	acquired := make(chan struct{})
	go func() {
		sem <- struct{}{}
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatal("slot was released by the unmatched ack -- it belongs to ev's still-outstanding send")
	case <-time.After(100 * time.Millisecond):
	}

	// ev's own, genuinely correlated ack: this must release it, and
	// handleFlow must still be responsive enough to do so.
	fc.receive(&wire.OkSubscriptionResponse{EventID: ev.ID, Accepted: true})

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("handleFlow appears stuck, or ev's slot was never released")
	}
}

// TestFlowContextInFlightResetsOnReconnectCycle guards open()'s
// fresh-channel-per-cycle design: a slot acquired on one (re)connect cycle
// but never acked (the connection died first) must not carry over and
// starve the next cycle's sends.
func TestFlowContextInFlightResetsOnReconnectCycle(t *testing.T) {
	stat := tui.NewOutboundMetrics(1, "dest", func() {})
	fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, true, nil)
	fc.setPublishConcurrency(1)

	fc.open() // cycle 1
	sem1 := fc.inFlightSlots()
	select {
	case sem1 <- struct{}{}: // simulate an in-flight send that will never be acked
	default:
		t.Fatal("expected to acquire cycle 1's free slot")
	}

	fc.open() // cycle 2 (reconnect)
	sem2 := fc.inFlightSlots()

	if sem1 == sem2 {
		t.Fatal("expected a fresh semaphore instance on reconnect, got the same one")
	}
	select {
	case sem2 <- struct{}{}:
	default:
		t.Fatal("new cycle's semaphore should start fully available, not inherit the old cycle's exhausted slot")
	}
}

// TestFlowContextInFlightNilWhenUnbounded confirms the default (no
// WriteConcurrency configured) stays exactly as unbounded as before this
// feature existed: inFlightSlots must be nil so RemoteSubscription.Write's
// acquire is skipped entirely, not a very-large-but-finite cap.
func TestFlowContextInFlightNilWhenUnbounded(t *testing.T) {
	stat := tui.NewOutboundMetrics(1, "dest", func() {})
	fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, true, nil)
	fc.open() // publishConcurrency left at its zero value (0)

	if sem := fc.inFlightSlots(); sem != nil {
		t.Fatalf("expected a nil semaphore when publishConcurrency is 0, got one with capacity %d", cap(sem))
	}
}
