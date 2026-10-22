package jobs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeMeili is the slice of the Meilisearch API the search index uses:
// indexes, settings, documents, swaps and tasks, all completing at once.
type fakeMeili struct {
	mu        sync.Mutex
	indexes   map[string]map[string]json.RawMessage
	task      int64
	failSwap  bool
	swapCalls int
}

func newFakeMeili(t *testing.T) (*fakeMeili, string) {
	t.Helper()
	f := &fakeMeili{indexes: map[string]map[string]json.RawMessage{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *fakeMeili) docs(index string) map[string]json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]json.RawMessage{}
	for k, v := range f.indexes[index] {
		out[k] = v
	}
	return out
}

func (f *fakeMeili) put(index, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.indexes[index] == nil {
		f.indexes[index] = map[string]json.RawMessage{}
	}
	f.indexes[index][id] = json.RawMessage(`{"id":"` + id + `"}`)
}

func (f *fakeMeili) taskInfo(w http.ResponseWriter, index string) {
	f.task++
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"taskUid": f.task, "indexUid": index, "status": "enqueued"})
}

func (f *fakeMeili) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case parts[0] == "tasks" && len(parts) == 2:
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"uid":`+parts[1]+`,"status":"succeeded"}`)
	case parts[0] == "swap-indexes":
		f.swapCalls++
		if f.failSwap {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"boom","code":"internal","type":"internal","link":""}`)
			return
		}
		var body []struct{ Indexes []string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		a, b := body[0].Indexes[0], body[0].Indexes[1]
		f.indexes[a], f.indexes[b] = f.indexes[b], f.indexes[a]
		f.taskInfo(w, "")
	case parts[0] == "indexes" && len(parts) == 1 && r.Method == http.MethodPost:
		var body struct{ Uid string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.indexes[body.Uid] == nil {
			f.indexes[body.Uid] = map[string]json.RawMessage{}
		}
		f.taskInfo(w, body.Uid)
	case parts[0] == "indexes" && len(parts) == 2:
		name := parts[1]
		switch r.Method {
		case http.MethodGet:
			if _, ok := f.indexes[name]; !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"message":"not found","code":"index_not_found","type":"invalid_request","link":""}`)
				return
			}
			_, _ = io.WriteString(w, `{"uid":"`+name+`","primaryKey":"id"}`)
		case http.MethodDelete:
			delete(f.indexes, name)
			f.taskInfo(w, name)
		}
	case parts[0] == "indexes" && len(parts) == 3 && parts[2] == "stats":
		docs, ok := f.indexes[parts[1]]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"not found","code":"index_not_found","type":"invalid_request","link":""}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"numberOfDocuments": len(docs), "isIndexing": false})
	case parts[0] == "indexes" && len(parts) == 3 && parts[2] == "settings":
		f.taskInfo(w, parts[1])
	case parts[0] == "indexes" && len(parts) == 3 && parts[2] == "documents":
		name := parts[1]
		if f.indexes[name] == nil {
			f.indexes[name] = map[string]json.RawMessage{}
		}
		switch r.Method {
		case http.MethodPost, http.MethodPut:
			var docs []json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&docs)
			for _, d := range docs {
				var id struct{ ID string }
				_ = json.Unmarshal(d, &id)
				f.indexes[name][id.ID] = d
			}
		case http.MethodDelete:
			f.indexes[name] = map[string]json.RawMessage{}
		}
		f.taskInfo(w, name)
	default:
		http.NotFound(w, r)
	}
}

func searchIndexWithProfiles(t *testing.T, url string, n int) (*SearchIndex, []string) {
	t.Helper()
	store := newStore(t, 2) // max_limit 2: the rebuild must page
	var pubkeys []string
	for i := 0; i < n; i++ {
		ev := signed(t, 10+i, 0, uint64(100+i))
		insert(t, store, ev)
		pubkeys = append(pubkeys, ev.PubKey)
	}
	return &SearchIndex{Store: store, Host: url, IndexName: "profiles"}, pubkeys
}

func TestSearchRebuildFreshSwapsInACleanIndex(t *testing.T) {
	meili, url := newFakeMeili(t)
	ix, pubkeys := searchIndexWithProfiles(t, url, 5)
	meili.put("profiles", "stale-profile")

	var scanned, indexed int
	if err := ix.Rebuild(context.Background(), Options{Fresh: true}, func(s, i int) { scanned, indexed = s, i }); err != nil {
		t.Fatal(err)
	}
	live := meili.docs("profiles")
	if _, stale := live["stale-profile"]; stale {
		t.Error("fresh rebuild kept a profile no longer stored")
	}
	for _, pk := range pubkeys {
		if _, ok := live[pk]; !ok {
			t.Errorf("profile %s missing after rebuild", pk[:8])
		}
	}
	if len(live) != len(pubkeys) {
		t.Errorf("live index holds %d docs, want %d", len(live), len(pubkeys))
	}
	if _, left := meili.indexes["profiles__next"]; left {
		t.Error("the swapped-out index was not deleted")
	}
	if meili.swapCalls != 1 || scanned != 5 || indexed != 5 {
		t.Errorf("swaps=%d scanned=%d indexed=%d", meili.swapCalls, scanned, indexed)
	}
}

func TestSearchRebuildInPlaceUpserts(t *testing.T) {
	meili, url := newFakeMeili(t)
	ix, pubkeys := searchIndexWithProfiles(t, url, 3)
	meili.put("profiles", "other")
	if err := ix.Rebuild(context.Background(), Options{}, nil); err != nil {
		t.Fatal(err)
	}
	live := meili.docs("profiles")
	if _, ok := live["other"]; !ok || len(live) != len(pubkeys)+1 {
		t.Errorf("in-place rebuild should only upsert: %d docs", len(live))
	}
	if meili.swapCalls != 0 {
		t.Error("in-place rebuild swapped indexes")
	}
}

func TestSearchRebuildSwapFailureKeepsLiveIndex(t *testing.T) {
	meili, url := newFakeMeili(t)
	ix, _ := searchIndexWithProfiles(t, url, 2)
	meili.put("profiles", "kept")
	meili.failSwap = true
	if err := ix.Rebuild(context.Background(), Options{Fresh: true}, nil); err == nil {
		t.Fatal("a failed swap was reported as success")
	}
	if _, ok := meili.docs("profiles")["kept"]; !ok {
		t.Error("the live index changed although the swap failed")
	}
}

func TestSearchDropEmptiesLiveIndex(t *testing.T) {
	meili, url := newFakeMeili(t)
	ix, _ := searchIndexWithProfiles(t, url, 1)
	meili.put("profiles", "a")
	if err := ix.Drop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(meili.docs("profiles")); n != 0 {
		t.Errorf("after drop %d docs remain", n)
	}
}

// Estimate used to call CountDocuments on a client Initialize never set
// up, panicking every index listing on a relay with search enabled.
func TestSearchEstimate(t *testing.T) {
	meili, url := newFakeMeili(t)
	meili.put("profiles", "a")
	meili.put("profiles", "b")
	ix := &SearchIndex{Host: url, IndexName: "profiles"}
	if got := ix.Estimate(); got != 2 {
		t.Errorf("live index = %d, want 2", got)
	}

	ix.Host = "http://127.0.0.1:1"
	if got := ix.Estimate(); got != -1 {
		t.Errorf("unreachable = %d, want -1", got)
	}
}

func TestIndexesRouteWithSearchEnabled(t *testing.T) {
	_, url := newFakeMeili(t)
	ix, _ := searchIndexWithProfiles(t, url, 1)
	_, h := newRoutes(t, ix)
	if code, out := call(t, h, "GET", "/admin/api/indexes", ""); code != 200 || out["indexes"] == nil {
		t.Fatalf("GET /admin/api/indexes = %d %v", code, out)
	}
	if code, out := call(t, h, "POST", "/admin/api/indexes/search/rebuild", `{"dry_run":true}`); code != 200 || out["estimate"] != float64(-1) {
		t.Fatalf("dry run = %d %v, want 200 with estimate -1 (no index yet)", code, out)
	}
}
