package climatrix

import (
	"bytes"
	"context"
	"net"
	"os/exec"
	"regexp"
	"strings"
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
	needsScript(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	line := shellQuote(append([]string{bin(t)}, args...))
	cmd := exec.CommandContext(ctx, "script", "-qfec", line, "/dev/null")
	cmd.Dir = e.Dir
	// TERM=screen: termenv skips its terminal colour queries, which script(1)
	// never answers (bubbletea v1 makes one at init; 5s each otherwise).
	cmd.Env = append(e.environ(), "TERM=screen")
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
	needsScript(t)
	line := shellQuote(append([]string{bin(t)}, args...))
	cmd := exec.Command("script", "-qfec", line, "/dev/null")
	cmd.Dir = e.Dir
	// TERM=screen: termenv skips its terminal colour queries, which script(1)
	// never answers (bubbletea v1 makes one at init; 5s each otherwise).
	cmd.Env = append(e.environ(), "TERM=screen")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
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
