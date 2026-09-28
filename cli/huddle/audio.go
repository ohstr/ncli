package huddle

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/ohstr/ncli/huddleaudio"
	"github.com/ohstr/ncli/huddleclient"
)

// audioPump turns the call into sound: it decodes and mixes every speaker, then
// writes the mix to a player at frame cadence.
//
// The mix is written on a ticker rather than on each inbound frame. Frames from
// several speakers arrive interleaved and slightly apart, so writing per frame
// would hand the device one speaker at a time instead of the sum, and a silent
// moment would starve it. A steady 20 ms write keeps the device fed and is what
// makes the result a conversation rather than alternating bursts.
type audioPump struct {
	mixer  *huddleaudio.Mixer
	player huddleaudio.Player

	// decodeErrors counts payloads the decoder rejected. A bad frame is dropped,
	// never fatal: one peer sending something unplayable must not end the call
	// for everyone.
	decodeErrors atomic.Int64
	written      atomic.Int64
}

func newAudioPump(player huddleaudio.Player) *audioPump {
	return &audioPump{mixer: huddleaudio.NewMixer(), player: player}
}

// add mixes one inbound frame. Unattributed frames are skipped: the mixer keys a
// decoder per peer, and guessing the peer would feed one speaker's stream into
// another's decoder, corrupting both.
func (a *audioPump) add(f huddleclient.Frame) {
	if !f.Attributed || len(f.Opus) == 0 {
		return
	}
	if err := a.mixer.Add(f.Author.Pubkey, f.Opus); err != nil {
		a.decodeErrors.Add(1)
	}
}

// forget drops a departed peer's decoder.
func (a *audioPump) forget(pubkey string) { a.mixer.Forget(pubkey) }

// run writes the mix until ctx is done.
func (a *audioPump) run(ctx context.Context) {
	ticker := time.NewTicker(frameInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pcm, _ := a.mixer.Drain()
			// Silence is written too, deliberately: a device left unfed
			// underruns, which is audible as a click.
			if err := a.player.Write(pcm); err != nil {
				return
			}
			a.written.Add(1)
		}
	}
}

func (a *audioPump) close() { _ = a.player.Close() }

// frameInterval is one huddle frame, 20 ms.
const frameInterval = 20 * time.Millisecond
