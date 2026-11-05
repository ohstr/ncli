package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/ohstr/nmilat/wire"
	"github.com/stretchr/testify/require"
)

// bootWrapServer boots NewServer with /query on and one kind 1059 addressed
// to scenarioMemberPub, signed by a one-time key as a gift wrap is.
func bootWrapServer(t *testing.T, recipientOnly []int) *httptest.Server {
	t.Helper()

	prevConfig := config
	t.Cleanup(func() { config = prevConfig })

	store, err := relay.NewEventStore(filepath.Join(t.TempDir(), "test.db"), &nip11.Limitation{MaxLimit: 1000})
	require.NoError(t, err)

	const wrapperPriv = "00000000000000000000000000000000000000000000000000000000000000aa"
	wrap, err := nip01.NewSignedEvent(1059, "sealed", wrapperPriv, []string{"p", scenarioMemberPub})
	require.NoError(t, err)
	require.NoError(t, store.InsertEvents(context.Background(), []*nip01.Event{wrap}))

	config = RelayConfig{
		Nip11:              nip11.Metadata{PubKey: testPubKey, PrivKey: testPrivKey, Self: testPubKey, URL: huddleRelayURL},
		HTTPBridge:         &HTTPBridgeConfig{Query: &QueryConfig{Enabled: true}},
		RecipientOnlyKinds: recipientOnly,
	}
	s := NewServer(store, nil)
	t.Cleanup(s.Stop)

	ts := httptest.NewServer(s.server.Handler)
	t.Cleanup(ts.Close)
	return ts
}

// reqWraps sends a REQ for kind 1059, optionally after AUTH as privKey, and
// returns the events served before EOSE, or the CLOSED message.
func reqWraps(t *testing.T, ts *httptest.Server, privKey string) (events []*nip01.Event, closed string) {
	t.Helper()
	u, err := url.Parse("ws" + strings.TrimPrefix(ts.URL, "http"))
	require.NoError(t, err)
	conn, err := relayclient.Connect(context.Background(), u)
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	if privKey != "" {
		authAs(t, conn, privKey)
	}

	conn.Outgoing() <- wire.NewRequestPacket("wraps", nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{Kinds: []int{1059}}))
	deadline := time.After(5 * time.Second)
	for {
		select {
		case res := <-conn.Read():
			switch r := res.(type) {
			case *wire.EventSubscriptionResponse:
				events = append(events, r.Event)
			case *wire.EOSESubscriptionResponse:
				return events, ""
			case *wire.ClosedSubscriptionResponse:
				return events, r.Message
			}
		case err := <-conn.Errors():
			t.Fatalf("connection error: %v", err)
		case <-deadline:
			t.Fatal("timed out waiting for EOSE")
		}
	}
}

// queryWraps asks /query for kind 1059 as privKey.
func queryWraps(t *testing.T, ts *httptest.Server, privKey string) []*nip01.Event {
	t.Helper()
	queryURL := ts.URL + "/query"
	body := []byte(`[{"kinds":[1059]}]`)
	header, err := common.GenerateNIP98Header(privKey, queryURL, http.MethodPost, body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, queryURL, strings.NewReader(string(body)))
	require.NoError(t, err)
	req.Header.Set("Authorization", header)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var events []*nip01.Event
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&events))
	return events
}

func TestNewServer_GiftWrapsOnlyToTheirRecipientByDefault(t *testing.T) {
	ts := bootWrapServer(t, nil)

	t.Run("a third party gets nothing over REQ", func(t *testing.T) {
		events, closed := reqWraps(t, ts, scenarioAgentPriv)
		require.Empty(t, closed)
		require.Empty(t, events)
	})
	t.Run("a third party gets nothing over /query", func(t *testing.T) {
		require.Empty(t, queryWraps(t, ts, scenarioAgentPriv))
	})
	t.Run("the recipient gets its wrap over REQ and /query", func(t *testing.T) {
		events, _ := reqWraps(t, ts, scenarioMemberPriv)
		require.Len(t, events, 1)
		require.Len(t, queryWraps(t, ts, scenarioMemberPriv), 1)
	})
	t.Run("an anonymous REQ naming 1059 is told to AUTH", func(t *testing.T) {
		_, closed := reqWraps(t, ts, "")
		require.True(t, strings.HasPrefix(closed, "auth-required:"), "got %q", closed)
	})
}

func TestNewServer_RecipientOnlyKindsEmptyServesWrapsToAnyone(t *testing.T) {
	ts := bootWrapServer(t, []int{})

	events, closed := reqWraps(t, ts, scenarioAgentPriv)
	require.Empty(t, closed)
	require.Len(t, events, 1)
	require.Len(t, queryWraps(t, ts, scenarioAgentPriv), 1)
}

func TestRecipientOnlyKindsConfig(t *testing.T) {
	t.Run("omitted: gift wraps", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, "port: 5500\n")
		require.Equal(t, []int{1059}, cfg.recipientOnlyKinds())
	})
	t.Run("empty list: off", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, "recipientOnlyKinds: []\n")
		require.Empty(t, cfg.recipientOnlyKinds())
	})
	t.Run("explicit list", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, "recipientOnlyKinds: [1059, 4]\n")
		require.Equal(t, []int{1059, 4}, cfg.recipientOnlyKinds())
	})
}
