package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/nip01"
)

// mockWSServer starts a plain (non-TLS) websocket-upgrading test server and
// returns its ws:// URL plus a hit counter -- used to prove a fallback URL
// was actually dialed, not just that the overall call errored (which could
// happen even without a retry).
func mockWSServer(t *testing.T) (wsURL *url.URL, hits *int32) {
	t.Helper()
	hits = new(int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.Close()
	}))
	t.Cleanup(server.Close)

	u, err := url.Parse("ws" + server.URL[len("http"):])
	if err != nil {
		t.Fatal(err)
	}
	return u, hits
}

// wrongSchemeURL returns a wss:// URL pointing at wsURL's host:port -- since
// the mock server isn't TLS, dialing this always fails with a
// *relayclient.ConnectionError, exactly like a real relay that only speaks
// ws:// would when a caller tries wss:// first (the scenario
// connectRelayWithFallback/readEventsWithFallback exist for).
func wrongSchemeURL(t *testing.T, wsURL *url.URL) *url.URL {
	t.Helper()
	u, err := url.Parse("wss://" + wsURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestConnectRelayWithFallback_RetriesOnDialFailure(t *testing.T) {
	wsURL, hits := mockWSServer(t)
	primary := wrongSchemeURL(t, wsURL)

	conn, err := connectRelayWithFallback(context.Background(), primary, wsURL, nil)
	if err != nil {
		t.Fatalf("connectRelayWithFallback() error = %v, want the ws:// fallback to succeed", err)
	}
	defer conn.Close()

	if atomic.LoadInt32(hits) == 0 {
		t.Fatal("fallback server was never dialed")
	}
}

func TestConnectRelayWithFallback_NoFallbackNoRetry(t *testing.T) {
	badURL, err := url.Parse("wss://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := connectRelayWithFallback(context.Background(), badURL, nil, nil); err == nil {
		t.Fatal("connectRelayWithFallback() with a nil fallback error = nil, want the primary's dial error")
	}
}

func TestReadEventsWithFallback_RetriesOnDialFailure(t *testing.T) {
	wsURL, hits := mockWSServer(t)
	primary := wrongSchemeURL(t, wsURL)

	// The mock server upgrades then closes without ever sending an EOSE, so
	// this still ends in an error -- what this test proves is that the
	// fallback URL actually got dialed (hits > 0), not that the whole
	// read succeeds; full read-to-EOSE behavior belongs to
	// relayclient.ReadEventsFromRelay itself, unchanged by this feature.
	_, err := readEventsWithFallback(context.Background(), 0, primary, wsURL, nip01.NewSubscriptionFilterGroup())
	if err == nil {
		t.Fatal("readEventsWithFallback() error = nil, want an error (mock server sends no EOSE)")
	}
	if atomic.LoadInt32(hits) == 0 {
		t.Fatal("fallback server was never dialed")
	}
}

func TestReadEventsWithFallback_NoFallbackNoRetry(t *testing.T) {
	badURL, err := url.Parse("wss://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := readEventsWithFallback(context.Background(), 0, badURL, nil, nip01.NewSubscriptionFilterGroup()); err == nil {
		t.Fatal("readEventsWithFallback() with a nil fallback error = nil, want the primary's dial error")
	}
}
