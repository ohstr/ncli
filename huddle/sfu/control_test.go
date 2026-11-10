package sfu_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/ncli/huddle/sfu"
	"github.com/ohstr/nmilat/huddle/room"
)

// The control plane: roster updates, media state and leave reasons, delivered
// over signalling. None of it needs a negotiated peer connection -- the relay
// reads and writes control as soon as a peer is in the room -- so these drive
// bare signalling sockets.

const carolPriv = "3cf0457a8b9c0d1e2f3a4b5c6d7e8f9011223344556677889900aabbccddeeff"

// join authenticates a signalling socket and returns it with its own joined.
func join(t *testing.T, h *harness, roomID, privKey string) (*websocket.Conn, message) {
	t.Helper()
	conn := h.dial(roomID)
	joined := authenticate(t, conn, privKey, relayURL)
	if joined.Type != "joined" {
		t.Fatalf("got %+v, want joined", joined)
	}
	return conn, joined
}

// readType reads until a message of the given type arrives, skipping the rest.
func readType(t *testing.T, conn *websocket.Conn, kind string) message {
	t.Helper()
	for {
		if m := readMessage(t, conn); m.Type == kind {
			return m
		}
	}
}

func sendJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	if err := conn.WriteJSON(v); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestRTCJoinedAdvertisesFeatures(t *testing.T) {
	h := newHarness(t, nil)
	_, joined := join(t, h, "room-1", alicePriv)
	for _, want := range []string{"control", "media", "leave"} {
		if !slices.Contains(joined.Features, want) {
			t.Errorf("features = %v, want %q among them", joined.Features, want)
		}
	}
}

// TestRTCPeerHearsOthersJoinAndLeave is the roster a browser never used to get:
// the room broadcast joined/left, but an RTC sink discarded control.
func TestRTCPeerHearsOthersJoinAndLeave(t *testing.T) {
	h := newHarness(t, nil)
	alice, _ := join(t, h, "room-1", alicePriv)
	bob, bobJoined := join(t, h, "room-1", bobPriv)

	if m := readType(t, alice, "joined"); m.Pubkey != bobJoined.Pubkey || !slices.Contains(m.Features, "media") {
		t.Fatalf("alice heard %+v, want bob's joined with features", m)
	}

	sendJSON(t, bob, map[string]any{"type": "leave"})
	left := readType(t, alice, "left")
	if left.Pubkey != bobJoined.Pubkey || left.Reason != sfu.ReasonLeave {
		t.Fatalf("left = %+v, want bob with reason %q", left, sfu.ReasonLeave)
	}
	h.waitForOccupancy("room-1", 1)
}

func TestRTCAbruptCloseIsReportedAsDropped(t *testing.T) {
	h := newHarness(t, nil)
	alice, _ := join(t, h, "room-1", alicePriv)
	bob, bobJoined := join(t, h, "room-1", bobPriv)
	readType(t, alice, "joined")

	_ = bob.Close()
	left := readType(t, alice, "left")
	if left.Pubkey != bobJoined.Pubkey || left.Reason != sfu.ReasonDropped {
		t.Fatalf("left = %+v, want bob with reason %q", left, sfu.ReasonDropped)
	}
}

// TestRTCEvictionClosesTheSession: revoking membership evicts through the room,
// which an RTC session used to ignore -- it left the room but kept its socket.
func TestRTCEvictionClosesTheSession(t *testing.T) {
	h := newHarness(t, nil)
	alice, _ := join(t, h, "room-1", alicePriv)
	bob, bobJoined := join(t, h, "room-1", bobPriv)
	readType(t, alice, "joined")

	if n := h.rooms.EvictPubkey(bobJoined.Pubkey, ""); n != 1 {
		t.Fatalf("evicted %d peers, want 1", n)
	}

	if err := bob.SetReadDeadline(time.Now().Add(window)); err != nil {
		t.Fatal(err)
	}
	for {
		if _, _, err := bob.ReadMessage(); err != nil {
			break // closed, as it should be
		}
	}
	if left := readType(t, alice, "left"); left.Reason != sfu.ReasonEvicted {
		t.Fatalf("left = %+v, want reason %q", left, sfu.ReasonEvicted)
	}
}

// TestRTCMediaReachesEveryPeer: a stopped camera is announced to the other
// browsers at once, and WebSocket peers receive it too (they ignore it, per the
// spec, but it costs nothing and keeps the room's control uniform).
func TestRTCMediaReachesEveryPeer(t *testing.T) {
	h := newHarness(t, nil)
	wsSink := room.NewChannelSink()
	if _, _, _, err := h.rooms.Join("room-1", "ws-peer", 3, wsSink); err != nil {
		t.Fatalf("ws peer join: %v", err)
	}
	alice, _ := join(t, h, "room-1", alicePriv)
	bob, bobJoined := join(t, h, "room-1", bobPriv)
	readType(t, alice, "joined")

	sendJSON(t, bob, map[string]any{"type": "media", "kind": "camera", "state": "off", "track_id": "cam-1", "stream_id": "s-1"})

	m := readType(t, alice, "media")
	if m.Pubkey != bobJoined.Pubkey || m.Kind != "camera" || m.State != sfu.MediaOff || m.TrackID != "cam-1" || m.StreamID != "s-1" || m.Seq != 1 {
		t.Fatalf("media = %+v, want bob's camera off, seq 1", m)
	}

	deadline := time.After(window)
	for {
		select {
		case c := <-wsSink.Control():
			var got message
			if json.Unmarshal([]byte(c.JSON), &got) == nil && got.Type == "media" {
				if got.Pubkey != bobJoined.Pubkey || got.State != sfu.MediaOff {
					t.Fatalf("ws peer got %+v", got)
				}
				return
			}
		case <-deadline:
			t.Fatal("the WebSocket peer never received the media message")
		}
	}
}

// TestRTCMediaIgnoresRepeatsAndInvalid: only real changes are broadcast and
// numbered, and a malformed message is dropped rather than relayed.
func TestRTCMediaIgnoresRepeatsAndInvalid(t *testing.T) {
	h := newHarness(t, nil)
	alice, _ := join(t, h, "room-1", alicePriv)
	bob, _ := join(t, h, "room-1", bobPriv)
	readType(t, alice, "joined")

	sendJSON(t, bob, map[string]any{"type": "media", "kind": "hologram", "state": "live"})
	sendJSON(t, bob, map[string]any{"type": "media", "kind": "camera", "state": "paused"})
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'x'
	}
	sendJSON(t, bob, map[string]any{"type": "media", "kind": "camera", "state": "live", "track_id": string(long)})
	sendJSON(t, bob, map[string]any{"type": "media", "kind": "camera", "state": "live", "track_id": "cam"})
	sendJSON(t, bob, map[string]any{"type": "media", "kind": "camera", "state": "live", "track_id": "cam"})
	sendJSON(t, bob, map[string]any{"type": "media", "kind": "camera", "state": "off", "track_id": "cam"})

	first := readType(t, alice, "media")
	second := readType(t, alice, "media")
	if first.State != sfu.MediaLive || first.Seq != 1 || first.TrackID != "cam" {
		t.Fatalf("first = %+v, want camera live seq 1", first)
	}
	if second.State != sfu.MediaOff || second.Seq != 2 {
		t.Fatalf("second = %+v, want camera off seq 2 (the repeat must not be relayed)", second)
	}
}

// TestRTCJoinedSnapshotCarriesMedia: someone joining mid-call learns whose
// camera is off from the roster itself, not from the next change.
func TestRTCJoinedSnapshotCarriesMedia(t *testing.T) {
	h := newHarness(t, nil)
	bob, _ := join(t, h, "room-1", bobPriv)
	alice, aliceJoined := join(t, h, "room-1", alicePriv)
	readType(t, bob, "joined")

	sendJSON(t, alice, map[string]any{"type": "media", "kind": "camera", "state": "off", "track_id": "cam"})
	sendJSON(t, alice, map[string]any{"type": "media", "kind": "screen", "state": "live", "track_id": "scr"})
	readType(t, bob, "media")
	readType(t, bob, "media") // both applied before carol arrives

	_, carolJoined := join(t, h, "room-1", carolPriv)
	var aliceEntry *peerMessage
	for i := range carolJoined.Peers {
		if carolJoined.Peers[i].Pubkey == aliceJoined.Pubkey {
			aliceEntry = &carolJoined.Peers[i]
		}
	}
	if aliceEntry == nil {
		t.Fatalf("roster %+v has no entry for alice", carolJoined.Peers)
	}
	want := []mediaMessage{
		{Kind: "camera", State: sfu.MediaOff, TrackID: "cam", Seq: 1},
		{Kind: "screen", State: sfu.MediaLive, TrackID: "scr", Seq: 2},
	}
	if !slices.Equal(aliceEntry.Media, want) {
		t.Fatalf("alice's media = %+v, want %+v", aliceEntry.Media, want)
	}
}
