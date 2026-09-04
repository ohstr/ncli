package blossom

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/wire"
)

// mockRelayServer starts a minimal websocket relay that accepts every
// EVENT frame it receives (replying OK true unconditionally, matching a
// real relay's happy path) and also stores it in memory, keyed by
// (kind, pubkey), so a subsequent REQ subscription can be answered for
// real -- a local analogue of cli/ncli/publish_test.go's write-only helper
// of the same shape (unexported there, so not importable here), extended
// with read support for "servers discover" (which needs to query a relay,
// not just publish to one). Keying by (kind, pubkey) rather than appending
// every event to a list mimics how a real relay treats a NIP-01
// replaceable event (kinds 10000-19999, which kind:10063 falls in): only
// the latest version is kept/served, so a later publish of the same kind
// by the same author overwrites the previous one rather than leaving both
// queryable. EVENT handling is otherwise unconditional accept-and-store:
// this is not a spec-compliant relay (no signature/PoW checks), just
// enough wire protocol to exercise the CLI's publish and discover paths
// end to end.
func mockRelayServer(t *testing.T) (wsURL string) {
	t.Helper()

	var mu sync.Mutex
	stored := make(map[string]*nip01.Event) // "<kind>|<pubkey>" -> latest event

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}

			var payload wire.RelayPayload
			if err := json.Unmarshal(msg, &payload); err != nil {
				continue
			}

			switch pkt := payload.Packet.(type) {
			case *wire.EventPacket:
				key := fmt.Sprintf("%d|%s", pkt.Event.Kind, pkt.Event.PubKey)
				mu.Lock()
				stored[key] = pkt.Event
				mu.Unlock()

				resp := &wire.OkSubscriptionResponse{EventID: pkt.Event.ID, Accepted: true}
				respJSON, err := resp.MarshalJSON()
				if err != nil {
					continue
				}
				if err := conn.WriteMessage(websocket.TextMessage, respJSON); err != nil {
					return
				}

			case *wire.RequestPacket:
				mu.Lock()
				var matches []*nip01.Event
				for _, ev := range stored {
					if pkt.Filters.Match(ev) {
						matches = append(matches, ev)
					}
				}
				mu.Unlock()

				for _, ev := range matches {
					evResp := &wire.EventSubscriptionResponse{SubscriptionID: pkt.SubscriptionID, Event: ev}
					evJSON, err := evResp.MarshalJSON()
					if err != nil {
						continue
					}
					if err := conn.WriteMessage(websocket.TextMessage, evJSON); err != nil {
						return
					}
				}

				eoseResp := &wire.EOSESubscriptionResponse{SubscriptionID: pkt.SubscriptionID}
				eoseJSON, err := eoseResp.MarshalJSON()
				if err != nil {
					continue
				}
				if err := conn.WriteMessage(websocket.TextMessage, eoseJSON); err != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(server.Close)

	u, _ := url.Parse(server.URL)
	u.Scheme = "ws"
	return u.String()
}

// TestServersAddPublish_RequiresIdentityBeforeMutation is the regression
// test for a code-review finding: "servers add --publish" used to save
// the server to prefs.yaml *before* checking --identity (required for
// --publish) was given, so a missing --identity left a misleading hard
// failure after a real, already-committed mutation. The fix moves that
// check into Args, which must run (and fail) before RunE ever touches
// prefs.
func TestServersAddPublish_RequiresIdentityBeforeMutation(t *testing.T) {
	r := newRunner(t)

	_, stderr, code := r.run("blossom", "servers", "add", "https://s.example", "--publish")
	if code == 0 {
		t.Fatal("exit code = 0, want a usage error (missing --identity)")
	}
	if !strings.Contains(stderr, "--identity") {
		t.Errorf("stderr = %q, want it to mention --identity", stderr)
	}

	out, _ := r.mustRun("blossom", "servers", "list", "--json")
	var got struct {
		Servers []string `json:"servers"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	if len(got.Servers) != 0 {
		t.Errorf("servers = %v, want none -- the failed --publish should not have left the server added", got.Servers)
	}
}

// TestServersPublish_PublishesUpdatedList covers both "servers add
// --publish" and "servers remove --publish" end to end against a real
// (mock) relay: the published kind:10063 event's server tags must match
// prefs.BlossomServers at the time of publish, including after a removal
// -- the second half is the regression test for "remove has no --publish
// counterpart to add", now fixed.
func TestServersPublish_PublishesUpdatedList(t *testing.T) {
	r := newRunner(t)
	relayURL := mockRelayServer(t)
	r.mustRun("prefs", "relays", "add", relayURL)

	t.Run("add --publish", func(t *testing.T) {
		out, _ := r.mustRun("blossom", "servers", "add", "https://a.example", "--identity", r.nsec, "--publish", "--json")
		var got struct {
			Added     bool   `json:"added"`
			Published string `json:"published"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
		}
		if !got.Added || got.Published == "" {
			t.Fatalf("got %+v, want added=true and a non-empty published event id", got)
		}
	})

	t.Run("add a second server --publish", func(t *testing.T) {
		r.mustRun("blossom", "servers", "add", "https://b.example", "--identity", r.nsec, "--publish")
	})

	t.Run("remove --publish re-publishes the shorter list", func(t *testing.T) {
		out, _ := r.mustRun("blossom", "servers", "remove", "https://b.example", "--identity", r.nsec, "--publish", "--json")
		var got struct {
			Removed   bool   `json:"removed"`
			Published string `json:"published"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
		}
		if !got.Removed || got.Published == "" {
			t.Fatalf("got %+v, want removed=true and a non-empty published event id", got)
		}

		listOut, _ := r.mustRun("blossom", "servers", "list", "--json")
		var list struct {
			Servers []string `json:"servers"`
		}
		if err := json.Unmarshal([]byte(listOut), &list); err != nil {
			t.Fatalf("invalid JSON: %v\nraw: %s", err, listOut)
		}
		if len(list.Servers) != 1 || list.Servers[0] != "https://a.example" {
			t.Errorf("servers = %v, want [https://a.example] after removing b.example", list.Servers)
		}
	})
}
