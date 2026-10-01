package huddlesfu_test

import (
	"bytes"
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
	"github.com/pion/rtcp"
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

// logSink captures the handler's log output so a test can assert on what the
// server reported. It deliberately does not write into t.Log: a session
// goroutine can outlive the test, and zerolog into a completed *testing.T would
// panic.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logSink) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

type harness struct {
	t     *testing.T
	srv   *httptest.Server
	rooms *room.Manager
	sink  *logSink
}

// logs returns everything the handler has logged so far.
func (h *harness) logs() string { return h.sink.String() }

func newHarness(t *testing.T, mutate func(*huddlesfu.Config)) *harness {
	t.Helper()
	rooms := room.NewManager(0)
	sink := &logSink{}
	cfg := huddlesfu.Config{
		Enabled:  true,
		RelayURL: relayURL,
		Rooms:    rooms,
		Logger:   zerolog.New(sink).Level(zerolog.DebugLevel),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	mux := http.NewServeMux()
	mux.Handle("/huddle/{id}/rtc", huddlesfu.NewHandler(cfg))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &harness{t: t, srv: srv, rooms: cfg.Rooms, sink: sink}
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
			assertNoDroppedCandidates(t, h)
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

// connectBrowser authenticates, publishes what opts asks for, and completes the
// WebRTC handshake.
func connectBrowser(t *testing.T, h *harness, roomID, privKey string, publish bool) (*browser, *webrtc.TrackLocalStaticRTP) {
	b, audio, _ := connectBrowserWith(t, h, roomID, privKey, publish, nil)
	return b, audio
}

// connectBrowserWith additionally publishes one video track per id in videoIDs,
// which is how a client offers a camera and a screen share at once.
//
// onTrack is optional and variadic so existing callers stay unchanged. It is
// registered before the handshake, which matters for a *late* subscriber: the SFU
// adds its video track on the next packet after it joins, i.e. within one frame
// interval, so a handler registered after this returns can lose that race.
func connectBrowserWith(t *testing.T, h *harness, roomID, privKey string, publishAudio bool, videoIDs []string, onTrack ...func(*webrtc.TrackRemote, *webrtc.RTPReceiver)) (*browser, *webrtc.TrackLocalStaticRTP, map[string]*webrtc.TrackLocalStaticRTP) {
	t.Helper()
	videos := map[string]*webrtc.TrackLocalStaticRTP{}

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

	for _, handler := range onTrack {
		pc.OnTrack(handler)
	}

	var track *webrtc.TrackLocalStaticRTP
	if publishAudio {
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

	for _, id := range videoIDs {
		video, err := webrtc.NewTrackLocalStaticRTP(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, id, "browser")
		if err != nil {
			t.Fatalf("video track %q: %v", id, err)
		}
		if _, err := pc.AddTrack(video); err != nil {
			t.Fatalf("AddTrack(%q): %v", id, err)
		}
		videos[id] = video
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
	return b, track, videos
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

// TestVideoAndScreenShareReachTheOtherBrowser is the capability the WebSocket
// transport cannot provide at all. One browser publishes a camera and a screen
// share at once; the other must receive both, told apart by the track id the
// publisher chose. It also pins the graceful degrade: video never enters the
// room, so a WebSocket peer in the same call hears the audio and simply misses
// the picture rather than being sent bytes it cannot use.
func TestVideoAndScreenShareReachTheOtherBrowser(t *testing.T) {
	h := newHarness(t, nil)

	// A WebSocket-side peer in the same room, to prove video never reaches it.
	wsSink := room.NewChannelSink()
	if _, _, _, err := h.rooms.Join("room-1", "ws-peer", 3, wsSink); err != nil {
		t.Fatalf("ws peer join: %v", err)
	}

	// The receiving browser joins first so it is already subscribed.
	receiver, _, _ := connectBrowserWith(t, h, "room-1", bobPriv, false, nil)

	received := make(chan string, 8)
	payloads := make(chan []byte, 8)
	receiver.pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track.Kind() != webrtc.RTPCodecTypeVideo {
			return
		}
		select {
		case received <- track.ID():
		default:
		}
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			select {
			case payloads <- packet.Payload:
			default:
			}
		}
	})

	// The publisher offers a camera and a screen share together.
	_, _, videos := connectBrowserWith(t, h, "room-1", alicePriv, false, []string{"camera", "screen"})
	h.waitForOccupancy("room-1", 3)

	frame := []byte{0x10, 0xAB, 0xCD, 0xEF}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		seq, ts := uint16(1), uint32(3000)
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, track := range videos {
				_ = track.WriteRTP(&rtp.Packet{
					Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 0xCAFE, PayloadType: 96},
					Payload: frame,
				})
			}
			seq++
			ts += 3000
			time.Sleep(20 * time.Millisecond)
		}
	}()

	// Both tracks must arrive, distinguishable by the publisher's own labels.
	seen := map[string]bool{}
	deadline := time.After(window)
	for len(seen) < 2 {
		select {
		case id := <-received:
			seen[id] = true
		case <-deadline:
			t.Fatalf("only saw video tracks %v, want both camera and screen", seen)
		}
	}
	if !seen["camera"] || !seen["screen"] {
		t.Errorf("track ids = %v, want camera and screen", seen)
	}

	select {
	case payload := <-payloads:
		if string(payload) != string(frame) {
			t.Errorf("payload = %x, want the publisher's frame %x", payload, frame)
		}
	case <-time.After(window):
		t.Fatal("no video payload arrived")
	}

	// The WebSocket peer must have received no media at all: the publishers sent
	// only video, and video does not go through the room.
	select {
	case got := <-wsSink.Audio():
		t.Errorf("the WebSocket peer was sent %x; video must not enter the room", got)
	default:
	}
}

// TestLateSubscriberGetsAKeyframeRequest is the first coverage of the keyframe
// path, and times it.
//
// A subscriber joining mid-stream starts between keyframes, so its decoder has
// nothing to build on and shows nothing until the publisher's next natural
// keyframe -- which for a screen share can be many seconds. writeVideo asks the
// publisher for one the moment it creates a fresh outbound track, which is the
// difference between video appearing at once and appearing eventually. Nothing
// verified that the request actually reaches the publisher.
//
// This also reports the two latencies a user would feel: time to the first
// forwarded packet, and time until the keyframe request lands at the publisher.
// The assertions are loose on purpose -- the value is that the request arrives at
// all, plus the logged timings.
//
// Measured over 3 runs: the keyframe request reaches the publisher in ~20-22 ms
// and the first forwarded packet arrives in ~40 ms. The request lands *before*
// the first packet because writeVideo asks the moment it creates the track, then
// writes -- so a late joiner waits ~40 ms plus the publisher's own response time,
// not the seconds it would wait for a natural keyframe.
func TestLateSubscriberGetsAKeyframeRequest(t *testing.T) {
	h := newHarness(t, nil)

	publisher, _, videos := connectBrowserWith(t, h, "room-1", alicePriv, false, []string{"camera"})
	camera := videos["camera"]

	// Watch the publisher's own RTCP for the PLI the SFU is supposed to send.
	pli := make(chan time.Time, 8)
	watched := 0
	for _, sender := range publisher.pc.GetSenders() {
		track := sender.Track()
		if track == nil || track.ID() != "camera" {
			continue
		}
		watched++
		go func(s *webrtc.RTPSender) {
			for {
				packets, _, err := s.ReadRTCP()
				if err != nil {
					return
				}
				for _, p := range packets {
					if _, ok := p.(*rtcp.PictureLossIndication); ok {
						select {
						case pli <- time.Now():
						default:
						}
					}
				}
			}
		}(sender)
	}
	if watched != 1 {
		t.Fatalf("expected exactly one camera sender to watch, found %d", watched)
	}

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		seq, ts := uint16(1), uint32(3000)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			_ = camera.WriteRTP(&rtp.Packet{
				Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 0xCAFE, PayloadType: 96},
				Payload: []byte{0x10, 0xAB, 0xCD, 0xEF},
			})
			seq++
			ts += 3000
		}
	}()

	// Let the stream get going, so the subscriber below is genuinely arriving
	// mid-stream rather than at its start.
	time.Sleep(500 * time.Millisecond)

	firstPacket := make(chan time.Time, 1)
	joinedAt := time.Now()
	connectBrowserWith(t, h, "room-1", bobPriv, false, nil,
		func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			if track.Kind() != webrtc.RTPCodecTypeVideo {
				return
			}
			for {
				if _, _, err := track.ReadRTP(); err != nil {
					return
				}
				select {
				case firstPacket <- time.Now():
				default:
				}
			}
		})

	var toFirstPacket, toKeyframeRequest time.Duration
	select {
	case at := <-firstPacket:
		toFirstPacket = at.Sub(joinedAt)
	case <-time.After(20 * time.Second):
		t.Fatal("the late subscriber never received a forwarded video packet")
	}
	select {
	case at := <-pli:
		toKeyframeRequest = at.Sub(joinedAt)
	case <-time.After(20 * time.Second):
		t.Fatal("no keyframe request ever reached the publisher, so a late joiner would wait for its next natural keyframe")
	}

	t.Logf("late subscriber: first forwarded packet in %v, keyframe request reached the publisher in %v",
		toFirstPacket, toKeyframeRequest)

	if toFirstPacket > 15*time.Second {
		t.Errorf("time to first packet is implausible: %v", toFirstPacket)
	}
	if toKeyframeRequest > 15*time.Second {
		t.Errorf("time to keyframe request is implausible: %v", toKeyframeRequest)
	}
}

/////////////////////////////////////////////////////////////////////
// Trickle ICE ordering
/////////////////////////////////////////////////////////////////////

// assertNoDroppedCandidates fails if the server discarded a trickled candidate.
// A drop is only ever logged, never surfaced to the peer, so the log is the
// single place it can be observed.
func assertNoDroppedCandidates(t *testing.T, h *harness) {
	t.Helper()
	if logs := h.logs(); strings.Contains(logs, "could not add an ICE candidate") {
		t.Fatalf("the server dropped a trickled candidate:\n%s", logs)
	}
}

// pumpSignalling answers the server's signalling until the socket closes. It
// must handle a server-initiated offer: the SFU renegotiates whenever a new
// speaker's track is added, and a peer that ignores those never hears them.
func pumpSignalling(conn *websocket.Conn, client *webrtc.PeerConnection, send func(any)) {
	for {
		if err := conn.SetReadDeadline(time.Now().Add(window)); err != nil {
			return
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var m message
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "answer":
			_ = client.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: m.SDP})
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
}

// newPublishingClient builds a peer connection with one Opus track, the shape
// every browser peer here starts from.
func newPublishingClient(t *testing.T) (*webrtc.PeerConnection, *webrtc.TrackLocalStaticRTP) {
	t.Helper()
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
	return client, track
}

// TestCandidatesBeforeTheOfferAreNotDropped sends candidates ahead of the offer,
// which is what a browser does: it gathers as soon as it sets its local
// description, before the SDP has been handed to the socket. The server used to
// reject those outright and never retry them, losing the fastest paths and, off
// loopback, every reachable one.
//
// The log assertion is the real one. Both ends here sit on 127.0.0.1, so ICE
// still completes off the server's own candidates even when every one of the
// client's is discarded -- asserting only that the peers connect would pass
// against the bug.
func TestCandidatesBeforeTheOfferAreNotDropped(t *testing.T) {
	h := newHarness(t, nil)

	conn := h.dial("candidates-first")
	if joined := authenticate(t, conn, alicePriv, relayURL); joined.Type != "joined" {
		t.Fatalf("got %+v, want joined", joined)
	}

	client, _ := newPublishingClient(t)

	var writeMu sync.Mutex
	send := func(v any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.WriteJSON(v)
	}

	// Hold the gathered candidates back so they can be sent before the offer.
	var (
		candMu    sync.Mutex
		gathered  []webrtc.ICECandidateInit
		offerSent bool
	)
	firstCandidate := make(chan struct{})
	var gatherOnce sync.Once
	client.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		candMu.Lock()
		if !offerSent {
			gathered = append(gathered, init)
			candMu.Unlock()
			gatherOnce.Do(func() { close(firstCandidate) })
			return
		}
		candMu.Unlock()
		send(map[string]any{"type": "candidate", "candidate": init})
	})

	connected := make(chan struct{})
	var connectedOnce sync.Once
	client.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			connectedOnce.Do(func() { close(connected) })
		}
	})

	offer, err := client.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	if err := client.SetLocalDescription(offer); err != nil {
		t.Fatalf("SetLocalDescription: %v", err)
	}

	select {
	case <-firstCandidate:
	case <-time.After(window):
		t.Fatal("the client gathered no candidate to trickle")
	}

	candMu.Lock()
	early := append([]webrtc.ICECandidateInit(nil), gathered...)
	gathered, offerSent = nil, true
	candMu.Unlock()

	// The ordering under test: candidates first, offer second.
	for _, c := range early {
		send(map[string]any{"type": "candidate", "candidate": c})
	}
	send(map[string]any{"type": "offer", "sdp": offer.SDP})

	go pumpSignalling(conn, client, send)

	select {
	case <-connected:
	case <-time.After(window):
		t.Fatalf("the peer connection never reached connected (state %s)\n%s", client.ConnectionState(), h.logs())
	}
	assertNoDroppedCandidates(t, h)
}
