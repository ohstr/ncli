package ncli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
)

func newTestPingCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "ping"}
	cmd.Flags().StringP("targets", "t", "", "")
	cmd.Flags().Duration("timeout", 30*time.Second, "")
	return cmd
}

func TestPingArgs_NoArgsNoFlagsIsFine(t *testing.T) {
	// Unlike find, ping has no required identifier -- omitting positional
	// relays and --targets both is valid, it just falls back to prefs relays.
	cmd := newTestPingCmd()
	if err := pingCmd.Args(cmd, nil); err != nil {
		t.Fatalf("Args(no args, no flags) error = %v, want nil", err)
	}
}

func TestPingArgs_PositionalRelaysIsFine(t *testing.T) {
	cmd := newTestPingCmd()
	if err := pingCmd.Args(cmd, []string{"relay.primal.net", "wss://relay.damus.io"}); err != nil {
		t.Fatalf("Args(positional relays) error = %v, want nil", err)
	}
}

func TestPingArgs_TargetsWithPositionalRelaysIsMutuallyExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "targets.yaml")
	if err := os.WriteFile(path, []byte("kind: targets\nspec:\n  relays: []\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := newTestPingCmd()
	mustSet(t, cmd, "targets", path)
	if err := pingCmd.Args(cmd, []string{"relay.primal.net"}); err == nil {
		t.Fatal("Args(--targets + positional relay) error = nil, want a mutual-exclusion error")
	}
}

func TestPingArgs_TargetsMustExist(t *testing.T) {
	cmd := newTestPingCmd()
	mustSet(t, cmd, "targets", filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err := pingCmd.Args(cmd, nil); err == nil {
		t.Fatal("Args(--targets pointing at a missing file) error = nil, want an error")
	}
}

// TestPingPositionalRelaysToReport exercises the same
// client.TargetsFromRelayList -> client.Ping composition RunE uses for
// positional relay arguments (ncli ping relay.primal.net ...), against one
// live (mock) relay and one that can never be reached. RunE turns
// report.AllReachable() == false into a non-nil error via
// common.RuntimeError(cmd, fmt.Errorf(...)) -- deliberately not
// common.RuntimeError(cmd, nil), since wrapCLIError (cli/common/errors.go)
// turns a nil err into a nil return, which would make an all-unreachable
// ping silently report success.
func TestPingPositionalRelaysToReport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.Close()
	}))
	defer server.Close()
	wsURL := "ws" + server.URL[len("http"):]

	args := []string{wsURL, "ws://127.0.0.1:1"}

	targetsSpec, err := client.TargetsFromRelayList(args)
	if err != nil {
		t.Fatalf("TargetsFromRelayList() error = %v", err)
	}

	report := client.Ping(context.Background(), targetsSpec, client.PingOptions{Quiet: true, Timeout: 2 * time.Second})

	if report.AllReachable() {
		t.Fatal("report.AllReachable() = true, want false (one relay is unreachable)")
	}
	if report.Reachable != 1 || report.Unreachable != 1 {
		t.Fatalf("Reachable=%d Unreachable=%d, want 1/1", report.Reachable, report.Unreachable)
	}
}
