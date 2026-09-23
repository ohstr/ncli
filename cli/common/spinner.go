package common

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

// eraseLine returns the cursor to column 0 and clears to end of line -- how
// an in-flight spinner frame is wiped before anything else prints.
const eraseLine = "\r\x1b[K"

// spinnerFrames animates WithSpinner's "waiting on the network" indicator.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerState is the process-wide handle on whichever spinner is currently
// drawing. Every write to the spinner's stream goes through withStderrLock,
// including zerolog's console output (see spinnerSafeWriter), so a log line
// arriving mid-spin erases the frame, prints on a clean line, and lets the
// next tick redraw underneath it -- instead of the two interleaving into an
// unreadable smear.
var spinnerState struct {
	mu     sync.Mutex
	active bool
	out    io.Writer
}

// withStderrLock runs fn with exclusive access to the spinner's line, wiping
// an in-flight frame first so fn starts on a clean one.
func withStderrLock(fn func()) {
	spinnerState.mu.Lock()
	defer spinnerState.mu.Unlock()
	if spinnerState.active && spinnerState.out != nil {
		fmt.Fprint(spinnerState.out, eraseLine)
	}
	fn()
}

// spinnerSafeWriter serializes a log writer against the spinner. Wrapping
// zerolog's console destination in one is what keeps ncli's existing
// per-relay narration ("querying relay.example.com") visible while a spinner
// runs, rather than having to suppress it for the spinner's benefit.
type spinnerSafeWriter struct{ w io.Writer }

func (s spinnerSafeWriter) Write(p []byte) (n int, err error) {
	withStderrLock(func() { n, err = s.w.Write(p) })
	return n, err
}

// WithSpinner runs fn while animating message on stderr -- the shared
// "waiting on a network call" indicator, so every command that blocks on one
// looks the same instead of each inventing its own.
//
// It animates only when stderr is a real terminal and the caller isn't a
// script: --json and -q/--quiet both switch it off, as does NO_COLOR or a
// redirected/piped stderr (via isColorTerminal), because the \r-and-erase
// frames are just noise in a log file. Commands that hand the terminal to a
// TUI must not call this at all.
func WithSpinner(cmd *cobra.Command, message string, fn func() error) error {
	return withSpinner(os.Stderr, SpinnerEnabled(cmd), message, fn)
}

// SpinnerEnabled reports whether cmd should animate. Exported so a command
// that renders its own progress can make the same decision the same way.
func SpinnerEnabled(cmd *cobra.Command) bool {
	if cmd != nil {
		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			return false
		}
		if quiet, _ := cmd.Flags().GetBool("quiet"); quiet {
			return false
		}
	}
	return isColorTerminal(os.Stderr)
}

// withSpinner is WithSpinner with its output and on/off decision injected, so
// the animation's own behaviour (frames written, goroutine stopped before
// return) stays unit-testable without a real terminal.
func withSpinner(w io.Writer, animate bool, message string, fn func() error) error {
	// Never stack two spinners: an outer one is already drawing, and a second
	// would fight it for the same line.
	spinnerState.mu.Lock()
	busy := spinnerState.active
	spinnerState.mu.Unlock()
	if !animate || busy {
		return fn()
	}

	spinnerState.mu.Lock()
	spinnerState.active = true
	spinnerState.out = w
	spinnerState.mu.Unlock()

	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		frame := 0
		draw := func() {
			withStderrLock(func() {
				fmt.Fprintf(w, "\r%s %s", spinnerFrames[frame%len(spinnerFrames)], message)
			})
		}
		draw()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				frame++
				draw()
			}
		}
	}()

	err := fn()

	// Stop the goroutine *and wait for it* before clearing, so its last frame
	// can never land after the line is wiped -- otherwise a stray frame would
	// sit above whatever the caller (or EmitError) prints next.
	close(stop)
	<-stopped

	spinnerState.mu.Lock()
	spinnerState.active = false
	spinnerState.out = nil
	fmt.Fprint(w, eraseLine)
	spinnerState.mu.Unlock()

	return err
}
