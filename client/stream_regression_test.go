package client

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/wire"
)

func newRegressionTestEvent(i int) *nip01.Event {
	return &nip01.Event{
		ID:        fmt.Sprintf("%064x", i),
		PubKey:    fmt.Sprintf("%064x", i%37), // small pubkey cardinality, doesn't matter for these tests
		CreatedAt: uint64(time.Now().Unix()),
		Kind:      1,
		Tags:      [][]string{},
		Content:   "regression test event",
	}
}

// drainAndAck simulates a destination's write loop: it reads events handed
// to fc by broadcastEvents and acks each one back through fc.incoming, which
// is what handleFlow (already running via sc.handleFlow) correlates against
// fc.pending.
func drainAndAck(ctx context.Context, fc *FlowContext) {
	for {
		select {
		case ev := <-fc.readEvent():
			fc.receive(&wire.OkSubscriptionResponse{EventID: ev.ID, Accepted: true})
		case <-fc.closed():
			return
		case <-ctx.Done():
			return
		}
	}
}

func containsLogSubstring(logs [][]string, substr string) (string, bool) {
	for _, cols := range logs {
		joined := strings.Join(cols, " ")
		if strings.Contains(joined, substr) {
			return joined, true
		}
	}
	return "", false
}

// TestStreamNoFalseUnexpectedUnderBacklog reproduces the original bug
// scenario: a burst of events large enough to have exceeded the old shared,
// size-capped StreamEventHistory (maxEventHistorySize), delivered to one or
// two destinations that start draining only after the whole burst has been
// queued up. Under the old design this produced "unexpected event received"
// warnings for legitimate, successfully-written events once destinations
// caught up; the per-destination pending map must not have that problem,
// regardless of how many destinations are involved.
func TestStreamNoFalseUnexpectedUnderBacklog(t *testing.T) {
	const numEvents = maxEventHistorySize + 2000 // comfortably over the old global cap

	for _, numDest := range []int{1, 2} {
		numDest := numDest
		t.Run(fmt.Sprintf("%d_destination(s)", numDest), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sc := NewStreamChannel(2, nil) // sized as if there were 2 sources, matching the original repro
			go sc.broadcastEvents(ctx)

			destFCs := make([]*FlowContext, numDest)
			for i := 0; i < numDest; i++ {
				stat := tui.NewOutboundMetrics(i+1, fmt.Sprintf("dest-%d", i+1), func() {})
				fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, true, nil)
				sc.addSubscriber(fc)
				destFCs[i] = fc

				go sc.handleFlow(ctx, fc)
			}

			srcStat := tui.NewInboundMetrics(1, "src", func() {})
			srcFC := NewFlowContext(nip01.NewSubscriptionFilterGroup(), srcStat, true, nil)

			// Queue the whole burst before any destination starts draining,
			// so events sit pending far longer than the old 20000-entry cap
			// would have tolerated.
			for i := 0; i < numEvents; i++ {
				sc.handleEvent(ctx, srcFC, newRegressionTestEvent(i))
			}

			for _, fc := range destFCs {
				go drainAndAck(ctx, fc)
			}

			deadline := time.Now().Add(15 * time.Second)
			for _, fc := range destFCs {
				for {
					if fc.stat.FlatRow()[1] >= numEvents {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("timed out waiting for destination %s to process all %d events (got %d)",
							fc.stat.GetAttributes().Name, numEvents, fc.stat.FlatRow()[1])
					}
					time.Sleep(10 * time.Millisecond)
				}
			}

			if line, found := containsLogSubstring(sc.logger.GetLastLogs(), "unexpected"); found {
				t.Errorf("unexpected log line was recorded for legitimate events: %s", line)
			}
		})
	}
}

// TestStreamUnexpectedAckMessageIsFriendly forces the genuine fallback
// branch (an ACK for an event ID the destination never registered as
// pending, carrying relay-supplied text) and asserts the resulting warning
// is a readable, field-based message rather than a raw Go struct dump like
// "&{<id> true }". The message is deliberately non-empty: an
// accepted=true/message="" unmatched ack is a separate, intentionally
// silent case (see TestStreamEmptyMessageAckIsIgnored).
func TestStreamUnexpectedAckMessageIsFriendly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sc := NewStreamChannel(1, nil)
	stat := tui.NewOutboundMetrics(1, "dest", func() {})
	fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, true, nil)
	sc.addSubscriber(fc)

	go sc.handleFlow(ctx, fc)

	unknownID := fmt.Sprintf("%064x", 999999)
	fc.receive(&wire.OkSubscriptionResponse{EventID: unknownID, Accepted: true, Message: "some relay-supplied note"})

	deadline := time.Now().Add(2 * time.Second)
	var logs [][]string
	for time.Now().Before(deadline) {
		logs = sc.logger.GetLastLogs()
		if _, found := containsLogSubstring(logs, unknownID); found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	line, found := containsLogSubstring(logs, unknownID)
	if !found {
		t.Fatalf("expected a warning referencing event id %s, got logs: %v", unknownID, logs)
	}

	if strings.Contains(line, "&{") {
		t.Errorf("warning message still looks like a raw struct dump: %s", line)
	}
	if !strings.Contains(line, "accepted=") {
		t.Errorf("warning message doesn't include structured fields (accepted=...): %s", line)
	}
}

// TestStreamCloseDoesNotHangOnStuckDestination exercises the ctx.Done()
// escape in broadcastEvents/deliverToSubscriber: a destination that never
// drains its incomingEvents buffer must not prevent broadcastEvents from
// exiting promptly once the stream's context is cancelled, and must not
// leave any goroutines behind once it does.
func TestStreamCloseDoesNotHangOnStuckDestination(t *testing.T) {
	goroutinesBefore := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sc := NewStreamChannel(1, nil)
	stat := tui.NewOutboundMetrics(1, "stuck-dest", func() {})
	fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, true, nil)
	sc.addSubscriber(fc)
	// Intentionally never drain fc.incomingEvents or run handleFlow/drainAndAck.

	broadcastDone := make(chan struct{})
	go func() {
		sc.broadcastEvents(ctx)
		close(broadcastDone)
	}()

	srcStat := tui.NewInboundMetrics(1, "src", func() {})
	srcFC := NewFlowContext(nip01.NewSubscriptionFilterGroup(), srcStat, true, nil)

	// Fill the destination's buffer, then push one more to force
	// broadcastEvents into a blocked delivery attempt.
	go func() {
		for i := 0; i < streamFlowBufferSize+10; i++ {
			sc.handleEvent(ctx, srcFC, newRegressionTestEvent(i))
		}
	}()

	time.Sleep(200 * time.Millisecond) // let a delivery attempt actually get stuck
	cancel()

	select {
	case <-broadcastDone:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcastEvents did not exit promptly after context cancellation")
	}

	// Give the producer goroutine (which also only unblocks via ctx.Done())
	// a moment to actually unwind, then confirm nothing was left running.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if runtime.NumGoroutine() <= goroutinesBefore+1 { // small tolerance for runtime/GC bookkeeping
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("goroutine count did not return to baseline after Close: before=%d after=%d",
				goroutinesBefore, runtime.NumGoroutine())
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestNewStreamRecoveryAlwaysOn confirms recovery is constructed even when a
// spec has no Recovery block at all -- the opt-in `enabled` gate that used
// to make this nil (and silently drop rejected events) has been removed.
func TestNewStreamRecoveryAlwaysOn(t *testing.T) {
	filters := nip01.NewSubscriptionFilterGroup()
	spec := &StreamSpec{
		Recovery: nil,
		To: []*FlowSpec{
			{Type: FlOW_REMOTE, Relay: "wss://example.invalid/always-on-recovery-test", Trusted: true},
		},
		Filters: []*FilterSpec{{}},
	}
	spec.filters = filters

	s, err := NewStream(spec, false)
	if err != nil {
		t.Fatalf("NewStream failed: %v", err)
	}
	defer s.Close()

	if s.recovery == nil {
		t.Fatal("expected recovery to be enabled by default (no Recovery block), got nil")
	}
}

// TestDefaultRecoveryStorePathStable checks that the auto-derived recovery
// store path is stable across separate NewStream calls sharing the same
// destination topology (so a restart actually reuses -- and can replay --
// the same store), and differs when the topology differs.
func TestDefaultRecoveryStorePathStable(t *testing.T) {
	specA := &StreamSpec{
		To: []*FlowSpec{{Type: FlOW_REMOTE, Relay: "wss://relay-a.example"}},
	}
	specB := &StreamSpec{
		To: []*FlowSpec{{Type: FlOW_REMOTE, Relay: "wss://relay-a.example"}},
	}
	specC := &StreamSpec{
		To: []*FlowSpec{{Type: FlOW_REMOTE, Relay: "wss://relay-c.example"}},
	}

	pathA1 := defaultRecoveryStorePath(specA)
	pathA2 := defaultRecoveryStorePath(specA)
	pathB := defaultRecoveryStorePath(specB)
	pathC := defaultRecoveryStorePath(specC)

	if pathA1 != pathA2 {
		t.Errorf("path for the same spec instance should be stable across calls: %s vs %s", pathA1, pathA2)
	}
	if pathA1 != pathB {
		t.Errorf("path should be stable across separate specs with identical destination topology: %s vs %s", pathA1, pathB)
	}
	if pathA1 == pathC {
		t.Errorf("path should differ for a different destination topology, got the same for both: %s", pathA1)
	}
}

// TestFlowContextPendingDoesNotCrossContaminate is a focused unit check
// (without the volume of TestStreamNoFalseUnexpectedUnderBacklog) that two
// destinations acking the same event ID independently doesn't cause either
// one to hit the "unexpected" fallback -- the bug a naive shared
// "delete on first ack" fix would have reintroduced.
func TestFlowContextPendingDoesNotCrossContaminate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sc := NewStreamChannel(1, nil)

	stat1 := tui.NewOutboundMetrics(1, "dest-1", func() {})
	fc1 := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat1, true, nil)
	stat2 := tui.NewOutboundMetrics(2, "dest-2", func() {})
	fc2 := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat2, true, nil)

	sc.addSubscriber(fc1)
	sc.addSubscriber(fc2)

	go sc.broadcastEvents(ctx)
	go sc.handleFlow(ctx, fc1)
	go sc.handleFlow(ctx, fc2)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); drainAndAck(ctx, fc1) }()
	go func() { defer wg.Done(); drainAndAck(ctx, fc2) }()

	srcStat := tui.NewInboundMetrics(1, "src", func() {})
	srcFC := NewFlowContext(nip01.NewSubscriptionFilterGroup(), srcStat, true, nil)

	ev := newRegressionTestEvent(1)
	sc.handleEvent(ctx, srcFC, ev)

	deadline := time.Now().Add(2 * time.Second)
	for fc1.stat.FlatRow()[1] < 1 || fc2.stat.FlatRow()[1] < 1 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for both destinations to ack the shared event")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if line, found := containsLogSubstring(sc.logger.GetLastLogs(), "unexpected"); found {
		t.Errorf("cross-destination false positive: %s", line)
	}
}

// TestOutboundMetricsShowsSynced guards against the exact confusion a user
// hit in practice: re-running a stream against a destination that's already
// fully synced makes every event land as a duplicate ack, so
// Events/Failures/Retries all correctly stay at zero -- but if "Synced"
// (the destination's "other side already had this" counter) were hidden
// from destination rows, the panel would look identical to a stuck/broken
// destination. This asserts the Synced counter is visible and increments in
// exactly that scenario.
func TestOutboundMetricsShowsSynced(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sc := NewStreamChannel(1, nil)
	stat := tui.NewOutboundMetrics(1, "dest", func() {})
	fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, true, nil)
	sc.addSubscriber(fc)
	go sc.handleFlow(ctx, fc)

	ev := newRegressionTestEvent(1)
	fc.pending.add(ev)
	fc.receive(&wire.OkSubscriptionResponse{
		EventID:  ev.ID,
		Accepted: true,
		Message:  "duplicate: already have this event",
	})

	const syncedIndex = 3 // [id, events, failures, synced, retries, age] (Pubkeys/Kinds skipped on destinations)
	deadline := time.Now().Add(2 * time.Second)
	for stat.FlatRow()[syncedIndex] < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for Synced to increment, row=%v", stat.FlatRow())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if events := stat.FlatRow()[1]; events != 0 {
		t.Errorf("a duplicate ack should not count as a delivered event, got Events=%d", events)
	}

	found := false
	for _, header := range stat.Columns() {
		if header.Name == "Synced" {
			found = true
		}
	}
	if !found {
		t.Error("expected \"Synced\" to be a visible column on destination (OutboundMetrics) rows")
	}
}

// TestStreamEmptyMessageAckIsIgnored covers the fallback branch for an
// unmatched ack (no pending entry, not the "duplicate:"-prefixed message
// case) whose message is empty -- observed in practice as a relay
// re-sending (or sending late) an OK for an event this destination already
// moved past. Per-ID tracking to distinguish "already resolved" from
// "never seen" turned out not to be the right fix (it never caught cases
// where the ack races ahead of pending registration); the simpler, blunter
// rule is applied instead: accepted=true with no message is never worth a
// warning, matched-and-tracked or not.
func TestStreamEmptyMessageAckIsIgnored(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sc := NewStreamChannel(1, nil)
	stat := tui.NewOutboundMetrics(1, "dest", func() {})
	fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, true, nil)
	sc.addSubscriber(fc)
	go sc.handleFlow(ctx, fc)

	unknownID := fmt.Sprintf("%064x", 999999)
	fc.receive(&wire.OkSubscriptionResponse{EventID: unknownID, Accepted: true}) // no Message set: ""

	// There's no positive signal to wait on for "nothing happened", so give
	// handleFlow a moment to process it before asserting.
	time.Sleep(200 * time.Millisecond)

	if logs := sc.logger.GetLastLogs(); len(logs) != 0 {
		t.Errorf("an accepted=true, message=\"\" ack should produce no log output at all, got: %v", logs)
	}
}

// TestStreamEphemeralAckIsIgnored covers the same unmatched-ack fallback as
// TestStreamEmptyMessageAckIsIgnored, but for relays that explain themselves
// instead of leaving Message empty: an accepted=true ack for an ephemeral
// event (kind 20000-29999) commonly arrives with a message like "ephemeral:
// will not be stored" once the event has already been broadcast and dropped
// from the relay's storage path -- not a correlation failure, so it must not
// be logged as an unexpected ack either.
func TestStreamEphemeralAckIsIgnored(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sc := NewStreamChannel(1, nil)
	stat := tui.NewOutboundMetrics(1, "dest", func() {})
	fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, true, nil)
	sc.addSubscriber(fc)
	go sc.handleFlow(ctx, fc)

	unknownID := fmt.Sprintf("%064x", 999999)
	fc.receive(&wire.OkSubscriptionResponse{EventID: unknownID, Accepted: true, Message: "ephemeral: will not be stored"})

	// There's no positive signal to wait on for "nothing happened", so give
	// handleFlow a moment to process it before asserting.
	time.Sleep(200 * time.Millisecond)

	if logs := sc.logger.GetLastLogs(); len(logs) != 0 {
		t.Errorf("an accepted=true, message=\"ephemeral: ...\" ack should produce no log output at all, got: %v", logs)
	}
}
