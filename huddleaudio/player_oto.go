//go:build huddleaudio

package huddleaudio

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
)

const available = true

// otoPlayer plays PCM through the system's audio device.
//
// oto pulls from an io.Reader on its own goroutine rather than accepting pushed
// samples, so this holds a small ring of frames between Write and that reader.
// The ring is deliberately shallow: a deep one would add latency, which is the
// one thing a call cannot afford, so an overrun drops the oldest frame instead.
type otoPlayer struct {
	ctx    *oto.Context
	player *oto.Player

	mu     sync.Mutex
	buf    []byte
	closed bool
}

// playerRingBytes is four frames, i.e. 80 ms. Enough to absorb the scheduler
// without becoming audible latency of its own.
const playerRingBytes = 4 * BytesPerFrame

// NewPlayer opens the system audio device.
func NewPlayer() (Player, error) {
	ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
		SampleRate:   SampleRate,
		ChannelCount: Channels,
		Format:       oto.FormatSignedInt16LE,
	})
	if err != nil {
		return nil, fmt.Errorf("huddleaudio: opening the audio device: %w", err)
	}
	<-ready

	p := &otoPlayer{ctx: ctx, buf: make([]byte, 0, playerRingBytes)}
	p.player = ctx.NewPlayer(p)
	p.player.Play()
	return p, nil
}

// Read feeds oto's own goroutine. Underrun is silence, not an error: a gap in
// the call is not a failure of the device.
func (p *otoPlayer) Read(out []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return 0, io.EOF
	}
	n := copy(out, p.buf)
	p.buf = p.buf[:copy(p.buf, p.buf[n:])]
	for i := n; i < len(out); i++ {
		out[i] = 0
	}
	return len(out), nil
}

func (p *otoPlayer) Write(pcm []int16) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return ErrPlaybackUnavailable
	}
	need := len(pcm) * 2
	if len(p.buf)+need > playerRingBytes {
		// Drop the oldest rather than grow: latency matters more than every
		// sample surviving.
		drop := len(p.buf) + need - playerRingBytes
		if drop > len(p.buf) {
			drop = len(p.buf)
		}
		p.buf = p.buf[:copy(p.buf, p.buf[drop:])]
	}
	var scratch [2]byte
	for _, s := range pcm {
		binary.LittleEndian.PutUint16(scratch[:], uint16(s))
		p.buf = append(p.buf, scratch[0], scratch[1])
	}
	return nil
}

func (p *otoPlayer) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()

	// Give oto's reader a moment to see EOF before the context goes away.
	time.Sleep(10 * time.Millisecond)
	return p.player.Close()
}
