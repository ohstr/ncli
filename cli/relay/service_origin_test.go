package relay

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/nip11"
)

// A web client on any site must be able to open a WebSocket to the relay:
// access is NIP-42/NIP-43's business, not Origin's.
func TestWebClientsFromAnyOriginConnect(t *testing.T) {
	ts := bootScenario(t, RelayConfig{
		Nip11:        nip11.Metadata{PubKey: scenarioMemberPub},
		RelayNotesDb: "unused",
		Port:         defaultPort,
	})
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"
	for _, origin := range []string{"https://primal.net", "https://snort.social", "http://127.0.0.1:5501"} {
		h := http.Header{"Origin": {origin}}
		conn, resp, err := websocket.DefaultDialer.Dial(url, h)
		if err != nil {
			code := 0
			if resp != nil {
				code = resp.StatusCode
			}
			t.Errorf("WebSocket from %s refused (HTTP %d): %v", origin, code, err)
			continue
		}
		_ = conn.Close()
	}
}
