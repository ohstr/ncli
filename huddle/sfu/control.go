package sfu

import (
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/utils"
)

// The control plane on top of signalling: what a peer says it is doing, as
// opposed to what its media happens to be doing.
//
// RTP cannot tell a stopped camera from a lost one. A browser that stops its
// camera detaches the track, packets simply cease, and to everyone else that
// looks exactly like a network fault. So a client says so -- {"type":"media"}
// -- and the relay fans that out at once and remembers it for anyone joining
// later. Leaving is the same: a client that means to go says {"type":"leave"}
// first, so the others are told it left rather than that it dropped.

// Features is what this endpoint advertises on "joined", so a client can tell
// a relay that sends roster updates, media state and leave reasons from an
// older one that does none of it.
var Features = []string{"control", "media", "leave"}

// Reasons carried on "left".
const (
	ReasonLeave   = "leave"   // the client said it was leaving
	ReasonEvicted = "evicted" // the relay removed it (membership revoked)
	ReasonDropped = "dropped" // anything else: closed, timed out, failed
)

// Media states and kinds a client may announce.
const (
	MediaLive = "live"
	MediaOff  = "off"
)

var mediaKinds = map[string]bool{"mic": true, "camera": true, "screen": true}

// maxMediaID bounds a client-supplied track or stream id. Browsers use UUIDs;
// the bound only stops a peer from making every other peer store a megabyte.
const maxMediaID = 128

// controlQueueDepth is how many control messages may wait for this peer's
// socket. Control is a handful of messages per membership or media change, so
// a full queue means the peer has stopped reading; it is dropped from, never
// waited on, because the room delivers control while holding its lock.
const controlQueueDepth = 64

// mediaState is one kind of media a peer has said it is or is not publishing.
// Seq increases with every change a session makes, so a receiver holding both
// a roster snapshot and a live update can keep whichever is newer.
type mediaState struct {
	Kind     string `json:"kind"`
	State    string `json:"state"`
	TrackID  string `json:"track_id,omitempty"`
	StreamID string `json:"stream_id,omitempty"`
	Seq      uint64 `json:"seq"`
}

func mediaKey(pubkey string, index uint8) string {
	return pubkey + "/" + strconv.Itoa(int(index))
}

func validMedia(msg signal) bool {
	return mediaKinds[msg.Kind] && (msg.State == MediaLive || msg.State == MediaOff) &&
		len(msg.TrackID) <= maxMediaID && len(msg.StreamID) <= maxMediaID
}

// handleMedia records what this peer says it is publishing and tells the room.
func (s *session) handleMedia(msg signal) {
	if !validMedia(msg) {
		s.log.Debug().Str("kind", msg.Kind).Str("state", msg.State).Msg("ignoring an invalid media message")
		return
	}
	s.mediaMu.Lock()
	previous, known := s.media[msg.Kind]
	if known && previous.State == msg.State && previous.TrackID == msg.TrackID && previous.StreamID == msg.StreamID {
		s.mediaMu.Unlock()
		return
	}
	s.mediaSeq++
	state := mediaState{Kind: msg.Kind, State: msg.State, TrackID: msg.TrackID, StreamID: msg.StreamID, Seq: s.mediaSeq}
	s.media[msg.Kind] = state
	s.mediaMu.Unlock()

	message, err := json.Marshal(signal{
		Type: "media", Pubkey: s.pubkey, PeerIndex: s.peer.Index, Epoch: s.peer.Epoch,
		Kind: state.Kind, State: state.State, TrackID: state.TrackID, StreamID: state.StreamID, Seq: state.Seq,
	})
	if err != nil {
		return
	}
	s.room.BroadcastControl(string(message), s.peer.ID)
}

// mediaSnapshot is this session's announced media, oldest change first.
func (s *session) mediaSnapshot() []mediaState {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	if len(s.media) == 0 {
		return nil
	}
	out := make([]mediaState, 0, len(s.media))
	for _, m := range s.media {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// media returns every WebRTC session's announced media in a room, keyed by
// mediaKey, for the roster on "joined".
func (h *videoHub) media(roomID string) map[string][]mediaState {
	h.mu.Lock()
	sessions := make([]*session, 0, len(h.sessions[roomID]))
	for s := range h.sessions[roomID] {
		sessions = append(sessions, s)
	}
	h.mu.Unlock()

	out := make(map[string][]mediaState, len(sessions))
	for _, s := range sessions {
		if s.peer == nil {
			continue
		}
		if snapshot := s.mediaSnapshot(); len(snapshot) > 0 {
			out[mediaKey(s.pubkey, s.peer.Index)] = snapshot
		}
	}
	return out
}

// setReason records why this session is ending. The first reason wins: a
// client that says leave and then has its socket closed still left.
func (s *session) setReason(reason string) {
	s.reasonMu.Lock()
	defer s.reasonMu.Unlock()
	if s.reason == "" {
		s.reason = reason
	}
}

func (s *session) leftReason() string {
	s.reasonMu.Lock()
	defer s.reasonMu.Unlock()
	if s.reason == "" {
		return ReasonDropped
	}
	return s.reason
}

// enqueueControl is rtp.Config.OnControl. The room calls it while holding its
// lock, so it must never block: the socket write happens in controlLoop.
func (s *session) enqueueControl(c room.Control) {
	if c.Close {
		// Eviction. The message before it (if any) is already queued, so the
		// close goes behind it; only if the queue is full does it skip ahead.
		s.setReason(ReasonEvicted)
	}
	select {
	case s.control <- c:
	default:
		if c.Close {
			s.shutdown()
			return
		}
		s.log.Warn().Msg("dropping a control message: the peer is not reading")
	}
}

// controlLoop writes queued control messages to the socket. It starts only
// after this peer's own "joined" is on the wire, so nothing about the room
// reaches a client before it knows it is in.
func (s *session) controlLoop() {
	defer utils.RecoverPanic(s.log)
	for {
		select {
		case <-s.done:
			return
		case c := <-s.control:
			if c.Close {
				s.shutdown()
				return
			}
			if err := s.writeRaw(c.JSON); err != nil {
				s.shutdown()
				return
			}
		}
	}
}

// writeRaw writes an already-encoded control message.
func (s *session) writeRaw(message string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.conn.SetWriteDeadline(time.Now().Add(s.cfg.writeTimeout())); err != nil {
		return err
	}
	return s.conn.WriteMessage(websocket.TextMessage, []byte(message))
}
