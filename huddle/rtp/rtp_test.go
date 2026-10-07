package rtp

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/huddle/wire"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// A Sink must satisfy room.Sink, or the room cannot deliver to it at all.
var _ room.Sink = (*Sink)(nil)

// A real pion track must satisfy PacketWriter, or the SFU cannot hand one to a
// Sink. This is the assertion that catches an rtp major-version drift between
// this package and pion/webrtc: pion/webrtc/v4 builds on pion/rtp v1, and a v2
// *rtp.Packet is a different type, so importing rtp/v2 here makes this line fail
// to compile rather than failing silently at the integration point.
var _ PacketWriter = (*webrtc.TrackLocalStaticRTP)(nil)

const recvWindow = 5 * time.Second

// recorder captures written packets and signals each one, so a test can wait for
// the writer goroutine rather than sleeping.
type recorder struct {
	mu      sync.Mutex
	packets []*rtp.Packet
	written chan struct{}
	err     error
	block   chan struct{}
}

func newRecorder() *recorder {
	return &recorder{written: make(chan struct{}, 64)}
}

func (r *recorder) WriteRTP(p *rtp.Packet) error {
	if r.block != nil {
		<-r.block
	}
	r.mu.Lock()
	// Copy the packet: the sink reuses nothing, but a test asserting on payload
	// bytes should not depend on that.
	clone := *p
	clone.Payload = append([]byte(nil), p.Payload...)
	r.packets = append(r.packets, &clone)
	err := r.err
	r.mu.Unlock()
	select {
	case r.written <- struct{}{}:
	default:
	}
	return err
}

func (r *recorder) snapshot() []*rtp.Packet {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*rtp.Packet(nil), r.packets...)
}

func (r *recorder) waitFor(t *testing.T, n int) []*rtp.Packet {
	t.Helper()
	deadline := time.After(recvWindow)
	for {
		if got := r.snapshot(); len(got) >= n {
			return got
		}
		select {
		case <-r.written:
		case <-deadline:
			t.Fatalf("only %d packet(s) written, want %d", len(r.snapshot()), n)
		}
	}
}

func newSink(t *testing.T, rec *recorder, mutate func(*Config)) *Sink {
	t.Helper()
	ssrc := uint32(0x1000)
	cfg := Config{
		NewTrack: func(room.PeerInfo) (PacketWriter, error) { return rec, nil },
		NewSSRC: func() (uint32, error) {
			ssrc++
			return ssrc, nil
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func frame(author room.PeerInfo, header wire.FrameHeader, opus []byte) room.Frame {
	client := wire.EncodeFrame(header, opus)
	return room.Frame{
		Author:  author,
		Version: 3,
		Client:  client,
		Relayed: wire.RelayFrame(3, author.Index, author.Epoch, client),
	}
}

var alice = room.PeerInfo{Pubkey: "alice-pubkey", Index: 3, Epoch: 1}
var bob = room.PeerInfo{Pubkey: "bob-pubkey", Index: 7, Epoch: 2}

/////////////////////////////////////////////////////////////////////
// The core claim: no transcode
/////////////////////////////////////////////////////////////////////

func TestOpusPayloadIsCarriedVerbatim(t *testing.T) {
	rec := newRecorder()
	s := newSink(t, rec, nil)

	opus := []byte{0xFC, 0x01, 0x02, 0x03, 0xDE, 0xAD, 0xBE, 0xEF}
	if !s.SendFrame(frame(alice, wire.FrameHeader{Seq: 1, Ts48k: 960, LevelDbov: -20}, opus)) {
		t.Fatal("SendFrame refused")
	}

	packets := rec.waitFor(t, 1)
	if !bytes.Equal(packets[0].Payload, opus) {
		t.Errorf("payload = %x, want the Opus bytes verbatim %x", packets[0].Payload, opus)
	}
	// The huddle header must not leak into the RTP payload.
	if len(packets[0].Payload) != len(opus) {
		t.Errorf("payload length = %d, want %d -- the 8-byte header should be stripped", len(packets[0].Payload), len(opus))
	}
}

// A room.Frame's slices alias the broadcast's buffers, so the sink must copy
// before queueing or an in-flight frame is corrupted by the next broadcast.
func TestPayloadIsCopiedNotAliased(t *testing.T) {
	rec := newRecorder()
	rec.block = make(chan struct{})
	s := newSink(t, rec, nil)

	opus := []byte{0x01, 0x02, 0x03, 0x04}
	f := frame(alice, wire.FrameHeader{Ts48k: 960}, opus)
	if !s.SendFrame(f) {
		t.Fatal("SendFrame refused")
	}

	// Scribble over the caller's buffer, as a reused read buffer would.
	for i := range f.Client {
		f.Client[i] = 0xFF
	}
	close(rec.block)

	packets := rec.waitFor(t, 1)
	if !bytes.Equal(packets[0].Payload, opus) {
		t.Errorf("payload = %x, want %x -- the sink aliased the caller's buffer", packets[0].Payload, opus)
	}
}

/////////////////////////////////////////////////////////////////////
// RTP framing
/////////////////////////////////////////////////////////////////////

// A huddle header's 48 kHz timestamp is already Opus's RTP clock, so it maps
// across with no rescaling.
func TestTimestampMapsDirectly(t *testing.T) {
	rec := newRecorder()
	s := newSink(t, rec, nil)

	for i, ts := range []uint32{960, 1920, 2880} {
		if !s.SendFrame(frame(alice, wire.FrameHeader{Seq: uint16(i), Ts48k: ts}, []byte{0x01})) {
			t.Fatalf("frame %d refused", i)
		}
	}

	packets := rec.waitFor(t, 3)
	for i, want := range []uint32{960, 1920, 2880} {
		if packets[i].Timestamp != want {
			t.Errorf("packet %d timestamp = %d, want %d", i, packets[i].Timestamp, want)
		}
	}
}

// The RTP sequence number is ours. A huddle sequence wraps and may gap where
// frames were dropped upstream; forwarding it would show a receiver loss that
// did not happen on this leg.
func TestSequenceNumbersAreOursAndMonotonic(t *testing.T) {
	rec := newRecorder()
	s := newSink(t, rec, nil)

	// Author sequence numbers that gap wildly and wrap.
	for _, seq := range []uint16{5, 9, 65535, 0, 1} {
		if !s.SendFrame(frame(alice, wire.FrameHeader{Seq: seq, Ts48k: 960}, []byte{0x01})) {
			t.Fatalf("frame with author seq %d refused", seq)
		}
	}

	packets := rec.waitFor(t, 5)
	for i := 1; i < len(packets); i++ {
		if packets[i].SequenceNumber != packets[i-1].SequenceNumber+1 {
			t.Errorf("packet %d seq = %d, want %d (contiguous)", i, packets[i].SequenceNumber, packets[i-1].SequenceNumber+1)
		}
	}
}

func TestPayloadTypeDefaultsToOpusAndIsOverridable(t *testing.T) {
	rec := newRecorder()
	s := newSink(t, rec, nil)
	if !s.SendFrame(frame(alice, wire.FrameHeader{Ts48k: 960}, []byte{0x01})) {
		t.Fatal("refused")
	}
	if got := rec.waitFor(t, 1)[0].PayloadType; got != OpusPayloadType {
		t.Errorf("PayloadType = %d, want %d", got, OpusPayloadType)
	}

	rec2 := newRecorder()
	s2 := newSink(t, rec2, func(c *Config) { c.PayloadType = 96 })
	if !s2.SendFrame(frame(alice, wire.FrameHeader{Ts48k: 960}, []byte{0x01})) {
		t.Fatal("refused")
	}
	if got := rec2.waitFor(t, 1)[0].PayloadType; got != 96 {
		t.Errorf("PayloadType = %d, want the negotiated 96", got)
	}
}

// The marker bit flags the first packet of a talkspurt, i.e. the first speech
// frame after comfort noise.
func TestMarkerBitFlagsTheStartOfATalkspurt(t *testing.T) {
	rec := newRecorder()
	s := newSink(t, rec, nil)

	send := func(dtx bool) {
		t.Helper()
		h := wire.FrameHeader{Ts48k: 960}
		if dtx {
			h.Flags = wire.FlagDTX
		}
		if !s.SendFrame(frame(alice, h, []byte{0x01})) {
			t.Fatal("refused")
		}
	}

	send(false) // first speech after the track's initial silence -> marker
	send(false) // mid-talkspurt -> no marker
	send(true)  // comfort noise
	send(false) // speech resumes -> marker

	packets := rec.waitFor(t, 4)
	want := []bool{true, false, false, true}
	for i, w := range want {
		if packets[i].Marker != w {
			t.Errorf("packet %d marker = %v, want %v", i, packets[i].Marker, w)
		}
	}
}

// A v1 room predates the frame header, so the whole client frame is the payload
// and there is no timestamp to carry.
func TestProtocolV1HasNoHeaderToRead(t *testing.T) {
	rec := newRecorder()
	s := newSink(t, rec, nil)

	opus := []byte{0xAA, 0xBB}
	if !s.SendFrame(room.Frame{Author: alice, Version: 1, Client: opus, Relayed: append([]byte{alice.Index}, opus...)}) {
		t.Fatal("refused")
	}

	packets := rec.waitFor(t, 1)
	if !bytes.Equal(packets[0].Payload, opus) {
		t.Errorf("payload = %x, want the whole v1 frame %x", packets[0].Payload, opus)
	}
	if packets[0].Timestamp != 0 {
		t.Errorf("Timestamp = %d, want 0 with no header to read", packets[0].Timestamp)
	}
}

/////////////////////////////////////////////////////////////////////
// One track per speaker
/////////////////////////////////////////////////////////////////////

func TestEachSpeakerGetsItsOwnTrackAndSSRC(t *testing.T) {
	var mu sync.Mutex
	writers := map[string]*recorder{}
	ssrc := uint32(0x2000)

	s, err := New(Config{
		NewTrack: func(a room.PeerInfo) (PacketWriter, error) {
			mu.Lock()
			defer mu.Unlock()
			r := newRecorder()
			writers[a.Pubkey] = r
			return r, nil
		},
		NewSSRC: func() (uint32, error) { ssrc++; return ssrc, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)

	for _, author := range []room.PeerInfo{alice, bob, alice} {
		if !s.SendFrame(frame(author, wire.FrameHeader{Ts48k: 960}, []byte{0x01})) {
			t.Fatalf("frame from %s refused", author.Pubkey)
		}
	}

	deadline := time.Now().Add(recvWindow)
	for s.Tracks() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := s.Tracks(); got != 2 {
		t.Fatalf("Tracks() = %d, want 2 -- one per speaker, reused on repeat", got)
	}

	mu.Lock()
	aliceRec, bobRec := writers[alice.Pubkey], writers[bob.Pubkey]
	mu.Unlock()

	alicePackets := aliceRec.waitFor(t, 2)
	bobPackets := bobRec.waitFor(t, 1)
	if alicePackets[0].SSRC == bobPackets[0].SSRC {
		t.Error("two speakers share an SSRC; a receiver cannot tell them apart")
	}
	// The repeat speaker stayed on one stream rather than restarting.
	if alicePackets[1].SSRC != alicePackets[0].SSRC {
		t.Error("the same speaker got a second SSRC")
	}
	if alicePackets[1].SequenceNumber != alicePackets[0].SequenceNumber+1 {
		t.Error("the same speaker's sequence restarted instead of continuing")
	}
}

// A factory failure must not be cached, or a transient inability to add a track
// would silence that speaker permanently.
func TestTrackFactoryFailureIsRetried(t *testing.T) {
	rec := newRecorder()
	var calls int
	var mu sync.Mutex

	s, err := New(Config{
		NewTrack: func(room.PeerInfo) (PacketWriter, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 1 {
				return nil, errors.New("transient")
			}
			return rec, nil
		},
		NewSSRC: func() (uint32, error) { return 1, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)

	for i := 0; i < 2; i++ {
		if !s.SendFrame(frame(alice, wire.FrameHeader{Ts48k: 960}, []byte{0x01})) {
			t.Fatalf("frame %d refused", i)
		}
	}

	// The second frame succeeds, so exactly one packet lands.
	packets := rec.waitFor(t, 1)
	if len(packets) != 1 {
		t.Errorf("packets = %d, want 1", len(packets))
	}
	if s.Tracks() != 1 {
		t.Errorf("Tracks() = %d, want 1", s.Tracks())
	}
}

/////////////////////////////////////////////////////////////////////
// The room.Sink contract
/////////////////////////////////////////////////////////////////////

// SendFrame is called with the room's lock held, so a slow writer must cost
// dropped frames and never a blocked room.
func TestFullQueueDropsAndNeverBlocks(t *testing.T) {
	rec := newRecorder()
	rec.block = make(chan struct{})
	s := newSink(t, rec, func(c *Config) { c.QueueDepth = 2 })

	f := frame(alice, wire.FrameHeader{Ts48k: 960}, []byte{0x01})

	done := make(chan int, 1)
	go func() {
		accepted := 0
		for i := 0; i < 40; i++ {
			if s.SendFrame(f) {
				accepted++
			}
		}
		done <- accepted
	}()

	select {
	case accepted := <-done:
		if accepted >= 40 {
			t.Errorf("accepted %d of 40 with a blocked writer; the queue is not bounded", accepted)
		}
		if accepted == 0 {
			t.Error("accepted none; the queue should absorb a few")
		}
	case <-time.After(recvWindow):
		t.Fatal("SendFrame blocked; a room.Sink must never block")
	}

	if s.Dropped() == 0 {
		t.Error("Dropped() = 0, want the refused frames counted")
	}
	close(rec.block)
}

func TestEmptyPayloadIsAcceptedWithoutAPacket(t *testing.T) {
	rec := newRecorder()
	s := newSink(t, rec, nil)

	// A header with no Opus after it: nothing to carry, but not a drop either.
	if !s.SendFrame(room.Frame{Author: alice, Version: 3, Client: wire.EncodeFrame(wire.FrameHeader{}, nil)}) {
		t.Error("SendFrame reported a drop for a frame with no audio in it")
	}
	if !s.SendFrame(room.Frame{Author: alice, Version: 1, Client: nil}) {
		t.Error("SendFrame reported a drop for an empty v1 frame")
	}

	time.Sleep(50 * time.Millisecond)
	if got := rec.snapshot(); len(got) != 0 {
		t.Errorf("wrote %d packet(s) for empty payloads", len(got))
	}
}

func TestSendControl(t *testing.T) {
	rec := newRecorder()

	var mu sync.Mutex
	var seen []room.Control
	s := newSink(t, rec, func(c *Config) {
		c.OnControl = func(m room.Control) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, m)
		}
	})
	if !s.SendControl(room.Control{JSON: `{"type":"joined"}`}) {
		t.Error("SendControl reported a drop")
	}
	mu.Lock()
	got := len(seen)
	mu.Unlock()
	if got != 1 {
		t.Errorf("OnControl called %d times, want 1", got)
	}

	// No handler is not an error: a subscriber that only wants audio is valid.
	plain := newSink(t, newRecorder(), nil)
	if !plain.SendControl(room.Control{JSON: "x"}) {
		t.Error("SendControl reported a drop with no OnControl set")
	}
}

func TestNewRequiresATrackFactory(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrNoTrackFactory) {
		t.Fatalf("err = %v, want ErrNoTrackFactory", err)
	}
}

func TestCloseIsIdempotentAndStopsAccepting(t *testing.T) {
	rec := newRecorder()
	s, err := New(Config{
		NewTrack: func(room.PeerInfo) (PacketWriter, error) { return rec, nil },
		NewSSRC:  func() (uint32, error) { return 1, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	s.Close()
	s.Close() // must not panic

	// After Close the writer is gone, so a frame cannot be delivered. Either
	// answer is acceptable as long as it does not block or panic.
	done := make(chan bool, 1)
	go func() { done <- s.SendFrame(frame(alice, wire.FrameHeader{Ts48k: 960}, []byte{0x01})) }()
	select {
	case <-done:
	case <-time.After(recvWindow):
		t.Fatal("SendFrame blocked after Close")
	}
}

// benchOpusBytes is a realistic 20 ms Opus frame at ~64 kbps.
const benchOpusBytes = 160

// BenchmarkSinkSendFrame measures the room-to-RTP direction, which runs once per
// frame *per WebRTC subscriber*.
//
// Worth measuring separately from the room's own fan-out because the allocation
// story differs. room.BroadcastFrame builds one relayed buffer and shares it with
// every recipient, so its allocations are flat in peer count. SendFrame copies the
// payload per call -- a room.Frame's slices alias the broadcast's buffers and are
// only valid for that call -- so a room with N WebRTC subscribers pays N copies
// per frame where the WebSocket side pays one. That is a deliberate trade for
// correctness, and this is the number that says what it costs.
//
// The Sink is built by hand rather than through New so this benchmark owns the
// draining. New starts a writeLoop that packetizes and writes, which a benchmark
// producer always outruns; the queue would then fill and the rest of the run would
// measure the drop path instead of the enqueue path.
//
// Measured on an AMD EPYC-Genoa: ~470 ns and 160 B / 1 alloc per call, which is
// the payload copy. Five WebRTC subscribers at 50 frames a second is 250 calls/s,
// so ~0.12 ms of CPU and ~40 KB of garbage per second of call. The per-subscriber
// copy is real but nowhere near mattering at any plausible subscriber count.
func BenchmarkSinkSendFrame(b *testing.B) {
	s := &Sink{
		cfg:    Config{PayloadType: OpusPayloadType},
		queue:  make(chan queued, 1024),
		done:   make(chan struct{}),
		tracks: make(map[string]*track),
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-s.queue:
			case <-s.done:
				return
			}
		}
	}()
	b.Cleanup(func() {
		close(s.done)
		<-drained
	})

	f := frame(alice, wire.FrameHeader{Seq: 1, Ts48k: 960, LevelDbov: -20}, make([]byte, benchOpusBytes))

	b.ReportAllocs()
	b.ResetTimer()
	accepted := 0
	for i := 0; i < b.N; i++ {
		if s.SendFrame(f) {
			accepted++
		}
	}
	b.StopTimer()

	// A drop here is the sink working as designed, not a failure -- but if most
	// sends dropped, this measured the drop path, so say so rather than report a
	// number that means something else.
	b.ReportMetric(float64(accepted)/float64(b.N)*100, "%accepted")
}

// BenchmarkFrameFromRTP measures the RTP-to-room direction: once per inbound
// packet from a browser, before it reaches the room at all.
//
// Measured: ~128 ns and 176 B / 1 alloc, and the same with or without the
// audio-level extension (129 vs 126 ns). Parsing the extension is free, so there
// is no reason to skip negotiating it to save work.
func BenchmarkFrameFromRTP(b *testing.B) {
	// Both paths matter: the audio-level extension is optional per packet, so a
	// sender may negotiate it and still omit it, and the no-extension path is
	// what runs for a sender that never negotiated it at all.
	for _, tc := range []struct {
		name  string
		level *byte
		id    uint8
	}{
		{"with-audio-level", bptr(0x80 | 20), audioLevelExtID},
		{"without-audio-level", nil, 0},
	} {
		b.Run(tc.name, func(b *testing.B) {
			p := &rtp.Packet{
				Header:  rtp.Header{Version: 2, SequenceNumber: 42, Timestamp: 960, SSRC: 0x1234},
				Payload: make([]byte, benchOpusBytes),
			}
			if tc.level != nil {
				if err := p.SetExtension(audioLevelExtID, []byte{*tc.level}); err != nil {
					b.Fatalf("SetExtension: %v", err)
				}
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, ok := FrameFromRTP(p, tc.id); !ok {
					b.Fatal("FrameFromRTP rejected a packet with a payload")
				}
			}
		})
	}
}

// BenchmarkAudioLevel measures the RFC 6464 extension parse on its own, since it
// runs for every inbound packet that carries the extension.
//
// Measured: ~2.3 ns, zero allocations. It reads one byte. This is why
// BenchmarkFrameFromRTP cannot tell the two extension cases apart.
func BenchmarkAudioLevel(b *testing.B) {
	p := &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: 42, Timestamp: 960, SSRC: 0x1234},
		Payload: make([]byte, benchOpusBytes),
	}
	if err := p.SetExtension(audioLevelExtID, []byte{0x80 | 20}); err != nil {
		b.Fatalf("SetExtension: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, present := AudioLevel(p, audioLevelExtID); !present {
			b.Fatal("AudioLevel did not see the extension it was given")
		}
	}
}
