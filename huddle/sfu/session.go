package sfu

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/ncli/huddle/rtp"
	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/huddle/wire"
	"github.com/ohstr/nmilat/nip42"
	"github.com/ohstr/nmilat/utils"
	"github.com/pion/webrtc/v4"
	"github.com/rs/zerolog"
)

// maxPendingCandidates caps the candidates held back per direction while the
// description they belong to is still in flight. A real gathering run is a
// handful; the cap is only so a peer that never offers cannot grow the queue.
const maxPendingCandidates = 64

// session is one WebRTC peer's lifetime in a room.
type session struct {
	cfg    Config
	api    *webrtc.API
	log    zerolog.Logger
	conn   *websocket.Conn
	roomID string

	pubkey  string
	version uint8
	room    *room.Room
	peer    *room.Peer
	sink    *rtp.Sink
	pc      *webrtc.PeerConnection

	// hub carries video between the WebRTC peers of a room. Audio does not use
	// it -- that goes through the room, which is what lets a WebSocket peer hear
	// the call.
	hub *videoHub
	// videoTracks are this subscriber's outbound tracks, keyed by publication.
	videoMu     sync.Mutex
	videoTracks map[string]*webrtc.TrackLocalStaticRTP

	// writeMu serialises websocket writes. Signalling originates from three
	// places -- the read loop, ICE gathering, and renegotiation when a speaker
	// joins -- and gorilla allows only one writer at a time.
	writeMu sync.Mutex

	// negotiateMu guards renegotiation so two speakers joining at once cannot
	// produce overlapping offers.
	negotiateMu sync.Mutex
	// negotiated is false until the first offer/answer completes. Until then a
	// renegotiation request is dropped: there is nothing to renegotiate from,
	// and the client's own initial offer will carry whatever tracks exist.
	negotiated bool
	// pendingTracks records that a track was added while no renegotiation was
	// possible. Without it, offering unconditionally after the first answer
	// sends the client a description identical to the one it just agreed to,
	// which is at best wasted work and at worst glare.
	pendingTracks bool

	// pendingRemote holds candidates that arrived before the client's offer.
	// pion rejects a candidate outright while there is no remote description,
	// and a browser gathers as soon as it sets its local description -- often
	// before it sends us the SDP. A dropped candidate can be the only reachable
	// path. Touched only by the signalling goroutine, so it needs no lock:
	// renegotiate is the one cross-goroutine caller and reaches neither this nor
	// the remote description.
	pendingRemote []webrtc.ICECandidateInit

	// pendingLocal holds our own candidates until the description they belong to
	// is on the wire, because pion starts gathering inside SetLocalDescription.
	// A client that receives a candidate first rejects it for the same reason we
	// would. Guarded by writeMu, which already serialises these two writers.
	localSent    bool
	pendingLocal []webrtc.ICECandidateInit

	done      chan struct{}
	closeOnce sync.Once
}

func (s *session) run(ctx context.Context) {
	defer func() { _ = s.conn.Close() }()
	s.done = make(chan struct{})
	s.videoTracks = make(map[string]*webrtc.TrackLocalStaticRTP)
	s.conn.SetReadLimit(wire.MaxControlBytes * 16) // SDP is far larger than a huddle control frame

	if !s.cfg.Enabled {
		s.writeError(CodeAudioUnavailable, "this relay does not serve huddle audio", nil)
		return
	}
	if !s.handshake(ctx) {
		return
	}
	defer s.teardown()

	s.signallingLoop()
}

// handshake runs challenge -> auth -> authorize -> join, then builds the peer
// connection. It reports whether the peer is in.
func (s *session) handshake(ctx context.Context) bool {
	challenge := nip42.NewChallenge()
	if err := s.write(signal{Type: "challenge", Challenge: challenge}); err != nil {
		return false
	}

	auth, ok := s.readAuth()
	if !ok {
		return false
	}
	if auth.Event == nil {
		s.writeError(CodeAuthFailed, "auth event is missing", nil)
		return false
	}
	if err := nip42.ValidateAuthEvent(auth.Event.Kind, auth.Event.Tags, auth.Event.CreatedAt, challenge, s.cfg.RelayURL); err != nil {
		s.log.Debug().Err(err).Msg("rtc auth event rejected")
		s.writeError(CodeAuthFailed, "auth failed", nil)
		return false
	}
	if err := auth.Event.Verify(); err != nil {
		s.log.Debug().Err(err).Msg("rtc auth signature rejected")
		s.writeError(CodeAuthFailed, "auth failed", nil)
		return false
	}
	s.pubkey = auth.Event.PubKey
	s.version = wire.DefaultProtocolVersion
	if auth.ProtocolVersion != nil {
		s.version = *auth.ProtocolVersion
	}

	if s.cfg.Authorize != nil {
		if err := s.cfg.Authorize(ctx, s.roomID, s.pubkey); err != nil {
			s.log.Debug().Err(err).Msg("rtc join not authorized")
			s.writeError(CodeJoinRejected, "not permitted to join this room", nil)
			return false
		}
	}

	// The peer connection must exist before the sink, because the sink's track
	// factory adds tracks to it.
	pc, err := s.api.NewPeerConnection(webrtc.Configuration{ICEServers: s.cfg.ICEServers})
	if err != nil {
		s.log.Warn().Err(err).Msg("could not create a peer connection")
		s.writeError(CodeNegotiationFailed, "could not create a peer connection", nil)
		return false
	}
	s.pc = pc
	s.wirePeerConnection()

	sink, err := rtp.New(rtp.Config{NewTrack: s.newSpeakerTrack})
	if err != nil {
		s.writeError(CodeNegotiationFailed, "could not build the audio sink", nil)
		return false
	}
	s.sink = sink

	joinedRoom, peer, roster, err := s.cfg.Rooms.Join(s.roomID, s.pubkey, s.version, sink)
	if err != nil {
		s.writeJoinError(err)
		return false
	}
	s.room, s.peer = joinedRoom, peer
	s.log = s.log.With().Str("pubkey", s.pubkey).Uint8("peer_index", peer.Index).Logger()
	s.hub.join(s.roomID, s)

	if err := s.write(signal{
		Type: "joined", Revision: roster.Revision, Pubkey: s.pubkey,
		PeerIndex: peer.Index, Epoch: peer.Epoch, Peers: rosterMessages(roster.Peers),
	}); err != nil {
		return false
	}
	s.announceJoin(roster)
	return true
}

// newSpeakerTrack is rtp's TrackFactory: one outbound track per speaker,
// added to the peer connection, which triggers renegotiation.
func (s *session) newSpeakerTrack(author room.PeerInfo) (rtp.PacketWriter, error) {
	// Channels is 2, not wire.Channels. Those describe different things:
	// wire.Channels is how many channels the audio actually has (mono), while
	// this is the SDP codec parameter, which for Opus is conventionally 2
	// whatever the content is. Declaring 1 here makes the track unbindable
	// against pion's registered opus/48000/2 -- the remote answer then looks
	// like it does not support the codec at all.
	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: wire.SampleRate, Channels: 2},
		// Stream id is the speaker, so a receiver can group their tracks.
		"audio-"+author.Pubkey, author.Pubkey,
	)
	if err != nil {
		return nil, err
	}
	if _, err := s.pc.AddTrack(track); err != nil {
		return nil, err
	}
	s.log.Debug().Str("speaker", author.Pubkey).Msg("added an outbound track")
	s.renegotiate()
	return track, nil
}

func (s *session) wirePeerConnection() {
	s.pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		if !s.queueLocalCandidate(init) {
			return
		}
		_ = s.write(signal{Type: "candidate", Candidate: &init})
	})

	s.pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		s.log.Debug().Str("state", state.String()).Msg("rtc connection state")
		switch state {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			s.shutdown()
		}
	})

	s.pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if track.Kind() == webrtc.RTPCodecTypeVideo {
			// Camera or screen share. Forwarded only among WebRTC peers, since a
			// WebSocket peer is audio-only.
			s.forwardVideo(track)
			return
		}
		s.publishInbound(track, receiver)
	})
}

// publishInbound carries this peer's microphone into the room.
func (s *session) publishInbound(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	defer utils.RecoverPanic(s.log)

	// The audio-level extension id is negotiated per session, so read it from
	// the answer rather than assuming the conventional value.
	var levelExtID uint8
	for _, ext := range receiver.GetParameters().HeaderExtensions {
		if ext.URI == rtp.AudioLevelExtensionURI {
			levelExtID = uint8(ext.ID)
			break
		}
	}

	for {
		select {
		case <-s.done:
			return
		default:
		}
		packet, _, err := track.ReadRTP()
		if err != nil {
			s.log.Debug().Err(err).Msg("inbound track ended")
			return
		}
		frame, ok := rtp.FrameFromRTP(packet, levelExtID)
		if !ok {
			continue
		}
		if valid, reason := wire.ValidClientFrame(s.version, frame); !valid {
			s.log.Debug().Str("reason", reason).Msg("dropping a bridged frame")
			continue
		}
		s.room.BroadcastFrame(s.peer.ID, frame)
	}
}

// renegotiate offers the client an updated description after a track was added.
func (s *session) renegotiate() {
	s.negotiateMu.Lock()
	defer s.negotiateMu.Unlock()

	if !s.negotiated || s.pc.SignalingState() != webrtc.SignalingStateStable {
		// Either the first exchange has not finished, or one is in flight. Note
		// it and offer once things settle.
		s.pendingTracks = true
		return
	}
	s.pendingTracks = false
	offer, err := s.pc.CreateOffer(nil)
	if err != nil {
		s.log.Warn().Err(err).Msg("could not create a renegotiation offer")
		return
	}
	if err := s.pc.SetLocalDescription(offer); err != nil {
		s.log.Warn().Err(err).Msg("could not set the local description")
		return
	}
	_ = s.writeDescription("offer", offer.SDP)
}

func (s *session) signallingLoop() {
	defer utils.RecoverPanic(s.log)

	for {
		var msg signal
		if err := s.readJSON(&msg); err != nil {
			return
		}
		switch msg.Type {
		case "offer":
			s.handleOffer(msg.SDP)
		case "answer":
			s.handleAnswer(msg.SDP)
		case "candidate":
			if msg.Candidate != nil {
				s.addRemoteCandidate(*msg.Candidate)
			}
		default:
			s.log.Debug().Str("type", msg.Type).Msg("ignoring an unexpected signalling message")
		}
	}
}

func (s *session) handleOffer(sdp string) {
	if err := s.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		s.log.Debug().Err(err).Msg("bad offer")
		s.writeError(CodeNegotiationFailed, "could not accept the offer", nil)
		return
	}
	s.flushRemoteCandidates()

	answer, err := s.pc.CreateAnswer(nil)
	if err != nil {
		s.writeError(CodeNegotiationFailed, "could not answer", nil)
		return
	}
	if err := s.pc.SetLocalDescription(answer); err != nil {
		s.writeError(CodeNegotiationFailed, "could not answer", nil)
		return
	}
	if err := s.writeDescription("answer", answer.SDP); err != nil {
		return
	}

	s.negotiateMu.Lock()
	s.negotiated = true
	pending := s.pendingTracks
	s.negotiateMu.Unlock()

	// Only offer again if a track really was added while the exchange was in
	// flight. Offering unconditionally would hand the client a description
	// identical to the one it just answered.
	if pending {
		s.renegotiate()
	}
}

func (s *session) handleAnswer(sdp string) {
	if err := s.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp}); err != nil {
		s.log.Debug().Err(err).Msg("bad answer")
		return
	}
	s.flushRemoteCandidates()

	// Signalling is stable again, so anything added while that exchange was in
	// flight can go out now. Without this a second track -- a screen share
	// alongside a camera, or a second speaker joining moments after the first --
	// is added to the peer connection and then never negotiated, so the receiver
	// never learns it exists.
	s.negotiateMu.Lock()
	pending := s.pendingTracks
	s.negotiateMu.Unlock()
	if pending {
		s.renegotiate()
	}
}

// addRemoteCandidate applies a trickled candidate, holding it back when the
// description it belongs to has not arrived yet.
func (s *session) addRemoteCandidate(candidate webrtc.ICECandidateInit) {
	if s.pc.RemoteDescription() == nil {
		if len(s.pendingRemote) >= maxPendingCandidates {
			s.log.Debug().Msg("dropping an ICE candidate: too many arrived before the offer")
			return
		}
		s.pendingRemote = append(s.pendingRemote, candidate)
		return
	}
	if err := s.pc.AddICECandidate(candidate); err != nil {
		s.log.Warn().Err(err).Msg("could not add an ICE candidate")
	}
}

// flushRemoteCandidates applies whatever arrived before the remote description.
func (s *session) flushRemoteCandidates() {
	pending := s.pendingRemote
	s.pendingRemote = nil
	for _, candidate := range pending {
		if err := s.pc.AddICECandidate(candidate); err != nil {
			s.log.Warn().Err(err).Msg("could not add a queued ICE candidate")
		}
	}
}

// queueLocalCandidate reports whether a gathered candidate may go out now,
// holding it back until the description it belongs to has been written.
func (s *session) queueLocalCandidate(candidate webrtc.ICECandidateInit) bool {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.localSent {
		return true
	}
	if len(s.pendingLocal) < maxPendingCandidates {
		s.pendingLocal = append(s.pendingLocal, candidate)
	}
	return false
}

// writeDescription writes an offer or answer and then releases the candidates
// gathered while it was being prepared, so the client never sees a candidate
// before the description it belongs to.
func (s *session) writeDescription(kind, sdp string) error {
	s.writeMu.Lock()
	err := s.writeLocked(signal{Type: kind, SDP: sdp})
	pending := s.pendingLocal
	s.pendingLocal, s.localSent = nil, true
	s.writeMu.Unlock()

	if err != nil {
		return err
	}
	for _, candidate := range pending {
		if err := s.write(signal{Type: "candidate", Candidate: &candidate}); err != nil {
			return err
		}
	}
	return nil
}

func (s *session) readAuth() (signal, bool) {
	if err := s.conn.SetReadDeadline(time.Now().Add(s.cfg.authTimeout())); err != nil {
		return signal{}, false
	}
	for {
		var msg signal
		if err := s.readJSON(&msg); err != nil {
			return signal{}, false
		}
		if msg.Type == "auth" {
			// Clear the handshake deadline: signalling has no fixed cadence.
			_ = s.conn.SetReadDeadline(time.Time{})
			return msg, true
		}
	}
}

func (s *session) readJSON(v any) error {
	_, data, err := s.conn.ReadMessage()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (s *session) write(v signal) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writeLocked(v)
}

// writeLocked is write for a caller already holding writeMu.
func (s *session) writeLocked(v signal) error {
	if err := s.conn.SetWriteDeadline(time.Now().Add(s.cfg.writeTimeout())); err != nil {
		return err
	}
	return s.conn.WriteJSON(v)
}

func (s *session) writeError(code, message string, currentVersion *uint8) {
	_ = s.write(signal{Type: "error", Code: code, Message: message, CurrentVersion: currentVersion})
}

func (s *session) writeJoinError(err error) {
	switch {
	case errors.Is(err, room.ErrRoomFull):
		s.writeError(CodeRoomFull, "room participant capacity reached", nil)
	case errors.Is(err, room.ErrRoomEnded):
		s.writeError(CodeRoomEnded, "huddle has ended", nil)
	case errors.Is(err, room.ErrUpgradeRequired):
		var current *uint8
		if r, ok := s.cfg.Rooms.Get(s.roomID); ok {
			v := r.ProtocolVersion()
			current = &v
		}
		s.writeError(CodeUpgradeRequired, "huddle audio protocol version not supported by this room", current)
	case errors.Is(err, room.ErrBadVersion):
		v := uint8(wire.CurrentProtocolVersion)
		s.writeError(CodeUpgradeRequired, "unsupported huddle audio protocol version", &v)
	case errors.Is(err, room.ErrTooManyRooms):
		s.writeError(CodeRoomUnavailable, "relay is hosting too many rooms", nil)
	default:
		s.log.Warn().Err(err).Msg("rtc join failed")
		s.writeError(CodeJoinRejected, "join rejected", nil)
	}
}

func (s *session) announceJoin(roster room.Roster) {
	message, err := json.Marshal(signal{
		Type: "joined", Revision: roster.Revision, Pubkey: s.pubkey,
		PeerIndex: s.peer.Index, Epoch: s.peer.Epoch, Peers: rosterMessages(roster.Peers),
	})
	if err != nil {
		return
	}
	s.room.BroadcastControl(string(message), s.peer.ID)
}

func (s *session) teardown() {
	s.shutdown()
	s.hub.leave(s.roomID, s)
	if s.sink != nil {
		s.sink.Close()
	}
	if s.pc != nil {
		_ = s.pc.Close()
	}
	if s.peer == nil || s.room == nil {
		return
	}
	s.cfg.Rooms.Leave(s.roomID, s.peer.ID)
	roster := s.room.Roster()
	message, err := json.Marshal(signal{
		Type: "left", Revision: roster.Revision, Pubkey: s.pubkey,
		PeerIndex: s.peer.Index, Epoch: s.peer.Epoch,
	})
	if err != nil {
		return
	}
	s.room.BroadcastControl(string(message), s.peer.ID)
}

func (s *session) shutdown() {
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.conn.SetReadDeadline(time.Now())
	})
}
