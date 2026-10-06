// Package audio turns the Opus frames a huddle delivers into PCM and plays
// them.
//
// Decoding is pure Go (pion/opus) and builds on every target. Playing is not: the
// only maintained pure-Go output library, ebitengine/oto, needs cgo and ALSA on
// Linux, which ncli's CGO_ENABLED=0 release builds cannot have. So output lives
// behind the `huddleaudio` build tag and the default build gets a stub that says
// so. See player.go.
package audio

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ohstr/nmilat/huddle/wire"
	"github.com/pion/opus"
)

// The frame geometry is fixed by the huddle wire format, not chosen here.
const (
	SampleRate      = wire.SampleRate      // 48000
	Channels        = wire.Channels        // 1
	SamplesPerFrame = wire.SamplesPerFrame // 960, i.e. 20 ms

	// BytesPerFrame is one decoded frame as signed 16-bit little-endian PCM,
	// which is what pion/opus writes and what every sink here consumes.
	BytesPerFrame = SamplesPerFrame * 2
)

// ErrShortDecode reports that the decoder produced fewer samples than a full
// frame. It is separate from a decode failure: the payload was valid enough to
// decode, there was just less of it than a 20 ms frame.
var ErrShortDecode = errors.New("audio: decoder produced a short frame")

// Decoder decodes one peer's Opus stream.
//
// One per peer, never shared. An Opus decoder carries stream state across
// packets -- that is how it handles the 20 ms frames' inter-frame prediction --
// so feeding two speakers' packets to one decoder corrupts both of them.
type Decoder struct {
	dec opus.Decoder
	pcm []byte
	out []int16
}

// NewDecoder returns a decoder configured for the huddle frame geometry.
func NewDecoder() (*Decoder, error) {
	dec, err := opus.NewDecoderWithOutput(SampleRate, Channels)
	if err != nil {
		return nil, fmt.Errorf("audio: decoder: %w", err)
	}
	return &Decoder{
		dec: dec,
		pcm: make([]byte, BytesPerFrame),
		out: make([]int16, SamplesPerFrame),
	}, nil
}

// Decode turns one Opus payload into PCM samples.
//
// The returned slice is reused on the next call, so a caller keeping it must
// copy. That mirrors client.Frame.Opus and keeps a 50-per-second hot path
// from allocating.
func (d *Decoder) Decode(payload []byte) ([]int16, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	if _, _, err := d.dec.Decode(payload, d.pcm); err != nil {
		return nil, fmt.Errorf("audio: decode: %w", err)
	}
	for i := range d.out {
		// Little-endian, matching what pion/opus writes.
		d.out[i] = int16(uint16(d.pcm[2*i]) | uint16(d.pcm[2*i+1])<<8)
	}
	return d.out, nil
}

// Mixer decodes every speaker and sums them into one stream.
//
// A call has many speakers but one pair of ears: the frames have to be summed,
// not played one after another, or two people talking would arrive as alternating
// bursts instead of a conversation.
type Mixer struct {
	mu       sync.Mutex
	decoders map[string]*Decoder
	acc      []int32
	out      []int16
	mixed    int
}

// NewMixer returns an empty mixer.
func NewMixer() *Mixer {
	return &Mixer{
		decoders: make(map[string]*Decoder),
		acc:      make([]int32, SamplesPerFrame),
		out:      make([]int16, SamplesPerFrame),
	}
}

// Add decodes one peer's frame and accumulates it into the current mix.
//
// Accumulation is in int32 so summing several loud speakers cannot wrap before
// Drain clamps it. Summing in int16 would turn a loud moment into a loud click,
// which is far more noticeable than the clipping clamping produces.
func (m *Mixer) Add(pubkey string, payload []byte) error {
	if len(payload) == 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	dec, ok := m.decoders[pubkey]
	if !ok {
		var err error
		dec, err = NewDecoder()
		if err != nil {
			return err
		}
		m.decoders[pubkey] = dec
	}

	samples, err := dec.Decode(payload)
	if err != nil {
		return err
	}
	if len(samples) == 0 {
		return nil
	}
	if len(samples) < len(m.acc) {
		return ErrShortDecode
	}
	for i := range m.acc {
		m.acc[i] += int32(samples[i])
	}
	m.mixed++
	return nil
}

// Drain returns the accumulated mix and resets it, clamping to int16.
//
// The returned slice is reused on the next call. mixed reports how many frames
// went into it, so a caller can tell silence from nobody having spoken.
func (m *Mixer) Drain() (pcm []int16, mixed int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, v := range m.acc {
		switch {
		case v > 32767:
			m.out[i] = 32767
		case v < -32768:
			m.out[i] = -32768
		default:
			m.out[i] = int16(v)
		}
		m.acc[i] = 0
	}
	mixed = m.mixed
	m.mixed = 0
	return m.out, mixed
}

// Forget drops a peer's decoder, for when they leave. Keeping it would hold a
// decoder per person who has ever spoken in the room.
func (m *Mixer) Forget(pubkey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.decoders, pubkey)
}

// Speakers is how many peers the mixer is holding a decoder for.
func (m *Mixer) Speakers() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.decoders)
}
