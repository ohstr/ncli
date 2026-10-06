//go:build !huddleaudio

package audio

import (
	"errors"
	"testing"
)

// TestDefaultBuildHasNoPlayer pins the default build's behaviour: it must say
// clearly that it was built without audio rather than fail obscurely at the first
// frame. The release binaries are all built this way.
func TestDefaultBuildHasNoPlayer(t *testing.T) {
	if Available() {
		t.Error("Available() is true in a build without the huddleaudio tag")
	}
	p, err := NewPlayer()
	if p != nil {
		t.Error("NewPlayer returned a player in a build without audio output")
	}
	if !errors.Is(err, ErrPlaybackUnavailable) {
		t.Errorf("NewPlayer error = %v, want ErrPlaybackUnavailable", err)
	}
	// The message has to tell an operator what to do about it.
	if got := err.Error(); !contains(got, "huddleaudio") {
		t.Errorf("the error should name the build tag, got %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
