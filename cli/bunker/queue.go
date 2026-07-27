package bunker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ohstr/nmilat/nip01"
)

// DefaultMaxPending bounds the queue's size: past this many simultaneous
// unresolved requests, a new one is rejected outright ("signer busy")
// rather than accepted and left to grow memory unboundedly.
const DefaultMaxPending = 200

// DefaultPendingTTL is how long a request waits for a human decision
// before it's auto-rejected -- bounds both memory and how stale a
// surprise approval can be; the requesting client gets a real NIP-46
// error response when this fires, not a silent drop.
const DefaultPendingTTL = 5 * time.Minute

var (
	// ErrQueueFull is returned by Add when the pending queue is already at
	// its configured capacity.
	ErrQueueFull = errors.New("bunker: pending queue is full")
	// ErrNotPending is returned by Resolve when id doesn't name a
	// currently-pending request (already resolved, expired, or never
	// existed).
	ErrNotPending = errors.New("bunker: no such pending request")
)

// Pending is one request awaiting a human decision.
type Pending struct {
	ID        string // NIP-46 request id -- also queue.go's de-dup key
	ClientKey string // requesting app's pubkey
	Method    string
	Kind      int // only meaningful for Method == "sign_event"
	Params    []string
	CreatedAt time.Time
	ExpiresAt time.Time
	Event     *nip01.Event // the raw sign_event target, if Method == "sign_event" -- nil otherwise, for display
}

// resolution is delivered exactly once per Pending, either by a human
// decision (Queue.Resolve) or by the expiry sweep.
type resolution struct {
	verdict Decision
	expired bool
}

// ResolvedEvent is delivered to Queue.OnResolved for every request that
// finishes being decided -- by a human (Resolve) or by the queue's own
// expiry sweep (sweepExpired) -- not just the ones still currently
// pending (Queue.List). Daemon's own request history (see daemon.go's
// recordHistory) is built entirely from this: it's the one place that
// ever sees a request that's no longer in byID.
type ResolvedEvent struct {
	Pending Pending
	Verdict Decision
	// Remembered is true if this decision also created/updated a Store
	// grant (client.go's resolve passes this through from its own
	// remember != nil check) -- distinct from a one-off Approve/Reject
	// Once, which never touches the Store at all.
	Remembered bool
	// Expired is true if the queue's own TTL swept this away unanswered
	// rather than a human deciding it.
	Expired bool
	// AutoApproved is true if this request never touched Queue.Add at all
	// -- Store.Decide already resolved it to Allow via a standing grant
	// (or, for "connect", a pending GrantSpec) -- see Handler.Handle and
	// Handler.OnAutoApproved. Always false for anything delivered through
	// Queue.Resolve/sweepExpired, which is what constructs every other
	// ResolvedEvent; recordAutoApproved (daemon.go) is the only
	// constructor that ever sets it.
	AutoApproved bool
}

// entry's result is written once, then done is closed -- never the other
// way around, and never a second time. That ordering matters: closing a
// channel and then having every waiter's `<-done` immediately return lets
// *all* of them (not just one) safely observe result afterward, per Go's
// memory model (a close happens-before a receive that observes it). A
// single buffered channel that multiple callers each try to receive from
// doesn't have this property -- only the first receiver gets the sent
// value, and every other one just observes the channel being closed and
// gets the zero value instead, silently corrupting a duplicate request's
// verdict.
type entry struct {
	pending Pending
	done    chan struct{}
	result  resolution
}

// Queue holds requests waiting for a human decision. Safe for concurrent
// use; Add/Resolve are the hot paths (one call per incoming/decided
// request), List is for the TUI's periodic render tick.
type Queue struct {
	mu         sync.Mutex
	byID       map[string]*entry
	max        int
	ttl        time.Duration
	now        func() time.Time
	onAdded    func(Pending)       // fired from Add, the instant a new request actually starts waiting -- see OnAdded
	onResolved func(ResolvedEvent) // fired from Resolve/sweepExpired -- see OnResolved
}

// NewQueue builds an empty Queue. max <= 0 defaults to DefaultMaxPending;
// ttl <= 0 defaults to DefaultPendingTTL.
func NewQueue(max int, ttl time.Duration) *Queue {
	if max <= 0 {
		max = DefaultMaxPending
	}
	if ttl <= 0 {
		ttl = DefaultPendingTTL
	}
	return &Queue{
		byID: map[string]*entry{},
		max:  max,
		ttl:  ttl,
		now:  time.Now,
	}
}

// OnAdded registers a callback fired from Add, the instant a new request
// actually starts waiting on a human decision (i.e. Decide itself didn't
// already resolve it via a remembered grant) -- board.go/ipc_server.go
// use this to push live updates to attached TUI clients instead of
// polling the queue on a tight loop.
func (q *Queue) OnAdded(fn func(Pending)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.onAdded = fn
}

// OnResolved registers a callback fired from Resolve and sweepExpired
// alike, for every request that finishes being decided -- see
// ResolvedEvent's own doc comment for why this (not Queue.List) is the
// only place a caller can observe a request that's no longer pending,
// which is what Daemon's own request history is built from.
func (q *Queue) OnResolved(fn func(ResolvedEvent)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.onResolved = fn
}

// Add enqueues a new pending request and blocks until it's resolved (by
// Resolve or by expiry), returning the verdict. Duplicate request IDs
// (the same NIP-46 request resent, or maliciously replayed) never
// double-enqueue or trigger a second independent decision: the second
// Add call for an ID already pending just waits on the same entry's
// resolution instead of creating a new one.
func (q *Queue) Add(p Pending) (Decision, error) {
	q.mu.Lock()
	if existing, ok := q.byID[p.ID]; ok {
		q.mu.Unlock()
		<-existing.done
		return existing.result.verdict, nil
	}
	if len(q.byID) >= q.max {
		q.mu.Unlock()
		return Ask, ErrQueueFull
	}

	now := q.now()
	p.CreatedAt = now
	p.ExpiresAt = now.Add(q.ttl)
	e := &entry{pending: p, done: make(chan struct{})}
	q.byID[p.ID] = e
	onAdded := q.onAdded
	q.mu.Unlock()

	if onAdded != nil {
		onAdded(p)
	}

	<-e.done
	return e.result.verdict, nil
}

// Resolve records a human decision for id, waking whichever Add call(s)
// are blocked on it. Reports ErrNotPending if id isn't currently pending
// (already resolved/expired, or unknown) -- callers (the IPC server, the
// TUI) use this to distinguish "you were too late" from success, e.g. when
// two attached clients race to resolve the same card. remembered should be
// true only when this decision also persisted a Store grant (see
// client.go's resolve) -- threaded straight into the OnResolved event
// rather than re-derived here, since the Store write already happened at
// the call site and Queue has no reason to know about Store itself.
func (q *Queue) Resolve(id string, verdict Decision, remembered bool) error {
	q.mu.Lock()
	e, ok := q.byID[id]
	if !ok {
		q.mu.Unlock()
		return ErrNotPending
	}
	delete(q.byID, id)
	onResolved := q.onResolved
	q.mu.Unlock()

	e.result = resolution{verdict: verdict}
	// onResolved before close(e.done), deliberately: close(e.done) is what
	// wakes the goroutine blocked in Add (Handle, still running the
	// original request) to go on and act on the verdict -- for an
	// approved sign_event, that means signing it and (via Handler.OnSigned)
	// attaching the result to this same request's history entry. That
	// attach step needs the entry to already exist, so the record has to
	// land here first; doing it the other way risked a real race, not
	// just a theoretical one -- Add's own goroutine is scheduled the
	// instant done closes, with no guarantee it doesn't reach OnSigned
	// before this call even gets to onResolved.
	if onResolved != nil {
		onResolved(ResolvedEvent{Pending: e.pending, Verdict: verdict, Remembered: remembered})
	}
	close(e.done)
	return nil
}

// Get returns a snapshot of one pending request by ID, and false if it
// isn't (or is no longer) pending -- used by Approve/Reject (client.go)
// to look up which app a remembered grant should be recorded against.
func (q *Queue) Get(id string) (Pending, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	e, ok := q.byID[id]
	if !ok {
		return Pending{}, false
	}
	return e.pending, true
}

// List returns a snapshot of every currently-pending request, oldest
// first.
func (q *Queue) List() []Pending {
	q.mu.Lock()
	defer q.mu.Unlock()

	out := make([]Pending, 0, len(q.byID))
	for _, e := range q.byID {
		out = append(out, e.pending)
	}
	return out
}

// sweepExpired auto-rejects (Decision Deny) every pending request whose
// ExpiresAt has passed, waking its Add call so the requesting client gets
// a real error response instead of hanging forever.
func (q *Queue) sweepExpired() {
	now := q.now()

	q.mu.Lock()
	var expired []*entry
	for id, e := range q.byID {
		if !now.Before(e.pending.ExpiresAt) {
			expired = append(expired, e)
			delete(q.byID, id)
		}
	}
	onResolved := q.onResolved
	q.mu.Unlock()

	for _, e := range expired {
		e.result = resolution{verdict: Deny, expired: true}
		// Same before-close(e.done) ordering as Resolve above, for the
		// same reason -- consistency more than necessity here, since an
		// expired request is always Deny and so never reaches signing at
		// all, but there's no reason for this path to be the one
		// exception to the rule.
		if onResolved != nil {
			onResolved(ResolvedEvent{Pending: e.pending, Verdict: Deny, Expired: true})
		}
		close(e.done)
	}
}

// Run drives the periodic sweep (expired pending requests, then
// policy.Store grant pruning -- one ticker, two sweeps, per the plan) until
// ctx is cancelled. Call once, in its own goroutine, for the lifetime of
// the daemon.
func (q *Queue) Run(ctx context.Context, interval time.Duration, store *Store) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			q.sweepExpired()
			if store != nil {
				store.Prune()
			}
		case <-ctx.Done():
			return
		}
	}
}
