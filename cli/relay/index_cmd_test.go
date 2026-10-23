package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/relay/jobs"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var renderNow = time.Unix(1_800_000_000, 0)

func TestRenderIndexes(t *testing.T) {
	running := &jobs.Job{ID: "ab12", Index: "zaps", Kind: jobs.KindCatchUp, Trigger: jobs.TriggerReconcile, State: jobs.StateRunning,
		Reason: "1 234 event(s) stored since it was last current", Done: 50, Total: 100, Applied: 7, StartedAt: renderNow.Unix() - 10}
	last := &jobs.Job{Kind: jobs.KindRebuild, State: jobs.StateSucceeded, FinishedAt: renderNow.Unix() - 180}
	list := []jobs.IndexInfo{
		{Status: jobs.Status{Name: "search", Enabled: true, State: jobs.StateUnreachable, Entries: 1204, Reason: "Meilisearch can't be reached", Error: "dial tcp: refused"}, Last: last},
		{Status: jobs.Status{Name: "zaps", Enabled: true, State: jobs.StateCatchingUp, Entries: 18402, Behind: 1234}, Running: running},
		{Status: jobs.Status{Name: "stats", State: jobs.StateDisabled, HasData: true, Entries: 12033, Hint: "it still holds 12 033 entries; remove them with ncli relay index drop stats"}},
		{Status: jobs.Status{Name: "extra", Enabled: true, State: jobs.StateNeedsRebuild, Action: jobs.KindRebuild, Reason: "never built"}, RetryAt: renderNow.Unix() + 300},
		{Status: jobs.Status{Name: "parked", Enabled: true, State: jobs.StateNeedsRebuild, Action: jobs.KindRebuild, Reason: "never built"}, Parked: "the relay stopped 3 times in a row"},
	}
	var buf bytes.Buffer
	renderIndexes(&buf, list, renderNow)
	out := buf.String()
	for _, want := range []string{
		"INDEX   STATE          ENTRIES  BEHIND  LAST JOB",
		"search  unreachable    1 204    -       rebuild succeeded 3m ago",
		"zaps    catching up    18 402   1 234   -",
		"stats   disabled       12 033   -       -",
		"→ search: Meilisearch can't be reached (dial tcp: refused)",
		"→ zaps: catch-up 50%, ~10s left, 7 applied (job ab12, started by the relay: 1 234 event(s) stored since it was last current)",
		"→ stats: it still holds 12 033 entries; remove them with ncli relay index drop stats",
		"→ extra: rebuild needed (never built); the last try failed, retrying in 5m",
		"→ parked: rebuild needed (never built); parked: the relay stopped 3 times in a row",
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "idle")
}

func TestRenderIndexes_ReadyIndexHasNoNote(t *testing.T) {
	var buf bytes.Buffer
	renderIndexes(&buf, []jobs.IndexInfo{{Status: jobs.Status{Name: "zaps", Enabled: true, State: jobs.StateReady, Entries: 3}}}, renderNow)
	assert.Equal(t, "INDEX  STATE  ENTRIES  BEHIND  LAST JOB\nzaps   ready  3        0       -\n", buf.String())
}

func TestProgressText(t *testing.T) {
	start := renderNow.Unix() - 20
	for _, c := range []struct {
		job  jobs.Job
		want string
	}{
		{jobs.Job{Phase: "waiting"}, "waiting for another index job to finish"},
		{jobs.Job{Phase: "cancelling"}, "cancelling"},
		{jobs.Job{Phase: "waiting for Meilisearch: refused"}, "waiting for Meilisearch: refused"},
		{jobs.Job{}, "starting"},
		{jobs.Job{Phase: "scan", Done: 25, Total: 100, StartedAt: start}, "25%, ~1m0s left"},
		{jobs.Job{Phase: "deletes", Done: 3, Total: 3, Applied: 3, StartedAt: start}, "deletes, 100%, 3 applied"},
		{jobs.Job{Phase: "scan", Done: 300, Total: 100}, "100%"},
		{jobs.Job{Phase: "free", Applied: 12345}, "free, 12 345 applied"},
	} {
		assert.Equal(t, c.want, progressText(c.job, renderNow), "%+v", c.job)
	}
}

func TestPlanText(t *testing.T) {
	assert.Equal(t, "zaps: rebuild — reads 1 204 331 stored events, 18 402 entries now; builds beside",
		planText(jobs.Plan{Index: "zaps", Kind: jobs.KindRebuild, State: jobs.StateReady, Scan: 1204331, Entries: 18402, Effect: "builds beside"}))
	assert.Equal(t, "zaps: rebuild — reads 9 stored events, 0 entries now (never built); x",
		planText(jobs.Plan{Index: "zaps", Kind: jobs.KindRebuild, State: jobs.StateNeedsRebuild, Reason: "never built", Scan: 9, Effect: "x"}))
	assert.Equal(t, "stats: drop — removes 3 entries",
		planText(jobs.Plan{Index: "stats", Kind: jobs.KindDrop, Effect: "removes 3 entries"}))
}

func TestRenderJobs(t *testing.T) {
	list := []jobs.Job{
		{ID: "r1", Index: "zaps", Kind: jobs.KindRebuild, Trigger: jobs.TriggerAdmin, State: jobs.StateRunning, Done: 1, Total: 4, StartedAt: renderNow.Unix() - 3},
		{ID: "f1", Index: "stats", Kind: jobs.KindCatchUp, Trigger: jobs.TriggerReconcile, State: jobs.StateFailed, StartedAt: renderNow.Unix() - 3600, DurationMs: 1500, Summary: "stats: catch-up failed after 1.5s: disk full"},
	}
	var buf bytes.Buffer
	renderJobs(&buf, list, renderNow)
	out := buf.String()
	assert.Contains(t, out, "ID  INDEX  KIND      BY         STATE    STARTED   TOOK  SUMMARY")
	assert.Contains(t, out, "r1  zaps   rebuild   admin      running  just now  -     25%")
	assert.Contains(t, out, "f1  stats  catch-up  reconcile  failed   1h ago    1.5s  stats: catch-up failed after 1.5s: disk full")

	buf.Reset()
	renderJobs(&buf, nil, renderNow)
	assert.Equal(t, " (no jobs)\n", buf.String())
}

func TestRenderJob(t *testing.T) {
	var buf bytes.Buffer
	renderJob(&buf, jobs.Job{ID: "a", Index: "zaps", Kind: jobs.KindRebuild, Trigger: jobs.TriggerAdmin, State: jobs.StateSucceeded,
		EntriesBefore: 18402, EntriesAfter: 18977, DurationMs: 42000, Summary: "zaps: rebuilt in 42s, 18 402 → 18 977 entries", StartedAt: renderNow.Unix()}, renderNow)
	out := buf.String()
	assert.Contains(t, out, " Index:    zaps (rebuild, started by admin)")
	assert.Contains(t, out, " Result:   zaps: rebuilt in 42s, 18 402 → 18 977 entries")
	assert.Contains(t, out, " Entries:  18 402 → 18 977")
	assert.Contains(t, out, " Took:     42s")
}

func TestRelativeTimeAt(t *testing.T) {
	assert.Equal(t, "-", relativeTimeAt(0, renderNow))
	assert.Equal(t, "just now", relativeTimeAt(renderNow.Unix()-2, renderNow))
	assert.Equal(t, "3m ago", relativeTimeAt(renderNow.Unix()-180, renderNow))
	assert.Equal(t, "in 5m", relativeTimeAt(renderNow.Unix()+300, renderNow))
	assert.Equal(t, "2d ago", relativeTimeAt(renderNow.Unix()-49*3600, renderNow))
}

// fakeAdmin serves the index API with scripted jobs.
type fakeAdmin struct {
	mu      sync.Mutex
	indexes []jobs.IndexInfo
	// polls is how many GETs a job takes to end, and how it ends.
	polls map[string]int
	ends  map[string]jobs.State
	jobs  map[string]jobs.Job
	start func(index string, kind jobs.Kind) (jobs.Job, int)
	seen  []string
}

func newFakeAdmin(t *testing.T) (*fakeAdmin, string) {
	f := &fakeAdmin{polls: map[string]int{}, ends: map[string]jobs.State{}, jobs: map[string]jobs.Job{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	withViper(t, map[string]any{"nip11.privkey": testPrivKey})
	return f, srv.URL
}

func (f *fakeAdmin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, r.Method+" "+r.URL.Path)
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/admin/api/indexes":
		reply(200, map[string]any{"indexes": f.indexes})
	case len(parts) == 5 && parts[2] == "indexes":
		name, kind := parts[3], jobs.Kind(parts[4])
		var body struct {
			DryRun bool `json:"dry_run"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.DryRun {
			reply(200, jobs.Plan{Index: name, Kind: kind, Entries: 7, Scan: 100, Effect: "e", DryRun: true})
			return
		}
		job, status := f.start(name, kind)
		if job.ID != "" {
			f.jobs[job.ID] = job
		}
		reply(status, job)
	case len(parts) == 4 && parts[2] == "jobs" && r.Method == http.MethodGet:
		j, ok := f.jobs[parts[3]]
		if !ok {
			reply(404, map[string]string{"error": "no such job"})
			return
		}
		if f.polls[j.ID] > 0 {
			f.polls[j.ID]--
			j.Done++
		} else if st, ok := f.ends[j.ID]; ok {
			j.State = st
			j.Summary = j.Index + ": ended " + string(st)
		}
		f.jobs[j.ID] = j
		reply(200, j)
	case len(parts) == 4 && parts[2] == "jobs" && r.Method == http.MethodDelete:
		j := f.jobs[parts[3]]
		j.Phase = "cancelling"
		f.ends[j.ID] = jobs.StateCancelled
		reply(202, j)
	default:
		http.NotFound(w, r)
	}
}

// runIndexCmd runs an index or jobs command against url and returns its
// stdout and error.
func runIndexCmd(t *testing.T, url string, run func(*cobra.Command, []string) error, args []string, flags map[string]string) (string, error) {
	t.Helper()
	cmd := &cobra.Command{Use: "x"}
	cmd.Flags().String("admin-url", url, "")
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("quiet", true, "")
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().Bool("yes", false, "")
	cmd.Flags().Bool("fresh", false, "")
	cmd.Flags().Bool("reverify", false, "")
	cmd.Flags().String("index", "", "")
	addFollowFlags(cmd)
	for k, v := range flags {
		require.NoError(t, cmd.Flags().Set(k, v))
	}
	r, w, err := os.Pipe()
	require.NoError(t, err)
	stdout := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	runErr := run(cmd, args)
	_ = w.Close()
	os.Stdout = stdout
	return <-done, runErr
}

func codeOf(err error) common.ErrorCode {
	var ce *common.CLIError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

func TestIndexRebuild_JSONReturnsTheJobAtOnce(t *testing.T) {
	f, url := newFakeAdmin(t)
	f.start = func(index string, kind jobs.Kind) (jobs.Job, int) {
		return jobs.Job{ID: "j1", Index: index, Kind: kind, State: jobs.StateRunning, Summary: "zaps: rebuild started"}, 202
	}
	out, err := runIndexCmd(t, url, runIndexRebuild, []string{"zaps"}, map[string]string{"json": "true"})
	require.NoError(t, err)
	var j jobs.Job
	require.NoError(t, json.Unmarshal([]byte(out), &j))
	assert.Equal(t, "j1", j.ID)
	assert.Equal(t, jobs.StateRunning, j.State)
}

func TestIndexRebuild_WaitPrintsTheFinalJob(t *testing.T) {
	f, url := newFakeAdmin(t)
	f.start = func(index string, kind jobs.Kind) (jobs.Job, int) {
		f.polls["j1"], f.ends["j1"] = 1, jobs.StateSucceeded
		return jobs.Job{ID: "j1", Index: index, Kind: kind, State: jobs.StateRunning}, 202
	}
	out, err := runIndexCmd(t, url, runIndexRebuild, []string{"zaps"}, map[string]string{"json": "true", "wait": "true"})
	require.NoError(t, err)
	var j jobs.Job
	require.NoError(t, json.Unmarshal([]byte(out), &j))
	assert.Equal(t, jobs.StateSucceeded, j.State)
}

func TestIndexRebuild_FailedJobFailsTheCommand(t *testing.T) {
	f, url := newFakeAdmin(t)
	f.start = func(index string, kind jobs.Kind) (jobs.Job, int) {
		f.ends["j1"] = jobs.StateFailed
		return jobs.Job{ID: "j1", Index: index, Kind: kind, State: jobs.StateRunning}, 202
	}
	out, err := runIndexCmd(t, url, runIndexRebuild, []string{"zaps"}, map[string]string{"json": "true", "wait": "true"})
	assert.Equal(t, common.CodeInternal, codeOf(err))
	var ce *common.CLIError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "j1", ce.Input)
	assert.Contains(t, out, `"state": "failed"`, "the final job is still printed")
}

func TestIndexRebuild_FollowStreamsNDJSON(t *testing.T) {
	f, url := newFakeAdmin(t)
	f.start = func(index string, kind jobs.Kind) (jobs.Job, int) {
		f.polls["j1"], f.ends["j1"] = 2, jobs.StateSucceeded
		return jobs.Job{ID: "j1", Index: index, Kind: kind, State: jobs.StateRunning}, 202
	}
	out, err := runIndexCmd(t, url, runIndexRebuild, []string{"zaps"}, map[string]string{"json": "true", "follow": "true"})
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.GreaterOrEqual(t, len(lines), 3)
	var last jobs.Job
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &last))
	assert.Equal(t, jobs.StateSucceeded, last.State, "ends with the final job")
	for _, l := range lines {
		assert.True(t, json.Valid([]byte(l)), l)
	}
}

func TestIndexRebuild_TimeoutIsRetryable(t *testing.T) {
	f, url := newFakeAdmin(t)
	f.start = func(index string, kind jobs.Kind) (jobs.Job, int) {
		f.polls["j1"] = 1000
		return jobs.Job{ID: "j1", Index: index, Kind: kind, State: jobs.StateRunning}, 202
	}
	_, err := runIndexCmd(t, url, runIndexRebuild, []string{"zaps"}, map[string]string{"json": "true", "wait": "true", "timeout": "1500ms"})
	assert.Equal(t, common.CodeConflict, codeOf(err))
	assert.Contains(t, err.Error(), "keeps running")
}

func TestIndexRebuild_TextSaysWhatHappened(t *testing.T) {
	f, url := newFakeAdmin(t)
	f.start = func(index string, kind jobs.Kind) (jobs.Job, int) {
		f.ends["j1"] = jobs.StateSucceeded
		return jobs.Job{ID: "j1", Index: index, Kind: kind, State: jobs.StateRunning}, 202
	}
	out, err := runIndexCmd(t, url, runIndexRebuild, []string{"zaps"}, nil)
	require.NoError(t, err)
	assert.Contains(t, out, "zaps: ended succeeded", "the summary, on stdout")
}

func TestIndexRebuild_NoopSaysSo(t *testing.T) {
	f, url := newFakeAdmin(t)
	f.start = func(index string, kind jobs.Kind) (jobs.Job, int) {
		return jobs.Job{ID: "j0", Index: index, Kind: kind, State: jobs.StateRunning, Noop: true, Summary: "zaps: already running rebuild (job j0)"}, 200
	}
	out, err := runIndexCmd(t, url, runIndexRebuild, []string{"zaps"}, map[string]string{"json": "true"})
	require.NoError(t, err)
	assert.Contains(t, out, `"noop": true`)
}

func TestIndexRebuild_AllSkipsDisabledOutLoud(t *testing.T) {
	f, url := newFakeAdmin(t)
	f.indexes = []jobs.IndexInfo{
		{Status: jobs.Status{Name: "search", Unavailable: "search is not enabled on this relay"}},
		{Status: jobs.Status{Name: "zaps", Enabled: true}},
	}
	f.start = func(index string, kind jobs.Kind) (jobs.Job, int) {
		return jobs.Job{ID: "j-" + index, Index: index, Kind: kind, State: jobs.StateRunning}, 202
	}
	out, err := runIndexCmd(t, url, runIndexRebuild, []string{"all"}, map[string]string{"json": "true"})
	require.NoError(t, err)
	var got struct {
		Jobs    []jobs.Job `json:"jobs"`
		Skipped []skip     `json:"skipped"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got.Jobs, 1)
	assert.Equal(t, "zaps", got.Jobs[0].Index)
	assert.Equal(t, []skip{{"search", "search is not enabled on this relay"}}, got.Skipped)
}

func TestIndexRebuild_FreshWasRemoved(t *testing.T) {
	_, url := newFakeAdmin(t)
	_, err := runIndexCmd(t, url, runIndexRebuild, []string{"search"}, map[string]string{"fresh": "true"})
	assert.Equal(t, common.CodeUsage, codeOf(err))
}

func TestIndexDrop_RefusedWithoutYesInAScript(t *testing.T) {
	f, url := newFakeAdmin(t)
	f.start = func(string, jobs.Kind) (jobs.Job, int) {
		t.Fatal("nothing may start before confirming")
		return jobs.Job{}, 0
	}
	_, err := runIndexCmd(t, url, runIndexDrop, []string{"stats"}, map[string]string{"json": "true"})
	assert.Equal(t, common.CodeUsage, codeOf(err))
	assert.Contains(t, err.Error(), "stats (7 entries)", "names what it would remove")
}

func TestIndexDrop_NothingToDrop(t *testing.T) {
	f, url := newFakeAdmin(t)
	f.start = func(index string, kind jobs.Kind) (jobs.Job, int) {
		return jobs.Job{Index: index, Kind: kind, State: jobs.StateSucceeded, Noop: true, Summary: "stats: nothing to drop"}, 200
	}
	out, err := runIndexCmd(t, url, runIndexDrop, []string{"stats"}, map[string]string{"yes": "true"})
	require.NoError(t, err)
	assert.Contains(t, out, "stats: nothing to drop")
}

func TestJobsCancel_WaitsForTheJobToStop(t *testing.T) {
	f, url := newFakeAdmin(t)
	f.jobs["j1"] = jobs.Job{ID: "j1", Index: "zaps", Kind: jobs.KindRebuild, State: jobs.StateRunning}
	out, err := runIndexCmd(t, url, runJobsCancel, []string{"j1"}, nil)
	require.NoError(t, err)
	assert.Contains(t, out, "zaps: ended cancelled")
}
