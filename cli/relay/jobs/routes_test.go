package jobs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type muxRouter struct{ mux *http.ServeMux }

func (m muxRouter) Handle(pattern string, h http.HandlerFunc) { m.mux.HandleFunc(pattern, h) }

func newRoutes(t *testing.T, indexes ...Index) (*Manager, http.Handler) {
	t.Helper()
	m := New("", zerolog.Nop(), indexes...)
	mux := http.NewServeMux()
	m.Routes(muxRouter{mux}, nil)
	return m, mux
}

func call(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
	return w.Code, out
}

func TestRoutes_RebuildAnswersAtOnce(t *testing.T) {
	ix := newFake("zaps")
	m, h := newRoutes(t, ix)

	code, plan := call(t, h, "POST", "/admin/api/indexes/zaps/rebuild", `{"dry_run":true}`)
	assert.Equal(t, 200, code)
	assert.Equal(t, true, plan["dry_run"])
	assert.Equal(t, float64(5), plan["entries"])
	assert.Contains(t, plan["effect"], "keeps answering")
	assert.Zero(t, m.Running(), "a dry run starts nothing")

	code, job := call(t, h, "POST", "/admin/api/indexes/zaps/rebuild", ``)
	assert.Equal(t, http.StatusAccepted, code)
	assert.Equal(t, "running", job["state"])
	assert.Equal(t, "admin", job["trigger"])
	id := job["id"].(string)

	code, again := call(t, h, "POST", "/admin/api/indexes/zaps/rebuild", ``)
	assert.Equal(t, 200, code)
	assert.Equal(t, true, again["noop"])
	assert.Equal(t, id, again["id"])

	ix.release <- nil
	code, ended := call(t, h, "GET", "/admin/api/jobs/"+id+"?wait=5", ``)
	assert.Equal(t, 200, code)
	assert.Equal(t, "succeeded", ended["state"])
	assert.Contains(t, ended["summary"], "rebuilt")
}

func TestRoutes_Errors(t *testing.T) {
	off := newFake("stats")
	off.why = "stats are not enabled"
	busy := newFake("zaps")
	_, h := newRoutes(t, busy, off)
	_, err := (&Manager{}).Lookup("x")
	require.Error(t, err)

	for _, c := range []struct {
		method, path, body string
		code               int
		has                string
	}{
		{"POST", "/admin/api/indexes/nope/rebuild", ``, 404, "unknown index"},
		{"POST", "/admin/api/indexes/stats/rebuild", ``, 501, "stats are not enabled"},
		{"POST", "/admin/api/indexes/zaps/drop", ``, 400, "rebuild it instead"},
		{"POST", "/admin/api/indexes/zaps/drop", `{"dry_run":true}`, 400, "rebuild it instead"},
		{"POST", "/admin/api/indexes/zaps/rebuild", `{"fresh":true}`, 400, "fresh was removed"},
		{"POST", "/admin/api/indexes/zaps/rebuild", `{`, 400, "invalid request body"},
		{"GET", "/admin/api/jobs/nope", ``, 404, "no such job"},
		{"GET", "/admin/api/jobs?index=nope", ``, 404, "unknown index"},
		{"DELETE", "/admin/api/jobs/nope", ``, 404, "no such job"},
	} {
		code, out := call(t, h, c.method, c.path, c.body)
		assert.Equal(t, c.code, code, "%s %s", c.method, c.path)
		assert.Contains(t, out["error"], c.has, "%s %s", c.method, c.path)
	}
}

func TestRoutes_BusyNamesTheJobInTheWay(t *testing.T) {
	ix := newFake("zaps")
	m, h := newRoutes(t, ix)
	j, err := m.Start("zaps", KindReap, TriggerReconcile, "")
	require.NoError(t, err)
	// A reap isn't superseded by a catch-up; only an admin rebuild or drop
	// takes over a reconcile job.
	_, err = m.Start("zaps", KindCatchUp, TriggerAdmin, "")
	require.ErrorIs(t, err, ErrBusy)
	code, out := call(t, h, "POST", "/admin/api/indexes/zaps/rebuild", ``)
	assert.Equal(t, http.StatusAccepted, code, "an admin rebuild supersedes it")
	old, _ := m.Get(j.ID)
	assert.Equal(t, StateSuperseded, old.State)
	ix.release <- nil
	waitEnded(t, m, out["id"].(string))
}

func TestRoutes_DropNothingIsANoop(t *testing.T) {
	off := newFake("stats")
	off.why, off.entries = "off", 0
	_, h := newRoutes(t, off)
	code, out := call(t, h, "POST", "/admin/api/indexes/stats/drop", ``)
	assert.Equal(t, 200, code)
	assert.Equal(t, true, out["noop"])
	assert.Equal(t, "stats: nothing to drop", out["summary"])
}

func TestRoutes_CancelAndList(t *testing.T) {
	ix := newFake("zaps")
	m, h := newRoutes(t, ix, newFake("stats"))
	j, err := m.Start("zaps", KindRebuild, TriggerAdmin, "")
	require.NoError(t, err)

	code, out := call(t, h, "DELETE", "/admin/api/jobs/"+j.ID, ``)
	assert.Equal(t, http.StatusAccepted, code)
	assert.Equal(t, "cancelling", out["phase"])
	waitEnded(t, m, j.ID)
	code, out = call(t, h, "DELETE", "/admin/api/jobs/"+j.ID, ``)
	assert.Equal(t, 200, code)
	assert.Equal(t, true, out["noop"])

	code, out = call(t, h, "GET", "/admin/api/jobs?index=stats", ``)
	assert.Equal(t, 200, code)
	assert.Equal(t, []any{}, out["jobs"], "an empty list, never null")
	_, out = call(t, h, "GET", "/admin/api/jobs?index=zaps", ``)
	assert.Len(t, out["jobs"], 1)
}

func TestRoutes_IndexesShowTheRunningJob(t *testing.T) {
	ix := newFake("zaps")
	m, h := newRoutes(t, ix)
	j, err := m.Start("zaps", KindCatchUp, TriggerReconcile, "behind")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return m.RunningJob("zaps").Done == 7 }, time.Second, time.Millisecond)
	_, out := call(t, h, "GET", "/admin/api/indexes", ``)
	list := out["indexes"].([]any)
	zaps := list[0].(map[string]any)
	assert.Equal(t, "catching_up", zaps["state"])
	assert.Equal(t, j.ID, zaps["running"].(map[string]any)["id"])
	ix.release <- nil
	waitEnded(t, m, j.ID)
}

func TestRoutes_CancelOfLiveSearchIsUnsupported(t *testing.T) {
	ix := newFake("search")
	ix.writes = false
	m, h := newRoutes(t, ix)
	j, err := m.Start("search", KindCatchUp, TriggerReconcile, "")
	require.NoError(t, err)
	code, out := call(t, h, "DELETE", "/admin/api/jobs/"+j.ID, ``)
	assert.Equal(t, http.StatusUnprocessableEntity, code)
	assert.Contains(t, out["error"], "turn search off")
	ix.release <- nil
	waitEnded(t, m, j.ID)
}
