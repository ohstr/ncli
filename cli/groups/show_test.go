package groups

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/wire"
)

// newEmptyRelay answers every REQ with a bare EOSE: a relay that refuses
// nothing and simply has no such group.
func newEmptyRelay(t *testing.T) *url.URL {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var payload wire.RelayPayload
			if err := json.Unmarshal(data, &payload); err != nil {
				return
			}
			if req, ok := payload.Packet.(*wire.RequestPacket); ok {
				b, _ := (&wire.EOSESubscriptionResponse{SubscriptionID: req.SubscriptionID}).MarshalJSON()
				_ = conn.WriteMessage(websocket.TextMessage, b)
			}
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse("ws://" + srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestGroupsShow_MissingGroupIsNotFound(t *testing.T) {
	relayURL := newEmptyRelay(t)
	for _, jsonMode := range []bool{true, false} {
		cmd := NewGroupsCommand()
		cmd.PersistentFlags().Bool("json", jsonMode, "")
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs([]string{"--relay", relayURL.String(), "show", "nosuch"})

		err := cmd.Execute()
		var cliErr *common.CLIError
		if !errors.As(err, &cliErr) || cliErr.Code != common.CodeNotFound {
			t.Errorf("json=%v: show of a missing group = %v, want not_found", jsonMode, err)
		}
	}
}
