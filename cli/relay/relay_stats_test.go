package relay

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/ncli/cli/relay/internal/zaptest"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const statsURL = "wss://relay.example.com"

func statsEvents(t *testing.T, store *relay.EventStore) []*nip01.Event {
	t.Helper()
	evs, err := store.QueryEvents(context.Background(), &nip01.SubscriptionFilter{Kinds: []int{relay.KindRelayDiscovery}})
	require.NoError(t, err)
	return evs
}

func tagsOf(ev *nip01.Event, name string) [][]string {
	var out [][]string
	for _, t := range ev.Tags {
		if len(t) > 0 && t[0] == name {
			out = append(out, t)
		}
	}
	return out
}

func countTag(ev *nip01.Event, name string) string {
	for _, t := range tagsOf(ev, "count") {
		if len(t) > 2 && t[1] == name {
			return t[2]
		}
	}
	return ""
}

func withStatsConfig(t *testing.T, cache *CacheConfig) {
	prev := config
	t.Cleanup(func() { config = prev })
	config = RelayConfig{Nip11: nip11.Metadata{PubKey: profilesTestPub, PrivKey: profilesTestPriv, URL: statsURL}, Cache: cache}
}

func staticInfo(allowed, disallowed []int) func() (nip11.Metadata, []int, []int) {
	return func() (nip11.Metadata, []int, []int) {
		return nip11.Metadata{Name: "test", SupportedNips: []nip11.NIPID{nip11.NIP(1), nip11.NIP(11)}}, allowed, disallowed
	}
}

func TestRelayStatsPublisher_PublishesOnlyWhatChanged(t *testing.T) {
	withStatsConfig(t, &CacheConfig{Relay: &RelayCacheConfig{Stats: &RelayStatsConfig{Enabled: true}}})
	store := newProfilesStore(t)
	ctx := context.Background()
	alice := zaptest.NewKey(t, 1)
	note := &nip01.Event{PubKey: alice.Pub, Kind: 1, CreatedAt: uint64(time.Now().Unix()) - 10}
	require.NoError(t, note.Sign(alice.Priv))
	require.NoError(t, store.InsertEvents(ctx, []*nip01.Event{note}))

	p := newRelayStatsPublisher(store, staticInfo(nil, nil))
	published, err := p.tick(ctx)
	require.NoError(t, err)
	assert.True(t, published)
	first := statsEvents(t, store)
	require.Len(t, first, 1)
	ev := first[0]
	require.NoError(t, ev.Verify())
	assert.Equal(t, profilesTestPub, ev.PubKey, "signed with nip11.privkey")
	assert.Equal(t, [][]string{{"d", statsURL + "/"}}, tagsOf(ev, "d"))
	assert.Equal(t, "1", countTag(ev, "events"))
	assert.Equal(t, "1", countTag(ev, "authors"))
	assert.Empty(t, countTag(ev, "profiles"), "profile features are off")
	assert.Empty(t, tagsOf(ev, "total"), "zaps are off")
	assert.Empty(t, tagsOf(ev, "k"))
	assert.JSONEq(t, `"test"`, mustField(t, ev.Content, "name"))

	// The relay's own event now counts; one more tick settles on that.
	_, err = p.tick(ctx)
	require.NoError(t, err)
	settled := statsEvents(t, store)[0]
	assert.Equal(t, "2", countTag(settled, "events"))

	published, err = p.tick(ctx)
	require.NoError(t, err)
	assert.False(t, published, "nothing written since: nothing published")

	bob := zaptest.NewKey(t, 2)
	more := &nip01.Event{PubKey: bob.Pub, Kind: 1, CreatedAt: uint64(time.Now().Unix()) - 5}
	require.NoError(t, more.Sign(bob.Priv))
	require.NoError(t, store.InsertEvents(ctx, []*nip01.Event{more}))
	published, err = p.tick(ctx)
	require.NoError(t, err)
	assert.True(t, published)
	latest := statsEvents(t, store)
	require.Len(t, latest, 1, "addressable: the store keeps the newest")
	assert.Equal(t, "3", countTag(latest[0], "events"))
	assert.Greater(t, latest[0].CreatedAt, ev.CreatedAt)
}

func mustField(t *testing.T, content, field string) string {
	t.Helper()
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(content), &m))
	return string(m[field])
}

func TestRelayStatsPublisher_TagsFollowFeatures(t *testing.T) {
	withStatsConfig(t, &CacheConfig{
		Profiles: &ProfilesConfig{Stats: &ProfileStatsConfig{Enabled: true}},
		Zaps:     &ZapsConfig{Enabled: true, Windows: []string{"24h", "all"}},
		Relay:    &RelayCacheConfig{Stats: &RelayStatsConfig{Enabled: true}},
	})
	store := newProfilesStore(t)
	ctx := context.Background()
	sender, provider, receiver := zaptest.NewKey(t, 1), zaptest.NewKey(t, 2), zaptest.NewKey(t, 3)
	now := uint64(time.Now().Unix())
	require.NoError(t, store.InsertEvents(ctx, []*nip01.Event{
		zaptest.Receipt(t, sender, provider, receiver.Pub, 21_000, now-60),
		zaptest.LokiReceipt(t, sender, provider, receiver.Pub, 7_000, now-50),
	}))

	p := newRelayStatsPublisher(store, staticInfo([]int{1, 9735}, []int{4}))
	_, err := p.tick(ctx)
	require.NoError(t, err)
	ev := statsEvents(t, store)[0]
	assert.Equal(t, "0", countTag(ev, "profiles"))
	assert.Equal(t, "2", countTag(ev, "zap-receipts"))
	assert.Equal(t, [][]string{{"total", "zaps", "21", "sats"}, {"total", "zaps", "7", "loki"}}, tagsOf(ev, "total"))
	assert.Equal(t, [][]string{{"zap-ranking", "windows", "24h", "all"}, {"zap-ranking", "coins", "sats", "loki"}}, tagsOf(ev, "zap-ranking"))
	assert.Equal(t, [][]string{{"k", "1"}, {"k", "9735"}, {"k", "!4"}}, tagsOf(ev, "k"))
	assert.Contains(t, tagsOf(ev, "N"), []string{"N", "11"})
}

func TestRelayStatsPublisher_RunStopsWithItsContext(t *testing.T) {
	withStatsConfig(t, &CacheConfig{Relay: &RelayCacheConfig{Stats: &RelayStatsConfig{Enabled: true, Interval: "10ms"}}})
	store := newProfilesStore(t)
	p := newRelayStatsPublisher(store, staticInfo(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.run(ctx); close(done) }()
	require.Eventually(t, func() bool { return len(statsEvents(t, store)) == 1 }, 5*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run didn't stop")
	}
}

// TestNewServer_RelayStatsReachLiveSubscribers boots a real server and
// checks a subscriber gets each new 30166 as it's published.
func TestNewServer_RelayStatsReachLiveSubscribers(t *testing.T) {
	withStatsConfig(t, &CacheConfig{Relay: &RelayCacheConfig{Stats: &RelayStatsConfig{Enabled: true, Interval: "50ms"}}})
	store := newProfilesStore(t)
	s := NewServer(store, nil)
	t.Cleanup(s.Stop)
	ts := httptest.NewServer(s.server.Handler)
	t.Cleanup(ts.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	require.NoError(t, conn.WriteMessage(websocket.TextMessage,
		[]byte(`["REQ","s",{"kinds":[30166],"authors":["`+profilesTestPub+`"]}]`)))
	next := func() *nip01.Event {
		t.Helper()
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
		for {
			_, msg, err := conn.ReadMessage()
			require.NoError(t, err)
			var arr []json.RawMessage
			require.NoError(t, json.Unmarshal(msg, &arr))
			var typ string
			require.NoError(t, json.Unmarshal(arr[0], &typ))
			if typ == "EVENT" {
				ev := &nip01.Event{}
				require.NoError(t, json.Unmarshal(arr[2], ev))
				return ev
			}
		}
	}

	first := next()
	alice := zaptest.NewKey(t, 9)
	note := &nip01.Event{PubKey: alice.Pub, Kind: 1, CreatedAt: uint64(time.Now().Unix())}
	require.NoError(t, note.Sign(alice.Priv))
	require.NoError(t, store.InsertEvents(context.Background(), []*nip01.Event{note}))

	// Each later version arrives live; wait for one that counts alice.
	for {
		ev := next()
		require.GreaterOrEqual(t, ev.CreatedAt, first.CreatedAt)
		if countTag(ev, "authors") == "2" {
			break
		}
	}
}

// With zap ranking off the store keeps no zap index, so the publisher must
// not ask it for totals.
func TestRelayStatsPublisher_ZapsOffNeedsNoZapIndex(t *testing.T) {
	withStatsConfig(t, &CacheConfig{Relay: &RelayCacheConfig{Stats: &RelayStatsConfig{Enabled: true}}})
	store, err := openRelayStore(&RelayConfig{RelayNotesDb: filepath.Join(t.TempDir(), "notes.db")})
	require.NoError(t, err)
	t.Cleanup(store.Close)
	sender, provider, receiver := zaptest.NewKey(t, 1), zaptest.NewKey(t, 2), zaptest.NewKey(t, 3)
	require.NoError(t, store.InsertEvents(context.Background(), []*nip01.Event{
		zaptest.Receipt(t, sender, provider, receiver.Pub, 21_000, uint64(time.Now().Unix())),
	}))

	p := newRelayStatsPublisher(store, staticInfo([]int{1}, nil))
	_, err = p.tick(context.Background())
	require.NoError(t, err)
	ev := statsEvents(t, store)[0]
	assert.Empty(t, countTag(ev, "zap-receipts"))
	assert.Empty(t, tagsOf(ev, "total"))
}
