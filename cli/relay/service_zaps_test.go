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
	"github.com/stretchr/testify/require"
)

type rankedZap struct {
	Pubkey string `json:"pubkey"`
	Total  uint64 `json:"total"`
}

// TestNewServer_ZapRanking boots a real NewServer from a cache.zaps config
// and asks for rankings over a WebSocket: windows and coins come from the
// config, the default is the first of each, and anything else is refused.
func TestNewServer_ZapRanking(t *testing.T) {
	const testPrivKey = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	const testPubKey = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"
	recent, older := strings.Repeat("a", 64), strings.Repeat("b", 64)

	prevConfig := config
	t.Cleanup(func() { config = prevConfig })
	store, err := relay.NewEventStore(filepath.Join(t.TempDir(), "test.db"), &nip11.Limitation{MaxLimit: 1000})
	require.NoError(t, err)
	now := time.Now()
	sender, provider := zaptest.NewKey(t, 1), zaptest.NewKey(t, 2)
	require.NoError(t, store.InsertEvents(context.Background(), []*nip01.Event{
		zaptest.Receipt(t, sender, provider, recent, 1_000_000, uint64(now.Add(-20*time.Minute).Unix())),
		zaptest.Receipt(t, sender, provider, older, 5_000_000, uint64(now.Add(-45*time.Minute).Unix())),
		zaptest.LokiReceipt(t, sender, provider, recent, 9_000_000, uint64(now.Add(-10*time.Minute).Unix())),
	}))

	config = RelayConfig{
		Nip11: nip11.Metadata{PubKey: testPubKey, PrivKey: testPrivKey},
		Cache: &CacheConfig{Zaps: &ZapsConfig{Enabled: true, Windows: []string{"30m", "24h"}, MaxLimit: 10}},
	}
	s := NewServer(store, nil)
	t.Cleanup(s.Stop)
	ts := httptest.NewServer(s.server.Handler)
	t.Cleanup(ts.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// ask sends a cache REQ and returns the ranking event, or the CLOSED reason.
	ask := func(cache string) (*nip01.Event, string) {
		t.Helper()
		require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`["REQ","z",{"cache":`+cache+`}]`)))
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
		var got *nip01.Event
		for {
			_, msg, err := conn.ReadMessage()
			require.NoError(t, err)
			var arr []json.RawMessage
			require.NoError(t, json.Unmarshal(msg, &arr))
			var typ string
			require.NoError(t, json.Unmarshal(arr[0], &typ))
			switch typ {
			case "EVENT":
				got = &nip01.Event{}
				require.NoError(t, json.Unmarshal(arr[2], got))
			case "CLOSED":
				var reason string
				require.NoError(t, json.Unmarshal(arr[2], &reason))
				return got, reason
			}
		}
	}
	totals := func(ev *nip01.Event) []rankedZap {
		t.Helper()
		require.NotNil(t, ev)
		require.Equal(t, relay.KindZapRanking, ev.Kind)
		require.NoError(t, ev.Verify(), "signed with nip11.privkey")
		var out []rankedZap
		require.NoError(t, json.Unmarshal([]byte(ev.Content), &out))
		return out
	}

	ev, _ := ask(`["zap-ranking"]`)
	require.Equal(t, []rankedZap{{recent, 1000}}, totals(ev), "default: first window (30m) and coin (sats)")

	ev, _ = ask(`["zap-ranking",{"window":"24h"}]`)
	require.Equal(t, []rankedZap{{older, 5000}, {recent, 1000}}, totals(ev))

	ev, _ = ask(`["zap-ranking",{"coin":"loki"}]`)
	require.Equal(t, []rankedZap{{recent, 9000}}, totals(ev), "coins never mix")

	_, reason := ask(`["zap-ranking",{"window":"7d"}]`)
	require.Equal(t, "invalid: window must be one of 30m, 24h", reason)
	_, reason = ask(`["top-zapped",{}]`)
	require.Equal(t, `invalid: unknown cache action "top-zapped"`, reason)
}
