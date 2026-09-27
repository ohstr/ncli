package huddlesfu_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/ncli/huddlesfu"
	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/huddle/wire"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip42"
	"github.com/ohstr/nmilat/utils"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/rs/zerolog"
)

const (
	relayURL  = "wss://relay.example"
	alicePriv = "0acd1245d4b0e9cae1a3b0e1a1e0cf3d1e5b2a7c8d9e0f1a2b3c4d5e6f70268c"
	bobPriv   = "1bde2356e5c1fadbf2b4c1f2b2f1d04e2f6c3b8d9eaf102b3c4d5e6f78901234"
	window    = 20 * time.Second
)

// message is the loose shape of a signalling frame, for the client side.
type message struct {
	Type      string                   `json:"type"`
	Challenge string                   `json:"challenge"`
	Code      string                   `json:"code"`
	Pubkey    string                   `json:"pubkey"`
	PeerIndex uint8                    `json:"peer_index"`
	Epoch     uint8                    `json:"epoch"`
	SDP       string                   `json:"sdp"`
	Candidate *webrtc.ICECandidateInit `json:"candidate"`
}

type harness struct {
	t     *testing.T
	srv   *httptest.Server
	rooms *room.Manager
}

func newHarness(t *testing.T, mutate func(*huddlesfu.Config)) *harness {
	t.Helper()
	rooms := room.NewManager(0)
	cfg := huddlesfu.Config{
		Enabled:  true,
		RelayURL: relayURL,
		Rooms:    rooms,
		// Nop on purpose: a session goroutine can outlive the test, and zerolog
		// into t.Log would panic a completed *testing.T.
		Logger: zerolog.Nop(),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	mux := http.NewServeMux()
	mux.Handle("/huddle/{id}/rtc", huddlesfu.NewHandler(cfg))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &harness{t: t, srv: srv, rooms: cfg.Rooms}
}

func (h *harness) dial(roomID string) *websocket.Conn {
	h.t.Helper()
	url := "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/huddle/" + roomID + "/rtc"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		h.t.Fatalf("dial: %v", err)
	}
	h.t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func (h *harness) waitForOccupancy(roomID string, want int) {
	h.t.Helper()
	deadline := time.Now().Add(window)
	for {
		if h.rooms.Occupancy()[roomID] == want {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("occupancy = %d, want %d", h.rooms.Occupancy()[roomID], want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readMessage(t *testing.T, conn *websocket.Conn) message {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(window)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m message
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return m
}

func signedAuth(t *testing.T, privKey, challenge, relay string) *nip01.Event {
	t.Helper()
	ev := nip42.NewAuthEvent(challenge, relay)
	if err := ev.Sign(privKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return ev
}

// authenticate runs challenge -> auth and returns the joined (or error) message.
func authenticate(t *testing.T, conn *websocket.Conn, privKey, relay string) message {
	t.Helper()
	challenge := readMessage(t, conn)
	if challenge.Type != "challenge" {
		t.Fatalf("expected a challenge, got %+v", challenge)
	}
	if err := conn.WriteJSON(map[string]any{
		"type": "auth", "event": signedAuth(t, privKey, challenge.Challenge, relay), "protocol_version": 3,
	}); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	return readMessage(t, conn)
}

/////////////////////////////////////////////////////////////////////
// Handshake
/////////////////////////////////////////////////////////////////////

func TestRTCHandshakeAdmitsAPeer(t *testing.T) {
	h := newHarness(t, nil)
	pubkey, err := utils.GetPublicKey(alicePriv)
	if err != nil {
		t.Fatalf("GetPublicKey: %v", err)
	}

	joined := authenticate(t, h.dial("room-1"), alicePriv, relayURL)
	if joined.Type != "joined" {
		t.Fatalf("got %+v, want joined", joined)
	}
	if joined.Pubkey != pubkey || joined.Epoch == 0 {
		t.Errorf("unexpected joined: %+v", joined)
	}
	h.waitForOccupancy("room-1", 1)
}

func TestRTCHandshakeRejections(t *testing.T) {
	tests := []struct {
		name   string
		relay  string
		mangle bool
	}{
		{name: "auth event names another relay", relay: "wss://somewhere.else"},
		{name: "bad signature", relay: relayURL, mangle: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			conn := h.dial("room-1")
			challenge := readMessage(t, conn)

			ev := signedAuth(t, alicePriv, challenge.Challenge, tc.relay)
			if tc.mangle {
				ev.Sig = strings.Repeat("00", 64)
			}
			if err := conn.WriteJSON(map[string]any{"type": "auth", "event": ev, "protocol_version": 3}); err != nil {
				t.Fatalf("write: %v", err)
			}

			msg := readMessage(t, conn)
			if msg.Type != "error" || msg.Code != huddlesfu.CodeAuthFailed {
				t.Fatalf("got %+v, want auth_failed", msg)
			}
			if h.rooms.Len() != 0 {
				t.Errorf("a refused peer left %d room(s)", h.rooms.Len())
			}
		})
	}
}

func TestRTCDisabledSaysSo(t *testing.T) {
	h := newHarness(t, func(c *huddlesfu.Config) { c.Enabled = false })
	msg := readMessage(t, h.dial("room-1"))
	if msg.Type != "error" || msg.Code != huddlesfu.CodeAudioUnavailable {
		t.Fatalf("got %+v, want huddle_audio_unavailable", msg)
	}
}

func TestRTCRequiresARoomID(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/huddle/rtc", huddlesfu.NewHandler(huddlesfu.Config{
		Enabled: true, RelayURL: relayURL, Rooms: room.NewManager(0), Logger: zerolog.Nop(),
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/huddle/rtc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

/////////////////////////////////////////////////////////////////////
// The real thing: a WebRTC peer's audio reaching a WebSocket peer
/////////////////////////////////////////////////////////////////////

// TestBrowserAudioReachesAWebSocketPeer drives a real pion PeerConnection as the
// browser would -- offer, answer, trickle ICE, then RTP on an Opus track -- and
// asserts the audio arrives at a peer sitting in the same room over the huddle
// WebSocket path. No browser required: pion plays both ends.
func TestBrowserAudioReachesAWebSocketPeer(t *testing.T) {
	h := newHarness(t, nil)

	// The WebSocket-side listener: joined directly with a channel sink, which is
	// exactly what wsaudio does for a buzz client.
	listener := room.NewChannelSink()
	if _, _, _, err := h.rooms.Join("room-1", "ws-listener", 3, listener); err != nil {
		t.Fatalf("listener join: %v", err)
	}

	conn := h.dial("room-1")
	if joined := authenticate(t, conn, alicePriv, relayURL); joined.Type != "joined" {
		t.Fatalf("got %+v, want joined", joined)
	}
	h.waitForOccupancy("room-1", 2)

	// Client side: a peer connection publishing one Opus track.
	client, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("client peer connection: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"mic", "browser",
	)
	if err != nil {
		t.Fatalf("track: %v", err)
	}
	if _, err := client.AddTrack(track); err != nil {
		t.Fatalf("AddTrack: %v", err)
	}

	// Trickle the client's candidates to the server.
	var writeMu sync.Mutex
	send := func(v any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.WriteJSON(v)
	}
	client.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		send(map[string]any{"type": "candidate", "candidate": init})
	})

	connected := make(chan struct{})
	var once sync.Once
	client.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			once.Do(func() { close(connected) })
		}
	})

	offer, err := client.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	if err := client.SetLocalDescription(offer); err != nil {
		t.Fatalf("SetLocalDescription: %v", err)
	}
	send(map[string]any{"type": "offer", "sdp": offer.SDP})

	// Pump server signalling into the client until connected.
	go func() {
		for {
			var m message
			if err := conn.SetReadDeadline(time.Now().Add(window)); err != nil {
				return
			}
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			switch m.Type {
			case "answer":
				if err := client.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: m.SDP}); err != nil {
					t.Logf("CLIENT: SetRemoteDescription(answer) failed: %v", err)
				}
			case "offer":
				if client.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: m.SDP}) == nil {
					if answer, err := client.CreateAnswer(nil); err == nil {
						if client.SetLocalDescription(answer) == nil {
							send(map[string]any{"type": "answer", "sdp": answer.SDP})
						}
					}
				}
			case "candidate":
				if m.Candidate != nil {
					_ = client.AddICECandidate(*m.Candidate)
				}
			}
		}
	}()

	select {
	case <-connected:
	case <-time.After(window):
		t.Fatalf("the peer connection never reached connected (state %s)", client.ConnectionState())
	}

	// Now speak. Keep sending: the first packets can land before the SFU has
	// finished wiring its reader, and audio is lossy by design.
	opus := []byte{0xFC, 0xDE, 0xAD, 0xBE, 0xEF}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		seq, ts := uint16(1), uint32(960)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = track.WriteRTP(&rtp.Packet{
				Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 0xDEADBEEF, PayloadType: 111},
				Payload: opus,
			})
			seq++
			ts += 960
			time.Sleep(20 * time.Millisecond)
		}
	}()

	// The WebSocket listener must hear it, attributed to the WebRTC publisher.
	deadline := time.After(window)
	for {
		select {
		case relayed := <-listener.Audio():
			_, _, payload, ok := wire.ParseRelayFrame(3, relayed)
			if !ok {
				t.Fatalf("unparseable relayed frame: %x", relayed)
			}
			header, carried, parsed := wire.ParseFrame(payload)
			if !parsed {
				t.Fatalf("inner frame does not parse: %x", payload)
			}
			if string(carried) != string(opus) {
				t.Fatalf("payload = %x, want the browser's Opus %x", carried, opus)
			}
			if header.Ts48k == 0 {
				t.Error("timestamp did not survive the bridge")
			}
			return // success
		case <-deadline:
			t.Fatal("the WebSocket peer never heard the browser")
		}
	}
}

// browser is a pion peer connection standing in for a real browser, with the
// signalling pump that drives it. pion plays both ends, so none of this needs a
// browser or a display.
type browser struct {
	pc        *webrtc.PeerConnection
	conn      *websocket.Conn
	connected chan struct{}
}

// connectBrowser authenticates, publishes one Opus track, and completes the
// WebRTC handshake. publish is nil when the caller only wants to receive.
func connectBrowser(t *testing.T, h *harness, roomID, privKey string, publish bool) (*browser, *webrtc.TrackLocalStaticRTP) {
	t.Helper()

	conn := h.dial(roomID)
	if joined := authenticate(t, conn, privKey, relayURL); joined.Type != "joined" {
		t.Fatalf("got %+v, want joined", joined)
	}

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("client peer connection: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	b := &browser{pc: pc, conn: conn, connected: make(chan struct{})}

	var track *webrtc.TrackLocalStaticRTP
	if publish {
		// Channels is 2 because that is the SDP codec parameter for Opus, not the
		// channel count of the audio.
		track, err = webrtc.NewTrackLocalStaticRTP(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "mic", "browser")
		if err != nil {
			t.Fatalf("track: %v", err)
		}
		if _, err := pc.AddTrack(track); err != nil {
			t.Fatalf("AddTrack: %v", err)
		}
	} else {
		// Receive-only still needs a transceiver, or the offer carries no audio
		// section for the server to answer into.
		if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio,
			webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			t.Fatalf("AddTransceiver: %v", err)
		}
	}

	var writeMu sync.Mutex
	send := func(v any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.WriteJSON(v)
	}
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		send(map[string]any{"type": "candidate", "candidate": init})
	})
	var once sync.Once
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			once.Do(func() { close(b.connected) })
		}
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatalf("SetLocalDescription: %v", err)
	}
	send(map[string]any{"type": "offer", "sdp": offer.SDP})

	go func() {
		for {
			var m message
			if err := conn.SetReadDeadline(time.Now().Add(window)); err != nil {
				return
			}
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			switch m.Type {
			case "answer":
				if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: m.SDP}); err != nil {
					t.Logf("client SetRemoteDescription(answer): %v", err)
				}
			case "offer":
				// Renegotiation, which is how a newly-heard speaker's track arrives.
				if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: m.SDP}); err != nil {
					t.Logf("client SetRemoteDescription(offer): %v", err)
					continue
				}
				answer, err := pc.CreateAnswer(nil)
				if err != nil {
					continue
				}
				if pc.SetLocalDescription(answer) == nil {
					send(map[string]any{"type": "answer", "sdp": answer.SDP})
				}
			case "candidate":
				if m.Candidate != nil {
					_ = pc.AddICECandidate(*m.Candidate)
				}
			}
		}
	}()

	select {
	case <-b.connected:
	case <-time.After(window):
		t.Fatalf("the peer connection never connected (state %s)", pc.ConnectionState())
	}
	return b, track
}

// TestWebSocketPeerAudioReachesTheBrowser is the other half of the bridge: a peer
// on the huddle WebSocket speaks, and the browser receives it as an inbound RTP
// track. This is the path that exercises the track factory and renegotiation --
// the server has to add a track for a speaker it had not heard before and offer
// the client an updated description mid-call.
func TestWebSocketPeerAudioReachesTheBrowser(t *testing.T) {
	h := newHarness(t, nil)

	// A WebSocket-side speaker, joined the way wsaudio joins one.
	speakerSink := room.NewChannelSink()
	joinedRoom, speaker, _, err := h.rooms.Join("room-1", "ws-speaker", 3, speakerSink)
	if err != nil {
		t.Fatalf("speaker join: %v", err)
	}

	b, _ := connectBrowser(t, h, "room-1", alicePriv, false)
	h.waitForOccupancy("room-1", 2)

	inbound := make(chan []byte, 8)
	b.pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			select {
			case inbound <- packet.Payload:
			default:
			}
		}
	})

	// Keep speaking: the track and renegotiation only happen once the first frame
	// arrives, and audio is lossy by design.
	opus := []byte{0xFC, 0x11, 0x22, 0x33, 0x44}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		seq, ts := uint16(1), uint32(960)
		for {
			select {
			case <-stop:
				return
			default:
			}
			joinedRoom.BroadcastFrame(speaker.ID, wire.EncodeFrame(
				wire.FrameHeader{Seq: seq, Ts48k: ts, LevelDbov: -20}, opus))
			seq++
			ts += 960
			time.Sleep(20 * time.Millisecond)
		}
	}()

	select {
	case payload := <-inbound:
		if string(payload) != string(opus) {
			t.Errorf("payload = %x, want the speaker's Opus %x", payload, opus)
		}
	case <-time.After(window):
		t.Fatal("the browser never received the WebSocket peer's audio")
	}
}
