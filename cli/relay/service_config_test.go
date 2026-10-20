package relay

import (
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/nip11"
	"github.com/stretchr/testify/require"
)

// A server must not read the package-level config once built: tests swap
// it while an earlier server's WebSocket sessions are still starting or
// winding down (#173). Under -race this fails if sessions hold a pointer
// into it (nmilat reads the metadata as each connection opens).
func TestSessionsDontReadTheGlobalConfig(t *testing.T) {
	ts := bootScenario(t, RelayConfig{
		Nip11:        nip11.Metadata{PubKey: scenarioMemberPub},
		RelayNotesDb: "unused",
		Port:         defaultPort,
	})
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/"

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Swapped right after the upgrade, as a test cleanup does, while the
	// server is still setting the session up.
	config.Nip11 = nip11.Metadata{}

	// A round trip makes sure the session got past its setup.
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`["REQ","s",{"kinds":[1],"limit":1}]`)))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	// Skips the AUTH challenge the relay sends on connect.
	for {
		_, msg, err := conn.ReadMessage()
		require.NoError(t, err)
		if strings.Contains(string(msg), `["EOSE","s"]`) {
			return
		}
	}
}
