// Package rtp adapts a huddle audio room onto RTP, so a peer reached over
// WebRTC can sit in the same room as one reached over the huddle WebSocket.
//
// It implements nmilat/huddle/room.Sink. A room hands it frames; it repacketizes
// them as RTP and hands them to a writer. The conversion is deliberately not a
// transcode:
//
//	huddle frame:  [8-byte header][Opus]        (header is telemetry)
//	RTP packet:    [RTP header][Opus]           (same Opus bytes, verbatim)
//
// The Opus payload is copied but never decoded or re-encoded, which is what lets
// a relay bridge the two transports without linking a codec.
//
// # Why the timestamp maps directly
//
// A huddle frame's header carries a 48 kHz media timestamp, and Opus in RTP uses
// a 48 kHz clock. So the author's timestamp is the RTP timestamp -- no rescaling,
// and no clock of our own to drift.
//
// # One Sink per subscriber, one track per speaker
//
// A room delivers every other peer's audio to one Sink, but a WebRTC receiver
// expects each remote speaker on its own track with its own SSRC. So a Sink
// demultiplexes by author and creates a track on demand.
package rtp

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/ohstr/nmilat/huddle/room"
	"github.com/pion/rtp"
)

// OpusPayloadType is the dynamic payload type WebRTC conventionally negotiates
// for Opus. Override it in Config when the answer says otherwise -- it is a
// negotiated value, not a constant of the protocol.
const OpusPayloadType uint8 = 111

// DefaultQueueDepth is how many frames may be waiting to be written before new
// ones are dropped. It matches the room's own audio depth: 8 frames is 160 ms,
// and a deeper buffer trades latency for loss in the wrong direction.
const DefaultQueueDepth = 8

// ErrNoTrackFactory is returned by New when Config has no NewTrack.
var ErrNoTrackFactory = errors.New("rtp: Config.NewTrack is required")

// PacketWriter receives the RTP packets built for one speaker.
// *webrtc.TrackLocalStaticRTP satisfies it, so an SFU passes one directly;
// tests pass a recorder.
//
// The rtp import must stay on the same major version pion/webrtc uses (v1).
// pion/rtp/v2 exists and is newer, but a v2 *rtp.Packet is a different type, so
// building on it makes this interface unsatisfiable by pion's own track. There
// is a compile-time assertion in the tests pinning that.
type PacketWriter interface {
	WriteRTP(*rtp.Packet) error
}

// TrackFactory creates the writer for one speaker, called the first time that
// speaker is heard. Returning an error drops that speaker's audio for this
// subscriber and is retried on the next frame, so a transient failure to add a
// track does not silence them permanently.
type TrackFactory func(author room.PeerInfo) (PacketWriter, error)

// Config configures a Sink.
type Config struct {
	// NewTrack is required.
	NewTrack TrackFactory

	// PayloadType defaults to OpusPayloadType.
	PayloadType uint8

	// QueueDepth defaults to DefaultQueueDepth. Values below one are raised to
	// one: a zero-capacity queue would make every send wait for the writer,
	// which is the blocking a room.Sink must never do.
	QueueDepth int

	// OnControl, when set, receives the room's control messages. RTP has no
	// control plane, so an SFU that wants the roster forwards these itself --
	// over a data channel, say. Nil discards them, which is not an error: a
	// subscriber that only wants audio is a legitimate subscriber.
	OnControl func(room.Control)

	// NewSSRC generates a track's SSRC. Nil uses crypto/rand. Injectable so a
	// test can assert on packets without matching a random value.
	NewSSRC func() (uint32, error)
}

// Sink implements room.Sink by repacketizing audio onto RTP.
type Sink struct {
	cfg     Config
	queue   chan queued
	done    chan struct{}
	closer  sync.Once
	wg      sync.WaitGroup
	dropped uint64

	mu     sync.Mutex
	tracks map[string]*track
}

// queued is one frame waiting to be written. Payload is a copy: a room.Frame's
// slices alias the broadcast's buffers and are only valid for the duration of
// the SendFrame call.
type queued struct {
	author    room.PeerInfo
	timestamp uint32
	marker    bool
	payload   []byte
}

// track is one speaker's RTP stream to this subscriber.
type track struct {
	writer PacketWriter
	ssrc   uint32
	// seq is ours, not the author's. A huddle sequence number wraps every 2^16
	// frames and may gap where frames were dropped upstream; forwarding it would
	// make a receiver see loss that did not happen on this leg.
	seq uint16
	// silent tracks whether the last frame was comfort noise, so the marker bit
	// can flag the first packet of the next talkspurt.
	silent bool
}

// New returns a Sink and starts its writer goroutine. Call Close when the
// subscriber goes away.
func New(cfg Config) (*Sink, error) {
	if cfg.NewTrack == nil {
		return nil, ErrNoTrackFactory
	}
	if cfg.PayloadType == 0 {
		cfg.PayloadType = OpusPayloadType
	}
	if cfg.QueueDepth < 1 {
		cfg.QueueDepth = DefaultQueueDepth
	}
	if cfg.NewSSRC == nil {
		cfg.NewSSRC = randomSSRC
	}

	s := &Sink{
		cfg:    cfg,
		queue:  make(chan queued, cfg.QueueDepth),
		done:   make(chan struct{}),
		tracks: make(map[string]*track),
	}
	s.wg.Add(1)
	go s.writeLoop()
	return s, nil
}

// SendFrame implements room.Sink. It copies the payload, queues it, and returns
// immediately -- it is called with the room's lock held, so it must never block.
// A full queue drops the frame and reports false.
func (s *Sink) SendFrame(f room.Frame) bool {
	header, payload, hasHeader := f.Payload()
	if len(payload) == 0 {
		// Nothing to carry. Not a drop worth reporting: there was no audio.
		return true
	}

	item := queued{
		author:  f.Author,
		payload: append([]byte(nil), payload...),
	}
	if hasHeader {
		item.timestamp = header.Ts48k
		item.marker = !header.IsDTX()
	}

	select {
	case s.queue <- item:
		return true
	case <-s.done:
		return false
	default:
		s.mu.Lock()
		s.dropped++
		s.mu.Unlock()
		return false
	}
}

// SendControl implements room.Sink. RTP carries no control plane, so this hands
// the message to Config.OnControl when set and otherwise accepts and discards
// it.
func (s *Sink) SendControl(c room.Control) bool {
	if s.cfg.OnControl != nil {
		s.cfg.OnControl(c)
	}
	return true
}

// Dropped is how many frames were discarded because the write queue was full.
func (s *Sink) Dropped() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Close stops the writer goroutine and waits for it. Idempotent.
func (s *Sink) Close() {
	s.closer.Do(func() { close(s.done) })
	s.wg.Wait()
}

func (s *Sink) writeLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.done:
			return
		case item := <-s.queue:
			s.write(item)
		}
	}
}

func (s *Sink) write(item queued) {
	t, ok := s.trackFor(item.author)
	if !ok {
		return
	}

	s.mu.Lock()
	// A DTX frame is comfort noise; the marker bit flags the first packet of the
	// talkspurt that follows one.
	marker := item.marker && t.silent
	t.silent = !item.marker
	t.seq++
	packet := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Marker:         marker,
			PayloadType:    s.cfg.PayloadType,
			SequenceNumber: t.seq,
			Timestamp:      item.timestamp,
			SSRC:           t.ssrc,
		},
		Payload: item.payload,
	}
	writer := t.writer
	s.mu.Unlock()

	// Outside the lock: a writer may block on a network buffer, and holding the
	// lock there would stall every other speaker on this subscriber.
	_ = writer.WriteRTP(packet)
}

// trackFor returns the author's track, creating it on first use. A factory error
// is not cached, so the next frame from that speaker retries.
func (s *Sink) trackFor(author room.PeerInfo) (*track, bool) {
	s.mu.Lock()
	if t, ok := s.tracks[author.Pubkey]; ok {
		s.mu.Unlock()
		return t, true
	}
	s.mu.Unlock()

	writer, err := s.cfg.NewTrack(author)
	if err != nil || writer == nil {
		return nil, false
	}
	ssrc, err := s.cfg.NewSSRC()
	if err != nil {
		return nil, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Another frame from the same speaker may have won the race.
	if t, ok := s.tracks[author.Pubkey]; ok {
		return t, true
	}
	t := &track{writer: writer, ssrc: ssrc, silent: true}
	s.tracks[author.Pubkey] = t
	return t, true
}

// Tracks is how many distinct speakers this subscriber has heard.
func (s *Sink) Tracks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tracks)
}

func randomSSRC() (uint32, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("rtp: could not generate an SSRC: %w", err)
	}
	return binary.BigEndian.Uint32(b[:]), nil
}
