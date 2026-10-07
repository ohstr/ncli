package huddle

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/ncli/huddle/client"
	"github.com/ohstr/nmilat/huddle/wire"
)

// recordingPlayer stands in for a sound card. It is the only way to assert on any
// of this without one.
type recordingPlayer struct {
	mu      sync.Mutex
	writes  int
	samples int
	closed  bool
	err     error
}

func (p *recordingPlayer) Write(pcm []int16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.writes++
	p.samples += len(pcm)
	return nil
}

func (p *recordingPlayer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

func (p *recordingPlayer) count() (writes, samples int, closed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writes, p.samples, p.closed
}

func TestAudioPumpWritesAtFrameCadence(t *testing.T) {
	player := &recordingPlayer{}
	pump := newAudioPump(player)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); pump.run(ctx) }()

	// Silence is written too: an unfed device underruns, which is audible.
	// Nobody has spoken here, so every write is silence.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	writes, samples, _ := player.count()
	if writes == 0 {
		t.Fatal("the pump never wrote to the player, so a silent call would underrun")
	}
	if want := writes * wire.SamplesPerFrame; samples != want {
		t.Errorf("wrote %d samples over %d writes, want %d -- each write should be one frame",
			samples, writes, want)
	}
	// ~200 ms at 20 ms per write is about 10. Generous bounds: this is a real
	// ticker on a shared machine, not a fake clock.
	if writes < 3 || writes > 40 {
		t.Errorf("wrote %d frames in 200ms, want roughly 10", writes)
	}
}

func TestAudioPumpSkipsUnattributedFrames(t *testing.T) {
	pump := newAudioPump(&recordingPlayer{})

	// No pubkey means no decoder can be chosen. Guessing would feed one speaker's
	// stream into another's decoder and corrupt both.
	pump.add(client.Frame{
		Header:     wire.FrameHeader{},
		Opus:       []byte{0x01, 0x02, 0x03},
		Attributed: false,
	})
	if got := pump.mixer.Speakers(); got != 0 {
		t.Errorf("an unattributed frame created %d decoder(s), want 0", got)
	}
	if got := pump.decodeErrors.Load(); got != 0 {
		t.Errorf("an unattributed frame was decoded anyway (%d errors)", got)
	}

	// An empty payload is a header-only frame, also nothing to decode.
	pump.add(client.Frame{Author: client.Peer{Pubkey: "alice"}, Attributed: true})
	if got := pump.mixer.Speakers(); got != 0 {
		t.Errorf("an empty payload created %d decoder(s), want 0", got)
	}
}

func TestAudioPumpSurvivesAnUndecodableFrame(t *testing.T) {
	pump := newAudioPump(&recordingPlayer{})

	// One peer sending something unplayable must not end the call for anyone.
	pump.add(client.Frame{
		Author:     client.Peer{Pubkey: "alice"},
		Opus:       []byte{0xFF, 0xFF, 0xFF, 0xFF},
		Attributed: true,
	})
	if got := pump.decodeErrors.Load(); got != 1 {
		t.Errorf("decodeErrors = %d, want 1 -- a bad frame should be counted, not fatal", got)
	}

	// And the pump still runs afterwards.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	pump.run(ctx)
}

func TestAudioPumpStopsWhenThePlayerFails(t *testing.T) {
	player := &recordingPlayer{err: errors.New("device went away")}
	pump := newAudioPump(player)

	// A dead device must end the pump rather than spin forever writing into it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); pump.run(ctx) }()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the pump kept going after the player started failing")
	}
}

func TestBoardWithAudioClosesThePlayerOnLeave(t *testing.T) {
	fc := newFakeClient()
	b := NewBoard(nil, fc, "room-1")
	player := &recordingPlayer{}
	b.PlayAudio(player)

	b.Leave()
	if _, _, closed := player.count(); !closed {
		t.Error("leaving the call did not close the audio device")
	}
	// Idempotent, like the client close it sits beside.
	b.Leave()
}

func TestBoardStatusSaysWhetherAudioIsPlaying(t *testing.T) {
	fc := newFakeClient()

	silent := NewBoard(nil, fc, "room-1")
	if got := silent.statusLine(2); !strings.Contains(got, "watching only") {
		t.Errorf("a board without audio should say so, got %q", got)
	}

	loud := NewBoard(nil, fc, "room-1")
	loud.PlayAudio(&recordingPlayer{})
	if got := loud.statusLine(2); !strings.Contains(got, "playing audio") {
		t.Errorf("a board with audio should say so, got %q", got)
	}
}

func TestBoardForgetsADepartedPeersDecoder(t *testing.T) {
	c := newClock()
	r := newRoster("me", SpeakingHold, testThreshold, c.now)
	r.sync(peers("me", 0, "abe", 1))

	// sync reports departures so anything holding per-peer state -- an Opus
	// decoder here -- can drop it instead of accumulating one per person who has
	// ever been in the room.
	departed := r.sync(peers("me", 0))
	if len(departed) != 0 {
		// abe never spoke, so there is no level entry to notice him leaving by.
		t.Logf("departed (no audio seen from abe): %v", departed)
	}

	r.sync(peers("me", 0, "abe", 1))
	r.heardFrame(frame("abe", -20))
	departed = r.sync(peers("me", 0))
	if len(departed) != 1 || departed[0] != "abe" {
		t.Errorf("departed = %v, want [abe]", departed)
	}
}
