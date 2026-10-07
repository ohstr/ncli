//go:build !huddleaudio

package audio

const available = false

// NewPlayer always fails in the default build. See ErrPlaybackUnavailable: the
// output backend needs cgo, which the release builds do not use.
func NewPlayer() (Player, error) { return nil, ErrPlaybackUnavailable }
