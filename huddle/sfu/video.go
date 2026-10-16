package sfu

import (
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/ohstr/nmilat/utils"
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

// keyframeInterval is the least time between two keyframe requests one
// subscriber makes for one publication. A browser asks again on every frame it
// cannot decode, and each request makes the publisher send a full, much larger
// frame; forwarding them all would flood the very link that is losing packets.
const keyframeInterval = 500 * time.Millisecond

// subscription is one subscriber's outbound copy of a publication.
type subscription struct {
	track *webrtc.TrackLocalStaticRTP

	mu sync.Mutex
	// owner and ssrc name where keyframe requests go. They change when the
	// publisher reconnects: a client keeps its track ids across sessions, so the
	// new stream lands on this same subscription.
	owner         *session
	ssrc          webrtc.SSRC
	lastRequest   time.Time
	heardFromPeer bool
}

// adopt points the subscription at pub's source, reporting whether it moved.
func (sub *subscription) adopt(pub publication) bool {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.owner == pub.owner && sub.ssrc == pub.SSRC {
		return false
	}
	sub.owner, sub.ssrc = pub.owner, pub.SSRC
	return true
}

// requestKeyframe asks the publisher for a keyframe, at most once per
// keyframeInterval unless force is set.
func (sub *subscription) requestKeyframe(force bool) {
	sub.mu.Lock()
	now := time.Now()
	if !force && now.Sub(sub.lastRequest) < keyframeInterval {
		sub.mu.Unlock()
		return
	}
	sub.lastRequest = now
	owner, ssrc := sub.owner, sub.ssrc
	sub.mu.Unlock()
	if owner != nil {
		owner.requestKeyframe(ssrc)
	}
}

// writeVideo delivers one forwarded packet to this subscriber, creating the
// outbound track on first sight of the publication.
func (s *session) writeVideo(pub publication, packet *rtp.Packet) {
	sub, fresh := s.subscriptionFor(pub)
	if sub == nil {
		return
	}
	// A subscriber joining mid-stream starts between keyframes, so its decoder has
	// nothing to build on until the next one -- and so does one whose publisher
	// just reconnected, since the new encoder's stream continues on this track.
	if moved := sub.adopt(pub); fresh || moved {
		sub.requestKeyframe(true)
	}
	if err := sub.track.WriteRTP(packet); err != nil {
		s.log.Debug().Err(err).Str("track", pub.TrackID).Msg("dropping a forwarded video packet")
	}
}

// subscriptionFor returns this subscriber's outbound track for a publication,
// creating and negotiating it on first use. fresh reports whether it was just
// created.
func (s *session) subscriptionFor(pub publication) (sub *subscription, fresh bool) {
	s.videoMu.Lock()
	defer func() {
		s.videoMu.Unlock()
		if fresh {
			s.renegotiate()
		}
	}()
	if existing, ok := s.videoTracks[pub.key()]; ok {
		return existing, false
	}

	// The publisher's own track and stream ids are carried through, so a receiver
	// can tell a camera from a screen share exactly as the publisher labelled
	// them, and group a peer's tracks by stream.
	track, err := webrtc.NewTrackLocalStaticRTP(pub.Codec, pub.TrackID, pub.StreamID)
	if err != nil {
		s.log.Debug().Err(err).Msg("could not create an outbound video track")
		return nil, false
	}
	sender, err := s.pc.AddTrack(track)
	if err != nil {
		// Not recorded, so the next packet tries again rather than writing into a
		// track that was never added.
		s.log.Debug().Err(err).Msg("could not add an outbound video track")
		return nil, false
	}
	sub = &subscription{track: track}
	s.videoTracks[pub.key()] = sub
	go s.readSubscriberRTCP(sender, sub, pub.TrackID)
	return sub, true
}

// readSubscriberRTCP carries this subscriber's feedback on one forwarded track
// back to its publisher.
//
// Reading is also what makes pion's interceptors see the feedback at all, so
// without this loop the NACK responder never retransmits either. Two signals
// matter here:
//
//   - PLI and FIR, the subscriber saying it cannot decode: forwarded, throttled.
//   - The first packet of any kind, which proves the track is now bound. The
//     keyframe asked for when the track was created reached the publisher before
//     the subscriber had even answered the renegotiation, so that keyframe was
//     written into a track with nowhere to go. Asking again here is what makes
//     the first picture appear without waiting for the subscriber to complain.
func (s *session) readSubscriberRTCP(sender *webrtc.RTPSender, sub *subscription, trackID string) {
	defer utils.RecoverPanic(s.log)
	for {
		packets, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		sub.mu.Lock()
		first := !sub.heardFromPeer
		sub.heardFromPeer = true
		sub.mu.Unlock()
		if first {
			sub.requestKeyframe(true)
			continue
		}
		for _, packet := range packets {
			switch packet.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				s.log.Debug().Str("track", trackID).Msg("forwarding a keyframe request")
				sub.requestKeyframe(false)
			}
		}
	}
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
