package delegate

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildTestBinary builds the real ncli binary for this package's
// process-level tests -- the wizard-vs-non-interactive guard being tested
// here depends on real tty detection on stdin/stdout, which only exists at
// the process level (exec.Command's pipes are never a tty), not when
// calling RunE in-process.
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

// TestDelegateWizardGuard is the regression test for the bubbletea wizard
// launching unconditionally (ignoring --json, and with no tty check at all)
// whenever --issuer-key/NCLI_DELEGATE_ISSUERKEY was unset -- unlike
// "apply"'s headless() check or id.go's resolveVaultPassword, an agent or
// script invoking "ncli id delegate" without --issuer-key would hang or get
// garbled output deep inside bubbletea's screen init instead of a clear,
// immediate answer. Both cases below must now come back fast with a
// classified usage error instead of ever reaching RunWizard.
func TestDelegateWizardGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)

	t.Run("--json never launches the wizard", func(t *testing.T) {
		cmd := exec.Command(bin, "id", "delegate", "--json")
		cmd.Stdin = nil // no pipe at all -- closest to an agent's non-tty stdin
		out, err := cmd.CombinedOutput()
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("expected a non-zero exit (usage error), got err=%v out=%s", err, out)
		}
		if exitErr.ExitCode() != 2 {
			t.Fatalf("expected exit 2 (usage), got %d; out=%s", exitErr.ExitCode(), out)
		}
		var payload struct {
			Code string `json:"code"`
		}
		if jsonErr := json.Unmarshal(out, &payload); jsonErr != nil {
			t.Fatalf("expected a structured JSON error on the combined output, got: %v\nraw: %s", jsonErr, out)
		}
		if payload.Code != "usage" {
			t.Errorf("code = %q, want %q", payload.Code, "usage")
		}
	})

	t.Run("non-tty stdin/stdout without --json also skips the wizard", func(t *testing.T) {
		cmd := exec.Command(bin, "id", "delegate")
		// exec.Command's stdin/stdout, left as pipes/files rather than a
		// pty, are never a terminal -- exactly the agent-invocation shape
		// this guard exists for.
		out, err := cmd.CombinedOutput()
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("expected a non-zero exit (usage error), got err=%v out=%s", err, out)
		}
		if exitErr.ExitCode() != 2 {
			t.Fatalf("expected exit 2 (usage), got %d; out=%s", exitErr.ExitCode(), out)
		}
	})
}
