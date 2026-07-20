package common

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// ErrorCode classifies a CLIError so an agent can branch on failure kind
// without string-matching the message: whether retrying makes sense, and
// (via ExitCode) a distinct process exit status. Kept to this fixed set --
// add to it deliberately, don't let call sites invent ad hoc codes.
type ErrorCode string

const (
	CodeUsage        ErrorCode = "usage"         // bad flags/args -- cobra-level invocation mistake
	CodeInvalidInput ErrorCode = "invalid_input"  // a supplied value failed validation/parsing
	CodeNotFound     ErrorCode = "not_found"      // the referenced thing doesn't exist
	CodeConflict     ErrorCode = "conflict"       // collides with existing state (already exists, already running)
	CodeNetwork      ErrorCode = "network"        // a remote call (relay, HTTP, NIP-05 fetch) failed
	CodeAuth         ErrorCode = "auth"           // wrong credentials / rejected signature
	CodeInternal     ErrorCode = "internal"       // anything else -- the default/fallback bucket
)

// exitCodes assigns each code its own process exit status, so a script can
// branch on $? alone without reading stderr at all in the common case.
// CodeInternal's 1 doubles as the fallback for a bare, non-CLIError error.
var exitCodes = map[ErrorCode]int{
	CodeUsage:        2,
	CodeInvalidInput: 3,
	CodeNotFound:     4,
	CodeConflict:     5,
	CodeNetwork:      6,
	CodeAuth:         7,
	CodeInternal:     1,
}

// retryableCodes marks which codes describe a condition worth retrying
// (a transient network hiccup, or state that resolves once a conflicting
// operation finishes) versus one that won't change no matter how many
// times the same command is rerun.
var retryableCodes = map[ErrorCode]bool{
	CodeConflict: true,
	CodeNetwork:  true,
}

// CLIError wraps a command failure with an ErrorCode, and optionally the
// single input value that caused it, so main.go's single top-level sink
// (EmitError) can render it consistently -- a styled text line, or
// structured JSON on stderr when --json is set -- and pick an exit code,
// instead of every command choosing its own rendering and printing early.
// Input is left blank when there's no one clean value to echo, or when the
// value is sensitive (a private key, a vault password) and must not be
// echoed back at all.
type CLIError struct {
	Err   error
	Code  ErrorCode
	Input string
}

func (e *CLIError) Error() string { return e.Err.Error() }
func (e *CLIError) Unwrap() error { return e.Err }

// wrapCLIError tags err with code/input, unless it's already a *CLIError
// (e.g. returned as-is from a nested classifier call), in which case its
// original classification is kept rather than overwritten.
func wrapCLIError(code ErrorCode, input string, err error) error {
	if err == nil {
		return nil
	}
	if ce, ok := err.(*CLIError); ok {
		return ce
	}
	return &CLIError{Err: err, Code: code, Input: input}
}

// PrintJSON marshals v as indented JSON to stdout -- the shared success-
// output path for every command's --json mode.
func PrintJSON(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Println(`{"error":"failed to encode JSON output"}`)
		return
	}
	fmt.Println(string(data))
}

// EmitError is main.go's single point for reporting rootCmd.ExecuteC()'s
// returned error -- a *CLIError from one of this package's classifiers, or
// a bare error cobra returns itself (e.g. a flag-parsing failure before
// any Args validator runs). jsonMode comes from cmd's own --json flag (a
// root persistent flag inherited by every command), checked once here
// instead of duplicated per-command. A JSON-mode error goes to stderr, not
// stdout -- stdout stays reserved for a command's actual data result, so a
// script can always trust that only a clean success result ever lands
// there.
func EmitError(cmd *cobra.Command, err error) {
	jsonMode := false
	if cmd != nil {
		jsonMode, _ = cmd.Flags().GetBool("json")
	}

	if jsonMode {
		ce, _ := err.(*CLIError)
		payload := struct {
			Error     string    `json:"error"`
			Code      ErrorCode `json:"code"`
			Retryable bool      `json:"retryable"`
			Input     string    `json:"input,omitempty"`
		}{
			Error:     err.Error(),
			Code:      codeOf(err),
			Retryable: retryableCodes[codeOf(err)],
		}
		if ce != nil {
			payload.Input = ce.Input
		}
		data, mErr := json.Marshal(payload)
		if mErr != nil {
			data = []byte(`{"error":"failed to encode error output"}`)
		}
		fmt.Fprintln(os.Stderr, string(data))
		return
	}

	log.Error().Msg(err.Error())
}

// ExitCode picks the process exit code for err from its ErrorCode (see
// exitCodes), falling back to 1 for a bare non-CLIError error.
func ExitCode(err error) int {
	ce, ok := err.(*CLIError)
	if !ok {
		return 1
	}
	if code, exists := exitCodes[ce.Code]; exists {
		return code
	}
	return 1
}

func codeOf(err error) ErrorCode {
	if ce, ok := err.(*CLIError); ok {
		return ce.Code
	}
	return CodeInternal
}

// RedactSecretInput returns s unchanged unless it looks like private-key
// material (an nsec1... NIP-19 string), in which case it returns "" --
// call this before passing a resolved identifier/argument as a
// classifier's input value, so a malformed or mistyped private key never
// gets echoed back in a --json error.
func RedactSecretInput(s string) string {
	if strings.HasPrefix(s, "nsec1") {
		return ""
	}
	return s
}
