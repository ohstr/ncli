package jobs

// Tests against a real Meilisearch in Docker. They skip with -short or
// without Docker; CI runs them in the integrations job.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common/meilisearch"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/search"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// meiliImage is the Meilisearch the docs recommend;
// NCLI_TEST_MEILI_IMAGE tests another.
func meiliImage() string {
	if img := os.Getenv("NCLI_TEST_MEILI_IMAGE"); img != "" {
		return img
	}
	return "getmeili/meilisearch:v1.55.0"
}

// startMeili runs Meilisearch in Docker and returns its URL.
func startMeili(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Meilisearch in Docker")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("ncli-jobs-meili-%d", time.Now().UnixNano())
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "-p", "127.0.0.1::7700",
		"-e", "MEILI_NO_ANALYTICS=true", "-e", "MEILI_ENV=development", meiliImage()).CombinedOutput()
	if err != nil {
		t.Skipf("can't start %s: %v\n%s", meiliImage(), err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	// The published port, else (when the tests run in a container
	// themselves) the container's own address.
	var urls []string
	if port, err := exec.Command("docker", "port", name, "7700/tcp").Output(); err == nil {
		urls = append(urls, "http://"+strings.TrimSpace(strings.Split(string(port), "\n")[0]))
	}
	if ip, err := exec.Command("docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name).Output(); err == nil && len(strings.TrimSpace(string(ip))) > 0 {
		urls = append(urls, "http://"+strings.TrimSpace(string(ip))+":7700")
	}
	probe := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		for _, url := range urls {
			if resp, err := probe.Get(url + "/health"); err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return url
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("meilisearch never answered at %v", urls)
	return ""
}

func meiliGet(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Less(t, resp.StatusCode, 300, url)
	require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
}

// allDocs is every document in index, id to name.
func allDocs(t *testing.T, host, index string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for offset := 0; ; offset += 1000 {
		var page struct {
			Results []struct{ ID, Name string } `json:"results"`
		}
		meiliGet(t, fmt.Sprintf("%s/indexes/%s/documents?limit=1000&offset=%d&fields=id,name", host, index, offset), &page)
		for _, d := range page.Results {
			out[d.ID] = d.Name
		}
		if len(page.Results) < 1000 {
			return out
		}
	}
}

func profile(t *testing.T, i int, createdAt uint64, content string) *nip01.Event {
	t.Helper()
	priv, pub := testKey(t, i)
	ev := &nip01.Event{PubKey: pub, Kind: 0, CreatedAt: createdAt, Content: content}
	require.NoError(t, ev.Sign(priv))
	return ev
}

// insertChunked stores events a thousand per call.
func insertChunked(t *testing.T, store *relay.EventStore, events []*nip01.Event) {
	t.Helper()
	for len(events) > 0 {
		n := min(1000, len(events))
		insert(t, store, events[:n]...)
		events = events[n:]
	}
}

type meiliFixture struct {
	t      *testing.T
	host   string
	store  *relay.EventStore
	client *meilisearch.MeiliClient
	ix     *SearchIndex
	m      *Manager
}

func newMeiliFixture(t *testing.T, batch, inFlight int) *meiliFixture {
	host := startMeili(t)
	store := openAt(t, filepath.Join(t.TempDir(), "notes.db"), relay.DerivedIndexes{Social: true})
	client := meilisearch.NewMeiliClient(host, "", "profiles")
	indexer, err := relay.NewSearchIndexer(store, client, relay.WithSearchBatch(batch), relay.WithSearchInFlight(inFlight))
	require.NoError(t, err)
	ix := &SearchIndex{Store: store, Indexer: indexer, Host: host, IndexName: "profiles", Client: client, BatchSize: batch, MaxInFlight: inFlight}
	return &meiliFixture{t: t, host: host, store: store, client: client, ix: ix, m: New("", zerolog.Nop(), ix)}
}

func (f *meiliFixture) rebuild() {
	f.t.Helper()
	j, err := f.m.Start("search", KindRebuild, TriggerAdmin, "")
	require.NoError(f.t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	done, err := f.m.Wait(ctx, j.ID)
	require.NoError(f.t, err)
	require.Equal(f.t, StateSucceeded, done.State, done.Error)
}

func TestMeili_LiveUpdatesLastWriteWins(t *testing.T) {
	f := newMeiliFixture(t, 2000, 4)
	f.rebuild()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); f.ix.Indexer.Run(ctx) }()

	// 49,000 profiles, and 20 updates to each of 50 more spread over the
	// inserts, so a profile's versions go out in different tasks.
	want := map[string]string{}
	const updated, versions = 50, 20
	var batch []*nip01.Event
	for i := range 49_000 {
		ev := profile(t, 1000+i, 100, fmt.Sprintf(`{"name":"user%d"}`, i))
		want[ev.PubKey] = fmt.Sprintf("user%d", i)
		batch = append(batch, ev)
		if i%(49_000/versions) == 0 {
			v := i / (49_000 / versions)
			for u := range updated {
				ev := profile(t, u, uint64(100+v), fmt.Sprintf(`{"name":"hot%d-v%d"}`, u, v))
				want[ev.PubKey] = fmt.Sprintf("hot%d-v%d", u, v)
				batch = append(batch, ev)
			}
			insertChunked(t, f.store, batch)
			batch = nil
			time.Sleep(50 * time.Millisecond) // let the indexer send some
		}
	}
	insertChunked(t, f.store, batch)
	require.Eventually(t, func() bool {
		st := f.ix.Indexer.Status()
		return st.Behind == 0 && st.Queued == 0
	}, 5*time.Minute, 200*time.Millisecond)
	cancel()
	wg.Wait()

	got := allDocs(t, f.host, "profiles")
	require.Len(t, got, len(want))
	for pk, name := range want {
		if got[pk] != name {
			t.Fatalf("%s: got %q, want %q", pk, got[pk], name)
		}
	}
	assert.Positive(t, f.ix.Indexer.Status().Upserted)
}

func TestMeili_InterruptedRebuildMatchesAFullOne(t *testing.T) {
	f := newMeiliFixture(t, 1000, 4)
	want := map[string]string{}
	var events []*nip01.Event
	for i := range 20_000 {
		ev := profile(t, i, 100, fmt.Sprintf(`{"name":"user%d","about":"about %d"}`, i, i))
		want[ev.PubKey] = fmt.Sprintf("user%d", i)
		events = append(events, ev)
		if i%2 == 0 {
			events = append(events, signed(t, i, 1, 100))
		}
	}
	insertChunked(t, f.store, events)

	// A crashed rebuild leaves a partial build index behind.
	leftover := meilisearch.NewMeiliClient(f.host, "", "profiles__next")
	require.NoError(t, leftover.Initialize(context.Background()))
	require.NoError(t, leftover.Upsert(context.Background(), []*search.ProfileDocument{{ID: "stale", Name: "stale"}}))

	// Stopped half way.
	ctx, cancel := context.WithCancel(context.Background())
	_, err := f.ix.Run(ctx, KindRebuild, func(p Progress) {
		if p.Phase == "build" && p.Done >= 10_000 {
			cancel()
		}
	})
	require.Error(t, err)
	assert.Equal(t, StateNeedsRebuild, f.ix.Status().State)

	f.rebuild()
	got := allDocs(t, f.host, "profiles")
	assert.Equal(t, want, got)
	st := f.ix.Status()
	assert.Equal(t, StateReady, st.State)
	assert.Equal(t, int64(20_000), st.Entries)
}

func TestMeili_SearchPaths(t *testing.T) {
	f := newMeiliFixture(t, 1000, 4)
	fiatjaf := profile(t, 1, 100, `{"name":"fiatjaf","display_name":"fiatjaf","nip05":"_@fiatjaf.com","about":"nostr"}`)
	npub, err := nip19.EncodePublicKey(fiatjaf.PubKey)
	require.NoError(t, err)
	nprofile, err := nip19.EncodeProfile(fiatjaf.PubKey, nil)
	require.NoError(t, err)
	// Mentions fiatjaf's keys in its about text.
	fan := profile(t, 2, 100, fmt.Sprintf(`{"name":"fan","about":"follow %s and %s"}`, npub, fiatjaf.PubKey))
	events := []*nip01.Event{fiatjaf, fan}
	for i := range 200 {
		events = append(events, profile(t, 10+i, 100, fmt.Sprintf(`{"name":"filler%d","about":"fiat money talk %d"}`, i, i)))
	}
	insertChunked(t, f.store, events)
	f.rebuild()

	ctx := context.Background()
	for _, q := range []string{"fiatjaf", "fia", "_@fiatjaf.com", npub, nprofile, fiatjaf.PubKey, strings.ToUpper(fiatjaf.PubKey), fiatjaf.PubKey + " "} {
		got, err := f.client.FindProfiles(ctx, q, 10)
		require.NoError(t, err, q)
		require.NotEmpty(t, got, q)
		assert.Equal(t, fiatjaf.PubKey, got[0], q)
	}
	for _, q := range []string{npub, fiatjaf.PubKey, nprofile} {
		got, err := f.client.FindProfiles(ctx, q, 10)
		require.NoError(t, err)
		assert.Equal(t, []string{fiatjaf.PubKey}, got, "a key query never matches through about text: %s", q)
	}
	unknown, err := nip19.EncodePublicKey(strings.Repeat("ab", 32))
	require.NoError(t, err)
	got, err := f.client.FindProfiles(ctx, unknown, 10)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestMeili_SettingsAfterRebuild(t *testing.T) {
	f := newMeiliFixture(t, 1000, 4)
	insert(t, f.store, profile(t, 1, 100, `{"name":"a"}`))
	f.rebuild()

	var settings struct {
		SearchableAttributes []string          `json:"searchableAttributes"`
		FilterableAttributes []json.RawMessage `json:"filterableAttributes"`
		SortableAttributes   []string          `json:"sortableAttributes"`
		FacetSearch          bool              `json:"facetSearch"`
	}
	meiliGet(t, f.host+"/indexes/profiles/settings", &settings)
	assert.Equal(t, []string{"name", "display_name", "about", "nip05", "lud16"}, settings.SearchableAttributes)
	assert.False(t, settings.FacetSearch)
	require.Len(t, settings.FilterableAttributes, 1)
	assert.JSONEq(t, `{"attributePatterns":["pubkey","kind","is_human"],"features":{"facetSearch":false,"filter":{"equality":true,"comparison":false}}}`, string(settings.FilterableAttributes[0]))
	assert.ElementsMatch(t, []string{"created_at", "promoted", "bot_score", "score"}, settings.SortableAttributes)

	// A settings change rebuilds into a new index, never the live one.
	identity, err := f.client.Identity(context.Background())
	require.NoError(t, err)
	f.ix.Client = meilisearch.NewMeiliClient(f.host, "", "profiles", meilisearch.WithoutAbout())
	require.NoError(t, f.ix.Client.Initialize(context.Background()))
	meiliGet(t, f.host+"/indexes/profiles/settings", &settings)
	assert.Contains(t, settings.SearchableAttributes, "about", "startup leaves the live index alone")
	assert.Equal(t, "the index settings changed", f.ix.Status().Reason)
	f.rebuild()
	meiliGet(t, f.host+"/indexes/profiles/settings", &settings)
	assert.NotContains(t, settings.SearchableAttributes, "about")
	after, err := f.client.Identity(context.Background())
	require.NoError(t, err)
	assert.NotEqual(t, identity, after, "swapped in, not patched")
}

func TestMeili_InFlightTasksAreMerged(t *testing.T) {
	f := newMeiliFixture(t, 500, 4)
	var events []*nip01.Event
	for i := range 20_000 {
		events = append(events, profile(t, i, 100, fmt.Sprintf(`{"name":"user%d","about":"some words about user %d"}`, i, i)))
	}
	insertChunked(t, f.store, events)
	f.rebuild()

	var batches struct {
		Results []struct {
			Details struct {
				ReceivedDocuments int64 `json:"receivedDocuments"`
			} `json:"details"`
		} `json:"results"`
	}
	meiliGet(t, f.host+"/batches?statuses=succeeded&types=documentAdditionOrUpdate&limit=100", &batches)
	most := int64(0)
	for _, b := range batches.Results {
		most = max(most, b.Details.ReceivedDocuments)
	}
	assert.Greater(t, most, int64(500), "Meilisearch merged several enqueued tasks into one batch")

	last := f.client.LastTask()
	require.NotNil(t, last, "the last task's batch is recorded")
	assert.NotEmpty(t, last.SlowestStep)
	assert.Equal(t, last, f.ix.Status().LastTask)
}
