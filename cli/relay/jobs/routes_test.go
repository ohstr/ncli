package jobs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

type muxRouter struct{ mux *http.ServeMux }

func (m muxRouter) Handle(pattern string, h http.HandlerFunc) { m.mux.HandleFunc(pattern, h) }

func newRoutes(t *testing.T, indexes ...Index) (*Manager, http.Handler) {
	t.Helper()
	m := New("", zerolog.Nop(), indexes...)
	mux := http.NewServeMux()
	m.Routes(muxRouter{mux})
	return m, mux
}

func call(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestRoutesRebuildLifecycle(t *testing.T) {
	ix := &fakeIndex{name: "zaps", release: make(chan struct{})}
	m, h := newRoutes(t, ix, &fakeIndex{name: "search", why: "search is off"})

	if code, out := call(t, h, "POST", "/admin/api/indexes/zaps/rebuild", `{"dry_run":true}`); code != 200 || out["estimate"] != float64(42) || m.Running() != 0 {
		t.Fatalf("dry run = %d %v (running %d)", code, out, m.Running())
	}
	code, first := call(t, h, "POST", "/admin/api/indexes/zaps/rebuild", `{"fresh":true}`)
	if code != http.StatusAccepted || first["state"] != "running" {
		t.Fatalf("start = %d %v", code, first)
	}
	if code, again := call(t, h, "POST", "/admin/api/indexes/zaps/rebuild", `{"fresh":true}`); code != http.StatusOK || again["id"] != first["id"] {
		t.Errorf("repeat start = %d %v, want 200 with the same job", code, again)
	}
	code, busy := call(t, h, "POST", "/admin/api/indexes/zaps/drop", "")
	if code != http.StatusConflict || busy["job"] == nil {
		t.Errorf("drop while rebuilding = %d %v, want 409 naming the job", code, busy)
	}
	if code, _ := call(t, h, "POST", "/admin/api/indexes/zaps/rebuild", `{bad`); code != http.StatusBadRequest {
		t.Errorf("bad body = %d, want 400", code)
	}
	if code, _ := call(t, h, "POST", "/admin/api/indexes/nope/rebuild", `{}`); code != http.StatusNotFound {
		t.Errorf("unknown index = %d, want 404", code)
	}
	if code, out := call(t, h, "POST", "/admin/api/indexes/search/rebuild", `{}`); code != http.StatusNotImplemented || !strings.Contains(out["error"].(string), "search is off") {
		t.Errorf("unavailable index = %d %v, want 501 with the reason", code, out)
	}

	id := first["id"].(string)
	if code, got := call(t, h, "GET", "/admin/api/jobs/"+id, ""); code != 200 || got["id"] != id {
		t.Errorf("get job = %d %v", code, got)
	}
	if code, list := call(t, h, "GET", "/admin/api/jobs", ""); code != 200 || len(list["jobs"].([]any)) != 1 {
		t.Errorf("list = %d %v", code, list)
	}
	if code, idx := call(t, h, "GET", "/admin/api/indexes", ""); code != 200 || len(idx["indexes"].([]any)) != 2 {
		t.Errorf("indexes = %d %v", code, idx)
	}
	if code, c := call(t, h, "DELETE", "/admin/api/jobs/"+id, ""); code != 200 || c["state"] != "cancelled" {
		t.Errorf("cancel = %d %v", code, c)
	}
	if code, _ := call(t, h, "GET", "/admin/api/jobs/nope", ""); code != http.StatusNotFound {
		t.Errorf("unknown job = %d", code)
	}
	if code, _ := call(t, h, "DELETE", "/admin/api/jobs/nope", ""); code != http.StatusNotFound {
		t.Errorf("cancel unknown job = %d", code)
	}

	// Drop waits for its (quick) job and reports it finished.
	if code, d := call(t, h, "POST", "/admin/api/indexes/zaps/drop", ""); code != 200 || d["state"] != "succeeded" || d["kind"] != "drop" {
		t.Errorf("drop = %d %v", code, d)
	}
}
