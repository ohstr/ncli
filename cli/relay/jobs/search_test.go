package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common/meilisearch"
	"github.com/ohstr/nmilat/relay"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

// fakeMeili is the slice of the Meilisearch API the relay uses. Like the
// real one it is asynchronous: a write only lands when its task runs, which
// here is when someone waits on it, so a caller that doesn't wait never
// sees it applied. failTasks makes every task fail.
type fakeMeili struct {
	mu        sync.Mutex
	indexes   map[string]*fakeIx
	tasks     map[int64]func() error
	nextTask  int64
	clock     int
	failTasks bool
	down      bool
}

type fakeIx struct {
	docs    map[string]json.RawMessage
	created string
}

func newFakeMeili(t *testing.T) (*fakeMeili, string) {
	t.Helper()
	f := &fakeMeili{indexes: map[string]*fakeIx{}, tasks: map[int64]func() error{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *fakeMeili) docs(index string) map[string]json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]json.RawMessage{}
	if ix := f.indexes[index]; ix != nil {
		for k, v := range ix.docs {
			out[k] = v
		}
	}
	return out
}

func (f *fakeMeili) ensure(name string) *fakeIx {
	if f.indexes[name] == nil {
		f.clock++
		f.indexes[name] = &fakeIx{docs: map[string]json.RawMessage{}, created: fmt.Sprintf("2026-01-01T00:00:%02dZ", f.clock)}
	}
	return f.indexes[name]
}

// recreate drops an index and makes it again, empty, as an operator might.
func (f *fakeMeili) recreate(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.indexes, name)
	f.ensure(name)
}

func (f *fakeMeili) enqueue(w http.ResponseWriter, index string, op func() error) {
	f.nextTask++
	f.tasks[f.nextTask] = op
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"taskUid": f.nextTask, "indexUid": index, "status": "enqueued"})
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"message":"not found","code":"index_not_found","type":"invalid_request","link":""}`)
}

func (f *fakeMeili) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		hj, _ := w.(http.Hijacker)
		if conn, _, err := hj.Hijack(); err == nil {
			_ = conn.Close()
		}
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case parts[0] == "tasks" && len(parts) == 2:
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		status, msg := "succeeded", ""
		if op, ok := f.tasks[id]; ok {
			delete(f.tasks, id)
			if f.failTasks {
				status, msg = "failed", "boom"
			} else if err := op(); err != nil {
				status, msg = "failed", err.Error()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"uid": id, "status": status, "error": map[string]string{"code": "internal", "message": msg}})
	case parts[0] == "swap-indexes":
		var body []struct{ Indexes []string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		a, b := body[0].Indexes[0], body[0].Indexes[1]
		f.enqueue(w, "", func() error {
			f.indexes[a], f.indexes[b] = f.indexes[b], f.indexes[a]
			return nil
		})
	case parts[0] == "indexes" && len(parts) == 1 && r.Method == http.MethodPost:
		var body struct{ Uid string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.enqueue(w, body.Uid, func() error { f.ensure(body.Uid); return nil })
	case parts[0] == "indexes" && len(parts) == 2:
		name := parts[1]
		switch r.Method {
		case http.MethodGet:
			ix, ok := f.indexes[name]
			if !ok {
				notFound(w)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"uid": name, "primaryKey": "id", "createdAt": ix.created, "updatedAt": ix.created})
		case http.MethodDelete:
			f.enqueue(w, name, func() error {
				if _, ok := f.indexes[name]; !ok {
					return fmt.Errorf("index_not_found")
				}
				delete(f.indexes, name)
				return nil
			})
		}
	case parts[0] == "indexes" && len(parts) == 3 && parts[2] == "stats":
		ix, ok := f.indexes[parts[1]]
		if !ok {
			notFound(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"numberOfDocuments": len(ix.docs), "isIndexing": false})
	case parts[0] == "indexes" && len(parts) == 3 && parts[2] == "settings":
		name := parts[1]
		f.ensure(name)
		f.enqueue(w, name, func() error { return nil })
	case parts[0] == "indexes" && len(parts) >= 3 && parts[2] == "documents":
		name := parts[1]
		switch {
		case r.Method == http.MethodPost && len(parts) == 4 && parts[3] == "delete-batch":
			var ids []string
			_ = json.NewDecoder(r.Body).Decode(&ids)
			f.enqueue(w, name, func() error {
				for _, id := range ids {
					delete(f.ensure(name).docs, id)
				}
				return nil
			})
		case r.Method == http.MethodPost || r.Method == http.MethodPut:
			var docs []json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&docs)
			f.enqueue(w, name, func() error {
				ix := f.ensure(name)
				for _, d := range docs {
					var id struct{ ID string }
					_ = json.Unmarshal(d, &id)
					ix.docs[id.ID] = d
				}
				return nil
			})
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

// searchFixture is a search-enabled store with its indexer and the search
// index, against a fake Meilisearch.
type searchFixture struct {
	t     *testing.T
	meili *fakeMeili
	store *relay.EventStore
	ix    *SearchIndex
	m     *Manager
}

func newSearchFixture(t *testing.T) *searchFixture {
	f, url := newFakeMeili(t)
	store := openAt(t, filepath.Join(t.TempDir(), "notes.db"), relay.DerivedIndexes{Social: true})
	client := meilisearch.NewMeiliClient(url, "", "profiles")
	indexer, err := relay.NewSearchIndexer(store, client)
	require.NoError(t, err)
	ix := &SearchIndex{Store: store, Indexer: indexer, Host: url, IndexName: "profiles"}
	return &searchFixture{t: t, meili: f, store: store, ix: ix, m: New("", zerolog.Nop(), ix)}
}

func (s *searchFixture) drain() {
	for range 100 {
		more, err := s.ix.Indexer.Step(context.Background())
		require.NoError(s.t, err)
		if !more {
			return
		}
	}
	s.t.Fatal("search feed never drained")
}

func (s *searchFixture) status() Status {
	s.ix.mu.Lock()
	s.ix.checked = time.Time{}
	s.ix.mu.Unlock()
	return s.ix.Status()
}

func TestSearch_FirstRunRebuildsThenStaysLive(t *testing.T) {
	s := newSearchFixture(t)
	insert(t, s.store, signed(t, 1, 0, 100), signed(t, 2, 0, 101), signed(t, 3, 1, 102))
	st := s.status()
	assert.Equal(t, StateNeedsRebuild, st.State)
	assert.Equal(t, KindRebuild, st.Action)
	assert.Equal(t, "the Meilisearch index doesn't exist", st.Reason)

	done := runToEnd(t, s.m, "search", KindRebuild)
	require.Equal(t, StateSucceeded, done.State, done.Error)
	assert.Equal(t, int64(2), done.EntriesAfter)
	assert.Len(t, s.meili.docs("profiles"), 2)
	assert.Empty(t, s.meili.docs("profiles__next"), "the build index is removed")
	assert.Equal(t, StateReady, s.status().State)

	// Live: a new profile and a deletion reach Meilisearch through the feed.
	p := signed(t, 4, 0, 103)
	insert(t, s.store, p)
	s.drain()
	assert.Len(t, s.meili.docs("profiles"), 3)
	insert(t, s.store, signed(t, 4, 5, 104, []string{"e", p.ID}))
	s.drain()
	assert.NotContains(t, s.meili.docs("profiles"), p.PubKey, "a kind 5 removes it from search")
}

func TestSearch_ProfilesStoredDuringARebuildAreKept(t *testing.T) {
	s := newSearchFixture(t)
	insert(t, s.store, signed(t, 1, 0, 100))
	done := runToEnd(t, s.m, "search", KindRebuild)
	require.Equal(t, StateSucceeded, done.State, done.Error)
	// Stored after the build read the store, before the indexer resumed.
	insert(t, s.store, signed(t, 2, 0, 101))
	s.drain()
	assert.Len(t, s.meili.docs("profiles"), 2)
}

func TestSearch_ReplacedIndexIsRebuilt(t *testing.T) {
	s := newSearchFixture(t)
	insert(t, s.store, signed(t, 1, 0, 100))
	runToEnd(t, s.m, "search", KindRebuild)
	require.Equal(t, StateReady, s.status().State)

	s.meili.recreate("profiles")
	st := s.status()
	assert.Equal(t, KindRebuild, st.Action)
	assert.Equal(t, "the Meilisearch index was replaced", st.Reason)
	runToEnd(t, s.m, "search", KindRebuild)
	assert.Len(t, s.meili.docs("profiles"), 1)
	assert.Equal(t, StateReady, s.status().State)
}

func TestSearch_UnreachableLosesNothing(t *testing.T) {
	s := newSearchFixture(t)
	insert(t, s.store, signed(t, 1, 0, 100))
	runToEnd(t, s.m, "search", KindRebuild)

	s.meili.mu.Lock()
	s.meili.failTasks = true
	s.meili.mu.Unlock()
	insert(t, s.store, signed(t, 2, 0, 101))
	_, err := s.ix.Indexer.Step(context.Background())
	require.Error(t, err)
	st := s.status()
	assert.Equal(t, StateUnreachable, st.State)
	assert.Contains(t, st.Error, "boom")
	assert.Empty(t, st.Action, "nothing to start while it can't be reached")

	s.meili.mu.Lock()
	s.meili.failTasks = false
	s.meili.mu.Unlock()
	s.drain()
	assert.Len(t, s.meili.docs("profiles"), 2, "the profile waited in the store")
	assert.Equal(t, StateReady, s.status().State)
}

func TestSearch_ScoringChangeRebuilds(t *testing.T) {
	s := newSearchFixture(t)
	runToEnd(t, s.m, "search", KindRebuild)
	require.NoError(t, s.store.Db().Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(searchState)
		if err != nil {
			return err
		}
		return b.Put(keyFingerprint, []byte("old"))
	}))
	st := s.status()
	assert.Equal(t, KindRebuild, st.Action)
	assert.Equal(t, "the scoring settings changed", st.Reason)
}

func TestSearch_BigBacklogShowsAsACatchUp(t *testing.T) {
	prev := searchBacklog
	searchBacklog = 2
	t.Cleanup(func() { searchBacklog = prev })
	s := newSearchFixture(t)
	runToEnd(t, s.m, "search", KindRebuild)
	insert(t, s.store, signed(t, 1, 0, 100), signed(t, 2, 0, 101), signed(t, 3, 0, 102))
	st := s.status()
	assert.Equal(t, StateBehind, st.State)
	assert.Equal(t, KindCatchUp, st.Action)

	j, err := s.m.Start("search", KindCatchUp, TriggerReconcile, st.Reason)
	require.NoError(t, err)
	// The live indexer works while the job watches it.
	indexed := make(chan error, 1)
	go func() {
		for {
			more, err := s.ix.Indexer.Step(context.Background())
			if err != nil || !more {
				indexed <- err
				return
			}
		}
	}()
	require.NoError(t, <-indexed)
	done := waitEnded(t, s.m, j.ID)
	assert.Equal(t, StateSucceeded, done.State, done.Error)
	assert.Equal(t, int64(3), done.EntriesAfter)
}

func TestSearch_DropWhenOff(t *testing.T) {
	s := newSearchFixture(t)
	insert(t, s.store, signed(t, 1, 0, 100))
	runToEnd(t, s.m, "search", KindRebuild)

	off := &SearchIndex{Store: s.store, Host: s.ix.Host, IndexName: "profiles", Disabled: "search is off"}
	st := off.Status()
	assert.Equal(t, StateDisabled, st.State)
	assert.True(t, st.HasData, "the store still tracks profiles for search")
	m := New("", zerolog.Nop(), off)
	done := runToEnd(t, m, "search", KindDrop)
	require.Equal(t, StateSucceeded, done.State, done.Error)
	assert.Equal(t, int64(1), done.EntriesBefore)
	s.meili.mu.Lock()
	_, exists := s.meili.indexes["profiles"]
	s.meili.mu.Unlock()
	assert.False(t, exists, "the Meilisearch index is removed")
	assert.False(t, off.Status().HasData)
	_, err := s.store.FeedStatus(relay.SearchFeed.Name)
	assert.ErrorIs(t, err, relay.ErrUnknownFeed)
}
