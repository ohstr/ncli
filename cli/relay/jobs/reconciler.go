package jobs

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Reconcile backoff after a failed job, and the slow tick that notices
// what nothing else signals (Meilisearch coming back, a backlog growing).
var (
	reconcileBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}
	reconcileTick    = 5 * time.Second
)

// Reconciler starts, for each index, the job its status asks for: on
// start, when a job ends, when Nudge is called, and on a slow tick. A job
// that failed is retried with backoff.
type Reconciler struct {
	m *Manager

	mu       sync.Mutex
	failures map[string]int
	retryAt  map[string]time.Time
	// failed is the last failed job counted, per index.
	failed map[string]string
	nudge  chan struct{}
	done   chan struct{}
}

// Reconcile starts the reconciler; it stops when ctx ends.
func (m *Manager) Reconcile(ctx context.Context) *Reconciler {
	r := &Reconciler{m: m, failures: map[string]int{}, retryAt: map[string]time.Time{}, failed: map[string]string{}, nudge: make(chan struct{}, 1), done: make(chan struct{})}
	go r.run(ctx)
	return r
}

// Nudge has the reconciler look again now.
func (r *Reconciler) Nudge() {
	select {
	case r.nudge <- struct{}{}:
	default:
	}
}

// Done closes when the reconciler stopped.
func (r *Reconciler) Done() <-chan struct{} { return r.done }

func (r *Reconciler) run(ctx context.Context) {
	defer close(r.done)
	tick := time.NewTicker(reconcileTick)
	defer tick.Stop()
	for {
		r.pass()
		select {
		case <-ctx.Done():
			return
		case <-r.m.finished:
		case <-r.nudge:
		case <-tick.C:
		}
	}
}

// pass starts what each idle index needs.
func (r *Reconciler) pass() {
	for _, name := range r.m.Names() {
		r.note(name)
		if r.m.RunningJob(name) != nil {
			continue
		}
		ix, _ := r.m.Lookup(name)
		st := ix.Status()
		if st.Action == "" {
			r.mu.Lock()
			delete(r.failures, name)
			r.mu.Unlock()
			continue
		}
		r.mu.Lock()
		wait := time.Now().Before(r.retryAt[name])
		r.mu.Unlock()
		if wait {
			continue
		}
		if _, err := r.m.Start(name, st.Action, TriggerReconcile, st.Reason); err != nil && !errors.Is(err, ErrBusy) {
			r.m.logger.Warn().Err(err).Str("index", name).Str("action", string(st.Action)).Msg("index job not started")
		}
	}
}

// note backs off an index whose last job failed.
func (r *Reconciler) note(name string) {
	last := r.m.LastJob(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if last == nil || last.Trigger != TriggerReconcile || last.State != StateFailed || r.failed[name] == last.ID {
		return
	}
	r.failed[name] = last.ID
	n := r.failures[name]
	r.failures[name] = n + 1
	delay := reconcileBackoff[min(n, len(reconcileBackoff)-1)]
	r.retryAt[name] = time.Now().Add(delay)
	r.m.logger.Warn().Str("index", name).Dur("retry_in", delay).Msg("index job failed; retrying later")
}

// RetryAt returns when a failed index is retried, zero when it isn't waiting.
func (r *Reconciler) RetryAt(name string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.retryAt[name]; time.Now().Before(t) {
		return t
	}
	return time.Time{}
}
