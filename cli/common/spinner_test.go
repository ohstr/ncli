package common

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer safe to read while the spinner goroutine is
// still writing to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestWithSpinner_DisabledWritesNothing is the contract that keeps --json,
// -q and a piped stderr byte-identical to before: no frames, no escape
// codes, nothing at all.
func TestWithSpinner_DisabledWritesNothing(t *testing.T) {
	var buf syncBuffer
	ran := false

	err := withSpinner(&buf, false, "working", func() error {
		ran = true
		return nil
	})

	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ran {
		t.Error("fn must still run when the animation is off")
	}
	if got := buf.String(); got != "" {
		t.Errorf("wrote %q, want nothing when disabled", got)
	}
}

func TestWithSpinner_AnimatesAndClears(t *testing.T) {
	var buf syncBuffer

	err := withSpinner(&buf, true, "working", func() error {
		// Long enough for at least one tick past the initial frame.
		time.Sleep(250 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}

	got := buf.String()
	if !strings.Contains(got, "working") {
		t.Errorf("output missing the message, got %q", got)
	}
	if !strings.Contains(got, spinnerFrames[0]) {
		t.Errorf("output missing the first frame, got %q", got)
	}
	if !strings.HasSuffix(got, eraseLine) {
		t.Errorf("output must end by erasing the spinner line, got %q", got)
	}
}

// TestWithSpinner_StoppedBeforeReturn is what keeps a stray frame from
// landing on top of whatever EmitError prints next: once withSpinner
// returns, its goroutine is joined, so nothing more can arrive.
func TestWithSpinner_StoppedBeforeReturn(t *testing.T) {
	var buf syncBuffer

	_ = withSpinner(&buf, true, "working", func() error {
		time.Sleep(150 * time.Millisecond)
		return nil
	})

	settled := buf.String()
	time.Sleep(250 * time.Millisecond) // several ticks' worth
	if after := buf.String(); after != settled {
		t.Errorf("spinner wrote %d more bytes after returning; goroutine outlived the call",
			len(after)-len(settled))
	}
}

// TestWithSpinner_PropagatesError confirms the wrapper is transparent: the
// spinner must never swallow or replace what the work returned.
func TestWithSpinner_PropagatesError(t *testing.T) {
	var buf syncBuffer
	want := errSentinel{}

	if got := withSpinner(&buf, true, "working", func() error { return want }); got != error(want) {
		t.Errorf("err = %v, want the original %v", got, want)
	}
}

type errSentinel struct{}

func (errSentinel) Error() string { return "sentinel" }

// TestWithSpinner_NoNesting: an inner spinner must not fight the outer one
// for the same line.
func TestWithSpinner_NoNesting(t *testing.T) {
	var outer, inner syncBuffer

	_ = withSpinner(&outer, true, "outer", func() error {
		return withSpinner(&inner, true, "inner", func() error {
			time.Sleep(120 * time.Millisecond)
			return nil
		})
	})

	if got := inner.String(); got != "" {
		t.Errorf("inner spinner wrote %q, want nothing while an outer one is drawing", got)
	}
}
