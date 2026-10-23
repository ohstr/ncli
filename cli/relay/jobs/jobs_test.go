package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/utils"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testKey(t *testing.T, i int) (priv, pub string) {
	t.Helper()
	sum := sha256.Sum256([]byte(fmt.Sprintf("jobs-test-key-%d", i)))
	priv = hex.EncodeToString(sum[:])
	pub, err := utils.GetPublicKey(priv)
	require.NoError(t, err)
	return priv, pub
}

func signed(t *testing.T, i, kind int, createdAt uint64, tags ...[]string) *nip01.Event {
	t.Helper()
	priv, pub := testKey(t, i)
	ev := &nip01.Event{PubKey: pub, Kind: kind, CreatedAt: createdAt, Tags: tags, Content: fmt.Sprintf(`{"name":"user%d"}`, i)}
	require.NoError(t, ev.Sign(priv))
	return ev
}

func insert(t *testing.T, store *relay.EventStore, events ...*nip01.Event) {
	t.Helper()
	require.NoError(t, store.InsertEvents(context.Background(), events))
}

// openAt opens path with ix, leaving index work to the test, and closes it
// at the end.
func openAt(t *testing.T, path string, ix relay.DerivedIndexes) *relay.EventStore {
	t.Helper()
	store, err := relay.NewEventStore(path, &nip11.Limitation{MaxLimit: 500},
		relay.WithEventStoreIndexes(ix), relay.WithEventStoreIndexMaintenance(relay.MaintenanceManual))
	require.NoError(t, err)
	t.Cleanup(store.Close)
	return store
}

// fakeIndex runs scripted operations: each blocks until released, and
// reports one progress step first.
type fakeIndex struct {
	name    string
	why     string
	writes  bool
	action  Kind
	entries int64
	release chan error
	panics  any
	mu      sync.Mutex
	ran     []Kind
	aborted []Kind
}

func newFake(name string) *fakeIndex {
	return &fakeIndex{name: name, writes: true, entries: 5, release: make(chan error, 10)}
}

func (f *fakeIndex) Name() string      { return f.name }
func (f *fakeIndex) WritesStore() bool { return f.writes }
func (f *fakeIndex) Status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := Status{Name: f.name, Enabled: f.why == "", Unavailable: f.why, State: StateReady, Entries: f.entries, HasData: f.entries > 0, Action: f.action}
	if f.why != "" {
		st.State = StateDisabled
	}
	return st
}
func (f *fakeIndex) Run(ctx context.Context, kind Kind, progress func(Progress)) (Result, error) {
	f.mu.Lock()
	f.ran = append(f.ran, kind)
	f.mu.Unlock()
	progress(Progress{Phase: "scan", Done: 7, Total: 42, Applied: 3})
	if f.panics != nil {
		panic(f.panics)
	}
	select {
	case err := <-f.release:
		f.mu.Lock()
		f.action = ""
		f.mu.Unlock()
		return Result{Applied: 3, EntriesBefore: 5, EntriesAfter: 8}, err
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}
func (f *fakeIndex) Drop(ctx context.Context, progress func(Progress)) (Result, error) {
	return f.Run(ctx, KindDrop, progress)
}
func (f *fakeIndex) Cancelled(kind Kind) {
	f.mu.Lock()
	f.aborted = append(f.aborted, kind)
	f.mu.Unlock()
}

func waitEnded(t *testing.T, m *Manager, id string) Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	j, err := m.Wait(ctx, id)
	require.NoError(t, err)
	require.True(t, j.Ended(), "job %s never ended: %+v", id, j)
	return j
}

func TestStart_RunsAndSummarizes(t *testing.T) {
	ix := newFake("zaps")
	finished := make(chan Job, 1)
	m := New("", zerolog.Nop(), ix)
	m.OnFinish = func(j Job) { finished <- j }

	j, err := m.Start("zaps", KindRebuild, TriggerAdmin, "")
	require.NoError(t, err)
	assert.Equal(t, StateRunning, j.State)
	assert.Equal(t, int64(5), j.EntriesBefore)
	assert.Equal(t, "zaps: rebuild started", j.Summary)
	require.Eventually(t, func() bool { r := m.RunningJob("zaps"); return r != nil && r.Done == 7 }, time.Second, time.Millisecond)

	ix.release <- nil
	done := <-finished
	assert.Equal(t, StateSucceeded, done.State)
	assert.Equal(t, int64(8), done.EntriesAfter)
	assert.Regexp(t, `^zaps: rebuilt in [0-9.]+m?s, 5 → 8 entries$`, done.Summary)
	assert.Empty(t, done.Phase)
}

func TestStart_SameKindJoinsTheRunningJob(t *testing.T) {
	ix := newFake("zaps")
	m := New("", zerolog.Nop(), ix)
	first, err := m.Start("zaps", KindRebuild, TriggerReconcile, "never built")
	require.NoError(t, err)
	again, err := m.Start("zaps", KindRebuild, TriggerAdmin, "")
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)
	assert.True(t, again.Noop)
	assert.Contains(t, again.Summary, "already running rebuild (job "+first.ID+")")
	ix.release <- nil
	waitEnded(t, m, first.ID)
}

func TestStart_AdminRebuildSupersedesAReconcileJob(t *testing.T) {
	ix := newFake("zaps")
	m := New("", zerolog.Nop(), ix)
	catchUp, err := m.Start("zaps", KindCatchUp, TriggerReconcile, "behind")
	require.NoError(t, err)
	rebuild, err := m.Start("zaps", KindRebuild, TriggerAdmin, "")
	require.NoError(t, err)
	assert.NotEqual(t, catchUp.ID, rebuild.ID)
	old, _ := m.Get(catchUp.ID)
	assert.Equal(t, StateSuperseded, old.State)
	assert.Equal(t, "zaps: catch-up stopped for a rebuild", old.Summary)
	assert.Empty(t, ix.aborted, "a superseded job isn't discarded")
	ix.release <- nil
	waitEnded(t, m, rebuild.ID)
}

func TestStart_OtherJobsAreBusy(t *testing.T) {
	ix := newFake("zaps")
	m := New("", zerolog.Nop(), ix)
	reap, err := m.Start("zaps", KindReap, TriggerAdmin, "")
	require.NoError(t, err)
	running, err := m.Start("zaps", KindCatchUp, TriggerReconcile, "")
	assert.ErrorIs(t, err, ErrBusy)
	assert.Equal(t, reap.ID, running.ID, "names the job in the way")
	ix.release <- nil
	waitEnded(t, m, reap.ID)
}

func TestStart_RefusesByFeature(t *testing.T) {
	off := newFake("stats")
	off.why = "stats are not enabled"
	m := New("", zerolog.Nop(), newFake("zaps"), off)

	_, err := m.Start("zaps", KindDrop, TriggerAdmin, "")
	assert.ErrorIs(t, err, ErrEnabled)
	assert.Contains(t, err.Error(), "ncli relay index rebuild zaps")
	_, err = m.Start("stats", KindRebuild, TriggerAdmin, "")
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.Contains(t, err.Error(), "stats are not enabled")
	_, err = m.Start("nope", KindRebuild, TriggerAdmin, "")
	assert.ErrorIs(t, err, ErrUnknownIndex)

	off.entries = 0
	j, err := m.Start("stats", KindDrop, TriggerAdmin, "")
	assert.ErrorIs(t, err, ErrNothingToDo)
	assert.True(t, j.Noop)
	assert.Equal(t, "stats: nothing to drop", j.Summary)
	assert.Zero(t, m.Running())
}

func TestStoreWritingJobsTakeTurns(t *testing.T) {
	a, b := newFake("zaps"), newFake("stats")
	search := newFake("search")
	search.writes = false
	m := New("", zerolog.Nop(), a, b, search)

	ja, err := m.Start("zaps", KindRebuild, TriggerAdmin, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return m.RunningJob("zaps").Phase == "scan" }, time.Second, time.Millisecond)
	jb, err := m.Start("stats", KindRebuild, TriggerAdmin, "")
	require.NoError(t, err)
	js, err := m.Start("search", KindRebuild, TriggerAdmin, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return m.RunningJob("search").Phase == "scan" }, time.Second, time.Millisecond, "search doesn't wait")
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, "waiting", m.RunningJob("stats").Phase, "one store writer at a time")

	a.release <- nil
	waitEnded(t, m, ja.ID)
	require.Eventually(t, func() bool { return m.RunningJob("stats").Phase == "scan" }, time.Second, time.Millisecond)
	b.release <- nil
	search.release <- nil
	waitEnded(t, m, jb.ID)
	waitEnded(t, m, js.ID)
}

func TestCancel(t *testing.T) {
	ix := newFake("zaps")
	m := New("", zerolog.Nop(), ix)
	j, err := m.Start("zaps", KindRebuild, TriggerAdmin, "")
	require.NoError(t, err)
	got, err := m.Cancel(j.ID)
	require.NoError(t, err)
	assert.Equal(t, "cancelling", got.Phase, "answers at once")
	done := waitEnded(t, m, j.ID)
	assert.Equal(t, StateCancelled, done.State)
	assert.Equal(t, []Kind{KindRebuild}, ix.aborted, "a cancelled rebuild is discarded")

	again, err := m.Cancel(j.ID)
	require.NoError(t, err)
	assert.True(t, again.Noop)
	assert.Equal(t, fmt.Sprintf("job %s already cancelled; nothing to cancel", j.ID), again.Summary)
	_, err = m.Cancel("nope")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestCancel_LiveSearchCatchUpCantBeStopped(t *testing.T) {
	ix := newFake("search")
	ix.writes = false
	m := New("", zerolog.Nop(), ix)
	j, err := m.Start("search", KindCatchUp, TriggerReconcile, "")
	require.NoError(t, err)
	_, err = m.Cancel(j.ID)
	assert.ErrorIs(t, err, ErrNotCancellable)
	ix.release <- nil
	waitEnded(t, m, j.ID)
}

func TestFailedJob(t *testing.T) {
	ix := newFake("zaps")
	m := New("", zerolog.Nop(), ix)
	j, _ := m.Start("zaps", KindCatchUp, TriggerReconcile, "")
	ix.release <- errors.New("disk full")
	done := waitEnded(t, m, j.ID)
	assert.Equal(t, StateFailed, done.State)
	assert.Equal(t, "disk full", done.Error)
	assert.Regexp(t, `^zaps: catch-up failed after .*: disk full$`, done.Summary)
}

func TestShutdownInterruptsAndARestartSaysSo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	ix := newFake("zaps")
	m := New(path, zerolog.Nop(), ix)
	j, err := m.Start("zaps", KindRebuild, TriggerReconcile, "never built")
	require.NoError(t, err)

	// What a crash leaves: the job on disk as running.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var onDisk []Job
	require.NoError(t, json.Unmarshal(data, &onDisk))
	require.Len(t, onDisk, 1)
	assert.Equal(t, StateRunning, onDisk[0].State)

	reloaded := New(path, zerolog.Nop(), newFake("zaps"))
	last := reloaded.LastJob("zaps")
	require.NotNil(t, last)
	assert.Equal(t, StateInterrupted, last.State)
	assert.Equal(t, "zaps: rebuild ended with the relay process; it resumes on its own", last.Summary)

	// A clean stop records it the same way.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.Shutdown(ctx)
	got, _ := m.Get(j.ID)
	assert.Equal(t, StateInterrupted, got.State)
	_, err = m.Start("zaps", KindRebuild, TriggerAdmin, "")
	assert.Error(t, err, "no new jobs once stopping")
}

func TestHistoryIsKeptPerIndex(t *testing.T) {
	a, b := newFake("zaps"), newFake("stats")
	m := New("", zerolog.Nop(), a, b)
	run := func(ix *fakeIndex) {
		j, err := m.Start(ix.name, KindCatchUp, TriggerReconcile, "")
		require.NoError(t, err)
		ix.release <- nil
		waitEnded(t, m, j.ID)
	}
	run(b)
	for range keptJobs + 5 {
		run(a)
	}
	assert.Len(t, m.List("zaps"), keptJobs)
	assert.Len(t, m.List("stats"), 1, "a busy index doesn't push another's history out")
	assert.Len(t, m.List(""), keptJobs+1)
}

func TestCount(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1 000", 1204331: "1 204 331", -18402: "-18 402"} {
		assert.Equal(t, want, Count(n))
	}
}

func TestReconciler_StartsWhatEachIndexNeeds(t *testing.T) {
	ix := newFake("zaps")
	ix.action = KindCatchUp
	idle := newFake("stats")
	m := New("", zerolog.Nop(), ix, idle)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := m.Reconcile(ctx)

	require.Eventually(t, func() bool { return m.RunningJob("zaps") != nil }, time.Second, time.Millisecond)
	j := m.RunningJob("zaps")
	assert.Equal(t, KindCatchUp, j.Kind)
	assert.Equal(t, TriggerReconcile, j.Trigger)
	assert.Nil(t, m.RunningJob("stats"), "nothing to do there")
	ix.release <- nil
	waitEnded(t, m, j.ID)
	r.Nudge()
	time.Sleep(20 * time.Millisecond)
	assert.Nil(t, m.RunningJob("zaps"), "done: the index needs nothing more")
}

func TestReconciler_BacksOffAfterAFailure(t *testing.T) {
	prev := reconcileBackoff
	reconcileBackoff = []time.Duration{time.Hour}
	t.Cleanup(func() { reconcileBackoff = prev })

	ix := newFake("zaps")
	ix.action = KindRebuild
	m := New("", zerolog.Nop(), ix)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := m.Reconcile(ctx)
	require.Eventually(t, func() bool { return m.RunningJob("zaps") != nil }, time.Second, time.Millisecond)
	ix.mu.Lock()
	ix.action = KindRebuild // still needed after the failure
	ix.mu.Unlock()
	ix.release <- errors.New("boom")
	require.Eventually(t, func() bool { return !r.RetryAt("zaps").IsZero() }, time.Second, time.Millisecond)
	ix.mu.Lock()
	ix.action = KindRebuild
	ix.mu.Unlock()
	r.Nudge()
	time.Sleep(20 * time.Millisecond)
	assert.Nil(t, m.RunningJob("zaps"), "not retried before the backoff")
	info := m.Indexes(r)
	assert.NotZero(t, info[0].RetryAt)

	// An admin needn't wait.
	j, err := m.Start("zaps", KindRebuild, TriggerAdmin, "")
	require.NoError(t, err)
	ix.release <- nil
	waitEnded(t, m, j.ID)
}

func TestPanickingJobFailsInsteadOfTheRelay(t *testing.T) {
	prev := reconcileBackoff
	reconcileBackoff = []time.Duration{time.Hour}
	t.Cleanup(func() { reconcileBackoff = prev })

	ix := newFake("zaps")
	ix.action, ix.panics = KindRebuild, "runtime error: index out of range [-1]"
	m := New("", zerolog.Nop(), ix)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := m.Reconcile(ctx)
	require.Eventually(t, func() bool { return !r.RetryAt("zaps").IsZero() }, time.Second, time.Millisecond)
	last := m.LastJob("zaps")
	require.NotNil(t, last)
	assert.Equal(t, StateFailed, last.State)
	assert.Equal(t, "panic: runtime error: index out of range [-1]", last.Error)
	assert.Nil(t, m.RunningJob("zaps"), "retried after the backoff, not at once")

	// The writer was handed back: another store job runs.
	ix.mu.Lock()
	ix.panics = nil
	ix.mu.Unlock()
	j, err := m.Start("zaps", KindRebuild, TriggerAdmin, "")
	require.NoError(t, err)
	ix.release <- nil
	assert.Equal(t, StateSucceeded, waitEnded(t, m, j.ID).State)
}

// crashWith starts a reconcile job on a fresh manager over path and leaves
// it running, as a relay that dies mid-job leaves its file.
func crashWith(t *testing.T, path string) {
	ix := newFake("zaps")
	ix.action = KindRebuild
	m := New(path, zerolog.Nop(), ix)
	require.Empty(t, m.Parked("zaps"), "not parked before the job runs")
	j, err := m.Start("zaps", KindRebuild, TriggerReconcile, "never built")
	require.NoError(t, err)
	t.Cleanup(func() { ix.release <- nil; waitEnded(t, m, j.ID) })
}

func TestCrashLoopIsParked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	for range crashLoopRuns - 1 {
		crashWith(t, path)
	}
	ix := newFake("zaps")
	ix.action = KindRebuild
	m := New(path, zerolog.Nop(), ix)
	assert.Empty(t, m.Parked("zaps"), "two deaths in a row still resume")
	j, err := m.Start("zaps", KindRebuild, TriggerReconcile, "never built")
	require.NoError(t, err)
	prev, prevIx, prevID := m, ix, j.ID
	t.Cleanup(func() { prevIx.release <- nil; waitEnded(t, prev, prevID) })

	ix = newFake("zaps")
	ix.action = KindRebuild
	m = New(path, zerolog.Nop(), ix)
	last := m.LastJob("zaps")
	require.NotNil(t, last)
	assert.Equal(t, StateFailed, last.State)
	assert.True(t, last.Parked)
	assert.Contains(t, m.Parked("zaps"), "the relay stopped 3 times in a row during rebuild")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := m.Reconcile(ctx)
	r.Nudge()
	time.Sleep(20 * time.Millisecond)
	assert.Nil(t, m.RunningJob("zaps"), "a parked index isn't started on its own")
	info := m.Indexes(r)
	assert.NotEmpty(t, info[0].Parked)
	assert.Zero(t, info[0].RetryAt)

	again := New(path, zerolog.Nop(), newFake("zaps"))
	assert.NotEmpty(t, again.Parked("zaps"), "still parked after another restart")

	// An admin starts it over.
	j, err = m.Start("zaps", KindRebuild, TriggerAdmin, "")
	require.NoError(t, err)
	ix.release <- nil
	waitEnded(t, m, j.ID)
	assert.Empty(t, m.Parked("zaps"))
}

func TestCrashLoopNeedsDeathsInARow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	crashWith(t, path)
	crashWith(t, path)

	// A clean stop breaks the run.
	ix := newFake("zaps")
	m := New(path, zerolog.Nop(), ix)
	_, err := m.Start("zaps", KindRebuild, TriggerReconcile, "")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.Shutdown(ctx)

	crashWith(t, path)
	assert.Empty(t, New(path, zerolog.Nop(), newFake("zaps")).Parked("zaps"))
}

func TestReconciler_SearchWaitsForTheStoreIndexes(t *testing.T) {
	zaps, search := newFake("zaps"), newFake("search")
	search.writes = false
	zaps.action, search.action = KindRebuild, KindRebuild
	m := New("", zerolog.Nop(), search, zaps)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := m.Reconcile(ctx)

	require.Eventually(t, func() bool { return m.RunningJob("zaps") != nil }, time.Second, time.Millisecond)
	r.Nudge()
	time.Sleep(20 * time.Millisecond)
	assert.Nil(t, m.RunningJob("search"), "search scores with zaps, so it waits")
	zaps.release <- nil
	require.Eventually(t, func() bool { return m.RunningJob("search") != nil }, time.Second, time.Millisecond)
	search.release <- nil
}
