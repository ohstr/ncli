package ncli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

func newTestPublishCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "publish"}
	cmd.Flags().StringP("events", "e", "", "")
	cmd.Flags().StringP("relays", "s", "", "")
	return cmd
}

func TestPublishArgs_RequiresEvents(t *testing.T) {
	cmd := newTestPublishCmd()
	if err := publishCmd.Args(cmd, nil); err == nil {
		t.Fatal("Args(no --events) error = nil, want an error")
	}
}

func TestPublishArgs_RejectsBadExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.yaml")
	if err := os.WriteFile(path, []byte("[]"), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := newTestPublishCmd()
	mustSet(t, cmd, "events", path)
	if err := publishCmd.Args(cmd, nil); err == nil {
		t.Fatal("Args(--events with a .yaml extension) error = nil, want an error (.json/.jsonp only)")
	}
}

func TestPublishArgs_MissingFileErrors(t *testing.T) {
	cmd := newTestPublishCmd()
	mustSet(t, cmd, "events", filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err := publishCmd.Args(cmd, nil); err == nil {
		t.Fatal("Args(--events pointing at a missing file) error = nil, want an error")
	}
}

// mockRelayServer starts a minimal websocket relay that accepts (or
// rejects) every EVENT frame it receives, for ncli publish's process-level
// tests -- a small local duplicate of client package's own (unexported,
// so not importable here) test helper of the same shape.
func mockRelayServer(t *testing.T, accept bool) (wsURL string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			s := string(msg)
			const marker = "\"id\":\""
			idStart := -1
			for i := 0; i < len(s)-len(marker); i++ {
				if s[i:i+len(marker)] == marker {
					idStart = i + len(marker)
					break
				}
			}
			if idStart == -1 {
				continue
			}
			idEnd := idStart
			for idEnd < len(s) && s[idEnd] != '"' {
				idEnd++
			}
			id := s[idStart:idEnd]

			if accept {
				conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`["OK", "%s", true, ""]`, id)))
			} else {
				conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`["OK", "%s", false, "rejected"]`, id)))
			}
		}
	}))
	t.Cleanup(server.Close)

	u, _ := url.Parse(server.URL)
	u.Scheme = "ws"
	return u.String()
}

// TestPublishAcceptedExitsZero proves `ncli publish` exits 0 and reports a
// fully-succeeded report when the relay accepts the event.
func TestPublishAcceptedExitsZero(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	relayURL := mockRelayServer(t, true)

	dir := t.TempDir()
	eventsPath := filepath.Join(dir, "signed.json")
	if err := os.WriteFile(eventsPath, []byte(testMinerEventYAML), 0644); err != nil {
		t.Fatal(err)
	}
	// testMinerEventYAML is YAML; publish only accepts JSON -- reuse the
	// same fixture content but written out as a single JSON event object.
	minedPath := filepath.Join(dir, "mined.json")
	if _, err := exec.Command(bin, "miner", "mine", "-e", eventsPath, "-o", minedPath, "-d", "4").CombinedOutput(); err != nil {
		t.Fatalf("failed to prepare a fixture event: %v", err)
	}

	cmd := exec.Command(bin, "publish", "-e", minedPath, "-s", relayURL, "--json")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ncli publish failed: %v\nstderr: %s", err, exitErrStderr(err))
	}

	var report struct {
		Attempted int `json:"attempted"`
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\nraw: %s", err, out)
	}
	if report.Attempted != 1 || report.Succeeded != 1 || report.Failed != 0 {
		t.Fatalf("expected attempted=1 succeeded=1 failed=0, got %+v", report)
	}
}

// TestPublishTextModeResultOnStdout is the regression test for publish's
// text-mode (non --json) result: it used to be emitted entirely via
// log.Info/log.Error (stderr), leaving stdout empty on success -- breaking
// the same "stdout is only ever the clean result" contract every other
// command follows (see AGENTS.md).
func TestPublishTextModeResultOnStdout(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	relayURL := mockRelayServer(t, true)

	dir := t.TempDir()
	eventsPath := filepath.Join(dir, "signed.json")
	if err := os.WriteFile(eventsPath, []byte(testMinerEventYAML), 0644); err != nil {
		t.Fatal(err)
	}
	minedPath := filepath.Join(dir, "mined.json")
	if _, err := exec.Command(bin, "miner", "mine", "-e", eventsPath, "-o", minedPath, "-d", "4").CombinedOutput(); err != nil {
		t.Fatalf("failed to prepare a fixture event: %v", err)
	}

	cmd := exec.Command(bin, "publish", "-e", minedPath, "-s", relayURL)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ncli publish failed: %v\nstderr: %s", err, exitErrStderr(err))
	}
	if !strings.Contains(string(out), "published ") {
		t.Errorf("expected a \"published <id> to <relay>\" line on stdout, got: %q", out)
	}
	if !strings.Contains(string(out), "attempted 1, succeeded 1, failed 0") {
		t.Errorf("expected the summary line on stdout, got: %q", out)
	}
}

// TestPublishRejectedExitsNonZero proves `ncli publish` exits non-zero when
// the relay rejects the event, matching miner check's exit-code contract.
func TestPublishRejectedExitsNonZero(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	relayURL := mockRelayServer(t, false)

	dir := t.TempDir()
	eventsPath := filepath.Join(dir, "signed.json")
	if err := os.WriteFile(eventsPath, []byte(testMinerEventYAML), 0644); err != nil {
		t.Fatal(err)
	}
	minedPath := filepath.Join(dir, "mined.json")
	if _, err := exec.Command(bin, "miner", "mine", "-e", eventsPath, "-o", minedPath, "-d", "4").CombinedOutput(); err != nil {
		t.Fatalf("failed to prepare a fixture event: %v", err)
	}

	cmd := exec.Command(bin, "publish", "-e", minedPath, "-s", relayURL)
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected a *exec.ExitError (non-zero exit) when the relay rejects, got %v", err)
	}
	if exitErr.ExitCode() == 0 {
		t.Fatal("expected a non-zero exit code when the relay rejects the event")
	}
}
