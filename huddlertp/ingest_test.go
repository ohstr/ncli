package huddlertp

import (
	"bytes"
	"testing"

	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/huddle/wire"
	"github.com/pion/rtp"
)

const audioLevelExtID uint8 = 1

func rtpPacket(t *testing.T, seq uint16, ts uint32, payload []byte, level *byte) *rtp.Packet {
	t.Helper()
	p := &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 0x1234},
		Payload: payload,
	}
	if level != nil {
		if err := p.SetExtension(audioLevelExtID, []byte{*level}); err != nil {
			t.Fatalf("SetExtension: %v", err)
		}
	}
	return p
}

func bptr(b byte) *byte { return &b }

// levelExt builds an RFC 6464 extension byte: bit 7 is voice activity, bits 0-6
// carry the level as a magnitude in -dBov.
func levelExt(voice bool, magnitude byte) byte {
	if voice {
		return 0x80 | magnitude
	}
	return magnitude
}

// The whole point of the bridge: a WebSocket peer's frame reaches a WebRTC peer
// and back again with the Opus untouched. This drives the real Sink in one
// direction and FrameFromRTP in the other.
func TestBridgeRoundTripPreservesOpus(t *testing.T) {
	rec := newRecorder()
	s := newSink(t, rec, nil)

	opus := []byte{0xFC, 0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02}
	original := wire.FrameHeader{Seq: 4242, Ts48k: 960 * 7, LevelDbov: -14}

	if !s.SendFrame(frame(alice, original, opus)) {
		t.Fatal("SendFrame refused")
	}
	packets := rec.waitFor(t, 1)

	// ...and back the other way.
	back, ok := FrameFromRTP(packets[0], 0)
	if !ok {
		t.Fatal("FrameFromRTP returned ok = false")
	}
	header, payload, parsed := wire.ParseFrame(back)
	if !parsed {
		t.Fatalf("the round-tripped frame does not parse: %x", back)
	}

	if !bytes.Equal(payload, opus) {
		t.Errorf("payload = %x, want the original Opus %x", payload, opus)
	}
	// The 48 kHz timestamp survives both hops unscaled.
	if header.Ts48k != original.Ts48k {
		t.Errorf("Ts48k = %d, want %d", header.Ts48k, original.Ts48k)
	}
}

func TestFrameFromRTPMapsTimestampAndSequence(t *testing.T) {
	p := rtpPacket(t, 9001, 48000, []byte{0x01, 0x02}, nil)

	frameBytes, ok := FrameFromRTP(p, audioLevelExtID)
	if !ok {
		t.Fatal("ok = false")
	}
	header, payload, parsed := wire.ParseFrame(frameBytes)
	if !parsed {
		t.Fatal("does not parse")
	}
	if header.Seq != 9001 {
		t.Errorf("Seq = %d, want the RTP sequence 9001 forwarded", header.Seq)
	}
	if header.Ts48k != 48000 {
		t.Errorf("Ts48k = %d, want 48000 unscaled", header.Ts48k)
	}
	if !bytes.Equal(payload, []byte{0x01, 0x02}) {
		t.Errorf("payload = %x", payload)
	}
}

// Without the extension there is no level to report, so the frame claims the
// silence floor -- and must NOT claim comfort noise, which would be inventing
// information the sender never gave.
func TestFrameFromRTPWithoutTheExtension(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extID uint8
		level *byte
	}{
		{name: "extension not negotiated", extID: 0, level: bptr(levelExt(true, 10))},
		{name: "negotiated but absent on this packet", extID: audioLevelExtID, level: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := rtpPacket(t, 1, 960, []byte{0x01}, tc.level)
			frameBytes, ok := FrameFromRTP(p, tc.extID)
			if !ok {
				t.Fatal("ok = false")
			}
			header, _, _ := wire.ParseFrame(frameBytes)
			if header.LevelDbov != wire.LevelSilenceFloor {
				t.Errorf("LevelDbov = %d, want the silence floor", header.LevelDbov)
			}
			if header.IsDTX() {
				t.Error("DTX set with no voice-activity information to justify it")
			}
		})
	}
}

func TestFrameFromRTPReadsTheAudioLevel(t *testing.T) {
	tests := []struct {
		name      string
		ext       byte
		wantLevel int8
		wantDTX   bool
	}{
		// bit 7 = voice activity, bits 0-6 = level magnitude in -dBov.
		{name: "loudest with voice", ext: levelExt(true, 0), wantLevel: 0, wantDTX: false},
		{name: "moderate with voice", ext: levelExt(true, 30), wantLevel: -30, wantDTX: false},
		{name: "quietest with voice", ext: levelExt(true, 127), wantLevel: -127, wantDTX: false},
		// No voice activity is comfort noise, which is what DTX means.
		{name: "no voice activity", ext: levelExt(false, 60), wantLevel: -60, wantDTX: true},
		{name: "silence, no voice", ext: levelExt(false, 127), wantLevel: -127, wantDTX: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := rtpPacket(t, 1, 960, []byte{0x01}, bptr(tc.ext))
			frameBytes, ok := FrameFromRTP(p, audioLevelExtID)
			if !ok {
				t.Fatal("ok = false")
			}
			header, _, _ := wire.ParseFrame(frameBytes)
			if header.LevelDbov != tc.wantLevel {
				t.Errorf("LevelDbov = %d, want %d", header.LevelDbov, tc.wantLevel)
			}
			if header.IsDTX() != tc.wantDTX {
				t.Errorf("IsDTX() = %v, want %v", header.IsDTX(), tc.wantDTX)
			}
		})
	}
}

func TestAudioLevelAccessor(t *testing.T) {
	p := rtpPacket(t, 1, 960, []byte{0x01}, bptr(levelExt(true, 25)))

	level, voice, present := AudioLevel(p, audioLevelExtID)
	if !present || level != -25 || !voice {
		t.Errorf("AudioLevel = %d, %v, %v; want -25, true, true", level, voice, present)
	}

	if _, _, present := AudioLevel(p, 0); present {
		t.Error("present = true with extID 0")
	}
	if _, _, present := AudioLevel(nil, audioLevelExtID); present {
		t.Error("present = true for a nil packet")
	}
}

func TestFrameFromRTPRejectsNothingToCarry(t *testing.T) {
	if _, ok := FrameFromRTP(nil, audioLevelExtID); ok {
		t.Error("ok = true for a nil packet")
	}
	if _, ok := FrameFromRTP(rtpPacket(t, 1, 960, nil, nil), audioLevelExtID); ok {
		t.Error("ok = true for a packet with no payload")
	}
}

// A frame built from RTP must be acceptable to the room it is bound for,
// otherwise the relay would reject the browser's own audio.
func TestFrameFromRTPProducesAFrameTheRoomAccepts(t *testing.T) {
	p := rtpPacket(t, 7, 960, []byte{0xFC, 0x01}, bptr(levelExt(true, 20)))
	frameBytes, ok := FrameFromRTP(p, audioLevelExtID)
	if !ok {
		t.Fatal("ok = false")
	}

	for _, version := range []uint8{2, 3} {
		if valid, reason := wire.ValidClientFrame(version, frameBytes); !valid {
			t.Errorf("v%d rejected the bridged frame: %s", version, reason)
		}
	}

	// And it really lands in a room, attributed to the publisher.
	r := room.New()
	publisher, _, err := r.AddPeer("browser", 3, room.NewChannelSink())
	if err != nil {
		t.Fatalf("AddPeer: %v", err)
	}
	listener := room.NewChannelSink()
	if _, _, err := r.AddPeer("ws-peer", 3, listener); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	if dropped := r.BroadcastFrame(publisher.ID, frameBytes); dropped != 0 {
		t.Fatalf("dropped = %d, want 0", dropped)
	}
	select {
	case relayed := <-listener.Audio():
		index, _, payload, parsed := wire.ParseRelayFrame(3, relayed)
		if !parsed {
			t.Fatalf("unparseable: %x", relayed)
		}
		if index != publisher.Index {
			t.Errorf("attributed to %d, want the publisher at %d", index, publisher.Index)
		}
		if !bytes.Equal(payload, frameBytes) {
			t.Errorf("payload = %x, want the bridged frame %x", payload, frameBytes)
		}
	default:
		t.Fatal("the WebSocket peer never received the browser's audio")
	}
}
