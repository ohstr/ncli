package meilisearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/search"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testHex = "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"

func TestPubkeyQuery(t *testing.T) {
	npub, err := nip19.EncodePublicKey(testHex)
	require.NoError(t, err)
	nprofile, err := nip19.EncodeProfile(testHex, []string{"wss://relay.example"})
	require.NoError(t, err)

	for _, tc := range []struct {
		name, q string
		want    bool
	}{
		{"npub", npub, true},
		{"nprofile", nprofile, true},
		{"hex", testHex, true},
		{"hex with a trailing space", testHex + " ", true},
		{"upper-case hex", strings.ToUpper(testHex), true},
		{"63-char hex", testHex[:63], false},
		{"65-char hex", testHex + "a", false},
		{"64 chars, not hex", strings.Repeat("z", 64), false},
		{"a name", "fiatjaf", false},
		{"a broken npub", "npub1notreallyanpub", false},
		{"two tokens", npub + " fiatjaf", false},
		{"empty", "  ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pk, ok := PubkeyQuery(tc.q)
			assert.Equal(t, tc.want, ok)
			if tc.want {
				assert.Equal(t, testHex, pk)
			}
		})
	}
}

func TestChunks_SplitAtThePayloadCap(t *testing.T) {
	doc := func(n int) json.RawMessage { return json.RawMessage(`"` + strings.Repeat("x", n-2) + `"`) }
	docs := []json.RawMessage{doc(40), doc(40), doc(40), doc(200), doc(10)}
	got := Chunks(docs, 100)
	require.Len(t, got, 4)
	assert.Len(t, got[0], 2, "two 40-byte documents and brackets fit in 100")
	assert.Len(t, got[1], 1)
	assert.Len(t, got[2], 1, "a document over the cap goes alone")
	assert.Len(t, got[3], 1)
	n := 0
	for _, c := range got {
		n += len(c)
		raw, err := json.Marshal(c)
		require.NoError(t, err)
		if len(c) > 1 {
			assert.LessOrEqual(t, len(raw), 100)
		}
	}
	assert.Equal(t, len(docs), n, "nothing lost")
	assert.Len(t, Chunks(docs, 1<<20), 1)
	assert.Empty(t, Chunks(nil, 100))
}

func TestSettings_SlimSchema(t *testing.T) {
	c := NewMeiliClient("http://x", "", "profiles")
	s := c.Settings()
	searchable := s["searchableAttributes"].([]string)
	assert.NotContains(t, searchable, "id")
	assert.NotContains(t, searchable, "npub")
	assert.Contains(t, searchable, "about")
	assert.Equal(t, false, s["facetSearch"])
	raw, err := json.Marshal(s["filterableAttributes"])
	require.NoError(t, err)
	assert.JSONEq(t, `[{"attributePatterns":["pubkey","kind","is_human"],"features":{"facetSearch":false,"filter":{"equality":true,"comparison":false}}}]`, string(raw))

	noAbout := NewMeiliClient("http://x", "", "profiles", WithoutAbout())
	assert.NotContains(t, noAbout.Settings()["searchableAttributes"], "about")
	assert.NotEqual(t, c.SettingsFingerprint(), noAbout.SettingsFingerprint())
	assert.Equal(t, c.SettingsFingerprint(), NewMeiliClient("http://y", "k", "other").SettingsFingerprint(), "the fingerprint is the settings only")
	assert.Equal(t, c.SettingsFingerprint(), c.ForIndex("profiles__next").SettingsFingerprint())
}

func TestSlowestStep(t *testing.T) {
	step, d := SlowestStep(map[string]string{
		"processing tasks":                                       "200.5s",
		"processing tasks > post processing words":               "190s",
		"processing tasks > post processing words > word fst":    "66.2s",
		"processing tasks > post processing words > word prefix": "100s",
		"processing tasks > extracting":                          "3s",
		"processing tasks > tiny":                                "120µs",
		"bad":                                                    "?",
	})
	assert.Equal(t, "processing tasks > post processing words > word prefix", step, "the longest step with no child")
	assert.Equal(t, 100*time.Second, d)
	step, _ = SlowestStep(nil)
	assert.Empty(t, step)
}

func TestParseISODuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"PT1.5S":    1500 * time.Millisecond,
		"PT2M3.1S":  2*time.Minute + 3100*time.Millisecond,
		"PT1H":      time.Hour,
		"PT0.0001S": 100 * time.Microsecond,
	} {
		got, err := ParseISODuration(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "P1D", "PT", "PTxS", "1.5s"} {
		_, err := ParseISODuration(bad)
		assert.Error(t, err, bad)
	}
}

// recorder is a Meilisearch that applies every task at once and records
// the requests it got.
type recorder struct {
	mu       sync.Mutex
	requests []string
	bodies   map[string][]int
	exists   bool
	docs     map[string]bool
	task     int64
}

func newRecorder(t *testing.T) (*recorder, string) {
	r := &recorder{bodies: map[string][]int{}, docs: map[string]bool{}}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return r, srv.URL
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	body, _ := io.ReadAll(req.Body)
	key := req.Method + " " + req.URL.Path
	r.requests = append(r.requests, key)
	r.bodies[key] = append(r.bodies[key], len(body))
	w.Header().Set("Content-Type", "application/json")
	reply := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	enqueue := func() {
		r.task++
		reply(http.StatusAccepted, map[string]any{"taskUid": r.task, "status": "enqueued"})
	}
	path := req.URL.Path
	switch {
	case strings.HasPrefix(path, "/tasks/"):
		reply(200, map[string]any{"status": "succeeded", "batchUid": 7, "duration": "PT2.5S"})
	case path == "/batches/7":
		reply(200, map[string]any{"duration": "PT3S", "details": map[string]any{"receivedDocuments": 30000},
			"stats": map[string]any{"progressTrace": map[string]string{"a": "3s", "a > word fst": "2s", "a > merge": "1s"}}})
	case path == "/indexes/profiles" && req.Method == http.MethodGet:
		if !r.exists {
			reply(404, map[string]string{"code": "index_not_found", "message": "not found", "type": "invalid_request", "link": ""})
			return
		}
		reply(200, map[string]any{"uid": "profiles", "primaryKey": "id", "createdAt": "2026-01-01T00:00:00Z"})
	case path == "/indexes" && req.Method == http.MethodPost:
		r.exists = true
		enqueue()
	case strings.HasPrefix(path, "/indexes/profiles/documents/") && req.Method == http.MethodGet:
		id := strings.TrimPrefix(path, "/indexes/profiles/documents/")
		if !r.docs[id] {
			reply(404, map[string]string{"code": "document_not_found", "message": "not found", "type": "invalid_request", "link": ""})
			return
		}
		reply(200, map[string]string{"id": id})
	case path == "/indexes/profiles/search":
		reply(200, map[string]any{"hits": []map[string]string{{"id": "fromsearch"}}})
	default:
		enqueue()
	}
}

func (r *recorder) count(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, k := range r.requests {
		if k == key {
			n++
		}
	}
	return n
}

func TestInitialize_NeverChangesAnExistingIndex(t *testing.T) {
	r, url := newRecorder(t)
	c := NewMeiliClient(url, "", "profiles")
	require.NoError(t, c.Initialize(context.Background()))
	assert.Equal(t, 1, r.count("POST /indexes"))
	assert.Equal(t, 1, r.count("PATCH /indexes/profiles/settings"), "a new index gets the settings")

	require.NoError(t, c.Initialize(context.Background()))
	assert.Equal(t, 1, r.count("POST /indexes"))
	assert.Equal(t, 1, r.count("PATCH /indexes/profiles/settings"), "an existing index keeps its settings")
}

func TestEnqueueUpsert_SplitsAtThePayloadCapAndRecordsTheBatch(t *testing.T) {
	r, url := newRecorder(t)
	c := NewMeiliClient(url, "", "profiles", WithMaxPayload(4096))
	docs := make([]*search.ProfileDocument, 40)
	for i := range docs {
		docs[i] = &search.ProfileDocument{ID: strings.Repeat("a", 63) + string(rune('a'+i%26)), About: strings.Repeat("x", 300)}
	}
	p, err := c.EnqueueUpsert(context.Background(), docs)
	require.NoError(t, err)
	posts := r.count("POST /indexes/profiles/documents")
	assert.Greater(t, posts, 1, "split into several requests")
	for _, n := range r.bodies["POST /indexes/profiles/documents"] {
		assert.LessOrEqual(t, n, 4096)
	}
	assert.Nil(t, c.LastTask())
	require.NoError(t, p.Wait(context.Background()))
	last := c.LastTask()
	require.NotNil(t, last)
	assert.Equal(t, int64(7), last.Batch)
	assert.Equal(t, int64(30000), last.BatchDocs)
	assert.Equal(t, int64(3000), last.DurationMS)
	assert.Equal(t, "a > word fst", last.SlowestStep)
	assert.Equal(t, int64(2000), last.SlowestMS)
	assert.Same(t, last, c.ForIndex("other").LastTask(), "shared with clients for other indexes")
}

func TestFindProfiles_PubkeyQueryFetchesTheDocument(t *testing.T) {
	r, url := newRecorder(t)
	c := NewMeiliClient(url, "", "profiles")
	npub, err := nip19.EncodePublicKey(testHex)
	require.NoError(t, err)

	got, err := c.FindProfiles(context.Background(), npub, 10)
	require.NoError(t, err)
	assert.Empty(t, got, "no such profile")

	r.mu.Lock()
	r.docs[testHex] = true
	r.mu.Unlock()
	for _, q := range []string{npub, testHex, testHex + " "} {
		got, err = c.FindProfiles(context.Background(), q, 10)
		require.NoError(t, err)
		assert.Equal(t, []string{testHex}, got, q)
	}
	assert.Zero(t, r.count("POST /indexes/profiles/search"), "never a text search")

	got, err = c.FindProfiles(context.Background(), "fiatjaf", 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"fromsearch"}, got)
}
