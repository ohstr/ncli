package huddlertp

import (
	"github.com/ohstr/nmilat/huddle/wire"
	"github.com/pion/rtp/v2"
)

// AudioLevelExtensionURI is the RTP header extension a WebRTC peer negotiates to
// report how loud each packet is (RFC 6464). Its id is assigned per session in
// the SDP, so a caller passes the negotiated id to FrameFromRTP rather than
// assuming one.
const AudioLevelExtensionURI = "urn:ietf:params:rtp-hdrext:ssrc-audio-level"

// FrameFromRTP builds the huddle frame carrying p's Opus payload -- the reverse
// of what Sink does, so a WebRTC publisher's audio can be broadcast to peers on
// the huddle WebSocket.
//
// audioLevelExtID is the negotiated RFC 6464 extension id, or 0 when the peer did
// not offer it. Without it there is no level to report, so the frame claims
// silence: the level is untrusted telemetry either way, and a receiver treats
// the floor as "no information" rather than as a loud signal.
//
// Three fields map across rather than being invented:
//
//   - Timestamp. RTP's Opus clock is 48 kHz and so is the huddle header's, so it
//     copies over unscaled.
//   - Sequence. RTP's sequence number is a sender-authored 16-bit counter that
//     wraps, which is exactly what the huddle header's is, so it is forwarded --
//     a gap from real packet loss stays visible instead of being smoothed over.
//   - DTX. RFC 6464's V bit reports whether the packet carries voice activity.
//     No voice is comfort noise, which is what the DTX flag means.
//
// ok is false when the packet carries no payload; there is no frame to make.
func FrameFromRTP(p *rtp.Packet, audioLevelExtID uint8) (frame []byte, ok bool) {
	if p == nil || len(p.Payload) == 0 {
		return nil, false
	}

	header := wire.FrameHeader{
		Seq:       p.SequenceNumber,
		Ts48k:     p.Timestamp,
		LevelDbov: wire.LevelSilenceFloor,
	}

	if level, voice, present := AudioLevel(p, audioLevelExtID); present {
		header.LevelDbov = level
		if !voice {
			header.Flags |= wire.FlagDTX
		}
	}

	return wire.EncodeFrame(header, p.Payload), true
}

// AudioLevel reads the RFC 6464 audio-level extension from p. level is in dBov
// (negative, 0 loudest) and voice reports the V bit. present is false when the
// extension was not negotiated or not carried on this packet, which is normal:
// the extension is optional and a sender may omit it per-packet.
//
// The wire form is one byte: bit 7 is V, and bits 0-6 hold the level as a
// magnitude in -dBov, so 0 means 0 dBov and 127 means -127 dBov.
func AudioLevel(p *rtp.Packet, extID uint8) (level int8, voice bool, present bool) {
	if p == nil || extID == 0 {
		return 0, false, false
	}
	payload := p.GetExtension(extID)
	if len(payload) == 0 {
		return 0, false, false
	}
	b := payload[0]
	// Clamp through the same path a client-authored level takes, so an
	// out-of-range value cannot escape the canonical range here either.
	return wire.ClampLevel(-int8(b & 0x7F)), b&0x80 != 0, true
}
