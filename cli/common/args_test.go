package common

import (
	"errors"
	"testing"

	"github.com/spf13/cobra"
)

// TestRequireSubcommand guards a group command's RunE (e.g. "relay
// members", "prefs", "miner" -- see cli/relay/admin.go, cli/ncli/prefs.go,
// cli/ncli/miner.go) against regressing back to cobra's default no-RunE
// behavior: help text on stdout and exit 0 even for a bare or misspelled
// invocation, silently ignored under --json too. RequireSubcommand must
// always come back as a *CLIError classified "usage" (exit 2), whether
// invoked with no args (bare group command) or a stray one (a typo'd
// child name that didn't resolve).
func TestRequireSubcommand(t *testing.T) {
	newCmd := func() *cobra.Command {
		parent := &cobra.Command{Use: "relay"}
		child := &cobra.Command{Use: "members"}
		parent.AddCommand(child)
		return child
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no args (bare group command)", nil},
		{"stray unresolved arg (typo'd child)", []string{"bogus"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newCmd()
			err := RequireSubcommand(cmd, tc.args)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}

			var ce *CLIError
			if !errors.As(err, &ce) {
				t.Fatalf("expected a *CLIError, got %T: %v", err, err)
			}
			if ce.Code != CodeUsage {
				t.Errorf("Code = %q, want %q", ce.Code, CodeUsage)
			}
			if got := ExitCode(err); got != 2 {
				t.Errorf("ExitCode(err) = %d, want 2", got)
			}
			if retryableCodes[ce.Code] {
				t.Errorf("CodeUsage must not be retryable")
			}
			if !cmd.SilenceUsage || !cmd.SilenceErrors {
				t.Error("expected RequireSubcommand to silence cmd's own usage/error output (via UsageError)")
			}
		})
	}
}
