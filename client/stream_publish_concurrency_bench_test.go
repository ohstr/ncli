package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
)

// newFastAcceptingMockRelay is a minimal, always-accept relay for throughput
// benchmarking: no artificial delay, no rejection, just ack every EVENT
// frame as fast as it can be read and written back. Deliberately does not
// depend on the mockRateLimitedRelay/newMockRelay test helpers (both tied to
// *testing.T) so this file can be benchmarked with `go test -bench` (*testing.B)
// on its own.
func newFastAcceptingMockRelay(tb testing.TB) *httptest.Server {
	tb.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			// extractIDField lives in stream_publish_concurrency_test.go,
			// same package -- kept in one place now that both files are
			// permanent; only needed its own throwaway copy transiently
			// while A/B-benchmarking this one file against pre-fix
			// production code with the other test file moved aside.
			id := extractIDField(string(msg))
			if id == "" {
				continue
			}
			_ = conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`["OK", "%s", true, ""]`, id)))
		}
	}))
	tb.Cleanup(server.Close)
	return server
}

// benchRemoteWriteThroughput wires a real Stream (NewStream/Sync, the exact
// same production entry point apply stream uses) with one FlOW_REMOTE
// destination at writeConcurrency, against a fast-accepting mock relay, and
// times how long it takes to push+ack b.N events through it end to end.
// Deliberately built entirely from pre-existing public Stream/FlowSpec
// surface (no direct calls into FlowContext's new inFlightSlots/
// setPublishConcurrency internals) so the exact same benchmark body can run
// against the pre-fix code too, for a true apples-to-apples comparison --
// see communication.md/PR description for the before/after numbers this
// produced.
func benchRemoteWriteThroughput(b *testing.B, writeConcurrency int) {
	server := newFastAcceptingMockRelay(b)
	relayURL := mockRelayWSURL(server)

	u, err := url.Parse(relayURL)
	if err != nil {
		b.Fatalf("invalid relay URL %s: %v", relayURL, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spec := &StreamSpec{
		To: []*FlowSpec{
			{Type: FlOW_REMOTE, Relay: relayURL, Trusted: true, WriteConcurrency: writeConcurrency, relayURI: u},
		},
		Filters: []*FilterSpec{{}},
	}
	spec.filters = nip01.NewSubscriptionFilterGroup()

	stream, err := NewStream(spec, false)
	if err != nil {
		b.Fatalf("NewStream failed: %v", err)
	}
	defer stream.Close()

	upStats, _ := stream.Sync(ctx)
	if len(upStats) != 1 {
		b.Fatalf("expected 1 destination flow, got %d", len(upStats))
	}
	dstStat := upStats[0]

	// Wait for the destination to actually be registered as a subscriber
	// before timing starts, same as newPublishConcurrencyTestStream's
	// fixture -- otherwise b.N=1 runs (Go's initial calibration pass) could
	// spend their whole budget just waiting for the websocket handshake.
	deadline := time.Now().Add(2 * time.Second)
	for {
		n := 0
		stream.sc.subscribersMu.RLock()
		n = len(stream.sc.subscribers)
		stream.sc.subscribersMu.RUnlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			b.Fatal("destination flow context never registered")
		}
		time.Sleep(2 * time.Millisecond)
	}

	srcStat := tui.NewInboundMetrics(1, "src", func() {})
	srcFC := NewFlowContext(nip01.NewSubscriptionFilterGroup(), srcStat, true, nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stream.sc.handleEvent(ctx, srcFC, newRegressionTestEvent(i))
	}

	resolveDeadline := time.Now().Add(60 * time.Second)
	for {
		row := dstStat.FlatRow()
		if row[1]+row[2] >= b.N {
			break
		}
		if time.Now().After(resolveDeadline) {
			b.Fatalf("destination never resolved all %d events (resolved=%d)", b.N, row[1]+row[2])
		}
		time.Sleep(time.Millisecond)
	}
	b.StopTimer()
}

// BenchmarkRemoteSubscriptionWriteUnbounded is the regression check itself:
// throughput of apply stream's remote-destination write path with no
// WriteConcurrency configured -- the default, unchanged-since-before-this-
// feature code path every existing stream still runs today. Compare ns/op
// and allocs/op here against the same benchmark run on the pre-fix
// commit -- any regression in the common case would show up directly as a
// higher number here, not as reasoning about what a mutex "should" cost.
func BenchmarkRemoteSubscriptionWriteUnbounded(b *testing.B) {
	benchRemoteWriteThroughput(b, 0)
}

// BenchmarkRemoteSubscriptionWriteCapped is the same throughput measurement
// with a cap configured, quantifying the intentional pacing tradeoff for
// anyone who opts in -- not a regression check (there's no "before" for an
// option that didn't previously apply to remote destinations at all).
func BenchmarkRemoteSubscriptionWriteCapped(b *testing.B) {
	benchRemoteWriteThroughput(b, 16)
}

// BenchmarkFlowContextInFlightSlotsUnbounded isolates the one per-event cost
// RemoteSubscription.Write's hot loop unconditionally pays now, even when
// unbounded: a single inFlightSlots() call (pauseMu.RLock/RUnlock + a nil
// check). The end-to-end throughput benchmarks above go through a real
// websocket round trip per event, which -- as actually measured here across
// repeated runs, before and after this feature -- varies by tens of
// microseconds run to run purely from network/scheduling/GC noise, far
// larger than this call could plausibly cost; that noise floor makes them
// unable to resolve a change this small. This benchmark has no I/O and no
// allocation at all, so it directly measures the added cost in isolation
// instead of arguing from what an uncontended RWMutex "should" cost.
func BenchmarkFlowContextInFlightSlotsUnbounded(b *testing.B) {
	stat := tui.NewOutboundMetrics(1, "dest", func() {})
	fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, true, nil)
	// publishConcurrency left at 0 -- the default, unbounded case every
	// existing stream runs today.
	fc.open()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if sem := fc.inFlightSlots(); sem != nil {
			b.Fatal("expected nil semaphore when unbounded")
		}
	}
}

// BenchmarkFlowContextInFlightSlotsBounded is the same isolated call with a
// cap configured, for comparison -- the extra cost here (over the unbounded
// case above) is purely the actual channel send/receive pair anyone who
// opts into pacing accepts deliberately, not incidental overhead.
func BenchmarkFlowContextInFlightSlotsBounded(b *testing.B) {
	stat := tui.NewOutboundMetrics(1, "dest", func() {})
	fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, true, nil)
	fc.setPublishConcurrency(1)
	fc.open()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sem := fc.inFlightSlots()
		sem <- struct{}{}
		<-sem
	}
}
