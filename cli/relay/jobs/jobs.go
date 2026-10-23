// Package jobs runs a live relay's index work — catching an index up,
// rebuilding, rescoring, freeing old data, dropping — as jobs: each with a
// trigger, progress, a recorded outcome and a one-line summary, kept across
// restarts. A reconciler starts what each index needs on its own; an admin
// can start a rebuild or a drop.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Kind is what a job does to an index.
type Kind string

const (
	KindCatchUp Kind = "catch_up"
	KindRebuild Kind = "rebuild"
	KindRescore Kind = "rescore"
	KindReap    Kind = "reap"
	KindDrop    Kind = "drop"
)

// Trigger is who started a job.
type Trigger string

const (
	// TriggerReconcile: the relay, because the index needed it.
	TriggerReconcile Trigger = "reconcile"
	// TriggerAdmin: an admin, through the API.
	TriggerAdmin Trigger = "admin"
)

// State is where a job is in its life.
type State string

const (
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
	// StateSuperseded: stopped for a job that does more (a rebuild).
	StateSuperseded State = "superseded"
	// StateInterrupted: the relay stopped while it ran.
	StateInterrupted State = "interrupted"
)

// Job is one run, as reported to callers.
type Job struct {
	ID      string  `json:"id,omitempty"`
	Index   string  `json:"index"`
	Kind    Kind    `json:"kind"`
	Trigger Trigger `json:"trigger"`
	// Reason is why it was started.
	Reason string `json:"reason,omitempty"`
	State  State  `json:"state"`
	// Phase is what it is doing now (waiting, scan, deletes, rescore, free).
	Phase string `json:"phase,omitempty"`
	// Done and Total measure the phase; Applied counts entries changed.
	Done          int64  `json:"done"`
	Total         int64  `json:"total"`
	Applied       int64  `json:"applied"`
	EntriesBefore int64  `json:"entries_before"`
	EntriesAfter  int64  `json:"entries_after"`
	StartedAt     int64  `json:"started_at"`
	FinishedAt    int64  `json:"finished_at,omitempty"`
	DurationMs    int64  `json:"duration_ms"`
	Error         string `json:"error,omitempty"`
	// Summary is one line saying what it did, or does.
	Summary string `json:"summary"`
	// Noop is set on an answer that started nothing: the job named is the
	// one already doing it, or none was needed (Summary says which).
	Noop bool `json:"noop,omitempty"`
}

// Ended reports whether the job ended.
func (j Job) Ended() bool { return j.State != StateRunning }

// Progress is an index operation's position.
type Progress struct {
	Phase   string
	Done    int64
	Total   int64
	Applied int64
}

// Result is what an index operation did.
type Result struct {
	Applied       int64
	EntriesBefore int64
	EntriesAfter  int64
}

// Index is one maintainable index.
type Index interface {
	Name() string
	// Status describes the index and what it needs next.
	Status() Status
	// Run performs kind (any but drop) and reports progress.
	Run(ctx context.Context, kind Kind, progress func(Progress)) (Result, error)
	// Drop removes what a disabled index holds.
	Drop(ctx context.Context, progress func(Progress)) (Result, error)
	// Cancelled is told a job of kind was cancelled by an admin, so the
	// index can discard what it would otherwise resume.
	Cancelled(kind Kind)
	// WritesStore reports whether its operations write the event store,
	// which takes one writer at a time.
	WritesStore() bool
}

// ErrNothingToDo is returned by an index operation that isn't needed.
var ErrNothingToDo = errors.New("nothing to do")

var (
	// ErrUnknownIndex names an index that doesn't exist.
	ErrUnknownIndex = errors.New("unknown index")
	// ErrUnavailable is an index whose feature is off on this relay.
	ErrUnavailable = errors.New("index unavailable")
	// ErrEnabled is a drop of an index that is in use.
	ErrEnabled = errors.New("index in use")
	// ErrBusy is a job that conflicts with one already running.
	ErrBusy = errors.New("another job is running on this index")
	// ErrNotFound is an unknown job id.
	ErrNotFound = errors.New("no such job")
	// ErrNotCancellable is a job that can't be stopped.
	ErrNotCancellable = errors.New("job can't be cancelled")
)

// keptJobs bounds each index's finished-job history.
const keptJobs = 20

// Manager runs jobs and remembers recent ones.
type Manager struct {
	indexes map[string]Index
	order   []string
	path    string
	logger  zerolog.Logger
	// OnFinish, if set, is told each finished job (for metrics).
	OnFinish func(Job)
	// finished wakes the reconciler.
	finished chan struct{}
	// writer lets one store-writing job run at a time.
	writer chan struct{}

	mu      sync.Mutex
	running map[string]*run // by index
	history []Job           // finished, newest first
	closed  bool
	wg      sync.WaitGroup
}

type run struct {
	job    Job
	cancel context.CancelCauseFunc
	done   chan struct{}
}

// cancel causes.
var (
	errCancelled  = errors.New("cancelled")
	errSuperseded = errors.New("superseded")
	errShutdown   = errors.New("relay stopping")
)

// New manages indexes, persisting jobs to path ("" keeps them in memory).
func New(path string, logger zerolog.Logger, indexes ...Index) *Manager {
	m := &Manager{
		indexes: map[string]Index{}, path: path, logger: logger,
		running: map[string]*run{}, finished: make(chan struct{}, 1), writer: make(chan struct{}, 1),
	}
	for _, ix := range indexes {
		m.indexes[ix.Name()] = ix
		m.order = append(m.order, ix.Name())
	}
	m.load()
	return m
}

// Lookup returns an index by name.
func (m *Manager) Lookup(name string) (Index, error) {
	ix, ok := m.indexes[name]
	if !ok {
		return nil, ErrUnknownIndex
	}
	return ix, nil
}

// Names lists the indexes in order.
func (m *Manager) Names() []string { return append([]string(nil), m.order...) }

// Start begins kind on index. A rebuild supersedes a reconcile job on the
// same index; the same kind already running is returned with Noop set;
// anything else running there is ErrBusy (with that job).
func (m *Manager) Start(index string, kind Kind, trigger Trigger, reason string) (Job, error) {
	ix, err := m.Lookup(index)
	if err != nil {
		return Job{}, err
	}
	st := ix.Status()
	switch {
	case kind == KindDrop && st.Enabled:
		return Job{}, fmt.Errorf("%w: the %s index is on; rebuild it instead (ncli relay index rebuild %s)", ErrEnabled, index, index)
	case kind != KindDrop && kind != KindReap && !st.Enabled:
		return Job{}, fmt.Errorf("%w: %s", ErrUnavailable, st.Unavailable)
	case kind == KindDrop && !st.HasData && st.Retired == 0:
		return Job{Index: index, Kind: kind, Trigger: trigger, State: StateSucceeded, Noop: true,
			Summary: fmt.Sprintf("%s: nothing to drop", index)}, ErrNothingToDo
	}

	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return Job{}, errShutdown
		}
		r, busy := m.running[index]
		if !busy {
			break
		}
		cur := r.job
		switch {
		case cur.Kind == kind || (kind == KindRebuild && cur.Kind == KindRebuild):
			m.mu.Unlock()
			cur.Noop = true
			cur.Summary = fmt.Sprintf("%s: already running %s (job %s)", index, describe(cur.Kind), cur.ID)
			return cur, nil
		case trigger == TriggerAdmin && cur.Trigger == TriggerReconcile && (kind == KindRebuild || kind == KindDrop):
			r.cancel(errSuperseded)
			m.mu.Unlock()
			<-r.done
			continue
		}
		m.mu.Unlock()
		return cur, ErrBusy
	}
	defer m.mu.Unlock()

	ctx, cancel := context.WithCancelCause(context.Background())
	now := time.Now()
	r := &run{
		job: Job{
			ID: newID(), Index: index, Kind: kind, Trigger: trigger, Reason: reason,
			State: StateRunning, StartedAt: now.Unix(), EntriesBefore: st.Entries,
		},
		cancel: cancel,
		done:   make(chan struct{}),
	}
	r.job.Summary = fmt.Sprintf("%s: %s started", index, describe(kind))
	m.running[index] = r
	m.saveLocked()
	m.wg.Add(1)
	go m.execute(ctx, ix, r, r.job)
	return r.job, nil
}

func describe(k Kind) string {
	switch k {
	case KindCatchUp:
		return "catch-up"
	case KindReap:
		return "freeing old data"
	}
	return string(k)
}

func (m *Manager) execute(ctx context.Context, ix Index, r *run, j Job) {
	defer m.wg.Done()
	defer close(r.done)
	start := time.Now()
	m.logger.Info().Str("job", j.ID).Str("index", j.Index).Str("kind", string(j.Kind)).Str("trigger", string(j.Trigger)).Str("reason", j.Reason).Msg("index job started")

	var res Result
	var err error
	if ix.WritesStore() {
		m.update(r, func(j *Job) { j.Phase = "waiting" })
		select {
		case m.writer <- struct{}{}:
			defer func() { <-m.writer }()
		case <-ctx.Done():
			err = context.Cause(ctx)
		}
	}
	saved := time.Now()
	if err == nil {
		progress := func(p Progress) {
			m.update(r, func(j *Job) { j.Phase, j.Done, j.Total, j.Applied = p.Phase, p.Done, p.Total, p.Applied })
			if time.Since(saved) > 5*time.Second {
				saved = time.Now()
				m.mu.Lock()
				m.saveLocked()
				m.mu.Unlock()
			}
		}
		if j.Kind == KindDrop {
			res, err = ix.Drop(ctx, progress)
		} else {
			res, err = ix.Run(ctx, j.Kind, progress)
		}
	}
	cause := context.Cause(ctx)
	if errors.Is(cause, errCancelled) {
		ix.Cancelled(j.Kind)
	}

	m.mu.Lock()
	j = r.job
	j.FinishedAt = time.Now().Unix()
	j.DurationMs = time.Since(start).Milliseconds()
	if res.EntriesBefore != 0 || res.EntriesAfter != 0 || err == nil {
		j.EntriesBefore, j.EntriesAfter = res.EntriesBefore, res.EntriesAfter
	}
	j.Applied = max(j.Applied, res.Applied)
	j.Phase = ""
	switch {
	case err == nil, errors.Is(err, ErrNothingToDo):
		j.State = StateSucceeded
	case errors.Is(cause, errSuperseded):
		j.State = StateSuperseded
	case errors.Is(cause, errShutdown):
		j.State = StateInterrupted
	case errors.Is(cause, errCancelled):
		j.State = StateCancelled
	default:
		j.State, j.Error = StateFailed, err.Error()
	}
	if errors.Is(err, ErrNothingToDo) {
		j.Summary = fmt.Sprintf("%s: nothing to do", j.Index)
	} else {
		j.Summary = summarize(j)
	}
	r.job = j
	delete(m.running, j.Index)
	m.history = append([]Job{j}, m.history...)
	m.trimLocked()
	m.saveLocked()
	m.mu.Unlock()

	ev := m.logger.Info()
	if j.State == StateFailed {
		ev = m.logger.Error().Str("error", j.Error)
	}
	ev.Str("job", j.ID).Str("index", j.Index).Str("kind", string(j.Kind)).Str("state", string(j.State)).
		Int64("entries", j.EntriesAfter).Int64("ms", j.DurationMs).Msg(j.Summary)
	if m.OnFinish != nil {
		m.OnFinish(j)
	}
	select {
	case m.finished <- struct{}{}:
	default:
	}
}

func (m *Manager) update(r *run, fn func(*Job)) {
	m.mu.Lock()
	fn(&r.job)
	m.mu.Unlock()
}

// summarize is a finished job's one line.
func summarize(j Job) string {
	took := (time.Duration(j.DurationMs) * time.Millisecond).Round(100 * time.Millisecond)
	switch j.State {
	case StateFailed:
		return fmt.Sprintf("%s: %s failed after %s: %s", j.Index, describe(j.Kind), took, j.Error)
	case StateCancelled:
		return fmt.Sprintf("%s: %s cancelled after %s", j.Index, describe(j.Kind), took)
	case StateSuperseded:
		return fmt.Sprintf("%s: %s stopped for a rebuild", j.Index, describe(j.Kind))
	case StateInterrupted:
		return fmt.Sprintf("%s: %s interrupted by a relay stop; it resumes on its own", j.Index, describe(j.Kind))
	}
	change := fmt.Sprintf("%s → %s entries", Count(j.EntriesBefore), Count(j.EntriesAfter))
	switch j.Kind {
	case KindRebuild:
		return fmt.Sprintf("%s: rebuilt in %s, %s", j.Index, took, change)
	case KindCatchUp:
		return fmt.Sprintf("%s: caught up in %s, %s changes applied, %s", j.Index, took, Count(j.Applied), change)
	case KindRescore:
		return fmt.Sprintf("%s: rescored in %s, %s scores changed", j.Index, took, Count(j.Applied))
	case KindReap:
		return fmt.Sprintf("%s: freed %s old entries in %s", j.Index, Count(j.Applied), took)
	case KindDrop:
		return fmt.Sprintf("%s: dropped %s entries", j.Index, Count(j.EntriesBefore))
	}
	return fmt.Sprintf("%s: %s done in %s", j.Index, j.Kind, took)
}

// Count groups a number's digits: 18 402.
func Count(n int64) string {
	s := fmt.Sprint(n)
	neg := n < 0
	if neg {
		s = s[1:]
	}
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ' ')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// Get returns a job by id, running or finished.
func (m *Manager) Get(id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.running {
		if r.job.ID == id {
			return r.job, nil
		}
	}
	for _, j := range m.history {
		if j.ID == id {
			return j, nil
		}
	}
	return Job{}, ErrNotFound
}

// List returns running jobs, then finished ones newest first; index ""
// lists every index.
func (m *Manager) List(index string) []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Job{}
	for _, r := range m.running {
		if index == "" || r.job.Index == index {
			out = append(out, r.job)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	for _, j := range m.history {
		if index == "" || j.Index == index {
			out = append(out, j)
		}
	}
	return out
}

// RunningJob returns the job running on index, if any.
func (m *Manager) RunningJob(index string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.running[index]; ok {
		j := r.job
		return &j
	}
	return nil
}

// LastJob returns the last finished job on index, if any.
func (m *Manager) LastJob(index string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.history {
		if j.Index == index {
			return &j
		}
	}
	return nil
}

// Running returns how many jobs are running.
func (m *Manager) Running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.running)
}

// Cancel asks a running job to stop and returns it at once; it ends shortly
// after. A finished job is returned with Noop set.
func (m *Manager) Cancel(id string) (Job, error) {
	m.mu.Lock()
	var r *run
	for _, cand := range m.running {
		if cand.job.ID == id {
			r = cand
		}
	}
	if r == nil {
		m.mu.Unlock()
		j, err := m.Get(id)
		if err != nil {
			return Job{}, err
		}
		j.Noop = true
		j.Summary = fmt.Sprintf("job %s already %s; nothing to cancel", j.ID, j.State)
		return j, nil
	}
	if ix := m.indexes[r.job.Index]; r.job.Kind == KindCatchUp && !ix.WritesStore() {
		j := r.job
		m.mu.Unlock()
		return j, fmt.Errorf("%w: search catches up as part of live indexing; turn search off to stop it", ErrNotCancellable)
	}
	r.cancel(errCancelled)
	r.job.Phase = "cancelling"
	j := r.job
	m.mu.Unlock()
	return j, nil
}

// Wait waits until job id ends or ctx is done, and returns it.
func (m *Manager) Wait(ctx context.Context, id string) (Job, error) {
	m.mu.Lock()
	var done chan struct{}
	for _, r := range m.running {
		if r.job.ID == id {
			done = r.done
		}
	}
	m.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
	return m.Get(id)
}

// Shutdown stops every running job, recorded as interrupted, and waits.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	m.closed = true
	for _, r := range m.running {
		r.cancel(errShutdown)
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// trimLocked keeps the newest keptJobs finished jobs of each index.
func (m *Manager) trimLocked() {
	per := map[string]int{}
	kept := m.history[:0]
	for _, j := range m.history {
		if per[j.Index] < keptJobs {
			per[j.Index]++
			kept = append(kept, j)
		}
	}
	m.history = kept
}

func (m *Manager) load() {
	if m.path == "" {
		return
	}
	data, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		m.logger.Warn().Err(err).Str("file", m.path).Msg("ignoring unreadable job history")
		return
	}
	for i := range jobs {
		// A job recorded as running died with the previous process.
		if jobs[i].State == StateRunning {
			jobs[i].State, jobs[i].Phase = StateInterrupted, ""
			if jobs[i].FinishedAt == 0 {
				jobs[i].FinishedAt = jobs[i].StartedAt
			}
			jobs[i].Summary = summarize(jobs[i])
		}
	}
	sort.SliceStable(jobs, func(a, b int) bool { return jobs[a].StartedAt > jobs[b].StartedAt })
	m.history = jobs
	m.trimLocked()
}

// saveLocked writes running and finished jobs, so a crash leaves a record
// of what was running.
func (m *Manager) saveLocked() {
	if m.path == "" {
		return
	}
	all := make([]Job, 0, len(m.running)+len(m.history))
	for _, r := range m.running {
		all = append(all, r.job)
	}
	all = append(all, m.history...)
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err == nil {
		if err = os.Rename(tmp, m.path); err == nil {
			return
		}
	}
	m.logger.Warn().Str("file", m.path).Msg("failed to save job history")
}
