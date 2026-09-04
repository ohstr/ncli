package bunker

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/atotto/clipboard"
)

// copyToClipboard best-effort copies text via two independent mechanisms,
// since neither is reliably available everywhere:
//
//   - OSC 52, a terminal escape sequence most modern terminal emulators
//     (iTerm2, kitty, alacritty, WezTerm, Windows Terminal, foot, ...)
//     intercept and forward to the *local* clipboard -- crucially, this
//     works over SSH and through tmux without any clipboard tool
//     installed on the remote host, which is the common case for a
//     headless bunker daemon.
//   - atotto/clipboard's native OS integration (xclip/xsel/wl-copy on
//     Linux, pbcopy on macOS, the Windows clipboard API) -- covers a
//     local desktop session whose terminal doesn't support OSC 52.
//
// Neither failure is reported as an error: OSC 52 has no acknowledgment
// at all (an unsupporting terminal just silently ignores the escape
// sequence), so there's nothing meaningful to check. Callers should
// always also show the text plainly and selectably as the guaranteed
// fallback (see board.go's showBunkerURI) -- this is a convenience, not
// the only way to get the text out.
func copyToClipboard(text string) {
	writeOSC52(text)
	_ = clipboard.WriteAll(text) // best-effort; silently no-ops without xclip/xsel/pbcopy/etc.
}

// writeOSC52 writes text as an OSC 52 "set clipboard" escape sequence
// directly to stdout -- safe to do while tview/tcell owns the terminal:
// the sequence is a private escape code the terminal intercepts and never
// renders, so it doesn't corrupt whatever's currently on screen. Writing
// to stdout (rather than hunting for tcell's own internal tty handle,
// which isn't exposed through its public Screen interface) is safe here
// specifically because bunker's TUI only ever starts once
// requireInteractive has already confirmed stdout is a real terminal --
// there's no redirection case to worry about.
func writeOSC52(text string) {
	payload := base64.StdEncoding.EncodeToString([]byte(text))
	seq := fmt.Sprintf("\x1b]52;c;%s\x07", payload)

	if os.Getenv("TMUX") != "" {
		// tmux swallows an OSC sequence from the program it's running
		// unless wrapped in its own DCS passthrough, with every ESC byte
		// inside doubled first -- a bare ESC would otherwise be read as
		// ending the passthrough early.
		escaped := strings.ReplaceAll(seq, "\x1b", "\x1b\x1b")
		seq = "\x1bPtmux;\x1b" + escaped + "\x1b\\"
	}

	_, _ = os.Stdout.WriteString(seq)
}
