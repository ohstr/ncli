package audio

import (
	"bytes"
	"embed"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/pion/opus/pkg/oggreader"
)

// tiny.ogg is pion/opus's own fixture, MIT-licensed (see testdata/
// tiny.ogg.license). Vendored rather than referenced from the module cache so
// these tests do not depend on a dependency's internal layout.
//
// It is one audio packet, which is thin -- enough to prove the wrapper decodes
// real Opus to real audio, not enough to be a conformance test. Decode
// correctness rests on pion/opus's own suite; what is tested here is this
// package's wrapping of it.
//
//go:embed testdata/tiny.ogg
var fixtures embed.FS

// realOpusPacket returns the fixture's single Opus audio packet.
func realOpusPacket(t *testing.T) []byte {
	t.Helper()
	raw, err := fixtures.ReadFile("testdata/tiny.ogg")
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	ogg, _, err := oggreader.NewWith(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("oggreader: %v", err)
	}
	for {
		segments, _, err := ogg.ParseNextPage()
		if errors.Is(err, io.EOF) {
			t.Fatal("the fixture contained no audio packet")
		}
		if err != nil {
			t.Fatalf("ParseNextPage: %v", err)
		}
		if len(segments) == 0 || bytes.HasPrefix(segments[0], []byte("OpusTags")) {
			continue
		}
		return segments[0]
	}
}

func peak(pcm []int16) int32 {
	var p int32
	for _, s := range pcm {
		v := int32(s)
		if v < 0 {
			v = -v
		}
		if v > p {
			p = v
		}
	}
	return p
}

func TestDecoderProducesRealAudio(t *testing.T) {
	d, err := NewDecoder()
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}

	pcm, err := d.Decode(realOpusPacket(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(pcm) != SamplesPerFrame {
		t.Fatalf("got %d samples, want a full %d-sample frame", len(pcm), SamplesPerFrame)
	}
	// Silence would also have the right length, so check it is actually signal.
	if p := peak(pcm); p == 0 {
		t.Fatal("decoded a full frame of silence from a real Opus packet")
	} else {
		t.Logf("decoded peak amplitude %d of 32767", p)
	}
}

func TestDecoderEdgeCases(t *testing.T) {
	d, err := NewDecoder()
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}

	// An empty payload is a legitimate no-op, not an error: a frame can carry a
	// header and no audio.
	pcm, err := d.Decode(nil)
	if err != nil || pcm != nil {
		t.Errorf("Decode(nil) = %v, %v; want nil, nil", pcm, err)
	}

	// Garbage must come back as an error rather than as noise a listener would
	// hear, and must not take the decoder down.
	if _, err := d.Decode([]byte{0xFF, 0xFF, 0xFF, 0xFF}); err == nil {
		t.Error("Decode accepted garbage without complaint")
	}
	// Still usable afterwards.
	if _, err := d.Decode(realOpusPacket(t)); err != nil {
		t.Errorf("decoder unusable after a bad payload: %v", err)
	}
}

func TestDecoderReusesItsBuffer(t *testing.T) {
	d, err := NewDecoder()
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	first, err := d.Decode(realOpusPacket(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	second, err := d.Decode(realOpusPacket(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	// Documented behaviour: the slice is reused, so a caller must copy. Pinning
	// it so the doc comment cannot quietly become false.
	if &first[0] != &second[0] {
		t.Error("Decode allocated a new buffer; the doc comment says it reuses one")
	}
}

func TestMixerGivesEachPeerItsOwnDecoder(t *testing.T) {
	m := NewMixer()
	packet := realOpusPacket(t)

	if err := m.Add("alice", packet); err != nil {
		t.Fatalf("Add(alice): %v", err)
	}
	if err := m.Add("bob", packet); err != nil {
		t.Fatalf("Add(bob): %v", err)
	}
	if got := m.Speakers(); got != 2 {
		t.Errorf("Speakers() = %d, want 2 -- an Opus decoder is stateful and cannot be shared", got)
	}

	m.Forget("alice")
	if got := m.Speakers(); got != 1 {
		t.Errorf("Speakers() after Forget = %d, want 1", got)
	}
}

func TestMixerSumsSpeakers(t *testing.T) {
	packet := realOpusPacket(t)

	// One speaker, as the baseline.
	single := NewMixer()
	if err := single.Add("alice", packet); err != nil {
		t.Fatalf("Add: %v", err)
	}
	one, mixed := single.Drain()
	if mixed != 1 {
		t.Fatalf("mixed = %d, want 1", mixed)
	}
	basePeak := peak(one)
	if basePeak == 0 {
		t.Fatal("baseline is silent")
	}

	// Two speakers sending the identical packet, each through their own fresh
	// decoder, must sum to twice the amplitude.
	double := NewMixer()
	if err := double.Add("alice", packet); err != nil {
		t.Fatalf("Add(alice): %v", err)
	}
	if err := double.Add("bob", packet); err != nil {
		t.Fatalf("Add(bob): %v", err)
	}
	two, mixed := double.Drain()
	if mixed != 2 {
		t.Fatalf("mixed = %d, want 2", mixed)
	}
	if got, want := peak(two), basePeak*2; got != want {
		t.Errorf("two identical speakers peaked at %d, want %d (sum, not replace)", got, want)
	}
}

func TestMixerDrainResets(t *testing.T) {
	m := NewMixer()
	if err := m.Add("alice", realOpusPacket(t)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, mixed := m.Drain(); mixed != 1 {
		t.Fatalf("first Drain mixed = %d, want 1", mixed)
	}

	// Nobody spoke since, so the mix must be empty rather than repeating the
	// previous frame -- which would be an audible stutter.
	pcm, mixed := m.Drain()
	if mixed != 0 {
		t.Errorf("second Drain mixed = %d, want 0", mixed)
	}
	if p := peak(pcm); p != 0 {
		t.Errorf("second Drain still had signal (peak %d); Drain must reset", p)
	}
}

func TestMixerClampsInsteadOfWrapping(t *testing.T) {
	packet := realOpusPacket(t)

	// The baseline, from a decoder of its own, so the expectation is computed
	// independently of the mixer under test.
	base, err := NewDecoder()
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	single, err := base.Decode(packet)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	reference := append([]int16(nil), single...)

	// Enough copies of a real frame to exceed int16 range. Wrapping would turn a
	// loud moment into a loud click -- and flip the sample's sign -- which is far
	// worse than clipping.
	const speakers = 32
	m := NewMixer()
	for i := 0; i < speakers; i++ {
		if err := m.Add(string(rune('a'+i)), packet); err != nil {
			t.Fatalf("Add(%d): %v", i, err)
		}
	}
	pcm, mixed := m.Drain()
	if mixed != speakers {
		t.Fatalf("mixed = %d, want %d", mixed, speakers)
	}

	// Compare against the sum computed here and clamped here. This is the whole
	// assertion: every sample must match, which fails if the mixer wraps (sign
	// flips), saturates the wrong way, or does not sum at all.
	clamped := 0
	for i := range reference {
		want := int32(reference[i]) * speakers
		switch {
		case want > 32767:
			want = 32767
			clamped++
		case want < -32768:
			want = -32768
			clamped++
		}
		if int32(pcm[i]) != want {
			t.Fatalf("sample %d = %d, want %d (reference %d x %d speakers)",
				i, pcm[i], want, reference[i], speakers)
		}
	}
	if clamped == 0 {
		t.Fatalf("this fixture never exceeded int16 range at %d speakers, so nothing was clamped and the test proves nothing", speakers)
	}
	t.Logf("%d of %d samples clamped, none wrapped", clamped, len(reference))
}

func TestMixerConcurrentUse(t *testing.T) {
	m := NewMixer()
	packet := realOpusPacket(t)

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				switch w {
				case 0, 1:
					_ = m.Add(string(rune('a'+w)), packet)
				case 2:
					m.Drain()
				case 3:
					_ = m.Speakers()
					m.Forget("nobody")
				}
			}
		}(w)
	}
	wg.Wait()
}
