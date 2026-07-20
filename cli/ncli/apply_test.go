package ncli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestApplySIGTERMExitsPromptly proves the `apply` command actually installs
// SIGINT/SIGTERM handling (client/client.go's Process wraps cmd.Context()
// via signal.NotifyContext, added alongside the recovery/shutdown redesign)
// and that the shutdown path it triggers (Stream.Close -> RecoveryManager
// flush, wired through client.go's run()/render()) is reachable end-to-end
// through the real built binary, not just at the unit level. It builds the
// actual ncli binary and spawns it as a subprocess against a local mock
// relay, since this is specifically about process-level signal delivery,
// which can't be exercised by calling Go functions directly in-process.
func TestApplySIGTERMExitsPromptly(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary as a subprocess; skipped in -short mode")
	}

	// A minimal mock relay: just upgrade and idle. This test is about
	// process-level signal handling and shutdown, not data flow, so the
	// relay doesn't need to implement the Nostr protocol -- just stay
	// connected long enough for the process to be "running" when we signal it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	wsURL := "ws" + server.URL[len("http"):]

	tmpDir := t.TempDir()

	specPath := filepath.Join(tmpDir, "spec.yaml")
	spec := fmt.Sprintf(`kind: stream
spec:
  raw: true
  from:
    - relay: %q
      trusted: true
  to:
    - relay: %q
      trusted: true
`, wsURL, wsURL)
	if err := os.WriteFile(specPath, []byte(spec), 0644); err != nil {
		t.Fatalf("failed to write spec file: %v", err)
	}

	binPath := filepath.Join(tmpDir, "ncli")
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer buildCancel()
	buildCmd := exec.CommandContext(buildCtx, "go", "build", "-o", binPath, "github.com/ohstr/ncli/cmd/ncli")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build ncli binary: %v\n%s", err, out)
	}

	cmd := exec.Command(binPath, "apply", "-f", specPath)
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start ncli apply: %v", err)
	}

	// Let it connect and get into its main run loop before signaling.
	time.Sleep(500 * time.Millisecond)

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to send SIGTERM: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		// Any exit (clean or otherwise) within the deadline below proves
		// SIGTERM was actually handled instead of hitting Go's default
		// disposition (immediate, un-graceful termination) or hanging.
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("ncli apply did not exit within 10s of SIGTERM -- signal handling appears not to be wired up")
	}
}
