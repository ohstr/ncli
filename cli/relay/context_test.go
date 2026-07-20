package relay

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildTestBinary builds the real ncli binary for this package's
// process-level tests -- the "which config file gets used" precedence
// being tested here is decided in cli/ncli.InitConfig (called from
// cmd/ncli/main.go's PersistentPreRun), not reachable by calling a RunE
// in-process against this package's own cobra tree alone.
func buildTestBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "ncli")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/ohstr/ncli/cmd/ncli")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build ncli binary: %v\n%s", err, out)
	}
	return bin
}

// contextTestEnv isolates a spawned ncli process's prefs.yaml (relay
// contexts live there) and $HOME (the config-discovery fallback) to fresh
// temp dirs, so these tests never touch the real user's config and never
// pick up a stray ncli.yaml/relay.yaml some other test left behind.
func contextTestEnv(t *testing.T) []string {
	t.Helper()
	env := os.Environ()
	env = append(env, "XDG_CONFIG_HOME="+t.TempDir())
	env = append(env, "HOME="+t.TempDir())
	return env
}

// TestRelayContextUseAppliesWithoutConfigFlag is the end-to-end regression
// test for the actual feature: once a context is added and selected via
// "use", a later "relay stats" invocation with no --config flag, run from
// a directory with no ncli.yaml/relay.yaml of its own, must still resolve
// to that context's config file -- proven here by a distinctive port
// number in the context's config surfacing in the connection-refused
// error, since nothing is actually listening on it.
func TestRelayContextUseAppliesWithoutConfigFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	env := contextTestEnv(t)
	cwd := t.TempDir() // deliberately has no ncli.yaml/relay.yaml

	ctxConfigPath := filepath.Join(t.TempDir(), "prod.yaml")
	const ctxConfig = `
nip11:
  privkey: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
store: "test.db"
port: 59999
`
	if err := os.WriteFile(ctxConfigPath, []byte(ctxConfig), 0644); err != nil {
		t.Fatalf("failed to write context config fixture: %v", err)
	}

	run := func(args ...string) ([]byte, error) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		cmd.Dir = cwd
		return cmd.Output()
	}

	if _, err := run("relay", "context", "add", "prod", ctxConfigPath); err != nil {
		t.Fatalf("relay context add failed: %v", err)
	}
	if _, err := run("relay", "context", "use", "prod"); err != nil {
		t.Fatalf("relay context use failed: %v", err)
	}

	out, err := run("relay", "stats", "--json")
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected relay stats to fail (nothing listening on 59999), got err=%v out=%s", err, out)
	}

	// stderr also carries the --json-mode "using config file" info log
	// line (see getAdminConfig) ahead of the final structured error -- the
	// error itself is always the last line.
	lines := strings.Split(strings.TrimSpace(string(exitErr.Stderr)), "\n")
	lastLine := lines[len(lines)-1]

	var payload struct {
		Error string `json:"error"`
		Code  string `json:"code"`
		Input string `json:"input"`
	}
	if jsonErr := json.Unmarshal([]byte(lastLine), &payload); jsonErr != nil {
		t.Fatalf("expected a structured JSON error as the last stderr line, got: %v\nraw: %s", jsonErr, exitErr.Stderr)
	}
	if payload.Code != "network" {
		t.Errorf("code = %q, want %q; payload=%+v", payload.Code, "network", payload)
	}
	if want := "localhost:59999"; !strings.Contains(payload.Input, want) && !strings.Contains(payload.Error, want) {
		t.Errorf("expected the context's port (59999) to appear in the error, got: %+v", payload)
	}
}

// TestRelayContextYieldsToLocalConfigFile locks in the precedence choice
// in cli/ncli/root.go's resolveConfigFile: a ncli.yaml/relay.yaml in the
// working directory always wins over a saved relay context, unchanged
// from the cwd-discovery behavior that existed before contexts did.
func TestRelayContextYieldsToLocalConfigFile(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	env := contextTestEnv(t)
	cwd := t.TempDir()

	ctxConfigPath := filepath.Join(t.TempDir(), "prod.yaml")
	if err := os.WriteFile(ctxConfigPath, []byte(`
nip11:
  privkey: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
store: "test.db"
port: 59999
`), 0644); err != nil {
		t.Fatalf("failed to write context config fixture: %v", err)
	}

	// The cwd's own config uses a different, equally distinctive port.
	if err := os.WriteFile(filepath.Join(cwd, "relay.yaml"), []byte(`
nip11:
  privkey: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
store: "test.db"
port: 59998
`), 0644); err != nil {
		t.Fatalf("failed to write cwd relay.yaml fixture: %v", err)
	}

	run := func(args ...string) ([]byte, error) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		cmd.Dir = cwd
		return cmd.Output()
	}

	if _, err := run("relay", "context", "add", "prod", ctxConfigPath); err != nil {
		t.Fatalf("relay context add failed: %v", err)
	}
	if _, err := run("relay", "context", "use", "prod"); err != nil {
		t.Fatalf("relay context use failed: %v", err)
	}

	out, err := run("relay", "stats", "--json")
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected relay stats to fail (nothing listening), got err=%v out=%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(exitErr.Stderr)), "\n")
	lastLine := lines[len(lines)-1]

	var payload struct {
		Input string `json:"input"`
	}
	if jsonErr := json.Unmarshal([]byte(lastLine), &payload); jsonErr != nil {
		t.Fatalf("expected a structured JSON error as the last stderr line, got: %v\nraw: %s", jsonErr, exitErr.Stderr)
	}
	if !strings.Contains(payload.Input, "59998") {
		t.Errorf("expected the cwd config's port (59998) to win over the context's (59999), got input=%q", payload.Input)
	}
}

// TestRelayContextListAndRemove exercises "context" (bare list), "add",
// "use", and "remove" end-to-end, including that removing the current
// context clears CurrentRelayContext rather than leaving it dangling.
func TestRelayContextListAndRemove(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	env := contextTestEnv(t)
	cwd := t.TempDir()

	ctxConfigPath := filepath.Join(t.TempDir(), "staging.yaml")
	if err := os.WriteFile(ctxConfigPath, []byte("store: test.db\n"), 0644); err != nil {
		t.Fatalf("failed to write context config fixture: %v", err)
	}

	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		cmd.Dir = cwd
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%v failed: %v", args, err)
		}
		return out
	}

	t.Run("bare list on a fresh prefs file", func(t *testing.T) {
		var got struct {
			Contexts map[string]string `json:"contexts"`
			Current  string            `json:"current"`
		}
		if err := json.Unmarshal(run("relay", "context", "--json"), &got); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if len(got.Contexts) != 0 || got.Current != "" {
			t.Errorf("got %+v, want empty", got)
		}
	})

	t.Run("add then use then list reflects current", func(t *testing.T) {
		var added struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		if err := json.Unmarshal(run("relay", "context", "add", "staging", ctxConfigPath, "--json"), &added); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if added.Name != "staging" || added.Path == "" {
			t.Errorf("add result = %+v, want name=staging and a non-empty path", added)
		}

		run("relay", "context", "use", "staging", "--json")

		var list struct {
			Contexts map[string]string `json:"contexts"`
			Current  string            `json:"current"`
		}
		if err := json.Unmarshal(run("relay", "context", "--json"), &list); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if list.Current != "staging" {
			t.Errorf("current = %q, want staging", list.Current)
		}
		if _, ok := list.Contexts["staging"]; !ok {
			t.Errorf("contexts = %v, want a staging entry", list.Contexts)
		}
	})

	t.Run("remove clears current", func(t *testing.T) {
		var removed struct {
			Removed bool `json:"removed"`
		}
		if err := json.Unmarshal(run("relay", "context", "remove", "staging", "--json"), &removed); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if !removed.Removed {
			t.Fatalf("removed = false, want true")
		}

		var list struct {
			Contexts map[string]string `json:"contexts"`
			Current  string            `json:"current"`
		}
		if err := json.Unmarshal(run("relay", "context", "--json"), &list); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if list.Current != "" {
			t.Errorf("current after removing it = %q, want empty", list.Current)
		}
		if len(list.Contexts) != 0 {
			t.Errorf("contexts after remove = %v, want empty", list.Contexts)
		}
	})
}
