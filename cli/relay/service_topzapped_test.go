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
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
	"github.com/stretchr/testify/require"
)

// TestNewServer_TopZappedWindowFromYAML is an end-to-end check that the
// window configured under cache.topZapped.window in relay.yaml actually
// reaches a live "top-zapped" cache query, not just the pure config-parsing
// helpers covered by TestCacheWindow. It boots a real NewServer (the exact
// function that reads the package-level `config` var and builds
// SessionConfig) against a real EventStore, drives it over an actual
// WebSocket connection, and asserts on which zaps come back for two
// configs: a narrow explicit window that must exclude an older zap, and an
// omitted window that must fall back to the SDK's 24h default and include
// it -- the second case guards against the assertion trivially passing
// regardless of what's configured.
func TestNewServer_TopZappedWindowFromYAML(t *testing.T) {
	const testPrivKey = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	const testPubKey = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"
	userInWindow := strings.Repeat("a", 64)
	userOutOfWindow := strings.Repeat("b", 64)

	newStoreWithFixtures := func(t *testing.T) *relay.EventStore {
		t.Helper()
		dbPath := filepath.Join(t.TempDir(), "test.db")
		store, err := relay.NewEventStore(dbPath, &nip11.Limitation{MaxLimit: 1000})
		require.NoError(t, err)

		now := time.Now()
		events := []*nip01.Event{
			{
				ID:        strings.Repeat("1", 64),
				PubKey:    strings.Repeat("9", 64),
				Kind:      9735,
				CreatedAt: uint64(now.Add(-20 * time.Minute).Unix()),
				Tags:      [][]string{{"p", userInWindow}, {"amount", "1000"}},
			},
			{
				ID:        strings.Repeat("2", 64),
				PubKey:    strings.Repeat("9", 64),
				Kind:      9735,
				CreatedAt: uint64(now.Add(-45 * time.Minute).Unix()),
				Tags:      [][]string{{"p", userOutOfWindow}, {"amount", "5000"}},
			},
		}
		require.NoError(t, store.InsertEvents(context.Background(), events))
		return store
	}

	// queryTopZapped boots a real NewServer against store, sends a
	// window-less "top-zapped" cache REQ over a real WebSocket connection,
	// and returns the aggregated stats from the signed response.
	queryTopZapped := func(t *testing.T, topZapped *TopZappedConfig) []relay.ZapStats {
		t.Helper()

		prevConfig := config
		t.Cleanup(func() { config = prevConfig })

		store := newStoreWithFixtures(t)

		// Configure only via the package-level `config` var, the same thing
		// initConfig() populates from relay.yaml in production -- NewServer
		// itself never touches viper.
		config = RelayConfig{
			Nip11: nip11.Metadata{PubKey: testPubKey, PrivKey: testPrivKey},
			Cache: &CacheConfig{TopZapped: topZapped},
		}

		s := NewServer(store, nil)
		t.Cleanup(s.Stop)

		ts := httptest.NewServer(s.server.Handler)
		t.Cleanup(ts.Close)

		wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		require.NoError(t, err)
		defer conn.Close()

		require.NoError(t, conn.WriteMessage(websocket.TextMessage,
			[]byte(`["REQ","sub1",{"cache":["top-zapped",{}]}]`)))
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))

		var got *nip01.Event
		for got == nil {
			_, msg, err := conn.ReadMessage()
			require.NoError(t, err)

			var arr []json.RawMessage
			require.NoError(t, json.Unmarshal(msg, &arr))
			var msgType string
			require.NoError(t, json.Unmarshal(arr[0], &msgType))

			switch msgType {
			case "EVENT":
				got = &nip01.Event{}
				require.NoError(t, json.Unmarshal(arr[2], got))
			case "CLOSED":
				require.NotNil(t, got, "CLOSED arrived before any EVENT")
			}
		}

		require.Equal(t, 25521, got.Kind)
		require.NoError(t, got.Verify(), "response must be validly signed with nip11.privkey")

		var stats []relay.ZapStats
		require.NoError(t, json.Unmarshal([]byte(got.Content), &stats))
		return stats
	}

	t.Run("explicit 30m window excludes the 45-minute-old zap", func(t *testing.T) {
		stats := queryTopZapped(t, &TopZappedConfig{Enabled: true, Window: "30m"})

		require.Len(t, stats, 1)
		require.Equal(t, userInWindow, stats[0].Pubkey)
		require.Equal(t, uint64(1000), stats[0].TotalMLoki)
	})

	t.Run("omitted window falls back to the SDK's 24h default and includes both", func(t *testing.T) {
		stats := queryTopZapped(t, &TopZappedConfig{Enabled: true})

		require.Len(t, stats, 2)
	})
}
