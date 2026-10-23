// Package jobs runs index maintenance on a live relay: rebuilding or
// dropping the search and zap indexes, one job per index at a time, each
// with progress, cancellation and a recorded outcome.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Kind is what a job does to an index.
type Kind string

const (
	KindRebuild Kind = "rebuild"
	KindDrop    Kind = "drop"
)

// State is where a job is in its life.
type State string

const (
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

// Options tune a rebuild.
type Options struct {
	// Fresh builds a new index and swaps it in, dropping stale entries;
	// otherwise entries are upserted into the live index.
	Fresh bool `json:"fresh"`
}

// Job is one run, as reported to callers.
type Job struct {
	ID         string  `json:"id"`
	Index      string  `json:"index"`
	Kind       Kind    `json:"kind"`
	Options    Options `json:"options"`
	State      State   `json:"state"`
	Scanned    int64   `json:"scanned"`
	Indexed    int64   `json:"indexed"`
	Total      int64   `json:"total,omitempty"`
	StartedAt  int64   `json:"started_at"`
	FinishedAt int64   `json:"finished_at,omitempty"`
	Error      string  `json:"error,omitempty"`
}

// Progress reports a running job's position.
type Progress func(scanned, indexed int)

// Index states, as listed.
const (
	// IndexDisabled: its feature is off; data it still holds is stale.
	IndexDisabled = "disabled"
	// IndexStale: in use but missing events until rebuilt.
	IndexStale = "stale"
	// IndexBuilding: a rebuild is running.
	IndexBuilding = "building"
	// IndexReady: in use and current.
	IndexReady = "ready"
)

// Index is one maintainable index.
type Index interface {
	Name() string
	// Available returns "" when a job of kind can run on the index, or why
	// not.
	Available(kind Kind) string
	// State returns the index's state and whether it holds data.
	State() (state string, hasData bool)
	// Estimate returns the approximate number of entries in the index.
	Estimate() int64
	// ScanEstimate returns the approximate number of items a rebuild scans.
	ScanEstimate() int64
	Rebuild(ctx context.Context, opts Options, progress Progress) error
	Drop(ctx context.Context) error
}

var (
	// ErrUnknownIndex names an index that doesn't exist.
	ErrUnknownIndex = errors.New("unknown index")
	// ErrUnavailable is an index whose feature is off on this relay.
	ErrUnavailable = errors.New("index unavailable")
	// ErrBusy is a job that conflicts with one already running.
	ErrBusy = errors.New("another job is running on this index")
	// ErrNotFound is an unknown job id.
	ErrNotFound = errors.New("no such job")
)

// keptJobs bounds the finished-job history.
const keptJobs = 20

// Manager runs jobs and remembers recent ones.
type Manager struct {
	indexes map[string]Index
	order   []string
	path    string
	logger  zerolog.Logger
	// OnFinish, if set, is told each finished job (for metrics).
	OnFinish func(Job)

	mu      sync.Mutex
	running map[string]*run // by index
	history []Job           // finished, newest first
}

type run struct {
	job    Job
	cancel context.CancelFunc
	done   chan struct{}
}

// New manages indexes, persisting finished jobs to path ("" keeps them in
// memory only).
func New(path string, logger zerolog.Logger, indexes ...Index) *Manager {
	m := &Manager{indexes: map[string]Index{}, path: path, logger: logger, running: map[string]*run{}}
	for _, ix := range indexes {
		m.indexes[ix.Name()] = ix
		m.order = append(m.order, ix.Name())
	}
	m.load()
	return m
}

// IndexInfo describes an index for listing.
type IndexInfo struct {
	Name string `json:"name"`
	// Available reports whether it can be rebuilt; Unavailable says why not.
	Available   bool   `json:"available"`
	Unavailable string `json:"unavailable,omitempty"`
	// State is disabled, stale, building or ready.
	State string `json:"state"`
	// HasData reports whether it holds entries, even while disabled.
	HasData bool `json:"has_data"`
	// Estimate is the approximate number of entries in the index.
	Estimate int64 `json:"estimate"`
	// ScanEstimate is the approximate number of items a rebuild scans.
	ScanEstimate int64 `json:"scan_estimate"`
	Running      *Job  `json:"running,omitempty"`
	Last         *Job  `json:"last,omitempty"`
}

// Indexes lists every index with its running and last job.
func (m *Manager) Indexes() []IndexInfo {
	// Estimates can wait on Meilisearch, so take them before locking.
	out := make([]IndexInfo, 0, len(m.order))
	for _, name := range m.order {
		ix := m.indexes[name]
		why := ix.Available(KindRebuild)
		info := IndexInfo{Name: name, Available: why == "", Unavailable: why}
		info.State, info.HasData = ix.State()
		if why == "" {
			info.Estimate, info.ScanEstimate = ix.Estimate(), ix.ScanEstimate()
		} else if info.HasData {
			info.Estimate = ix.Estimate()
		}
		out = append(out, info)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range out {
		info, name := &out[i], out[i].Name
		if r, ok := m.running[name]; ok {
			j := r.job
			info.Running = &j
			if j.Kind == KindRebuild {
				info.State = IndexBuilding
			}
		}
		for h := range m.history {
			if m.history[h].Index == name {
				j := m.history[h]
				info.Last = &j
				break
			}
		}
	}
	return out
}

// Estimate returns an index's entry and scan estimates, for a dry run.
func (m *Manager) Estimate(index string) (entries, scan int64, err error) {
	ix, err := m.lookup(index, KindRebuild)
	if err != nil {
		return 0, 0, err
	}
	return ix.Estimate(), ix.ScanEstimate(), nil
}

func (m *Manager) lookup(index string, kind Kind) (Index, error) {
	ix, ok := m.indexes[index]
	if !ok {
		return nil, ErrUnknownIndex
	}
	if why := ix.Available(kind); why != "" {
		return nil, errors.Join(ErrUnavailable, errors.New(why))
	}
	return ix, nil
}

// Start begins kind on index. If the same kind is already running there
// (with the same options) it returns that job and started=false; any other
// job on the index is ErrBusy.
func (m *Manager) Start(index string, kind Kind, opts Options) (job Job, started bool, err error) {
	ix, err := m.lookup(index, kind)
	if err != nil {
		return Job{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.running[index]; ok {
		if r.job.Kind == kind && r.job.Options == opts {
			return r.job, false, nil
		}
		return r.job, false, ErrBusy
	}

	ctx, cancel := context.WithCancel(context.Background())
	r := &run{
		job: Job{
			ID: newID(), Index: index, Kind: kind, Options: opts, State: StateRunning,
			StartedAt: time.Now().Unix(),
		},
		cancel: cancel,
		done:   make(chan struct{}),
	}
	if kind == KindRebuild {
		r.job.Total = ix.ScanEstimate()
	}
	m.running[index] = r
	go m.execute(ctx, ix, r)
	return r.job, true, nil
}

func (m *Manager) execute(ctx context.Context, ix Index, r *run) {
	defer close(r.done)
	m.logger.Info().Str("job", r.job.ID).Str("index", r.job.Index).Str("kind", string(r.job.Kind)).Bool("fresh", r.job.Options.Fresh).Msg("index job started")

	var err error
	switch r.job.Kind {
	case KindRebuild:
		err = ix.Rebuild(ctx, r.job.Options, func(scanned, indexed int) {
			m.mu.Lock()
			r.job.Scanned, r.job.Indexed = int64(scanned), int64(indexed)
			if r.job.Total < r.job.Scanned {
				r.job.Total = r.job.Scanned
			}
			m.mu.Unlock()
		})
	case KindDrop:
		err = ix.Drop(ctx)
	}

	m.mu.Lock()
	r.job.FinishedAt = time.Now().Unix()
	switch {
	case err == nil:
		r.job.State = StateSucceeded
		if r.job.Kind == KindRebuild {
			r.job.Total = r.job.Scanned
		}
	case errors.Is(err, context.Canceled):
		r.job.State = StateCancelled
	default:
		r.job.State, r.job.Error = StateFailed, err.Error()
	}
	finished := r.job
	delete(m.running, r.job.Index)
	m.history = append([]Job{finished}, m.history...)
	if len(m.history) > keptJobs {
		m.history = m.history[:keptJobs]
	}
	// Saved under the lock: concurrent saves would share the .tmp file, and
	// the job shouldn't read as finished before its history is on disk.
	m.save(m.history)
	m.mu.Unlock()

	ev := m.logger.Info()
	if finished.State == StateFailed {
		ev = m.logger.Error().Str("error", finished.Error)
	}
	ev.Str("job", finished.ID).Str("index", finished.Index).Str("state", string(finished.State)).
		Int64("scanned", finished.Scanned).Int64("indexed", finished.Indexed).
		Int64("seconds", finished.FinishedAt-finished.StartedAt).Msg("index job finished")
	if m.OnFinish != nil {
		m.OnFinish(finished)
	}
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

// List returns running jobs, then finished ones newest first.
func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.running)+len(m.history))
	for _, r := range m.running {
		out = append(out, r.job)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return append(out, m.history...)
}

// Running returns how many jobs are running.
func (m *Manager) Running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.running)
}

// Cancel stops a running job and waits for it to wind down.
func (m *Manager) Cancel(ctx context.Context, id string) (Job, error) {
	m.mu.Lock()
	var r *run
	for _, cand := range m.running {
		if cand.job.ID == id {
			r = cand
		}
	}
	m.mu.Unlock()
	if r == nil {
		j, err := m.Get(id)
		if err != nil {
			return Job{}, err
		}
		return j, nil // already finished: nothing to cancel
	}
	r.cancel()
	select {
	case <-r.done:
	case <-ctx.Done():
	}
	return m.Get(id)
}

// Shutdown cancels every running job and waits for them.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	runs := make([]*run, 0, len(m.running))
	for _, r := range m.running {
		runs = append(runs, r)
	}
	m.mu.Unlock()
	for _, r := range runs {
		r.cancel()
	}
	for _, r := range runs {
		select {
		case <-r.done:
		case <-ctx.Done():
			return
		}
	}
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
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
			jobs[i].State, jobs[i].Error = StateFailed, "relay stopped while the job was running"
		}
	}
	if len(jobs) > keptJobs {
		jobs = jobs[:keptJobs]
	}
	m.history = jobs
}

func (m *Manager) save(history []Job) {
	if m.path == "" {
		return
	}
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err == nil {
		err = os.Rename(tmp, m.path)
		if err == nil {
			return
		}
	}
	m.logger.Warn().Str("file", m.path).Msg("failed to save job history")
}
