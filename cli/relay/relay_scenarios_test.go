package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/go-flokicoin/crypto/schnorr"
	"github.com/gorilla/websocket"
	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip42"
	"github.com/ohstr/nmilat/nipOA"
	"github.com/ohstr/nmilat/relay"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/ohstr/nmilat/wire"
	"github.com/stretchr/testify/require"
)

// Every shipped examples/relay/*.yaml, including the quickstart and
// every-field reference -- the doc-drift guard below loads all of them,
// not just the 8 named scenarios, since a stale field is just as silent
// in either.
var scenarioExampleFiles = []string{
	"minimal.yaml",
	"full.yaml",
	"personal-relay.yaml",
	"community-membership-relay.yaml",
	"public-search-relay.yaml",
	"anti-spam-relay.yaml",
	"dev-test-relay.yaml",
	"agent-swarm-relay.yaml",
	"community-voice-relay.yaml",
	"app-backend-relay.yaml",
}

// TestScenarioExamplesLoadThroughTheRealConfigLoader guards against the
// exact class of bug that motivated this file: examples/relay/full.yaml
// once documented `query:` as a bare top-level key when the real shape
// (since the httpBridge grouping landed) is `httpBridge.query` -- viper
// silently drops an unknown top-level key rather than erroring, so the
// stale example loaded with no complaint and simply never mounted the
// endpoint. Loading every shipped example through the real loader and
// asserting it binds into a non-zero RelayConfig (store/port/pubkey,
// which every one of them sets) catches a renamed/misspelled field the
// same way -- it doesn't prove the scenario's own promise, just that the
// file wasn't silently ignored. The tests below cover the actual promise
// for each of the 8 named scenarios.
func TestScenarioExamplesLoadThroughTheRealConfigLoader(t *testing.T) {
	for _, name := range scenarioExampleFiles {
		t.Run(name, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join("..", "..", "examples", "relay", name))
			require.NoError(t, err)

			cfg := loadRelayConfigFromYAML(t, string(content))

			require.NotZero(t, cfg.Port, "%s: port did not bind", name)
			require.NotEmpty(t, cfg.RelayNotesDb, "%s: store did not bind", name)
			require.True(t, cfg.Nip11.PubKey != "" || cfg.Nip11.PrivKey != "",
				"%s: neither nip11.pubkey nor nip11.privkey bound", name)
		})
	}
}

// bootScenario boots a real NewServer for cfg against a fresh store,
// exactly the way initConfig would populate the package-level config var
// in production -- same pattern as bootRelay/bootQueryServer.
func bootScenario(t *testing.T, cfg RelayConfig) *httptest.Server {
	t.Helper()

	prevConfig := config
	t.Cleanup(func() { config = prevConfig })
	config = cfg

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := relay.NewEventStore(dbPath, &nip11.Limitation{MaxLimit: 1000})
	require.NoError(t, err)

	s := NewServer(store, nil)
	t.Cleanup(s.Stop)

	ts := httptest.NewServer(s.server.Handler)
	t.Cleanup(ts.Close)
	return ts
}

// Local to this file: a generic enrolled-member identity and a NIP-OA
// owner/agent pair, reusing the official NIP-OA test vectors (see
// nmilat/relay/nipaa_test.go) so the agent-swarm test exercises the real
// cryptographic path against known-good key material, not throwaway keys.
const (
	scenarioMemberPriv = "0000000000000000000000000000000000000000000000000000000000000001"
	scenarioMemberPub  = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	scenarioAgentPriv  = "0000000000000000000000000000000000000000000000000000000000000002"
	scenarioAgentPub   = "c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5"
)

/////////////////////////////////////////////////////////////////////
// anti-spam-relay.yaml -- PoW actually rejects under-difficulty events
/////////////////////////////////////////////////////////////////////

func TestScenario_AntiSpamRelay_RejectsLowDifficultyEvent(t *testing.T) {
	ts := bootScenario(t, RelayConfig{
		// pow.min/pow.strict are the single source of truth for
		// Limitation.MinPowDifficulty/StrictPow in production (see
		// command.go's initConfig), but that translation happens at
		// startup, not inside NewServer -- so a test constructing the
		// effective config directly, like this one, sets the Limitation
		// fields themselves rather than relying on Pow alone.
		Nip11: nip11.Metadata{
			PubKey:     testPubKey,
			Limitation: nip11.Limitation{MinPowDifficulty: 20, StrictPow: true},
		},
		Pow: &PowConfig{Strict: true, Min: 20},
	})

	u, err := url.Parse("ws" + strings.TrimPrefix(ts.URL, "http"))
	require.NoError(t, err)
	conn, err := relayclient.Connect(context.Background(), u)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	// Ordinary content, no mining -- real difficulty is effectively 0.
	ev, err := nip01.NewSignedEvent(1, "no proof of work here", scenarioMemberPriv)
	require.NoError(t, err)

	ok, err := conn.Publish(context.Background(), ev)
	require.NoError(t, err)
	require.False(t, ok.Accepted, "a 0-difficulty event must be rejected when pow.strict is on")
	require.Contains(t, ok.Message, "pow:", "rejection message should name the pow check")
}

/////////////////////////////////////////////////////////////////////
// community-membership-relay.yaml -- membership_required actually gates EVENT
/////////////////////////////////////////////////////////////////////

func TestScenario_CommunityMembershipRelay_GatesNonMembers(t *testing.T) {
	ts := bootScenario(t, RelayConfig{
		Nip11: nip11.Metadata{
			PubKey:  testPubKey,
			PrivKey: testPrivKey,
			Self:    testPubKey,
			URL:     huddleRelayURL,
			Limitation: nip11.Limitation{
				AuthRequired:       true,
				MembershipRequired: true,
			},
		},
		Membership: &MembershipConfig{Enabled: true},
	})
	u, err := url.Parse("ws" + strings.TrimPrefix(ts.URL, "http"))
	require.NoError(t, err)

	t.Run("a non-member's event is refused", func(t *testing.T) {
		const strangerPriv = "000000000000000000000000000000000000000000000000000000000000000f"
		conn, err := relayclient.Connect(context.Background(), u)
		require.NoError(t, err)
		t.Cleanup(conn.Close)
		authAs(t, conn, strangerPriv)

		ev, err := nip01.NewSignedEvent(1, "hello", strangerPriv)
		require.NoError(t, err)
		ok, err := conn.Publish(context.Background(), ev)
		require.NoError(t, err)
		require.False(t, ok.Accepted, "an authenticated but unenrolled pubkey must still be refused")
	})

	t.Run("an enrolled member's event succeeds", func(t *testing.T) {
		enrollMember(t, ts.URL, scenarioMemberPub)

		conn, err := relayclient.Connect(context.Background(), u)
		require.NoError(t, err)
		t.Cleanup(conn.Close)
		authAs(t, conn, scenarioMemberPriv)

		ev, err := nip01.NewSignedEvent(1, "hello from a member", scenarioMemberPriv)
		require.NoError(t, err)
		ok, err := conn.Publish(context.Background(), ev)
		require.NoError(t, err)
		require.True(t, ok.Accepted, "an enrolled member's event must be accepted (message: %s)", ok.Message)
	})
}

/////////////////////////////////////////////////////////////////////
// agent-swarm-relay.yaml -- NIP-AA grants virtual membership from the owner's
/////////////////////////////////////////////////////////////////////

func TestScenario_AgentSwarmRelay_GrantsVirtualMembership(t *testing.T) {
	ts := bootScenario(t, RelayConfig{
		Nip11: nip11.Metadata{
			PubKey:  testPubKey,
			PrivKey: testPrivKey,
			Self:    testPubKey,
			URL:     huddleRelayURL,
			Limitation: nip11.Limitation{
				AuthRequired:       true,
				MembershipRequired: true,
			},
		},
		Membership: &MembershipConfig{Enabled: true},
		AgentAuth:  &AgentAuthConfig{Enabled: true},
	})
	u, err := url.Parse("ws" + strings.TrimPrefix(ts.URL, "http"))
	require.NoError(t, err)

	enrollMember(t, ts.URL, scenarioMemberPub)

	t.Run("an agent credentialed by an enrolled owner is admitted", func(t *testing.T) {
		conn, err := relayclient.Connect(context.Background(), u)
		require.NoError(t, err)
		t.Cleanup(conn.Close)

		challenge := awaitAuthChallenge(t, conn)
		authTag := signNIPOAAuthTag(t, scenarioMemberPriv, scenarioAgentPub, "")
		authEv := nip42.NewAuthEvent(challenge, huddleRelayURL)
		authEv.AddTag(authTag)
		require.NoError(t, authEv.Sign(scenarioAgentPriv))
		conn.Outgoing() <- &wire.AuthPacket{Event: authEv}
		require.True(t, awaitOKFor(t, conn, authEv.ID), "AUTH with a valid NIP-OA credential should be accepted")

		ev, err := nip01.NewSignedEvent(1, "posting as the agent", scenarioAgentPriv)
		require.NoError(t, err)
		ok, err := conn.Publish(context.Background(), ev)
		require.NoError(t, err)
		require.True(t, ok.Accepted, "virtual membership should let the agent's own EVENT through (message: %s)", ok.Message)
	})

	t.Run("an agent credentialed by a non-member owner is refused", func(t *testing.T) {
		const strangerPriv = "000000000000000000000000000000000000000000000000000000000000000e"
		conn, err := relayclient.Connect(context.Background(), u)
		require.NoError(t, err)
		t.Cleanup(conn.Close)

		challenge := awaitAuthChallenge(t, conn)
		authTag := signNIPOAAuthTag(t, strangerPriv, scenarioAgentPub, "")
		authEv := nip42.NewAuthEvent(challenge, huddleRelayURL)
		authEv.AddTag(authTag)
		require.NoError(t, authEv.Sign(scenarioAgentPriv))
		conn.Outgoing() <- &wire.AuthPacket{Event: authEv}
		accepted := awaitOKFor(t, conn, authEv.ID)
		require.False(t, accepted, "a credential from a non-member owner must not grant membership")
	})
}

/////////////////////////////////////////////////////////////////////
// app-backend-relay.yaml -- nip86 and httpBridge.query are mounted and
// auth-gated (the exact regression class the httpBridge nesting bug was)
/////////////////////////////////////////////////////////////////////

func TestScenario_AppBackendRelay_MountsNIP86AndQueryBehindAuth(t *testing.T) {
	ts := bootScenario(t, RelayConfig{
		Nip11: nip11.Metadata{
			PubKey:  testPubKey,
			PrivKey: testPrivKey,
			Self:    testPubKey,
			URL:     huddleRelayURL,
		},
		Membership: &MembershipConfig{Enabled: true},
		Nip86:      &Nip86Config{Enabled: true},
		HTTPBridge: &HTTPBridgeConfig{Query: &QueryConfig{Enabled: true}},
	})

	t.Run("nip86 rejects an unauthenticated management request, not a 404", func(t *testing.T) {
		resp, err := http.Post(ts.URL+"/", "application/nostr+json+rpc", strings.NewReader(`{"method":"supportedmethods"}`))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"nip86.enabled must mount the endpoint (401, auth-gated) rather than leave it unmounted (404)")
	})

	t.Run("nip86 serves an admin-signed management request", func(t *testing.T) {
		body := []byte(`{"method":"supportedmethods"}`)
		header, err := common.GenerateNIP98Header(testPrivKey, ts.URL+"/", http.MethodPost, body)
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(string(body)))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/nostr+json+rpc")
		req.Header.Set("Authorization", header)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("httpBridge.query rejects an unauthenticated request, not a 404", func(t *testing.T) {
		resp, err := http.Post(ts.URL+"/query", "application/json", strings.NewReader(`[{"kinds":[1]}]`))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"httpBridge.query.enabled must mount /query (401) rather than leave it unmounted (404) -- "+
				"this is the exact shape of the bug full.yaml's stale bare `query:` key caused")
	})
}

/////////////////////////////////////////////////////////////////////
// public-search-relay.yaml -- cache.topZapped is actually wired end-to-end
// (same technique as TestNewServer_TopZappedWindowFromYAML). Real
// Meilisearch-backed search correctness is deliberately left to
// cli/common/meilisearch's own tests and the agent-eval R12 round.
/////////////////////////////////////////////////////////////////////

func TestScenario_PublicSearchRelay_TopZappedIsWired(t *testing.T) {
	ts := bootScenario(t, RelayConfig{
		Nip11: nip11.Metadata{PubKey: testPubKey, PrivKey: testPrivKey},
		Cache: &CacheConfig{TopZapped: &TopZappedConfig{Enabled: true, Window: "24h"}},
	})

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	require.NoError(t, conn.WriteMessage(websocket.TextMessage,
		[]byte(`["REQ","sub1",{"cache":["top-zapped",{}]}]`)))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))

	// The relay sends an AUTH challenge on every new connection
	// unconditionally (not only when auth_required is set), so the first
	// frame here is that challenge, not yet the cache response -- loop
	// past anything that isn't the EVENT/CLOSED answer to our own REQ,
	// same as TestNewServer_TopZappedWindowFromYAML's own loop does.
	var arr []json.RawMessage
	var msgType string
	for {
		_, msg, err := conn.ReadMessage()
		require.NoError(t, err)
		arr = nil
		require.NoError(t, json.Unmarshal(msg, &arr))
		require.NoError(t, json.Unmarshal(arr[0], &msgType))
		if msgType == "EVENT" || msgType == "CLOSED" {
			break
		}
	}
	require.Equal(t, "EVENT", msgType, "a top-zapped cache REQ must be answered, not ignored or closed")

	var got nip01.Event
	require.NoError(t, json.Unmarshal(arr[2], &got))
	require.Equal(t, 25521, got.Kind)
	require.NoError(t, got.Verify(), "the cached response must be validly signed with nip11.privkey")
}

/////////////////////////////////////////////////////////////////////
// community-voice-relay.yaml -- both huddle doors are mounted
/////////////////////////////////////////////////////////////////////

func TestScenario_CommunityVoiceRelay_MountsHuddleEndpoints(t *testing.T) {
	ts := bootScenario(t, RelayConfig{
		Nip11:  nip11.Metadata{PubKey: testPubKey, URL: huddleRelayURL},
		Huddle: &HuddleConfig{Enabled: true, RTC: true},
	})

	_, audioJoined := handshakeOn(t, ts, "audio", "community-call", alicePriv)
	require.Equal(t, "joined", audioJoined.Type, "got %+v", audioJoined)

	_, rtcJoined := handshakeOn(t, ts, "rtc", "community-call", bobPriv)
	require.Equal(t, "joined", rtcJoined.Type, "got %+v", rtcJoined)
}

/////////////////////////////////////////////////////////////////////
// shared helpers
/////////////////////////////////////////////////////////////////////

// enrollMember POSTs to the bespoke membership admin API the same way
// `ncli relay members add` would, signing as the relay's own nip11.privkey
// (testPrivKey) -- the real path, not a backdoor into internal state.
func enrollMember(t *testing.T, baseURL, pubkey string) {
	t.Helper()
	target := baseURL + "/admin/membership/members"
	body := []byte(`{"pubkey":"` + pubkey + `"}`)
	header, err := common.GenerateNIP98Header(testPrivKey, target, http.MethodPost, body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(string(body)))
	require.NoError(t, err)
	req.Header.Set("Authorization", header)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "enrolling %s via the admin API failed", pubkey)
}

// authAs completes a plain NIP-42 handshake (no NIP-OA tag) for conn using
// privKey, against huddleRelayURL, and fails the test if the relay never
// settles it.
func authAs(t *testing.T, conn *relayclient.Connection, privKey string) {
	t.Helper()
	challenge := awaitAuthChallenge(t, conn)
	ev := nip42.NewAuthEvent(challenge, huddleRelayURL)
	require.NoError(t, ev.Sign(privKey))
	conn.Outgoing() <- &wire.AuthPacket{Event: ev}
	awaitOKFor(t, conn, ev.ID)
}

// awaitAuthChallenge reads conn's incoming channel until the relay's own
// NIP-42 challenge arrives.
func awaitAuthChallenge(t *testing.T, conn *relayclient.Connection) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case res := <-conn.Read():
			if c, ok := res.(*wire.AuthChallengeResponse); ok {
				return c.Challenge
			}
		case err := <-conn.Errors():
			t.Fatalf("connection error waiting for AUTH challenge: %v", err)
		case <-deadline:
			t.Fatal("timed out waiting for AUTH challenge")
		}
	}
}

// awaitOKFor reads conn's incoming channel until the OK for eventID
// arrives, and returns whether it was accepted.
func awaitOKFor(t *testing.T, conn *relayclient.Connection, eventID string) bool {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case res := <-conn.Read():
			if ok, isOk := res.(*wire.OkSubscriptionResponse); isOk && ok.EventID == eventID {
				return ok.Accepted
			}
		case err := <-conn.Errors():
			t.Fatalf("connection error waiting for OK(%s): %v", eventID, err)
		case <-deadline:
			t.Fatalf("timed out waiting for OK(%s)", eventID)
		}
	}
}

// signNIPOAAuthTag builds a valid NIP-OA "auth" tag signed by
// ownerPrivHex, authorizing eventPubkey under conditions. Mirrors
// nmilat/relay/nipaa_test.go's signAuthTag -- same recipe, reimplemented
// here because that helper is private to nmilat's own test package.
func signNIPOAAuthTag(t *testing.T, ownerPrivHex, eventPubkey, conditions string) []string {
	t.Helper()
	privBytes, err := hex.DecodeString(ownerPrivHex)
	require.NoError(t, err)
	privKey, _ := btcec.PrivKeyFromBytes(privBytes)
	digest := sha256.Sum256(nipOA.Preimage(eventPubkey, conditions))
	sig, err := schnorr.Sign(privKey, digest[:])
	require.NoError(t, err)
	ownerPub, err := nip11.DerivePubKey(ownerPrivHex)
	require.NoError(t, err)
	return []string{"auth", ownerPub, conditions, hex.EncodeToString(sig.Serialize())}
}
