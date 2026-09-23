package common

import (
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
)

func TestErrorPrefix(t *testing.T) {
	if got := errorPrefix(false); got != "Error:" {
		t.Errorf("errorPrefix(false) = %q, want plain %q", got, "Error:")
	}
	got := errorPrefix(true)
	if !strings.Contains(got, "Error:") {
		t.Errorf("errorPrefix(true) = %q, want it to still contain the word", got)
	}
	if !strings.HasPrefix(got, "\x1b[31m") || !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("errorPrefix(true) = %q, want it wrapped in red ANSI", got)
	}
}

// TestIsColorTerminal_NoColor pins the https://no-color.org contract: even
// on a real terminal, NO_COLOR turns color off.
func TestIsColorTerminal_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if isColorTerminal(os.Stdout) {
		t.Error("isColorTerminal = true with NO_COLOR set, want false")
	}
}

func newRenderTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "widget <name>",
		Short:   "Do a widget thing",
		Example: `  ncli widget thing`,
		RunE:    func(*cobra.Command, []string) error { return nil },
	}
	cmd.Flags().String("size", "", "how big")
	cmd.Flags().Bool("json", false, "json output")
	return cmd
}

// captureStd runs fn with both standard streams replaced by pipes, so a test
// can assert on exactly what a user would have seen -- and, just as
// importantly, that stdout stayed empty.
func captureStd(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outW, errW

	var wg sync.WaitGroup
	var outBytes, errBytes []byte
	wg.Add(2)
	go func() { defer wg.Done(); outBytes, _ = io.ReadAll(outR) }()
	go func() { defer wg.Done(); errBytes, _ = io.ReadAll(errR) }()

	fn()

	_ = outW.Close()
	_ = errW.Close()
	wg.Wait()
	os.Stdout, os.Stderr = origOut, origErr
	return string(outBytes), string(errBytes)
}

// TestEmitError_HelpModes is the whole point of the cashctl alignment: three
// distinct shapes, all on stderr, stdout untouched in every one.
func TestEmitError_HelpModes(t *testing.T) {
	cases := []struct {
		name        string
		mode        HelpMode
		wantErrLine bool
		wantHelp    bool
	}{
		{"runtime failure prints the error alone", HelpNone, true, false},
		{"bare invocation prints help alone", HelpOnly, false, true},
		{"wrong invocation prints both", HelpAfterError, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newRenderTestCmd()
			err := &CLIError{Err: errors.New("boom"), Code: CodeUsage, Help: tc.mode}

			stdout, stderr := captureStd(t, func() { EmitError(cmd, err) })

			if stdout != "" {
				t.Errorf("stdout = %q, want empty -- a failure must never touch the result stream", stdout)
			}
			if got := strings.Contains(stderr, "Error: boom"); got != tc.wantErrLine {
				t.Errorf("stderr has error line = %v, want %v\nstderr:\n%s", got, tc.wantErrLine, stderr)
			}
			if got := strings.Contains(stderr, "Usage:"); got != tc.wantHelp {
				t.Errorf("stderr has help = %v, want %v\nstderr:\n%s", got, tc.wantHelp, stderr)
			}
			if tc.wantErrLine && tc.wantHelp {
				if strings.Index(stderr, "Error: boom") > strings.Index(stderr, "Usage:") {
					t.Errorf("error line must come before help, got:\n%s", stderr)
				}
			}
		})
	}
}

// TestEmitError_JSONSkipsHelp guards the agent-facing contract: --json gets
// one structured line and never a help dump, whatever the HelpMode says.
func TestEmitError_JSONSkipsHelp(t *testing.T) {
	cmd := newRenderTestCmd()
	_ = cmd.Flags().Set("json", "true")
	err := &CLIError{Err: errors.New("boom"), Code: CodeUsage, Help: HelpAfterError}

	stdout, stderr := captureStd(t, func() { EmitError(cmd, err) })

	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if strings.Contains(stderr, "Usage:") {
		t.Errorf("--json must not dump help, got:\n%s", stderr)
	}
	if lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n"); len(lines) != 1 {
		t.Errorf("stderr = %d lines, want exactly 1 JSON line:\n%s", len(lines), stderr)
	}
	if !strings.Contains(stderr, `"code":"usage"`) {
		t.Errorf("stderr missing structured code, got: %s", stderr)
	}
}

// TestInvocationClassifiers checks the three constructors agree on code and
// exit status while differing only in how much help they ask for.
func TestInvocationClassifiers(t *testing.T) {
	cases := []struct {
		name string
		make func(*cobra.Command) error
		want HelpMode
	}{
		{"UsageError", func(c *cobra.Command) error { return UsageError(c, errors.New("x")) }, HelpNone},
		{"HelpError", func(c *cobra.Command) error { return HelpError(c, errors.New("x")) }, HelpOnly},
		{"InvocationError", func(c *cobra.Command) error { return InvocationError(c, errors.New("x")) }, HelpAfterError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newRenderTestCmd()
			err := tc.make(cmd)

			ce, ok := err.(*CLIError)
			if !ok {
				t.Fatalf("got %T, want *CLIError", err)
			}
			if ce.Code != CodeUsage {
				t.Errorf("Code = %q, want %q", ce.Code, CodeUsage)
			}
			if ce.Help != tc.want {
				t.Errorf("Help = %d, want %d", ce.Help, tc.want)
			}
			if ExitCode(err) != 2 {
				t.Errorf("ExitCode = %d, want 2", ExitCode(err))
			}
			if !cmd.SilenceUsage || !cmd.SilenceErrors {
				t.Error("cobra's own reporting must be silenced")
			}
		})
	}
}

// TestIsBareInvocation: inherited flags don't make an invocation non-bare,
// which is what lets `ncli decode --json` still answer with help.
func TestIsBareInvocation(t *testing.T) {
	t.Run("no args, no flags", func(t *testing.T) {
		if !IsBareInvocation(newRenderTestCmd(), nil) {
			t.Error("want bare")
		}
	})
	t.Run("positional arg makes it non-bare", func(t *testing.T) {
		if IsBareInvocation(newRenderTestCmd(), []string{"x"}) {
			t.Error("want non-bare")
		}
	})
	t.Run("command-specific flag makes it non-bare", func(t *testing.T) {
		cmd := newRenderTestCmd()
		_ = cmd.Flags().Set("size", "big")
		if IsBareInvocation(cmd, nil) {
			t.Error("want non-bare once --size was set")
		}
	})
}
