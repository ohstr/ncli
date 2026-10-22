package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/relay/internal/zaptest"
	nmconfig "github.com/ohstr/nmilat/config"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/relay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	profilesTestPriv = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	profilesTestPub  = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"
)

func TestVerificationSettings(t *testing.T) {
	cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+profilesTestPub+`"
store: "test.db"
cache:
  verification:
    ttl: 7d
    perHostInterval: 1s
    workers: 8
    retry:
      initial: 30m
      max: 6h
      maxAttempts: 3
`)
	got, err := verificationSettings(&cfg)
	require.NoError(t, err)
	assert.Equal(t, 7*24*time.Hour, got.TTL)
	assert.Equal(t, time.Second, got.PerHostInterval)
	assert.Equal(t, 30*time.Minute, got.RetryInitial)
	assert.Equal(t, 6*time.Hour, got.RetryMax)
	assert.Equal(t, 3, got.MaxAttempts)
	assert.True(t, strings.HasPrefix(got.UserAgent, "ncli/"))

	cfg.Cache.Verification.PerHostInterval = "250ms"
	got, err = verificationSettings(&cfg)
	require.NoError(t, err)
	assert.Equal(t, 250*time.Millisecond, got.PerHostInterval, "Go units work too")

	cfg.Cache.Verification.TTL = "0"
	got, err = verificationSettings(&cfg)
	require.NoError(t, err)
	assert.Negative(t, got.TTL, `"0" never re-checks`)

	cfg.Cache.Verification.TTL = "soon"
	_, err = verificationSettings(&cfg)
	var ce *common.CLIError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, common.CodeInvalidInput, ce.Code)
	assert.Equal(t, "soon", ce.Input)

	empty := RelayConfig{}
	got, err = verificationSettings(&empty)
	require.NoError(t, err)
	assert.Zero(t, got.TTL, "unset keeps nmilat's default")
}

func TestScoringOverride(t *testing.T) {
	cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+profilesTestPub+`"
store: "test.db"
cache:
  scoring:
    social:
      followers_weight: 25
    verification:
      nip05_score: 70
`)
	next, err := scoringOverride(cfg.Cache.Scoring)
	require.NoError(t, err)
	assert.Equal(t, 25.0, next.Social.FollowersWeight)
	assert.Equal(t, int64(70), next.Verification.Nip05Score)
	assert.Equal(t, nmconfig.Get().Search.Scoring.AboutBonus, next.Scoring.AboutBonus, "unset keys keep the built-in weights")

	prev := *nmconfig.Get()
	t.Cleanup(func() { nmconfig.Configure(func(c *nmconfig.AppConfig) { *c = prev }) })
	applyScoring(&cfg)
	assert.Equal(t, int64(70), nmconfig.Get().Search.Verification.Nip05Score)

	_, err = scoringOverride(map[string]any{"engine": map[string]any{"max_total_hits": 1}})
	assert.ErrorContains(t, err, "unknown section")
	_, err = scoringOverride(map[string]any{"social": map[string]any{"follower_weight": 1}})
	assert.Error(t, err, "a misspelt key fails rather than being ignored")
}

func TestVerifyTarget(t *testing.T) {
	k := zaptest.NewKey(t, 1)
	npub, err := nip19.EncodePublicKey(k.Pub)
	require.NoError(t, err)
	for _, in := range []string{k.Pub, strings.ToUpper(k.Pub), npub} {
		got, err := verifyTarget(in)
		require.NoError(t, err, in)
		assert.Equal(t, k.Pub, got, in)
	}
	_, err = verifyTarget("note1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq")
	assert.Error(t, err)
}

func TestProfileStatsEnabled(t *testing.T) {
	cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+profilesTestPub+`"
store: "test.db"
cache:
  profileStats:
    enabled: true
    maxTargets: 50
`)
	assert.True(t, profileStatsEnabled(&cfg))
	assert.Equal(t, 50, cfg.Cache.ProfileStats.MaxTargets)
	assert.False(t, profileStatsEnabled(&RelayConfig{}))
}

// fakeWeb answers HTTPS requests per host without a network.
type fakeWeb map[string]http.HandlerFunc

func (f fakeWeb) RoundTrip(req *http.Request) (*http.Response, error) {
	h := f[req.URL.Hostname()]
	if h == nil {
		return nil, errors.New("connection refused")
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Result(), nil
}

func newProfilesStore(t *testing.T) *relay.EventStore {
	t.Helper()
	store, err := relay.NewEventStore(filepath.Join(t.TempDir(), "test.db"), &nip11.Limitation{MaxLimit: 1000})
	require.NoError(t, err)
	t.Cleanup(store.Close)
	return store
}

func TestProfileRoutes(t *testing.T) {
	store := newProfilesStore(t)
	alice := zaptest.NewKey(t, 10)
	web := fakeWeb{"example.com": func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"names": map[string]string{"alice": alice.Pub}})
	}}
	worker := relay.NewProfileVerificationWorker(store, nil, relay.VerificationConfig{HTTPClient: &http.Client{Transport: web}})

	profile := &nip01.Event{PubKey: alice.Pub, CreatedAt: 100, Kind: 0, Content: `{"name":"alice","nip05":"alice@example.com"}`}
	require.NoError(t, profile.Sign(alice.Priv))
	require.NoError(t, store.InsertEvents(context.Background(), []*nip01.Event{profile}))

	mux := http.NewServeMux()
	profileRoutes(muxRouter{mux}, store, worker)
	call := func(method, path string) (int, profileReport) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		var r profileReport
		_ = json.Unmarshal(rec.Body.Bytes(), &r)
		return rec.Code, r
	}

	code, report := call(http.MethodGet, "/admin/api/profiles/"+alice.Pub)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "", report.Nip05.Status, "nothing was checked yet")

	code, report = call(http.MethodPost, "/admin/api/profiles/"+alice.Pub+"/verify")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, relay.CheckOK, report.Nip05.Status)
	assert.Equal(t, "alice@example.com", report.Nip05.Value)
	assert.Positive(t, report.Nip05.NextCheckAt)

	code, _ = call(http.MethodGet, "/admin/api/profiles/not-a-key")
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = call(http.MethodPost, "/admin/api/profiles/"+zaptest.NewKey(t, 11).Pub+"/verify")
	assert.Equal(t, http.StatusNotFound, code)
}

type muxRouter struct{ mux *http.ServeMux }

func (m muxRouter) Handle(pattern string, h http.HandlerFunc) { m.mux.HandleFunc(pattern, h) }

// TestNewServer_ProfileStats boots a real relay with cache.profileStats and
// asks it for NIP-85 assertions over a WebSocket.
func TestNewServer_ProfileStats(t *testing.T) {
	prevConfig := config
	t.Cleanup(func() { config = prevConfig })

	store := newProfilesStore(t)
	alice, bob, provider := zaptest.NewKey(t, 20), zaptest.NewKey(t, 21), zaptest.NewKey(t, 22)
	follow := &nip01.Event{PubKey: bob.Pub, CreatedAt: 100, Kind: 3, Tags: [][]string{{"p", alice.Pub}}}
	require.NoError(t, follow.Sign(bob.Priv))
	require.NoError(t, store.InsertEvents(context.Background(), []*nip01.Event{
		follow,
		zaptest.Receipt(t, bob, provider, alice.Pub, 21_000, 1_700_000_000),
		zaptest.Receipt(t, alice, provider, alice.Pub, 99_000, 1_700_000_001), // self-zap
	}))

	config = RelayConfig{
		Nip11: nip11.Metadata{PubKey: profilesTestPub, PrivKey: profilesTestPriv},
		Cache: &CacheConfig{ProfileStats: &ProfileStatsConfig{Enabled: true}},
	}
	s := NewServer(store, nil)
	t.Cleanup(s.Stop)
	ts := httptest.NewServer(s.server.Handler)
	t.Cleanup(ts.Close)

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/", nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.WriteMessage(websocket.TextMessage,
		[]byte(`["REQ","s",{"kinds":[30382],"#d":["`+alice.Pub+`"]}]`)))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))

	var got *nip01.Event
	for {
		_, msg, err := conn.ReadMessage()
		require.NoError(t, err)
		var arr []json.RawMessage
		require.NoError(t, json.Unmarshal(msg, &arr))
		var typ string
		require.NoError(t, json.Unmarshal(arr[0], &typ))
		if typ == "AUTH" {
			continue
		}
		if typ == "EVENT" {
			got = &nip01.Event{}
			require.NoError(t, json.Unmarshal(arr[2], got))
			continue
		}
		require.Equal(t, "EOSE", typ, string(msg))
		break
	}
	require.NotNil(t, got)
	require.NoError(t, got.Verify())
	assert.Equal(t, profilesTestPub, got.PubKey)
	tags := map[string]string{}
	for _, tag := range got.Tags {
		tags[tag[0]] = tag[1]
	}
	assert.Equal(t, alice.Pub, tags["d"])
	assert.Equal(t, "1", tags["followers"])
	assert.Equal(t, "1", tags["zap_cnt_recd"], "the self-zap doesn't count")
	assert.Equal(t, "21", tags["zap_amt_recd"])

	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	req.Header.Set("Accept", "application/nostr+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var info struct {
		SupportedNips []any `json:"supported_nips"` // numbers, and strings for lettered NIPs
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
	assert.Contains(t, info.SupportedNips, float64(85))
}
