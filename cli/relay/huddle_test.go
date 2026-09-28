package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/huddle/wire"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip42"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/utils"
	"github.com/stretchr/testify/require"
)

const (
	huddleRelayPriv = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	huddleRelayPub  = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"
	huddleRelayURL  = "wss://relay.test"
	alicePriv       = "0acd1245d4b0e9cae1a3b0e1a1e0cf3d1e5b2a7c8d9e0f1a2b3c4d5e6f70268c"
	bobPriv         = "1bde2356e5c1fadbf2b4c1f2b2f1d04e2f6c3b8d9eaf102b3c4d5e6f78901234"
)

// huddleControl is the subset of the endpoint's control plane these tests read.
type huddleControl struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Code      string `json:"code"`
	Pubkey    string `json:"pubkey"`
	PeerIndex uint8  `json:"peer_index"`
	Epoch     uint8  `json:"epoch"`
}

func newHuddleStore(t *testing.T) *relay.EventStore {
	t.Helper()
	store, err := relay.NewEventStore(filepath.Join(t.TempDir(), "test.db"), &nip11.Limitation{MaxLimit: 1000})
	require.NoError(t, err)
	return store
}

// bootRelay boots a real NewServer with huddle configured as given, exactly the
// way initConfig would populate the package-level config var in production.
func bootRelay(t *testing.T, huddle *HuddleConfig, url string) *httptest.Server {
	t.Helper()

	prevConfig := config
	t.Cleanup(func() { config = prevConfig })

	config = RelayConfig{
		Nip11:  nip11.Metadata{PubKey: huddleRelayPub, PrivKey: huddleRelayPriv, URL: url},
		Huddle: huddle,
	}

	s := NewServer(newHuddleStore(t), nil)
	t.Cleanup(s.Stop)

	ts := httptest.NewServer(s.server.Handler)
	t.Cleanup(ts.Close)
	return ts
}

func readControl(t *testing.T, conn *websocket.Conn) huddleControl {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	kind, data, err := conn.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, websocket.TextMessage, kind, "expected a control frame, got %x", data)

	var msg huddleControl
	require.NoError(t, json.Unmarshal(data, &msg))
	return msg
}

// handshake runs challenge -> signed auth -> joined against the audio endpoint.
func handshake(t *testing.T, ts *httptest.Server, roomID, privKey string) (*websocket.Conn, huddleControl) {
	return handshakeOn(t, ts, "audio", roomID, privKey)
}

// handshakeOn is handshake against a named endpoint ("audio" or "rtc"). Both
// admit a peer to the room during the handshake, before any media negotiation,
// so this is enough to put either kind of peer in a room.
func handshakeOn(t *testing.T, ts *httptest.Server, endpoint, roomID, privKey string) (*websocket.Conn, huddleControl) {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/huddle/" + roomID + "/" + endpoint
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	challenge := readControl(t, conn)
	require.Equal(t, "challenge", challenge.Type)
	require.NotEmpty(t, challenge.Challenge)

	// The relay tag names the relay itself, not the huddle endpoint.
	ev := nip42.NewAuthEvent(challenge.Challenge, huddleRelayURL)
	require.NoError(t, ev.Sign(privKey))
	require.NoError(t, conn.WriteJSON(map[string]any{
		"type":             "auth",
		"event":            ev,
		"protocol_version": 3,
	}))

	return conn, readControl(t, conn)
}

// TestHuddleEndpointIsMountedAndAdmitsAPeer is the end-to-end check that the
// huddle: block in relay.yaml actually reaches a live endpoint: it boots a real
// NewServer, dials /huddle/{id}/audio over a real WebSocket, and completes the
// NIP-42 handshake. Nothing below the config layer is stubbed.
func TestHuddleEndpointIsMountedAndAdmitsAPeer(t *testing.T) {
	ts := bootRelay(t, &HuddleConfig{Enabled: true}, huddleRelayURL)

	_, joined := handshake(t, ts, "room-1", alicePriv)

	alicePub, err := utils.GetPublicKey(alicePriv)
	require.NoError(t, err)
	require.Equal(t, "joined", joined.Type, "got %+v", joined)
	require.Equal(t, alicePub, joined.Pubkey)
	require.NotZero(t, joined.Epoch, "epochs start at 1 so 0 can mean unset")
}

// Disabled means the route is not mounted at all, so a client sees the same 404
// an older relay would give it rather than a huddle-specific error.
func TestHuddleEndpointAbsentWhenDisabled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		huddle *HuddleConfig
	}{
		{name: "block omitted", huddle: nil},
		{name: "explicitly disabled", huddle: &HuddleConfig{Enabled: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := bootRelay(t, tc.huddle, huddleRelayURL)

			resp, err := http.Get(ts.URL + "/huddle/room-1/audio")
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusNotFound, resp.StatusCode)
		})
	}
}

// The point of the whole exercise: two peers on a real ncli relay hear each
// other, with the speaker's routing identity attached and the Opus payload
// arriving byte-identical.
func TestTwoPeersHearEachOtherThroughTheRelay(t *testing.T) {
	ts := bootRelay(t, &HuddleConfig{Enabled: true}, huddleRelayURL)

	aliceConn, alice := handshake(t, ts, "room-1", alicePriv)
	require.Equal(t, "joined", alice.Type)
	bobConn, bob := handshake(t, ts, "room-1", bobPriv)
	require.Equal(t, "joined", bob.Type)

	// Alice is told Bob joined.
	require.Equal(t, "joined", readControl(t, aliceConn).Type)

	opus := []byte("opus-payload")
	frame := wire.EncodeFrame(wire.FrameHeader{Seq: 7, Ts48k: 960, LevelDbov: -20}, opus)
	require.NoError(t, aliceConn.WriteMessage(websocket.BinaryMessage, frame))

	require.NoError(t, bobConn.SetReadDeadline(time.Now().Add(10*time.Second)))
	kind, got, err := bobConn.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, websocket.BinaryMessage, kind)

	index, epoch, payload, ok := wire.ParseRelayFrame(3, got)
	require.True(t, ok, "unparseable relayed frame %x", got)
	require.Equal(t, alice.PeerIndex, index, "frame attributed to the wrong peer")
	require.Equal(t, alice.Epoch, epoch)
	require.Equal(t, frame, payload, "the author's frame must arrive verbatim")

	header, carried, hasHeader := wire.ParseFrame(payload)
	require.True(t, hasHeader)
	require.Equal(t, uint16(7), header.Seq)
	require.Equal(t, opus, carried, "the Opus payload must survive the relay untouched")
}

// registerHuddleRoutes is exercised directly here so the test can enrol a
// member, which NewServer gives no handle to.
func TestHuddleMembershipGate(t *testing.T) {
	newGatedRelay := func(t *testing.T, enrol []string) *httptest.Server {
		t.Helper()

		store := newHuddleStore(t)
		t.Cleanup(store.Close)

		metadata := &nip11.Metadata{PubKey: huddleRelayPub, PrivKey: huddleRelayPriv, URL: huddleRelayURL}
		wsHandler := relay.NewSessionHandler(store, metadata, nil)

		if membership := wsHandler.Membership(); membership != nil {
			for _, pubkey := range enrol {
				require.NoError(t, membership.Join(pubkey, nil))
			}
		} else {
			require.Empty(t, enrol, "cannot enrol without a membership service")
		}

		mux := http.NewServeMux()
		rooms := registerHuddleRoutes(mux, wsHandler, &HuddleConfig{Enabled: true, RequireMembership: true}, huddleRelayURL)
		require.NotNil(t, rooms, "the endpoint should be mounted")
		t.Cleanup(func() { endHuddleRooms(rooms) })

		ts := httptest.NewServer(mux)
		t.Cleanup(ts.Close)
		return ts
	}

	alicePub, err := utils.GetPublicKey(alicePriv)
	require.NoError(t, err)

	t.Run("a non-member is refused", func(t *testing.T) {
		ts := newGatedRelay(t, nil)
		_, msg := handshake(t, ts, "room-1", alicePriv)
		require.Equal(t, "error", msg.Type, "got %+v", msg)
		require.Equal(t, "join_rejected", msg.Code)
	})

	t.Run("an enrolled member is admitted", func(t *testing.T) {
		ts := newGatedRelay(t, []string{alicePub})
		_, msg := handshake(t, ts, "room-1", alicePriv)
		require.Equal(t, "joined", msg.Type, "got %+v", msg)
		require.Equal(t, alicePub, msg.Pubkey)
	})
}

// A NIP-42 event naming a different relay must not get in, which is what makes
// config.Nip11.URL load-bearing rather than decorative.
func TestHuddleRejectsAnAuthEventForAnotherRelay(t *testing.T) {
	ts := bootRelay(t, &HuddleConfig{Enabled: true}, huddleRelayURL)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/huddle/room-1/audio"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	challenge := readControl(t, conn)
	require.Equal(t, "challenge", challenge.Type)

	ev := nip42.NewAuthEvent(challenge.Challenge, "wss://somewhere.else")
	require.NoError(t, ev.Sign(alicePriv))
	require.NoError(t, conn.WriteJSON(map[string]any{"type": "auth", "event": ev, "protocol_version": 3}))

	msg := readControl(t, conn)
	require.Equal(t, "error", msg.Type)
	require.Equal(t, "auth_failed", msg.Code)
}

// endHuddleRooms tells live peers to close, which Shutdown cannot do for
// hijacked WebSocket connections.
func TestHuddleRoomsAreEndedOnShutdown(t *testing.T) {
	store := newHuddleStore(t)
	t.Cleanup(store.Close)

	metadata := &nip11.Metadata{PubKey: huddleRelayPub, PrivKey: huddleRelayPriv, URL: huddleRelayURL}
	wsHandler := relay.NewSessionHandler(store, metadata, nil)

	mux := http.NewServeMux()
	rooms := registerHuddleRoutes(mux, wsHandler, &HuddleConfig{Enabled: true}, huddleRelayURL)
	require.NotNil(t, rooms)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	_, joined := handshake(t, ts, "room-1", alicePriv)
	require.Equal(t, "joined", joined.Type)

	require.Eventually(t, func() bool { return rooms.Occupancy()["room-1"] == 1 },
		10*time.Second, 5*time.Millisecond, "peer never registered")

	endHuddleRooms(rooms)

	require.Zero(t, rooms.Len(), "live rooms survived shutdown")
	// Idempotent: a second Stop must not panic.
	endHuddleRooms(rooms)
	endHuddleRooms(nil)
}

// TestHuddleNIPsAreDeclared guards the blank imports in command.go. Registration
// is by linkage: a relayreg package declares its NIP and registers its event
// validators from init(), so forgetting the import leaves the relay silently not
// advertising the NIP and not validating its kinds -- with nothing failing.
func TestHuddleNIPsAreDeclared(t *testing.T) {
	declared := map[string]bool{}
	for _, id := range relay.RegisteredNIPs() {
		declared[id.String()] = true
	}

	// The room model and the media kinds a huddle-hosting relay ingests.
	for _, want := range []string{"29", "53", "71", "A0"} {
		if !declared[want] {
			var have []string
			for id := range declared {
				have = append(have, id)
			}
			sort.Strings(have)
			t.Errorf("NIP-%s is not declared; blank-import its relayreg in command.go. Declared: %v", want, have)
		}
	}
}

// TestWebSocketAndWebRTCPeersShareARoom is why both endpoints are handed the same
// room manager: a browser and a buzz client using one room id have to end up in
// one call. Give them separate managers and each would sit alone in its own room,
// hearing nothing, with no error anywhere to say why.
func TestWebSocketAndWebRTCPeersShareARoom(t *testing.T) {
	store := newHuddleStore(t)
	t.Cleanup(store.Close)

	metadata := &nip11.Metadata{PubKey: huddleRelayPub, PrivKey: huddleRelayPriv, URL: huddleRelayURL}
	wsHandler := relay.NewSessionHandler(store, metadata, nil)

	mux := http.NewServeMux()
	rooms := registerHuddleRoutes(mux, wsHandler, &HuddleConfig{Enabled: true, RTC: true}, huddleRelayURL)
	require.NotNil(t, rooms)
	t.Cleanup(func() { endHuddleRooms(rooms) })

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// A buzz-style client on the WebSocket endpoint.
	wsConn, wsPeer := handshakeOn(t, ts, "audio", "room-1", alicePriv)
	require.Equal(t, "joined", wsPeer.Type)

	// A browser on the WebRTC endpoint, same room id.
	_, rtcPeer := handshakeOn(t, ts, "rtc", "room-1", bobPriv)
	require.Equal(t, "joined", rtcPeer.Type, "got %+v", rtcPeer)

	// One room, two occupants -- not two rooms of one.
	require.Eventually(t, func() bool { return rooms.Occupancy()["room-1"] == 2 },
		10*time.Second, 10*time.Millisecond, "occupancy = %v, want room-1 with 2", rooms.Occupancy())
	require.Equal(t, 1, rooms.Len(), "the two peers landed in separate rooms")

	// They have distinct routing identities within that one room.
	require.NotEqual(t, wsPeer.PeerIndex, rtcPeer.PeerIndex, "both peers share a routing index")

	// And the WebSocket peer is told the browser joined, over the shared control
	// plane -- proof they are in one room's roster, not merely one manager.
	notice := readControl(t, wsConn)
	require.Equal(t, "joined", notice.Type, "got %+v", notice)
	require.Equal(t, rtcPeer.Pubkey, notice.Pubkey)
}

func TestHuddleRTCEndpointOnlyWhenEnabled(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rtc        bool
		wantStatus int
	}{
		{name: "rtc enabled", rtc: true, wantStatus: http.StatusBadRequest}, // upgrade required, not 404
		{name: "rtc disabled", rtc: false, wantStatus: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := bootRelay(t, &HuddleConfig{Enabled: true, RTC: tc.rtc}, huddleRelayURL)

			resp, err := http.Get(ts.URL + "/huddle/room-1/rtc")
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, tc.wantStatus, resp.StatusCode)
		})
	}
}

func TestICEServersConversion(t *testing.T) {
	got := iceServers([]ICEServerConfig{
		{URLs: []string{"stun:stun.example:3478"}},
		{URLs: []string{"turn:turn.example:3478"}, Username: "u", Credential: "p"},
		{URLs: nil}, // dropped: a server with no URL is not a server
	})
	require.Len(t, got, 2)
	require.Equal(t, []string{"stun:stun.example:3478"}, got[0].URLs)
	require.Empty(t, got[0].Username, "STUN needs no credentials")
	require.Equal(t, "u", got[1].Username)
	require.Equal(t, "p", got[1].Credential)
}
