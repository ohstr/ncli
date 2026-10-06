package sfu

import (
	"sync"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// Video takes a separate path from audio, and deliberately so.
//
// Audio flows through the huddle room, because a room is what a WebSocket peer
// can participate in. Video cannot: a room carries huddle audio frames and a
// WebSocket peer is audio-only by design. So video is forwarded directly between
// the WebRTC peers sharing a room id, and a WebSocket peer simply never sees it.
// That is the graceful degrade -- a buzz client in a screen-share call hears the
// call and misses only the picture.
//
//	WebRTC peer A --video--> videoHub --> WebRTC peer B, C
//	WebRTC peer A --audio--> room ------> WebRTC B, C *and* WebSocket peers

// videoHub tracks which WebRTC sessions share a room, so video can be forwarded
// among them without involving the room itself.
type videoHub struct {
	mu       sync.Mutex
	sessions map[string]map[*session]struct{}
}

func newVideoHub() *videoHub {
	return &videoHub{sessions: make(map[string]map[*session]struct{})}
}

func (h *videoHub) join(roomID string, s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions[roomID] == nil {
		h.sessions[roomID] = make(map[*session]struct{})
	}
	h.sessions[roomID][s] = struct{}{}
}

func (h *videoHub) leave(roomID string, s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions[roomID], s)
	if len(h.sessions[roomID]) == 0 {
		delete(h.sessions, roomID)
	}
}

// subscribers returns every session in the room except the publisher.
func (h *videoHub) subscribers(roomID string, except *session) []*session {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*session, 0, len(h.sessions[roomID]))
	for s := range h.sessions[roomID] {
		if s != except {
			out = append(out, s)
		}
	}
	return out
}

// publication identifies one inbound video track being forwarded. Publisher and
// TrackID together are the key: a peer sharing its camera and its screen at once
// publishes two, and a receiver tells them apart by TrackID, which is carried
// through unchanged from whatever the client labelled it.
type publication struct {
	Publisher string
	TrackID   string
	StreamID  string
	Codec     webrtc.RTPCodecCapability
	SSRC      webrtc.SSRC
	// owner is the publishing session, so a new subscriber can ask it for a
	// keyframe.
	owner *session
}

func (p publication) key() string { return p.Publisher + "/" + p.TrackID }

// forwardVideo reads an inbound video track and fans it out to the other WebRTC
// peers in the room. It returns when the track ends.
func (s *session) forwardVideo(track *webrtc.TrackRemote) {
	pub := publication{
		Publisher: s.pubkey,
		TrackID:   track.ID(),
		StreamID:  track.StreamID(),
		Codec:     track.Codec().RTPCodecCapability,
		SSRC:      track.SSRC(),
		owner:     s,
	}
	s.log.Debug().Str("track", pub.TrackID).Str("codec", pub.Codec.MimeType).Msg("forwarding an inbound video track")

	for {
		select {
		case <-s.done:
			return
		default:
		}
		packet, _, err := track.ReadRTP()
		if err != nil {
			s.log.Debug().Err(err).Str("track", pub.TrackID).Msg("inbound video track ended")
			return
		}
		for _, sub := range s.hub.subscribers(s.roomID, s) {
			sub.writeVideo(pub, packet)
		}
	}
}

// writeVideo delivers one forwarded packet to this subscriber, creating the
// outbound track on first sight of the publication.
func (s *session) writeVideo(pub publication, packet *rtp.Packet) {
	track, fresh := s.videoTrackFor(pub)
	if track == nil {
		return
	}
	if fresh {
		// A subscriber joining mid-stream starts between keyframes, so its
		// decoder has nothing to build on and shows nothing until the next one --
		// which can be seconds away. Asking the publisher for one now is the
		// difference between video appearing at once and appearing eventually.
		pub.owner.requestKeyframe(pub.SSRC)
	}
	if err := track.WriteRTP(packet); err != nil {
		s.log.Debug().Err(err).Str("track", pub.TrackID).Msg("dropping a forwarded video packet")
	}
}

// videoTrackFor returns this subscriber's outbound track for a publication,
// creating and negotiating it on first use. fresh reports whether it was just
// created.
func (s *session) videoTrackFor(pub publication) (track *webrtc.TrackLocalStaticRTP, fresh bool) {
	s.videoMu.Lock()
	if existing, ok := s.videoTracks[pub.key()]; ok {
		s.videoMu.Unlock()
		return existing, false
	}
	s.videoMu.Unlock()

	// The publisher's own track and stream ids are carried through, so a receiver
	// can tell a camera from a screen share exactly as the publisher labelled
	// them, and group a peer's tracks by stream.
	created, err := webrtc.NewTrackLocalStaticRTP(pub.Codec, pub.TrackID, pub.StreamID)
	if err != nil {
		s.log.Debug().Err(err).Msg("could not create an outbound video track")
		return nil, false
	}

	s.videoMu.Lock()
	if existing, ok := s.videoTracks[pub.key()]; ok {
		// Another packet from the same publication won the race.
		s.videoMu.Unlock()
		return existing, false
	}
	s.videoTracks[pub.key()] = created
	s.videoMu.Unlock()

	if _, err := s.pc.AddTrack(created); err != nil {
		s.log.Debug().Err(err).Msg("could not add an outbound video track")
		return nil, false
	}
	s.renegotiate()
	return created, true
}

// requestKeyframe asks this session's peer for a keyframe on one of its tracks.
func (s *session) requestKeyframe(ssrc webrtc.SSRC) {
	if s.pc == nil {
		return
	}
	if err := s.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(ssrc)}}); err != nil {
		s.log.Debug().Err(err).Msg("could not request a keyframe")
	}
}
