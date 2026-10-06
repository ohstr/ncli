package audio

import "errors"

// ErrPlaybackUnavailable reports that this binary was built without audio
// output. It is not a failure to fix at runtime: the output backend needs cgo and
// ALSA on Linux, which the CGO_ENABLED=0 release builds cannot have, so it is
// compiled in only under the `huddleaudio` build tag.
var ErrPlaybackUnavailable = errors.New("audio: this build has no audio output (rebuild with -tags huddleaudio)")

// Player is an audio sink taking 16-bit mono PCM at SampleRate.
//
// An interface rather than a concrete type so the two build variants can differ
// and a test can substitute a recorder, which is the only way to assert on
// anything here without a sound card.
type Player interface {
	// Write plays one frame's worth of samples. It must not block for longer
	// than a frame: a huddle delivers 50 a second and blocking here would
	// back-pressure the reader that feeds it.
	Write(pcm []int16) error
	Close() error
}

// Available reports whether this build can play audio, so a caller can say so up
// front instead of failing at the first frame.
func Available() bool { return available }
