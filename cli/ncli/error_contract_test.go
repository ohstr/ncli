package ncli

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// TestMissingArgsErrorContract confirms followup issue #1
// (integration/agent-eval/followup/issues.md): AGENTS.md's error-contract
// table says a bad/missing argument is a "usage" mistake -- code "usage",
// exit 2, and (since this is --json mode) the human-readable cobra help
// dump suppressed in favor of the structured error alone. But a command
// whose arg-count check is cobra's own built-in Args validator (e.g.
// decode.go's `Args: cobra.ExactArgs(1)`) never reaches RunE at all, so it
// never goes through common.UsageError -- the classifier that actually
// enforces that contract (tagging CodeUsage, and silencing cmd's own
// usage/error output). cobra's default ExecuteC behavior instead: prints
// "Error: ..." plus the full help/usage dump to stderr regardless of
// --json (both go through cobra's own Print/PrintErr, which in this cobra
// version both fall back to stderr when no explicit output is set), and
// returns a bare (non-*CLIError) error that main.go's ExitCode/EmitError
// fall back to treating as CodeInternal, exit 1 -- so ncli's own JSON
// error line lands on stderr too, stacked underneath cobra's own dump
// instead of appearing alone.
//
// Confirmed against two different commands (not just decode) to show the
// gap is systemic: every command relying on a bare cobra.ExactArgs/
// MinimumNArgs/MaximumNArgs/NoArgs Args validator has the same hole, vs.
// e.g. blossom/mirror.go's custom Args func that calls common.UsageError
// itself and gets the contract right.
func TestMissingArgsErrorContract(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)

	cases := []struct {
		name string
		args []string
	}{
		{"decode with no entity arg", []string{"decode", "--json"}},
		{"prefs relays add with no relay-url arg", []string{"prefs", "relays", "add", "--json"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin, tc.args...)
			cmd.Env = prefsTestEnv(t)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			runErr := cmd.Run()

			exitErr, ok := runErr.(*exec.ExitError)
			if !ok {
				t.Fatalf("expected a non-zero *exec.ExitError, got %v (stdout=%q stderr=%q)", runErr, stdout.String(), stderr.String())
			}

			if got := exitErr.ExitCode(); got != 2 {
				t.Errorf("exit code = %d, want 2 (usage) per AGENTS.md's error table -- confirms followup issue #1", got)
			}

			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty -- a command's stdout should hold nothing but a clean result on success, never spill over on failure", stdout.String())
			}

			// AGENTS.md: "exactly one top-level error report, always on
			// stderr" and "a usage mistake in --json mode skips the
			// human-readable help dump... in favor of the structured error
			// alone" -- so stderr should be exactly one line (the JSON
			// payload), not cobra's own "Error: ..." line plus its
			// multi-line help/usage dump plus ncli's own JSON line stacked
			// on top of each other.
			nonBlank := 0
			for _, line := range strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n") {
				if strings.TrimSpace(line) != "" {
					nonBlank++
				}
			}
			if nonBlank != 1 {
				t.Errorf("stderr has %d non-blank lines, want exactly 1 (the structured JSON error alone) -- cobra's own help dump and/or \"Error: ...\" line leaked in on top of it, confirming followup issue #1's double-reporting: stderr=%q", nonBlank, stderr.String())
			}

			payload := lastJSONLine(t, stderr.String())
			if payload.Code != "usage" {
				t.Errorf("code = %q, want %q per AGENTS.md's error table -- confirms followup issue #1", payload.Code, "usage")
			}
		})
	}
}

// lastJSONLine parses the last non-blank line of s as a CLIError JSON
// payload -- stderr can carry cobra's own "Error: ..." line ahead of
// ncli's own structured error (exactly the double-reporting AGENTS.md says
// should never happen, but does for the bug under test here), so the
// error payload itself is only ever the final line.
func lastJSONLine(t *testing.T, s string) struct {
	Error string `json:"error"`
	Code  string `json:"code"`
} {
	t.Helper()
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	last := lines[len(lines)-1]

	var payload struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal([]byte(last), &payload); err != nil {
		t.Fatalf("last stderr line is not valid JSON: %v\nfull stderr: %q", err, s)
	}
	return payload
}
