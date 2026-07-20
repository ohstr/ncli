package client

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
)

// waitForEventCount polls an outbound destination's success-ack counter
// (FlatRow()[1], incremented by handleFlow's AddEvent on every non-duplicate
// ACK) until it reaches at least want, or fails the test after timeout.
func waitForEventCount(t *testing.T, stat tui.FlowStat, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if got := stat.FlatRow()[1]; got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("destination did not process %d events within %s (got %d)", want, timeout, stat.FlatRow()[1])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// newLocalSubscriptionTestFixture wires up a real relay.EventStore-backed
// destination the same way Stream.addFlow does in production: a
// StreamChannel, one source FlowContext and one destination FlowContext
// registered as a subscriber, and a LocalSubscription driven via
// ClientSubscriptionContext.Run (the same entry point Stream.Sync uses).
func newLocalSubscriptionTestFixture(t *testing.T, ctx context.Context, writeConcurrency int) (
	store *relay.EventStore, sc *StreamChannel, dstStat *tui.OutboundMetrics, dstFC *FlowContext, srcFC *FlowContext, ls *LocalSubscription,
) {
	t.Helper()

	var err error
	store, err = relay.NewEventStore(t.TempDir()+"/x.db", &nip11.Limitation{})
	if err != nil {
		t.Fatalf("failed to open event store: %v", err)
	}
	t.Cleanup(store.Close)

	sc = NewStreamChannel(1, nil)
	go sc.broadcastEvents(ctx)

	dstStat = tui.NewOutboundMetrics(1, "local-dest", func() {})
	dstFC = NewFlowContext(nip01.NewSubscriptionFilterGroup(), dstStat, true, nil)
	sc.addSubscriber(dstFC)

	ls = NewLocalSubscription(store, true, writeConcurrency)

	srcStat := tui.NewInboundMetrics(1, "src", func() {})
	srcFC = NewFlowContext(nip01.NewSubscriptionFilterGroup(), srcStat, true, nil)

	return store, sc, dstStat, dstFC, srcFC, ls
}

// TestLocalSubscriptionWriteConcurrentAgainstRealStore regression-tests the
// throughput fix itself (previously untested): a burst of events driven
// through LocalSubscription.Write's worker pool against a real bbolt-backed
// EventStore must all be ACKed and actually persisted, not just accepted
// into the channel.
func TestLocalSubscriptionWriteConcurrentAgainstRealStore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, sc, dstStat, dstFC, srcFC, ls := newLocalSubscriptionTestFixture(t, ctx, localWriteConcurrency)

	writeDone := make(chan struct{})
	go func() {
		ls.Run(ctx, ls.Write, sc, dstFC)
		close(writeDone)
	}()

	// Throughput here is gated by the store's batch-flush interval (100ms)
	// times however many of the writeConcurrency workers have a task in
	// flight per flush -- roughly localWriteConcurrency/100ms, hence the
	// generous timeout below for a few thousand events.
	const numEvents = 3000
	for i := 0; i < numEvents; i++ {
		sc.handleEvent(ctx, srcFC, newRegressionTestEvent(i))
	}

	waitForEventCount(t, dstStat, numEvents, 20*time.Second)

	// Sample a handful of IDs to confirm they were actually persisted, not
	// just ACKed.
	for _, i := range []int{0, 1234, 2999} {
		id := fmt.Sprintf("%064x", i)
		pes, err := store.FindEvents(ctx, &nip01.SubscriptionFilter{IDs: []string{id}})
		if err != nil {
			t.Fatalf("FindEvents(%s): %v", id, err)
		}
		if len(pes) != 1 {
			t.Errorf("event %s: expected exactly 1 persisted match, got %d", id, len(pes))
		}
	}

	cancel()
	select {
	case <-writeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Write did not return promptly after context cancellation")
	}
}

// TestLocalSubscriptionWriteDoesNotDeadlockOnStoreClose reproduces the
// confirmed deadlock: closing the store mid-burst forces some in-flight
// store.Execute calls down the <-s.closeCh branch, which calls task.Done()
// synchronously -- before the fix (unbuffered task.errors channel), this
// blocked the calling worker forever since nothing was listening yet.
func TestLocalSubscriptionWriteDoesNotDeadlockOnStoreClose(t *testing.T) {
	goroutinesBefore := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, sc, _, dstFC, srcFC, ls := newLocalSubscriptionTestFixture(t, ctx, localWriteConcurrency)

	writeDone := make(chan struct{})
	go func() {
		ls.Run(ctx, ls.Write, sc, dstFC)
		close(writeDone)
	}()

	go func() {
		for i := 0; i < 20000; i++ {
			sc.handleEvent(ctx, srcFC, newRegressionTestEvent(i))
		}
	}()

	time.Sleep(50 * time.Millisecond) // let a burst of Execute() calls get in flight
	store.Close()                     // deliberate trigger, not cleanup -- see comment above

	select {
	case <-writeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Write did not return promptly after the store closed mid-burst (deadlock)")
	}

	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if runtime.NumGoroutine() <= goroutinesBefore+1 { // small tolerance for runtime/GC bookkeeping
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("goroutine count did not return to baseline: before=%d after=%d", goroutinesBefore, runtime.NumGoroutine())
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestLocalSubscriptionWriteDoesNotDeadlockOnContextCancel is the
// ctx-cancellation variant of the same deadlock: cancelling ctx mid-burst
// (instead of closing the store) exercises writeOne's <-ctx.Done() escape
// case while other tasks may still be resolving in the store's background
// workers -- the abandoned-task path of the same underlying bug.
func TestLocalSubscriptionWriteDoesNotDeadlockOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, sc, _, dstFC, srcFC, ls := newLocalSubscriptionTestFixture(t, ctx, localWriteConcurrency)

	// Captured after the fixture (and its store's own runtime.NumCPU()
	// handleTasks + housekeeper goroutines) exists, since this test only
	// cancels ctx -- unlike the store-close variant, the store itself (and
	// its background workers) stays alive and in scope for the rest of the
	// test, so it shouldn't count as a leak here.
	goroutinesBefore := runtime.NumGoroutine()

	writeDone := make(chan struct{})
	go func() {
		ls.Run(ctx, ls.Write, sc, dstFC)
		close(writeDone)
	}()

	go func() {
		for i := 0; i < 20000; i++ {
			sc.handleEvent(ctx, srcFC, newRegressionTestEvent(i))
		}
	}()

	time.Sleep(50 * time.Millisecond)
	cancel() // deliberate trigger

	select {
	case <-writeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Write did not return promptly after context cancellation mid-burst (deadlock)")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if runtime.NumGoroutine() <= goroutinesBefore+1 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("goroutine count did not return to baseline: before=%d after=%d", goroutinesBefore, runtime.NumGoroutine())
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestLocalSubscriptionWriteOrdersDeletionAfterInFlightInserts regression
// tests the NIP-09 barrier: a target event followed promptly by a kind:5
// deletion referencing it, interleaved with enough concurrent filler traffic
// that reordering would occur without deleteBarrier (the store runs
// runtime.NumCPU() independent handleTasks goroutines pulling off one queue,
// so near-simultaneous submissions can land in different, independently
// committed transactions with no ordering between them).
func TestLocalSubscriptionWriteOrdersDeletionAfterInFlightInserts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, sc, dstStat, dstFC, srcFC, ls := newLocalSubscriptionTestFixture(t, ctx, localWriteConcurrency)

	writeDone := make(chan struct{})
	go func() {
		ls.Run(ctx, ls.Write, sc, dstFC)
		close(writeDone)
	}()

	pubkey := fmt.Sprintf("%064x", 1)
	targetID := fmt.Sprintf("%064x", 999999)
	target := &nip01.Event{
		ID: targetID, PubKey: pubkey, Kind: 1,
		CreatedAt: uint64(time.Now().Unix()), Tags: [][]string{}, Content: "target",
	}
	deletion := &nip01.Event{
		ID: fmt.Sprintf("%064x", 1000000), PubKey: pubkey, Kind: 5,
		CreatedAt: uint64(time.Now().Unix()) + 1, Tags: [][]string{{"e", targetID}},
	}

	sc.handleEvent(ctx, srcFC, target)
	sc.handleEvent(ctx, srcFC, deletion)

	const numFiller = 2000
	for i := 0; i < numFiller; i++ {
		sc.handleEvent(ctx, srcFC, newRegressionTestEvent(i))
	}

	waitForEventCount(t, dstStat, numFiller+2, 10*time.Second)

	pes, err := store.FindEvents(ctx, &nip01.SubscriptionFilter{IDs: []string{targetID}})
	if err != nil {
		t.Fatalf("FindEvents(%s): %v", targetID, err)
	}
	if len(pes) != 0 {
		t.Errorf("target event %s survived its own deletion: barrier not enforced (found %d matches)", targetID, len(pes))
	}

	cancel()
	select {
	case <-writeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Write did not return promptly after context cancellation")
	}
}
