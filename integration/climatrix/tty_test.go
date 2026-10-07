package climatrix

import (
	"bytes"
	"context"
	"io"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Human mode: commands run on a real terminal via script(1), which is
// what decides colour, the spinner, and the TUIs.

func shellQuote(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

func needsScript(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script(1) not available")
	}
}

// RunTTY runs ncli on a pseudo-terminal; stdout and stderr arrive merged,
// as a person would see them.
func (e *Env) RunTTY(t *testing.T, args ...string) Result {
	t.Helper()
	return e.RunTTYKeys(t, nil, args...)
}

// ttyEnv is a realistic terminal. script(1) never answers terminal queries,
// so anything that waits on one shows up here as a slow command.
func (e *Env) ttyEnv() []string { return append(e.environ(), "TERM=xterm-256color") }

// RunTTYKeys is RunTTY typing keys, one chunk at a time, into the program.
func (e *Env) RunTTYKeys(t *testing.T, keys []string, args ...string) Result {
	t.Helper()
	needsScript(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// script(1)'s pty starts at 0x0 when its own stdin isn't a terminal; give it
	// a real size, as any terminal emulator would.
	line := "stty cols 120 rows 40 2>/dev/null; exec " + shellQuote(append([]string{bin(t)}, args...))
	cmd := exec.CommandContext(ctx, "script", "-qfec", line, "/dev/null")
	cmd.Dir = e.Dir
	cmd.Env = e.ttyEnv()
	if keys != nil {
		pr, pw := io.Pipe()
		cmd.Stdin = pr
		go func() {
			for _, k := range keys {
				time.Sleep(400 * time.Millisecond)
				_, _ = pw.Write([]byte(k))
			}
			time.Sleep(2 * time.Second)
			_ = pw.Close()
		}()
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	r := Result{Args: args, Stdout: out.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		r.Code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("script %v: %v", args, err)
	}
	return r
}

// StartTTY runs ncli on a pseudo-terminal in the background until the
// test ends.
func (e *Env) StartTTY(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd, _, _ := e.startTTY(t, args...)
	return cmd
}

// startTTY is StartTTY with the keyboard (stdin) and screen (merged
// output) handed back to the test.
func (e *Env) startTTY(t *testing.T, args ...string) (*exec.Cmd, io.Writer, *syncBuffer) {
	t.Helper()
	needsScript(t)
	// script(1)'s pty starts at 0x0 when its own stdin isn't a terminal; give it
	// a real size, as any terminal emulator would.
	line := "stty cols 120 rows 40 2>/dev/null; exec " + shellQuote(append([]string{bin(t)}, args...))
	cmd := exec.Command("script", "-qfec", line, "/dev/null")
	cmd.Dir = e.Dir
	cmd.Env = e.ttyEnv()
	pr, pw := io.Pipe()
	cmd.Stdin = pr
	screen := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = screen, screen
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pw.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd, pw, screen
}

// syncBuffer is a bytes.Buffer safe to read while a process writes it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// sgr matches a colour/style escape, not a terminal query.
var sgr = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestTTY_FailureShapes(t *testing.T) {
	e := NewEnv(t)

	bare := e.RunTTY(t, "decode")
	if bare.Code != 2 || !strings.Contains(bare.Stdout, "Usage:") || strings.Contains(bare.Stdout, "Error:") {
		t.Errorf("bare decode: want help alone, exit 2\n%s", bare)
	}
	wrong := e.RunTTY(t, "decode", "a", "b")
	if wrong.Code != 2 || !strings.Contains(wrong.Stdout, "Error:") || !strings.Contains(wrong.Stdout, "Usage:") {
		t.Errorf("decode a b: want Error: then help, exit 2\n%s", wrong)
	}
	if i, j := strings.Index(wrong.Stdout, "Error:"), strings.Index(wrong.Stdout, "Usage:"); i > j {
		t.Errorf("help printed before the error")
	}
	op := e.RunTTY(t, "ping")
	if op.Code != 4 || !strings.Contains(op.Stdout, "Error:") || strings.Contains(op.Stdout, "Usage:") {
		t.Errorf("ping with no relays: want Error: alone, exit 4\n%s", op)
	}
	root := e.RunTTY(t)
	if root.Code != 2 || !strings.Contains(root.Stdout, "Usage:") {
		t.Errorf("bare ncli: want help, exit 2\n%s", root)
	}
}

// escSeq matches CSI and OSC terminal sequences.
var escSeq = regexp.MustCompile("\x1b\\[[0-9;?<>=$ ]*[@-~]|\x1b\\][^\x07\x1b]*(\x07|\x1b\\\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78]")

// plain strips terminal control sequences, leaving the text a person saw.
func plain(s string) string {
	return strings.ReplaceAll(escSeq.ReplaceAllString(s, ""), "\r", "")
}

// Starting a command on a terminal must not wait on terminal queries the
// terminal may never answer (bubbletea v1 did, for 5s, on every command).
func TestTTY_StartupIsFast(t *testing.T) {
	e := NewEnv(t)
	bin(t)
	start := time.Now()
	e.RunTTY(t, "version")
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("ncli version on a terminal took %s", took.Round(100*time.Millisecond))
	}
}

// The id delegate wizard, driven by keystrokes: issuer, delegatee, the
// default kind, the default duration.
func TestTTY_DelegateWizard(t *testing.T) {
	e := NewEnv(t)
	alice, agent := A(t, "alice"), A(t, "agent")
	res := e.RunTTYKeys(t, []string{alice.PrivHex, "\r", agent.PrivHex, "\r", "\r", "\r", "q"}, "id", "delegate")
	out := plain(res.Stdout)
	if !strings.Contains(out, "Delegation Token Generated") {
		t.Fatalf("wizard didn't reach the result\n%s", out)
	}
	if !strings.Contains(out, agent.PubHex[:16]) || !strings.Contains(out, "kind=25521") {
		t.Errorf("result lacks the delegatee or the default kind\n%s", out)
	}
	if res.Code != 0 {
		t.Errorf("wizard exit %d", res.Code)
	}
}

func TestTTY_Color(t *testing.T) {
	e := NewEnv(t)
	tty := e.RunTTY(t, "decode", "a", "b")
	if !sgr.MatchString(tty.Stdout) {
		t.Errorf("Error: not coloured on a terminal\n%q", tty.Stdout)
	}

	e2 := NewEnv(t)
	e2.Setenv("NO_COLOR", "1")
	if res := e2.RunTTY(t, "decode", "a", "b"); sgr.MatchString(res.Stdout) {
		t.Errorf("colour with NO_COLOR set\n%q", res.Stdout)
	}
	if res := e.Run(t, "decode", "a", "b"); sgr.MatchString(res.Stderr) {
		t.Errorf("colour with stderr piped\n%q", res.Stderr)
	}
}

// blackhole accepts connections and never answers, so a query waits.
func blackhole(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	return "ws://" + l.Addr().String()
}

func TestTTY_Spinner(t *testing.T) {
	url := blackhole(t)
	spun := func(s string) bool { return strings.ContainsAny(s, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") }
	args := []string{"find", "--kinds", "1", "-s", url, "--timeout", "2s"}

	e := NewEnv(t)
	if res := e.RunTTY(t, args...); !spun(res.Stdout) {
		t.Errorf("no spinner while waiting on a terminal\n%q", res.Stdout)
	}
	for _, extra := range [][]string{{"-q"}, {"--json"}} {
		if res := e.RunTTY(t, append(args, extra...)...); spun(res.Stdout) {
			t.Errorf("spinner with %v", extra)
		}
	}
	e2 := NewEnv(t)
	e2.Setenv("NO_COLOR", "1")
	if res := e2.RunTTY(t, args...); spun(res.Stdout) {
		t.Errorf("spinner with NO_COLOR")
	}
	if res := e.Run(t, args...); spun(res.Stderr) {
		t.Errorf("spinner with stderr piped")
	}
}

func TestTTY_BunkerDaemon(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)
	alice := A(t, "alice")

	e.StartTTY(t, "bunker", "--identity", alice.Nsec, "--relay", r.URL)
	var st struct{ Running bool }
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if res := e.Run(t, "bunker", "status", "--json"); res.Code == 0 {
			res.JSON(t, &st)
			if st.Running {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !st.Running {
		t.Fatalf("bunker daemon never came up under a terminal")
	}

	var sessions any
	e.MustOK(t, "bunker", "sessions", "list", "--json").JSON(t, &sessions)
	var history any
	e.MustOK(t, "bunker", "history", "--json").JSON(t, &history)

	pk := A(t, "bob").PubHex
	e.Run(t, "bunker", "sessions", "grants", pk, "--json").ExpectErr(t, "not_found")
	e.Run(t, "bunker", "sessions", "revoke", pk, "--json").ExpectErr(t, "not_found")

	conn := e.MustOK(t, "bunker", "connect", "--json")
	if !strings.Contains(conn.Stdout, "bunker://") {
		t.Errorf("connect didn't print a bunker:// URI\n%s", conn)
	}
	e.Run(t, "bunker", "connect", "nostrconnect://garbage", "--json").ExpectErr(t, "invalid_input")

	e.MustOK(t, "bunker", "stop", "--json")
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		e.MustOK(t, "bunker", "status", "--json").JSON(t, &st)
		if !st.Running {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("bunker still running after stop")
}
