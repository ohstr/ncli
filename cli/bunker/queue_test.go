package bunker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueue_AddResolve(t *testing.T) {
	q := NewQueue(0, 0)

	done := make(chan Decision, 1)
	go func() {
		verdict, err := q.Add(Pending{ID: "req-1", ClientKey: "app1", Method: "sign_event", Kind: 1})
		if err != nil {
			t.Errorf("Add() error = %v", err)
		}
		done <- verdict
	}()

	// Wait for it to actually be enqueued before resolving.
	waitFor(t, func() bool { return len(q.List()) == 1 })

	if err := q.Resolve("req-1", Allow, false); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	select {
	case v := <-done:
		if v != Allow {
			t.Errorf("resolved verdict = %v, want Allow", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Add() never returned")
	}
}

// TestQueue_OnAdded_FiresOnceOnAdd guards the callback board.go's
// PendingTable/ipc_server.go rely on for live-update pushes: it must fire
// exactly once, with the enqueued Pending, from Add -- not from the
// duplicate-ID fast path (a resent request must not look like a second
// new arrival).
func TestQueue_OnAdded_FiresOnceOnAdd(t *testing.T) {
	q := NewQueue(0, 0)
	var got []Pending
	var mu sync.Mutex
	q.OnAdded(func(p Pending) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, p)
	})

	go q.Add(Pending{ID: "req-1", ClientKey: "app1", Method: "ping"})
	waitFor(t, func() bool { return len(q.List()) == 1 })

	// A duplicate Add for the same ID must not fire OnAdded again.
	go q.Add(Pending{ID: "req-1", ClientKey: "app1", Method: "ping"})
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].ID != "req-1" {
		t.Errorf("OnAdded calls = %+v, want exactly one for req-1", got)
	}
}

// TestQueue_OnResolved_FiresFromResolve guards the fix this hook exists
// for (daemon.go's recordHistory, board.go's HistoryTable): a human
// decision must deliver a ResolvedEvent carrying the verdict and the
// remembered flag exactly as passed to Resolve, with Expired false.
func TestQueue_OnResolved_FiresFromResolve(t *testing.T) {
	q := NewQueue(0, 0)
	var got *ResolvedEvent
	q.OnResolved(func(ev ResolvedEvent) { got = &ev })

	go q.Add(Pending{ID: "req-1", ClientKey: "app1", Method: "sign_event", Kind: 1})
	waitFor(t, func() bool { return len(q.List()) == 1 })

	if err := q.Resolve("req-1", Deny, true); err != nil {
		t.Fatal(err)
	}

	if got == nil {
		t.Fatal("OnResolved never fired")
	}
	if got.Pending.ID != "req-1" || got.Verdict != Deny || !got.Remembered || got.Expired {
		t.Errorf("ResolvedEvent = %+v, want ID=req-1 Verdict=Deny Remembered=true Expired=false", got)
	}
}

// TestQueue_OnResolvedCompletesBeforeAddUnblocks guards the ordering fix
// daemon.go's own signed-event history depends on (see Resolve's own
// comment): a request that Handle is blocked on inside Add must never be
// able to wake up and act on its verdict (e.g. sign the event) before
// OnResolved's callback (which is what creates that request's history
// entry) has actually finished running. onResolvedCalled is only ever
// written inside the callback and read from the goroutine unblocked by
// Add -- a plain bool would be a real data race if the ordering broke,
// which is exactly the failure mode this guards; using atomic.Bool
// keeps the test itself race-clean regardless of which way the ordering
// actually goes; the happens-before edge under test is close(e.done)
// itself, which Go's memory model already guarantees once the ordering
// in Resolve is correct.
func TestQueue_OnResolvedCompletesBeforeAddUnblocks(t *testing.T) {
	q := NewQueue(0, 0)
	var onResolvedCalled atomic.Bool
	q.OnResolved(func(ResolvedEvent) { onResolvedCalled.Store(true) })

	sawOnResolvedByAddTime := make(chan bool, 1)
	go func() {
		q.Add(Pending{ID: "req-1", ClientKey: "app1", Method: "ping"})
		sawOnResolvedByAddTime <- onResolvedCalled.Load()
	}()
	waitFor(t, func() bool { return len(q.List()) == 1 })

	if err := q.Resolve("req-1", Allow, false); err != nil {
		t.Fatal(err)
	}

	select {
	case saw := <-sawOnResolvedByAddTime:
		if !saw {
			t.Error("Add() unblocked before OnResolved finished running -- the happens-before ordering Resolve's own comment promises was violated")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Add() never returned")
	}
}

// TestQueue_OnResolved_FiresFromExpirySweep is OnResolved's other source
// -- an unanswered request auto-rejected by sweepExpired must still reach
// history, with Expired=true and Remembered=false (nobody decided
// anything to remember).
func TestQueue_OnResolved_FiresFromExpirySweep(t *testing.T) {
	q := NewQueue(0, time.Millisecond)
	fc := &fakeClock{t: time.Now()}
	q.now = fc.Now

	var got *ResolvedEvent
	q.OnResolved(func(ev ResolvedEvent) { got = &ev })

	go q.Add(Pending{ID: "req-exp", ClientKey: "app1", Method: "ping"})
	waitFor(t, func() bool { return len(q.List()) == 1 })

	fc.Advance(time.Hour)
	q.sweepExpired()

	if got == nil {
		t.Fatal("OnResolved never fired")
	}
	if got.Pending.ID != "req-exp" || got.Verdict != Deny || !got.Expired || got.Remembered {
		t.Errorf("ResolvedEvent = %+v, want ID=req-exp Verdict=Deny Expired=true Remembered=false", got)
	}
}

func TestQueue_DuplicateRequestID_NoDoubleEnqueue(t *testing.T) {
	q := NewQueue(0, 0)

	var wg sync.WaitGroup
	results := make([]Decision, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := q.Add(Pending{ID: "req-dup", ClientKey: "app1", Method: "ping"})
			if err != nil {
				t.Errorf("Add() error = %v", err)
			}
			results[i] = v
		}(i)
	}

	waitFor(t, func() bool { return len(q.List()) == 1 })
	if got := len(q.List()); got != 1 {
		t.Fatalf("List() len = %d, want 1 (a resent request must not double-enqueue)", got)
	}

	if err := q.Resolve("req-dup", Allow, false); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	for i, v := range results {
		if v != Allow {
			t.Errorf("caller %d verdict = %v, want Allow (both callers must observe the single decision)", i, v)
		}
	}
}

func TestQueue_ResolveUnknown(t *testing.T) {
	q := NewQueue(0, 0)
	if err := q.Resolve("nope", Allow, false); err != ErrNotPending {
		t.Errorf("Resolve() error = %v, want ErrNotPending", err)
	}
}

func TestQueue_ResolveRace_ExactlyOnce(t *testing.T) {
	q := NewQueue(0, 0)
	go q.Add(Pending{ID: "req-race", ClientKey: "app1", Method: "ping"})
	waitFor(t, func() bool { return len(q.List()) == 1 })

	var wg sync.WaitGroup
	successes := make([]error, 4)
	for i := range 4 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			successes[i] = q.Resolve("req-race", Allow, false)
		}(i)
	}
	wg.Wait()

	oks := 0
	for _, err := range successes {
		if err == nil {
			oks++
		} else if err != ErrNotPending {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if oks != 1 {
		t.Errorf("successful Resolve() calls = %d, want exactly 1", oks)
	}
}

func TestQueue_Full(t *testing.T) {
	q := NewQueue(2, 0)
	go q.Add(Pending{ID: "r1", ClientKey: "app1", Method: "ping"})
	go q.Add(Pending{ID: "r2", ClientKey: "app1", Method: "ping"})
	waitFor(t, func() bool { return len(q.List()) == 2 })

	if _, err := q.Add(Pending{ID: "r3", ClientKey: "app1", Method: "ping"}); err != ErrQueueFull {
		t.Errorf("Add() beyond capacity error = %v, want ErrQueueFull", err)
	}
}

func TestQueue_ExpirySweep_SendsRealResponse(t *testing.T) {
	q := NewQueue(0, time.Millisecond)
	fc := &fakeClock{t: time.Now()}
	q.now = fc.Now

	done := make(chan Decision, 1)
	go func() {
		v, _ := q.Add(Pending{ID: "req-exp", ClientKey: "app1", Method: "sign_event", Kind: 1})
		done <- v
	}()
	waitFor(t, func() bool { return len(q.List()) == 1 })

	fc.Advance(time.Hour)
	q.sweepExpired()

	select {
	case v := <-done:
		if v != Deny {
			t.Errorf("expired verdict = %v, want Deny (the waiting client must get a real rejection)", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expiry sweep never woke the waiting Add() call")
	}
	if got := len(q.List()); got != 0 {
		t.Errorf("List() after sweep = %d entries, want 0", got)
	}
}

func TestQueue_Run_SweepsOnTicker(t *testing.T) {
	q := NewQueue(0, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go q.Run(ctx, 5*time.Millisecond, nil)

	done := make(chan Decision, 1)
	go func() {
		v, _ := q.Add(Pending{ID: "req-run", ClientKey: "app1", Method: "ping"})
		done <- v
	}()

	select {
	case v := <-done:
		if v != Deny {
			t.Errorf("verdict = %v, want Deny", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run()'s ticker never expired the pending request")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition never became true")
}
