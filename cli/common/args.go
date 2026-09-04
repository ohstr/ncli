package common

import (
	"fmt"

	"github.com/spf13/cobra"
)

// silence marks cmd's error as already reported, so cobra doesn't also
// print its own "Error: ..." + usage dump on top of whatever the caller
// (main.go's EmitError, via the single top-level sink) reports.
func silence(cmd *cobra.Command) {
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
}

// UsageError marks cmd as badly called -- a missing/conflicting flag, wrong
// arg count, or similar invocation-shape mistake. It prints cmd's own help
// immediately in text mode (so the caller sees correct usage without a
// separate --help run); skipped in --json mode, where cmd.Help()'s default
// stdout destination would otherwise pollute the stream a script expects to
// hold nothing but clean data. Returns err unchanged so callers can
// propagate it from an Args validator -- which cobra runs before
// PersistentPreRun, so a bad invocation is rejected before config loading
// or logging setup ever runs.
func UsageError(cmd *cobra.Command, err error) error {
	silence(cmd)
	if jsonMode, _ := cmd.Flags().GetBool("json"); !jsonMode {
		_ = cmd.Help()
	}
	return wrapCLIError(CodeUsage, "", err)
}

// InvalidInputError marks err as caused by a supplied value that failed
// validation or parsing (a malformed identifier, relay URL, duration,
// private key, ...) -- distinct from UsageError (the invocation's shape,
// i.e. which flags/args were given, was wrong) even though both are
// equally the caller's fault and not worth retrying unchanged. input, if
// non-empty, is the specific value that failed, echoed back structurally
// in --json mode instead of only appearing embedded in the message text;
// pass "" when there's no single clean value or it would be unsafe to
// stringify (secret material).
func InvalidInputError(cmd *cobra.Command, input string, err error) error {
	silence(cmd)
	return wrapCLIError(CodeInvalidInput, input, err)
}

// NotFoundError marks err as failing because a referenced resource doesn't
// exist (a vault entry, a saved identity, a configured relay). input is
// the identifier that wasn't found, per InvalidInputError's rules.
func NotFoundError(cmd *cobra.Command, input string, err error) error {
	silence(cmd)
	return wrapCLIError(CodeNotFound, input, err)
}

// ConflictError marks err as colliding with existing state -- a vault
// label that's already taken, a reindex already running (admin API 409)
// -- which is retryable once the conflicting state clears, unlike most
// other error kinds. input is the conflicting value, per
// InvalidInputError's rules.
func ConflictError(cmd *cobra.Command, input string, err error) error {
	silence(cmd)
	return wrapCLIError(CodeConflict, input, err)
}

// NetworkError marks err as a failed remote call (a relay connection, an
// admin HTTP request, a NIP-05 HTTPS fetch) -- retryable, unlike most other
// error kinds. input is the host/URL that failed, per InvalidInputError's
// rules.
func NetworkError(cmd *cobra.Command, input string, err error) error {
	silence(cmd)
	return wrapCLIError(CodeNetwork, input, err)
}

// AuthError marks err as a rejected credential or signature (wrong vault
// password, a relay admin request's NIP-98 signature rejected). Never
// takes an input value -- there is never a safe one to echo back.
func AuthError(cmd *cobra.Command, err error) error {
	silence(cmd)
	return wrapCLIError(CodeAuth, "", err)
}

// UnsupportedError marks err as failing because the target server doesn't
// support the requested capability at all (e.g. a Blossom server with
// BUD-02 `list` disabled entirely), as opposed to NotFoundError's "this one
// specific resource doesn't exist" -- worth its own code specifically so an
// agent can tell "don't ever call this again this session" apart from
// "this one lookup missed, but the capability itself still works" without
// string-matching the message. input, if non-empty, is the server/URL that
// doesn't support it, per InvalidInputError's rules.
func UnsupportedError(cmd *cobra.Command, input string, err error) error {
	silence(cmd)
	return wrapCLIError(CodeUnsupported, input, err)
}

// ExactArgs, MaximumNArgs, MinimumNArgs, and NoArgs mirror cobra's own
// same-named Args validators (github.com/spf13/cobra), but route a wrong
// arg count through UsageError instead of returning a bare error. cobra's
// own versions run before RunE ever executes, and left unwrapped they
// bypass UsageError entirely -- no CodeUsage classification, no
// SilenceUsage/SilenceErrors -- so a wrong arg count comes back as the
// CodeInternal fallback (exit 1) with cobra's own "Error: ..." plus its
// full help dump printed on top, a contract violation even under --json
// (see AGENTS.md's error table and followup issue #1). Use these in place
// of the cobra.* equivalents on any command's Args field.
func ExactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return UsageError(cmd, fmt.Errorf("accepts %d arg(s), received %d", n, len(args)))
		}
		return nil
	}
}

func MaximumNArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > n {
			return UsageError(cmd, fmt.Errorf("accepts at most %d arg(s), received %d", n, len(args)))
		}
		return nil
	}
}

func MinimumNArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < n {
			return UsageError(cmd, fmt.Errorf("requires at least %d arg(s), only received %d", n, len(args)))
		}
		return nil
	}
}

func NoArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return UsageError(cmd, fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath()))
	}
	return nil
}

// RequireSubcommand is the RunE of a group command whose entire job is
// dispatching to children (e.g. "relay members", "prefs", "miner") -- it
// has nothing to run itself. Left with no RunE at all, cobra falls back to
// printing help and exiting 0 whenever invocation doesn't resolve to a
// child, whether that's a bare "ncli relay members" or a typo'd "ncli
// miner mnie" -- silently treating a missing/misspelled subcommand as
// success, on stdout, even in --json mode. Wiring this as the group's own
// RunE routes that same situation through UsageError instead, so it gets
// exit 2, stderr-only reporting, and a structured error under --json like
// every other invocation mistake.
func RequireSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return UsageError(cmd, fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath()))
	}
	return UsageError(cmd, fmt.Errorf("%q requires a subcommand", cmd.CommandPath()))
}

// RuntimeError is the fallback bucket for a failure that doesn't cleanly
// fit any of the more specific classifiers above -- typically because the
// underlying call funnels several distinct failure modes into one error
// value without a way to distinguish them here, or because it genuinely is
// an internal/unexpected failure (an encode error, a disk I/O failure).
func RuntimeError(cmd *cobra.Command, err error) error {
	silence(cmd)
	return wrapCLIError(CodeInternal, "", err)
}
