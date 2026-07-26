package bunker

import (
	"encoding/base64"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout redirects os.Stdout to a pipe for the duration of fn,
// returning everything written to it. writeOSC52 writes directly to
// os.Stdout (see its own doc comment for why), so this is the only way to
// observe its output without an actual terminal.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = original })

	fn()

	w.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWriteOSC52_PlainSequence(t *testing.T) {
	t.Setenv("TMUX", "")

	out := captureStdout(t, func() { writeOSC52("hello world") })

	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("hello world")) + "\x07"
	if out != want {
		t.Errorf("writeOSC52 output = %q, want %q", out, want)
	}
}

func TestWriteOSC52_TmuxPassthroughWrapping(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,12345,0")

	out := captureStdout(t, func() { writeOSC52("hello world") })

	inner := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("hello world")) + "\x07"
	wantInner := strings.ReplaceAll(inner, "\x1b", "\x1b\x1b")
	want := "\x1bPtmux;\x1b" + wantInner + "\x1b\\"

	if out != want {
		t.Errorf("writeOSC52 under tmux = %q, want %q", out, want)
	}
	if !strings.HasPrefix(out, "\x1bPtmux;") {
		t.Errorf("expected tmux DCS passthrough prefix, got %q", out)
	}
	if !strings.HasSuffix(out, "\x1b\\") {
		t.Errorf("expected tmux DCS passthrough terminator, got %q", out)
	}
}

func TestCopyToClipboard_DoesNotPanicWithoutAClipboardTool(t *testing.T) {
	// copyToClipboard must be safe to call even in this sandboxed test
	// environment, which has neither a real terminal (so OSC 52 is
	// harmless but pointless) nor xclip/xsel/pbcopy/clip.exe installed --
	// exactly the headless-daemon-over-SSH case the whole feature exists
	// for. Both failure modes must be silently absorbed, never panic or
	// propagate an error the caller would have to handle.
	captureStdout(t, func() { copyToClipboard("bunker://abc123?relay=wss://relay.example&secret=deadbeef") })
}
