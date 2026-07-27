package ncli

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// vaultTestEnv is prefsTestEnv's fresh-XDG_CONFIG_HOME isolation, with any
// ambient NCLI_VAULT_PASSWORD stripped out -- so this test's "no password
// set" precondition holds regardless of what the developer's own shell (or
// CI environment) happens to export, rather than merely relying on nothing
// having appended one later.
func vaultTestEnv(t *testing.T) []string {
	t.Helper()
	env := prefsTestEnv(t)
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "NCLI_VAULT_PASSWORD=") {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// TestIDSaveWithoutVaultPasswordErrorContract confirms followup issue #2
// (integration/agent-eval/followup/issues.md): saving a brand-new vault
// identity under --json with no NCLI_VAULT_PASSWORD set is a missing
// required precondition for the requested --json operation -- AGENTS.md's
// own definition of "usage" ("bad/missing/conflicting flags/args/config").
// id.go's resolveNewVaultPassword returns a bare
// `errors.New("vault password required; set NCLI_VAULT_PASSWORD")` in this
// case, which saveIdentity/runIDGenerate propagate up through
// common.RuntimeError instead of common.UsageError -- so it comes back as
// code "internal", exit 1, not "usage", exit 2.
func TestIDSaveWithoutVaultPasswordErrorContract(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)

	// A fresh XDG_CONFIG_HOME guarantees no vault.yaml exists yet, so this
	// exercises the "create a brand-new vault" path in saveIdentity, the
	// same one id.go's resolveNewVaultPassword guards.
	cmd := exec.Command(bin, "id", "--save", "--label", "eval-agent", "--json")
	cmd.Env = vaultTestEnv(t)
	out, err := cmd.Output()

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected a non-zero *exec.ExitError, got %v (stdout=%q)", err, out)
	}

	if got := exitErr.ExitCode(); got != 2 {
		t.Errorf("exit code = %d, want 2 (usage) per AGENTS.md's error table -- confirms followup issue #2", got)
	}

	var payload struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if jsonErr := json.Unmarshal(exitErr.Stderr, &payload); jsonErr != nil {
		t.Fatalf("stderr is not valid JSON: %v\nstderr: %s", jsonErr, exitErr.Stderr)
	}
	if payload.Code != "usage" {
		t.Errorf("code = %q, want %q per AGENTS.md's error table -- confirms followup issue #2", payload.Code, "usage")
	}
}
