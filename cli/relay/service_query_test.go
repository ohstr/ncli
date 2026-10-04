package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
	"github.com/stretchr/testify/require"
)

// bootQueryServer boots a real NewServer (the function that reads the
// package-level `config` var) against a store seeded with one fixture
// event, wraps it in a real HTTP server, and returns the server's /query
// URL and its store -- end-to-end coverage of the config.Query ->
// queryEnabled() -> mux.Handle("/query", ...) wiring, not just the unit
// pieces.
func bootQueryServer(t *testing.T, query *QueryConfig) string {
	t.Helper()

	prevConfig := config
	t.Cleanup(func() { config = prevConfig })

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := relay.NewEventStore(dbPath, &nip11.Limitation{MaxLimit: 1000})
	require.NoError(t, err)

	fixture := &nip01.Event{
		ID:        strings.Repeat("1", 64),
		PubKey:    testMember,
		Kind:      1,
		CreatedAt: uint64(time.Now().Unix()),
		Content:   "hello from the query bridge",
	}
	require.NoError(t, store.InsertEvents(context.Background(), []*nip01.Event{fixture}))

	config = RelayConfig{
		Nip11: nip11.Metadata{PubKey: testPubKey, PrivKey: testPrivKey},
		Query: query,
	}

	s := NewServer(store, nil)
	t.Cleanup(s.Stop)

	ts := httptest.NewServer(s.server.Handler)
	t.Cleanup(ts.Close)

	return ts.URL
}

func TestNewServer_QueryDisabledByDefault(t *testing.T) {
	url := bootQueryServer(t, nil)

	resp, err := http.Post(url+"/query", "application/json", strings.NewReader(`[]`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusNotFound, resp.StatusCode, "query: {} omitted (or enabled: false) must leave /query unmounted")
}

func TestNewServer_QueryOverHTTPReturnsMatchingEvents(t *testing.T) {
	url := bootQueryServer(t, &QueryConfig{Enabled: true})
	queryURL := url + "/query"

	body := []byte(`[{"kinds":[1]}]`)
	header, err := common.GenerateNIP98Header(testPrivKey, queryURL, http.MethodPost, body)
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
	require.Len(t, events, 1)
	require.Equal(t, testMember, events[0].PubKey)
	require.Equal(t, "hello from the query bridge", events[0].Content)
}

// NIP-98 here binds identity, not authorization: a signer who is nobody's
// idea of a member must still be served, the same as an anonymous REQ for
// the same filter would be over the WebSocket.
func TestNewServer_QueryServesAnyValidSigner_NotJustMembers(t *testing.T) {
	url := bootQueryServer(t, &QueryConfig{Enabled: true})
	queryURL := url + "/query"

	const nonMemberPrivKey = "0000000000000000000000000000000000000000000000000000000000000f"
	body := []byte(`[{"kinds":[1]}]`)
	header, err := common.GenerateNIP98Header(nonMemberPrivKey, queryURL, http.MethodPost, body)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, queryURL, strings.NewReader(string(body)))
	require.NoError(t, err)
	req.Header.Set("Authorization", header)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "query is not a membership/allowlist gate")
}

func TestNewServer_QueryRejectsUnauthenticatedRequest(t *testing.T) {
	url := bootQueryServer(t, &QueryConfig{Enabled: true})

	resp, err := http.Post(url+"/query", "application/json", strings.NewReader(`[{"kinds":[1]}]`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "NIP-98 is mandatory even though it isn't a membership gate")
}

func TestNewServer_QueryAdvertisesNIPCWWhenEnabled(t *testing.T) {
	prevConfig := config
	t.Cleanup(func() { config = prevConfig })

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := relay.NewEventStore(dbPath, &nip11.Limitation{MaxLimit: 1000})
	require.NoError(t, err)

	config = RelayConfig{
		Nip11: nip11.Metadata{PubKey: testPubKey, PrivKey: testPrivKey},
		Query: &QueryConfig{Enabled: true},
	}

	s := NewServer(store, nil)
	t.Cleanup(s.Stop)

	ts := httptest.NewServer(s.server.Handler)
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", nip11.ContentTypeHeader)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var doc struct {
		SupportedNIPs []json.RawMessage `json:"supported_nips"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))

	var sawCW bool
	for _, raw := range doc.SupportedNIPs {
		if strings.Contains(string(raw), "CW") {
			sawCW = true
			break
		}
	}
	require.True(t, sawCW, "supported_nips must advertise CW once query.enabled is true, so a buzz-relay client can tell the bridge is there before trying it")
}
