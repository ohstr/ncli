package relay

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// relayContextRunTestEnv is contextTestEnv's counterpart for tests that
// need to inspect what a spawned `ncli relay -c ...` actually wrote under
// XDG_CONFIG_HOME afterward (context_test.go's contextTestEnv returns only
// the env slice, not the path).
func relayContextRunTestEnv(t *testing.T) (env []string, xdgHome string) {
	t.Helper()
	xdgHome = t.TempDir()
	env = os.Environ()
	env = append(env, "XDG_CONFIG_HOME="+xdgHome)
	env = append(env, "HOME="+t.TempDir())
	env = append(env, "NCLI_VAULT_PASSWORD=test-password-123")
	return env, xdgHome
}

// waitForPort polls addr until something accepts a TCP connection, or
// fails the test after timeout -- the deterministic way to know a
// backgrounded `ncli relay` has actually finished starting up, without
// racing its log output.
func waitForPort(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("nothing accepted a connection on %s within %s", addr, timeout)
}

// runningRelay is a spawned, confirmed-listening `ncli relay` process.
// stop is idempotent-safe to call once and then again from t.Cleanup (a
// double-kill on an already-reaped process is harmless -- the error is
// discarded either way).
type runningRelay struct {
	cmd    *exec.Cmd
	Stderr *bytes.Buffer
}

func (r *runningRelay) stop() {
	_ = r.cmd.Process.Kill()
	_ = r.cmd.Wait()
}

// startRelay spawns `ncli relay` with args appended, waits for it to
// actually bind localhost:5500, and registers a t.Cleanup that stops it
// -- the common setup for every test below that needs a live server, not
// just the config/vault side effects of context creation. Callers that
// need to stop it before the test ends (to free the port for a second
// run) can call the returned runningRelay.stop() directly; the
// registered cleanup then becomes a no-op.
func startRelay(t *testing.T, bin string, env []string, cwd string, extraArgs ...string) *runningRelay {
	t.Helper()
	args := append([]string{"relay"}, extraArgs...)
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Dir = cwd
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start relay: %v", err)
	}
	r := &runningRelay{cmd: cmd, Stderr: &stderr}
	t.Cleanup(r.stop)
	waitForPort(t, "localhost:5500", 10*time.Second)
	return r
}

// TestRelayContextFlagCreatesAndRuns is the end-to-end regression test for
// `ncli relay -c <name>` against a name that isn't saved yet: it should
// generate a new identity, save it to the vault under a label matching
// the context name, write a minimal relay.yaml under
// common.AppConfigDir()/relays/<name>/, register the context in
// prefs.yaml, and then actually start serving on it -- all without a
// prompt, since --json never prompts.
func TestRelayContextFlagCreatesAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	env, xdgHome := relayContextRunTestEnv(t)
	cwd := t.TempDir()

	startRelay(t, bin, env, cwd, "-c", "newctx", "--json")

	relayDir := filepath.Join(xdgHome, ".ncli", "relays", "newctx")
	cfgPath := filepath.Join(relayDir, "relay.yaml")
	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", cfgPath, err)
	}
	cfg := string(cfgBytes)
	if !strings.Contains(cfg, `name: "newctx"`) {
		t.Errorf("relay.yaml missing nip11.name: %s", cfg)
	}
	if !strings.Contains(cfg, "privkey:") {
		t.Errorf("relay.yaml missing nip11.privkey: %s", cfg)
	}
	if !strings.Contains(cfg, filepath.Join(relayDir, "store.db")) {
		t.Errorf("relay.yaml missing store path under %s: %s", relayDir, cfg)
	}

	if _, err := os.Stat(filepath.Join(relayDir, "store.db")); err != nil {
		t.Errorf("expected store.db to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(relayDir, "logs")); err != nil {
		t.Errorf("expected logs dir to exist: %v", err)
	}

	vaultBytes, err := os.ReadFile(filepath.Join(xdgHome, ".ncli", "vault.yaml"))
	if err != nil {
		t.Fatalf("expected vault.yaml to exist: %v", err)
	}
	if !strings.Contains(string(vaultBytes), "label: newctx") {
		t.Errorf("expected vault.yaml to have an entry labeled newctx, got: %s", vaultBytes)
	}

	prefsBytes, err := os.ReadFile(filepath.Join(xdgHome, ".ncli", "prefs.yaml"))
	if err != nil {
		t.Fatalf("expected prefs.yaml to exist: %v", err)
	}
	if !strings.Contains(string(prefsBytes), "newctx: "+cfgPath) && !strings.Contains(string(prefsBytes), "newctx:") {
		t.Errorf("expected prefs.yaml to register the newctx context, got: %s", prefsBytes)
	}
}

// TestRelayContextFlagReusesExisting confirms a second `-c <name>` run
// against an already-saved context skips creation entirely: no second
// vault entry, no re-prompt (irrelevant here since --json never prompts
// anyway, but the config must resolve to the same relay.yaml either way).
func TestRelayContextFlagReusesExisting(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	env, xdgHome := relayContextRunTestEnv(t)
	cwd := t.TempDir()

	first := startRelay(t, bin, env, cwd, "-c", "reuseme", "--json")

	vaultBytes, err := os.ReadFile(filepath.Join(xdgHome, ".ncli", "vault.yaml"))
	if err != nil {
		t.Fatalf("expected vault.yaml to exist: %v", err)
	}
	firstCount := strings.Count(string(vaultBytes), "label:")
	if firstCount != 1 {
		t.Fatalf("expected exactly 1 vault entry after first run, got %d", firstCount)
	}

	// Stop the first server (its registered t.Cleanup becomes a harmless
	// no-op) so the second run can bind the same port.
	first.stop()
	waitPortFree(t, "localhost:5500", 5*time.Second)

	startRelay(t, bin, env, cwd, "-c", "reuseme", "--json")

	vaultBytes, err = os.ReadFile(filepath.Join(xdgHome, ".ncli", "vault.yaml"))
	if err != nil {
		t.Fatalf("expected vault.yaml to exist: %v", err)
	}
	secondCount := strings.Count(string(vaultBytes), "label:")
	if secondCount != 1 {
		t.Errorf("expected still exactly 1 vault entry after reusing the context, got %d", secondCount)
	}
}

// TestRelayContextFlagWithIdentitySkipsVault confirms --identity bypasses
// identity generation/vault-saving entirely: the given nsec's private key
// goes straight into the generated relay.yaml, and no vault.yaml is ever
// created.
func TestRelayContextFlagWithIdentitySkipsVault(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	env, xdgHome := relayContextRunTestEnv(t)
	cwd := t.TempDir()

	const nsec = "nsec1jxc4wrntfq52mrswj30dyqk6kaglca94cxuq96lhulq7zw2mzfqssquvt2"
	const privHex = "91b1570e6b4828ad8e0e945ed202dab751fc74b5c1b802ebf7e7c1e1395b1241"

	startRelay(t, bin, env, cwd, "-c", "withid", "--identity", nsec, "--json")

	cfgPath := filepath.Join(xdgHome, ".ncli", "relays", "withid", "relay.yaml")
	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", cfgPath, err)
	}
	if !strings.Contains(string(cfgBytes), privHex) {
		t.Errorf("expected relay.yaml to embed the --identity private key, got: %s", cfgBytes)
	}

	if _, err := os.Stat(filepath.Join(xdgHome, ".ncli", "vault.yaml")); err == nil {
		t.Error("expected no vault.yaml to be created when --identity is given")
	}
}

// TestRelayContextFlagMutuallyExclusiveWithConfig confirms --config and
// -c/--context together is a usage error (exit 2), not a silent
// last-flag-wins.
func TestRelayContextFlagMutuallyExclusiveWithConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	env, _ := relayContextRunTestEnv(t)
	cwd := t.TempDir()

	cfgPath := filepath.Join(cwd, "explicit.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
nip11:
  privkey: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
store: "test.db"
`), 0644); err != nil {
		t.Fatalf("failed to write config fixture: %v", err)
	}

	cmd := exec.Command(bin, "relay", "-c", "someone", "--config", cfgPath, "--json")
	cmd.Env = env
	cmd.Dir = cwd
	out, err := cmd.Output()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected a non-zero exit, got err=%v out=%s", err, out)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2 (usage)", exitErr.ExitCode())
	}

	var payload struct {
		Code string `json:"code"`
	}
	if jsonErr := json.Unmarshal(exitErr.Stderr, &payload); jsonErr != nil {
		t.Fatalf("expected structured JSON error, got: %v\nstderr: %s", jsonErr, exitErr.Stderr)
	}
	if payload.Code != "usage" {
		t.Errorf("code = %q, want %q", payload.Code, "usage")
	}
}

// TestRelayContextFlagRejectsUnsafeName confirms a context name that
// would escape or collide with its relays/ parent directory (a path
// separator, ".", "..") is rejected as invalid_input (exit 3) rather than
// silently used to build a filesystem path.
func TestRelayContextFlagRejectsUnsafeName(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	env, _ := relayContextRunTestEnv(t)
	cwd := t.TempDir()

	for _, name := range []string{"../evil", "a/b", "."} {
		cmd := exec.Command(bin, "relay", "-c", name, "--json")
		cmd.Env = env
		cmd.Dir = cwd
		out, err := cmd.Output()
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("name %q: expected a non-zero exit, got err=%v out=%s", name, err, out)
		}
		if exitErr.ExitCode() != 3 {
			t.Errorf("name %q: exit code = %d, want 3 (invalid_input)", name, exitErr.ExitCode())
		}
	}
}

// TestRelayContextFlagMalformedConfigIsInvalidInput confirms an existing,
// saved context whose config file fails to parse reports invalid_input
// (exit 3) -- the file's content is a malformed supplied value, the same
// classification initConfig's own viper.Unmarshal failure gets, not the
// generic internal/exit-1 fallback.
func TestRelayContextFlagMalformedConfigIsInvalidInput(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	env, xdgHome := relayContextRunTestEnv(t)
	cwd := t.TempDir()

	brokenPath := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(brokenPath, []byte("store: [unterminated"), 0644); err != nil {
		t.Fatalf("failed to write broken config fixture: %v", err)
	}
	prefsPath := filepath.Join(xdgHome, ".ncli", "prefs.yaml")
	if err := os.MkdirAll(filepath.Dir(prefsPath), 0755); err != nil {
		t.Fatalf("failed to create prefs dir: %v", err)
	}
	if err := os.WriteFile(prefsPath, []byte("relay_contexts:\n  broken: "+brokenPath+"\n"), 0644); err != nil {
		t.Fatalf("failed to seed prefs.yaml: %v", err)
	}

	cmd := exec.Command(bin, "relay", "-c", "broken", "--json")
	cmd.Env = env
	cmd.Dir = cwd
	out, err := cmd.Output()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected a non-zero exit, got err=%v out=%s", err, out)
	}
	if exitErr.ExitCode() != 3 {
		t.Errorf("exit code = %d, want 3 (invalid_input)", exitErr.ExitCode())
	}

	var payload struct {
		Code  string `json:"code"`
		Input string `json:"input"`
	}
	if jsonErr := json.Unmarshal(exitErr.Stderr, &payload); jsonErr != nil {
		t.Fatalf("expected structured JSON error, got: %v\nstderr: %s", jsonErr, exitErr.Stderr)
	}
	if payload.Code != "invalid_input" {
		t.Errorf("code = %q, want %q", payload.Code, "invalid_input")
	}
	if payload.Input != "broken" {
		t.Errorf("input = %q, want %q", payload.Input, "broken")
	}
}

// TestRelayContextFlagWarnsUnderQuietAndJSON confirms the "context does
// not exist" warning survives both -q/--quiet (which drops info-level
// narration but not warnings, per AGENTS.md's output conventions) and
// --json (as a JSON-formatted line, same as every other stderr log line)
// -- and that neither mode ever blocks on an interactive prompt.
func TestRelayContextFlagWarnsUnderQuietAndJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	env, _ := relayContextRunTestEnv(t)
	cwd := t.TempDir()

	r := startRelay(t, bin, env, cwd, "-c", "quietctx", "-q")
	if !strings.Contains(r.Stderr.String(), `relay context "quietctx" does not exist`) {
		t.Errorf("expected the does-not-exist warning to survive -q/--quiet, got stderr: %s", r.Stderr.String())
	}
	r.stop()
	waitPortFree(t, "localhost:5500", 5*time.Second)

	env2, _ := relayContextRunTestEnv(t)
	r2 := startRelay(t, bin, env2, cwd, "-c", "quietctx2", "--json")
	if !strings.Contains(r2.Stderr.String(), `"level":"warn"`) || !strings.Contains(r2.Stderr.String(), `quietctx2`) {
		t.Errorf("expected a JSON-formatted warning under --json, got stderr: %s", r2.Stderr.String())
	}
}

// waitPortFree polls addr until nothing accepts a connection anymore, or
// fails the test after timeout -- used between two sequential live-server
// subtests that both need the same default port.
func waitPortFree(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s was still accepting connections after %s", addr, timeout)
}
