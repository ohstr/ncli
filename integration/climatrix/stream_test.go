package climatrix

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// streamProc is a running streaming command (find -f, dm read -f, ...).
type streamProc struct {
	cmd    *exec.Cmd
	lines  chan string
	stderr *syncBuffer
	once   sync.Once
	done   chan struct{}
	code   int
}

// StartStream runs ncli args in the background and hands out its stdout
// lines. It is interrupted when the test ends.
func (e *Env) StartStream(t *testing.T, args ...string) *streamProc {
	t.Helper()
	cmd := exec.Command(bin(t), args...)
	cmd.Dir, cmd.Env = e.Dir, e.environ()
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &streamProc{cmd: cmd, lines: make(chan string, 100), stderr: &syncBuffer{}, done: make(chan struct{})}
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
		close(p.lines)
		_ = cmd.Wait()
		p.code = cmd.ProcessState.ExitCode()
		close(p.done)
	}()
	t.Cleanup(p.Stop)
	return p
}

// Next returns the next stdout line decoded into v, or fails after 20s.
func (p *streamProc) Next(t *testing.T, v any) {
	t.Helper()
	select {
	case line, ok := <-p.lines:
		if !ok {
			t.Fatalf("stream ended\nstderr:\n%s", p.stderr.String())
		}
		if err := json.Unmarshal([]byte(line), v); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("no line after 20s\nstderr:\n%s", p.stderr.String())
	}
}

// Quiet fails if a line arrives within d.
func (p *streamProc) Quiet(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case line, ok := <-p.lines:
		if ok {
			t.Fatalf("unexpected line %q", line)
		}
	case <-time.After(d):
	}
}

// Stop interrupts the process and returns its exit code.
func (p *streamProc) Stop() {
	p.once.Do(func() {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(os.Interrupt)
		}
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	})
}

// ExitCode stops p and returns how it exited.
func (p *streamProc) ExitCode() int {
	p.Stop()
	return p.code
}

func (p *streamProc) Stderr() string { return strings.TrimSpace(p.stderr.String()) }

// ExpectOK fails unless r exited 0.
func (r Result) ExpectOK(t *testing.T) Result {
	t.Helper()
	if r.Code != 0 {
		t.Fatalf("expected success\n%s", r)
	}
	return r
}
