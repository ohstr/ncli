package client

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/ncli/huddle/client"
	"github.com/ohstr/nmilat/huddle/wire"
	"github.com/ohstr/nmilat/nip42"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

// The WebRTC half of the huddle stack, against the same relay container as the
// WebSocket scenarios. See integration/huddle/README.md.
//
// A browser is not needed to drive this: pion is a full WebRTC client, so a
// real peer connection publishes real RTP here. What a browser adds over this
// is capture and playback -- the wire is the same.

// rtcSignal is the loose shape of a signalling frame on the /rtc endpoint.
type rtcSignal struct {
	Type      string                   `json:"type"`
	Challenge string                   `json:"challenge"`
	Code      string                   `json:"code"`
	Message   string                   `json:"message"`
	Pubkey    string                   `json:"pubkey"`
	SDP       string                   `json:"sdp"`
	Candidate *webrtc.ICECandidateInit `json:"candidate"`
}

// mediaKind labels what a track carries, so a receiver can tell a microphone
// from a camera from a screen share. It is the track id on the wire, carried
// through the SFU unchanged.
type mediaKind string

const (
	mediaAudio  mediaKind = "mic"
	mediaCamera mediaKind = "camera"
	mediaScreen mediaKind = "screen"
)

// received is one payload as it arrived, with who sent it and on what.
type received struct {
	kind    mediaKind
	author  string
	payload []byte
}

// rtcPeer is one WebRTC participant in a room: the signalling socket, the peer
// connection, its outbound tracks, and everything it has received.
type rtcPeer struct {
	t      *testing.T
	index  int
	pubkey string
	conn   *websocket.Conn
	pc     *webrtc.PeerConnection

	tracks map[mediaKind]*webrtc.TrackLocalStaticRTP

	writeMu sync.Mutex

	mu        sync.Mutex
	inbound   []received
	connected chan struct{}
	once      sync.Once
}

func huddleRTCEndpointFor(roomID string) string {
	return huddleIntegrationEndpoint() + "/huddle/" + roomID + "/rtc"
}

// dialRTCPeer joins roomID over WebRTC as peer i, publishing a track for each
// kind named. It returns once the peer connection is established, because a
// scenario that starts sending before then is only testing its own patience.
func dialRTCPeer(t *testing.T, roomID string, i int, kinds ...mediaKind) *rtcPeer {
	t.Helper()

	conn, _, err := websocket.DefaultDialer.Dial(huddleRTCEndpointFor(roomID), nil)
	require.NoError(t, err, "peer %d could not dial %s", i, roomID)
	t.Cleanup(func() { _ = conn.Close() })

	p := &rtcPeer{
		t:         t,
		index:     i,
		conn:      conn,
		tracks:    make(map[mediaKind]*webrtc.TrackLocalStaticRTP),
		connected: make(chan struct{}),
	}
	p.authenticate(i)

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err, "peer %d peer connection", i)
	p.pc = pc
	t.Cleanup(func() { _ = pc.Close() })

	for _, kind := range kinds {
		p.publish(kind)
	}

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			p.once.Do(func() { close(p.connected) })
		}
	})
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		p.readTrack(track)
	})
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		p.send(map[string]any{"type": "candidate", "candidate": init})
	})

	offer, err := pc.CreateOffer(nil)
	require.NoError(t, err, "peer %d CreateOffer", i)
	require.NoError(t, pc.SetLocalDescription(offer), "peer %d SetLocalDescription", i)
	p.send(map[string]any{"type": "offer", "sdp": offer.SDP})

	go p.pump()

	select {
	case <-p.connected:
	case <-time.After(45 * time.Second):
		t.Fatalf("peer %d never connected to %s (state %s)", i, roomID, pc.ConnectionState())
	}
	return p
}

// authenticate runs challenge -> auth -> joined, the same handshake the
// WebSocket endpoint uses.
func (p *rtcPeer) authenticate(i int) {
	p.t.Helper()

	var challenge rtcSignal
	require.NoError(p.t, p.conn.SetReadDeadline(time.Now().Add(30*time.Second)))
	require.NoError(p.t, p.conn.ReadJSON(&challenge), "peer %d challenge", i)
	require.Equal(p.t, "challenge", challenge.Type, "peer %d expected a challenge", i)

	ev := nip42.NewAuthEvent(challenge.Challenge, huddleIntegrationRelayURL)
	require.NoError(p.t, ev.Sign(huddlePeerKey(i)), "peer %d sign", i)
	p.pubkey = ev.PubKey

	require.NoError(p.t, p.conn.WriteJSON(map[string]any{
		"type": "auth", "event": ev, "protocol_version": wire.CurrentProtocolVersion,
	}), "peer %d auth", i)

	var joined rtcSignal
	require.NoError(p.t, p.conn.ReadJSON(&joined), "peer %d joined", i)
	require.Equal(p.t, "joined", joined.Type, "peer %d was refused: %s %s", i, joined.Code, joined.Message)
	require.NoError(p.t, p.conn.SetReadDeadline(time.Time{}))
}

// publish adds one outbound track. Opus for audio, VP8 for the two video kinds
// -- the SFU forwards the payload untouched either way, so what rides inside is
// the test's own bytes rather than encoded media.
func (p *rtcPeer) publish(kind mediaKind) {
	p.t.Helper()

	codec := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}
	if kind != mediaAudio {
		codec = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}
	}
	track, err := webrtc.NewTrackLocalStaticRTP(codec, string(kind), "stream-"+string(kind))
	require.NoError(p.t, err, "peer %d track %s", p.index, kind)
	_, err = p.pc.AddTrack(track)
	require.NoError(p.t, err, "peer %d AddTrack %s", p.index, kind)
	p.tracks[kind] = track
}

func (p *rtcPeer) send(v any) {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.conn.WriteJSON(v)
}

// pump answers the server's signalling for the life of the peer. Handling a
// server-initiated offer is not optional: the SFU renegotiates whenever a new
// speaker's track is added, and a peer that ignores those never hears anyone
// who joined after it did.
func (p *rtcPeer) pump() {
	for {
		_, data, err := p.conn.ReadMessage()
		if err != nil {
			return
		}
		var m rtcSignal
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "answer":
			_ = p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: m.SDP})
		case "offer":
			if p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: m.SDP}) != nil {
				continue
			}
			answer, err := p.pc.CreateAnswer(nil)
			if err != nil || p.pc.SetLocalDescription(answer) != nil {
				continue
			}
			p.send(map[string]any{"type": "answer", "sdp": answer.SDP})
		case "candidate":
			if m.Candidate != nil {
				_ = p.pc.AddICECandidate(*m.Candidate)
			}
		}
	}
}

// readTrack records everything arriving on one inbound track. The payload is
// copied: it aliases the read buffer only until the next read.
//
// The two kinds are labelled differently on the wire and deliberately so. An
// outbound audio track is named after its speaker ("audio-<pubkey>", stream id
// the pubkey) so a receiver can group one speaker's streams; a video track
// keeps the publisher's own id, which is what keeps a camera distinguishable
// from a screen share.
func (p *rtcPeer) readTrack(track *webrtc.TrackRemote) {
	kind, author := mediaKind(track.ID()), track.StreamID()
	if track.Kind() == webrtc.RTPCodecTypeAudio {
		kind = mediaAudio
	}
	for {
		packet, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		if len(packet.Payload) == 0 {
			continue
		}
		p.mu.Lock()
		p.inbound = append(p.inbound, received{
			kind: kind, author: author, payload: append([]byte(nil), packet.Payload...),
		})
		p.mu.Unlock()
	}
}

// writeMedia sends payload on one of this peer's tracks.
func (p *rtcPeer) writeMedia(kind mediaKind, seq uint16, payload []byte) {
	track, ok := p.tracks[kind]
	require.True(p.t, ok, "peer %d publishes no %s track", p.index, kind)

	clock := uint32(960)
	payloadType := uint8(111)
	if kind != mediaAudio {
		clock, payloadType = 3000, 96
	}
	_ = track.WriteRTP(&rtp.Packet{
		Header: rtp.Header{
			Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * clock,
			SSRC: 0x1000 + uint32(p.index), PayloadType: payloadType,
		},
		Payload: payload,
	})
}

// sawPayload reports whether this peer received payload on kind.
func (p *rtcPeer) sawPayload(kind mediaKind, payload []byte) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, got := range p.inbound {
		if got.kind == kind && string(got.payload) == string(payload) {
			return true
		}
	}
	return false
}

// sawAudioFrom reports whether payload arrived attributed to author. Matching
// both is what makes a crossed stream a failure rather than a count that merely
// comes out right.
func (p *rtcPeer) sawAudioFrom(author string, payload []byte) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, got := range p.inbound {
		if got.kind == mediaAudio && got.author == author && string(got.payload) == string(payload) {
			return true
		}
	}
	return false
}

// randomPayload is a distinct payload per sender and kind, so a crossed stream
// fails rather than a count merely coming out right. The relay never decodes
// it, which is what makes byte-identity the assertion.
func randomPayload(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 24)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

// keepSending repeats payload until the test ends. Media is lossy by design and
// the first packets can land before the SFU has finished wiring its reader, so
// a single send proves nothing either way.
func keepSending(t *testing.T, send func(seq uint16)) {
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for seq := uint16(1); ; seq++ {
			select {
			case <-stop:
				return
			default:
			}
			send(seq)
			time.Sleep(20 * time.Millisecond)
		}
	}()
}

// eventually polls until cond holds or the window expires.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// testHuddleMixedTransportConversation is the live call: browsers and
// WebSocket clients in one room, everyone hearing everyone, each payload
// matched to its author so a crossed stream fails rather than a count.
func testHuddleMixedTransportConversation(t *testing.T) {
	const roomID = "rtc-mixed"
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	alice := dialRTCPeer(t, roomID, 41, mediaAudio)
	bob := dialRTCPeer(t, roomID, 42, mediaAudio)
	carol := dialHuddlePeer(t, ctx, roomID, 43, 0)
	dave := dialHuddlePeer(t, ctx, roomID, 44, 0)

	alicePayload, bobPayload := randomPayload(t), randomPayload(t)
	carolPayload, davePayload := randomPayload(t), randomPayload(t)

	keepSending(t, func(seq uint16) { alice.writeMedia(mediaAudio, seq, alicePayload) })
	keepSending(t, func(seq uint16) { bob.writeMedia(mediaAudio, seq, bobPayload) })
	keepSending(t, func(seq uint16) { speak(t, carol, seq, carolPayload) })
	keepSending(t, func(seq uint16) { speak(t, dave, seq, davePayload) })

	// Each browser hears the other browser and both WebSocket peers, with every
	// payload attributed to the peer that actually sent it.
	for _, c := range []struct {
		listener *rtcPeer
		who      string
		author   string
		payload  []byte
	}{
		{alice, "the other browser", bob.pubkey, bobPayload},
		{alice, "the first WebSocket peer", carol.Self().Pubkey, carolPayload},
		{alice, "the second WebSocket peer", dave.Self().Pubkey, davePayload},
		{bob, "the other browser", alice.pubkey, alicePayload},
		{bob, "the first WebSocket peer", carol.Self().Pubkey, carolPayload},
		{bob, "the second WebSocket peer", dave.Self().Pubkey, davePayload},
	} {
		eventually(t, fmt.Sprintf("browser %d to hear %s", c.listener.index, c.who), func() bool {
			return c.listener.sawAudioFrom(c.author, c.payload)
		})
	}

	// Nobody hears themselves.
	require.False(t, alice.sawAudioFrom(alice.pubkey, alicePayload), "a browser heard its own audio echoed")
	require.False(t, bob.sawAudioFrom(bob.pubkey, bobPayload), "a browser heard its own audio echoed")

	// And each WebSocket peer hears both browsers, byte-identical.
	for _, c := range []struct {
		name     string
		listener *wsListener
		who      string
		payload  []byte
	}{
		{"the first WebSocket peer", newWSListener(carol), "the first browser", alicePayload},
		{"the first WebSocket peer", newWSListener(carol), "the second browser", bobPayload},
		{"the second WebSocket peer", newWSListener(dave), "the first browser", alicePayload},
		{"the second WebSocket peer", newWSListener(dave), "the second browser", bobPayload},
	} {
		eventually(t, fmt.Sprintf("%s to hear %s", c.name, c.who), func() bool {
			return c.listener.saw(c.payload)
		})
	}
}

// wsListener collects what a WebSocket peer hears, so several assertions can
// share one drain of its frame channel.
type wsListener struct {
	mu    sync.Mutex
	heard [][]byte
}

func newWSListener(c *client.Client) *wsListener {
	l := &wsListener{}
	go func() {
		for f := range c.Frames() {
			l.mu.Lock()
			l.heard = append(l.heard, append([]byte(nil), f.Opus...))
			l.mu.Unlock()
		}
	}()
	return l
}

func (l *wsListener) saw(payload []byte) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, got := range l.heard {
		if string(got) == string(payload) {
			return true
		}
	}
	return false
}

// testHuddleVideoAndScreenShare covers what only the WebRTC door carries:
// camera and screen at once, told apart by track id, forwarded between browsers
// without the relay decoding either.
func testHuddleVideoAndScreenShare(t *testing.T) {
	const roomID = "rtc-video"

	alice := dialRTCPeer(t, roomID, 45, mediaAudio, mediaCamera, mediaScreen)
	bob := dialRTCPeer(t, roomID, 46, mediaAudio, mediaCamera, mediaScreen)

	aliceCamera, aliceScreen := randomPayload(t), randomPayload(t)
	bobCamera, bobScreen := randomPayload(t), randomPayload(t)

	keepSending(t, func(seq uint16) {
		alice.writeMedia(mediaCamera, seq, aliceCamera)
		alice.writeMedia(mediaScreen, seq, aliceScreen)
		bob.writeMedia(mediaCamera, seq, bobCamera)
		bob.writeMedia(mediaScreen, seq, bobScreen)
	})

	eventually(t, "bob to see alice's camera", func() bool { return bob.sawPayload(mediaCamera, aliceCamera) })
	eventually(t, "bob to see alice's screen", func() bool { return bob.sawPayload(mediaScreen, aliceScreen) })
	eventually(t, "alice to see bob's camera", func() bool { return alice.sawPayload(mediaCamera, bobCamera) })
	eventually(t, "alice to see bob's screen", func() bool { return alice.sawPayload(mediaScreen, bobScreen) })

	// Neither may see its own stream back.
	require.False(t, alice.sawPayload(mediaCamera, aliceCamera), "alice saw her own camera echoed")
	require.False(t, bob.sawPayload(mediaScreen, bobScreen), "bob saw his own screen echoed")
}

// testHuddleWebSocketPeerSeesNoVideo is the documented graceful degrade: a
// WebSocket peer is audio-only, so it stays in the call and simply misses the
// picture rather than being kept out of rooms that have video in them.
func testHuddleWebSocketPeerSeesNoVideo(t *testing.T) {
	const roomID = "rtc-degrade"
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	alice := dialRTCPeer(t, roomID, 47, mediaAudio, mediaCamera)
	ws := dialHuddlePeer(t, ctx, roomID, 48, 0)

	audio, video := randomPayload(t), randomPayload(t)
	keepSending(t, func(seq uint16) {
		alice.writeMedia(mediaAudio, seq, audio)
		alice.writeMedia(mediaCamera, seq, video)
	})

	listener := newWSListener(ws)
	eventually(t, "the WebSocket peer to hear the browser", func() bool { return listener.saw(audio) })
	require.False(t, listener.saw(video), "video reached a WebSocket peer, which carries audio only")
}

// testHuddleLateJoinerGetsTheLiveCall joins a call already in progress. This is
// the renegotiation path: the SFU offers the newcomer a track per speaker it
// has to hear, and a peer that ignored those offers would sit in a silent room
// that looks connected.
func testHuddleLateJoinerGetsTheLiveCall(t *testing.T) {
	const roomID = "rtc-late"

	alice := dialRTCPeer(t, roomID, 49, mediaAudio, mediaCamera)
	alicePayload, aliceVideo := randomPayload(t), randomPayload(t)
	keepSending(t, func(seq uint16) {
		alice.writeMedia(mediaAudio, seq, alicePayload)
		alice.writeMedia(mediaCamera, seq, aliceVideo)
	})

	// Let the call run before anyone else arrives.
	time.Sleep(500 * time.Millisecond)

	late := dialRTCPeer(t, roomID, 50, mediaAudio)
	eventually(t, "the late joiner to hear the call", func() bool { return late.sawPayload(mediaAudio, alicePayload) })
	eventually(t, "the late joiner to see the call", func() bool { return late.sawPayload(mediaCamera, aliceVideo) })
}

// testHuddleCandidatesBeforeTheOffer is the regression guard for the drop that
// killed real calls: a browser gathers as soon as it sets its local
// description, so its candidates routinely reach the relay before the SDP. The
// relay used to reject and discard them.
//
// Unlike the in-process test in huddle/sfu, this one runs over a real network
// against a container, so a discarded candidate actually costs a path.
func testHuddleCandidatesBeforeTheOffer(t *testing.T) {
	const roomID = "rtc-candidates-first"

	conn, _, err := websocket.DefaultDialer.Dial(huddleRTCEndpointFor(roomID), nil)
	require.NoError(t, err, "dial")
	t.Cleanup(func() { _ = conn.Close() })

	p := &rtcPeer{t: t, index: 51, conn: conn, tracks: make(map[mediaKind]*webrtc.TrackLocalStaticRTP), connected: make(chan struct{})}
	p.authenticate(51)

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err, "peer connection")
	p.pc = pc
	t.Cleanup(func() { _ = pc.Close() })
	p.publish(mediaAudio)

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			p.once.Do(func() { close(p.connected) })
		}
	})

	var (
		candMu    sync.Mutex
		gathered  []webrtc.ICECandidateInit
		offerSent bool
	)
	first := make(chan struct{})
	var gatherOnce sync.Once
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		candMu.Lock()
		if !offerSent {
			gathered = append(gathered, init)
			candMu.Unlock()
			gatherOnce.Do(func() { close(first) })
			return
		}
		candMu.Unlock()
		p.send(map[string]any{"type": "candidate", "candidate": init})
	})

	offer, err := pc.CreateOffer(nil)
	require.NoError(t, err, "CreateOffer")
	require.NoError(t, pc.SetLocalDescription(offer), "SetLocalDescription")

	select {
	case <-first:
	case <-time.After(30 * time.Second):
		t.Fatal("no candidate was gathered")
	}

	candMu.Lock()
	early := append([]webrtc.ICECandidateInit(nil), gathered...)
	gathered, offerSent = nil, true
	candMu.Unlock()

	// The ordering under test: every gathered candidate, then the offer.
	for _, c := range early {
		p.send(map[string]any{"type": "candidate", "candidate": c})
	}
	p.send(map[string]any{"type": "offer", "sdp": offer.SDP})

	go p.pump()

	select {
	case <-p.connected:
	case <-time.After(45 * time.Second):
		t.Fatalf("the peer never connected after trickling before the offer (state %s)", pc.ConnectionState())
	}
}
