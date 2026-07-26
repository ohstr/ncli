package blossom

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ohstr/ncli/cli/common"
)

// runArgs builds a fresh NewBlossomCommand() tree, adds the "--json"
// persistent flag main.go's real root command normally provides (so the
// tree behaves the same standalone as it does mounted under the real
// root), and executes it with args + --json so a rejected invocation's
// UsageError doesn't also try to print cmd.Help() to a real terminal.
// Output is discarded -- these tests only care about the returned error.
func runArgs(t *testing.T, args ...string) error {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cmd := NewBlossomCommand()
	cmd.PersistentFlags().Bool("json", false, "")
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append(args, "--json"))
	return cmd.Execute()
}

func wantUsageError(t *testing.T, err error, desc string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: error = nil, want a usage error", desc)
	}
	ce, ok := err.(*common.CLIError)
	if !ok {
		t.Fatalf("%s: error = %T (%v), want *common.CLIError", desc, err, err)
	}
	if ce.Code != common.CodeUsage && ce.Code != common.CodeInvalidInput {
		t.Errorf("%s: code = %q, want usage or invalid_input", desc, ce.Code)
	}
}

func TestUploadArgs_RequiresAtLeastOneFile(t *testing.T) {
	err := runArgs(t, "upload", "--identity", "nsec1xxxxx")
	wantUsageError(t, err, "upload with no file args")
}

func TestUploadArgs_RequiresIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	err := runArgs(t, "upload", path)
	wantUsageError(t, err, "upload with no --identity")
}

func TestUploadArgs_RejectsMissingFile(t *testing.T) {
	err := runArgs(t, "upload", "--identity", "nsec1xxxxx", filepath.Join(t.TempDir(), "does-not-exist"))
	wantUsageError(t, err, "upload of a nonexistent file")
}

func TestUploadArgs_RejectsDirectory(t *testing.T) {
	err := runArgs(t, "upload", "--identity", "nsec1xxxxx", t.TempDir())
	wantUsageError(t, err, "upload of a directory")
}

func TestDownloadArgs_RequiresExactlyOneArg(t *testing.T) {
	if err := runArgs(t, "download"); err == nil {
		t.Error("download with no args: error = nil, want an error")
	}
	if err := runArgs(t, "download", "a", "b"); err == nil {
		t.Error("download with two args: error = nil, want an error")
	}
}

func TestRmArgs_RejectsInvalidHash(t *testing.T) {
	err := runArgs(t, "rm", "--identity", "nsec1xxxxx", "not-a-hash", "--yes")
	wantUsageError(t, err, "rm with a malformed hash")
}

func TestRmArgs_RequiresIdentity(t *testing.T) {
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	err := runArgs(t, "rm", hash, "--yes")
	wantUsageError(t, err, "rm with no --identity")
}

func TestMirrorArgs_RequiresIdentity(t *testing.T) {
	err := runArgs(t, "mirror", "https://example.com/file.png")
	wantUsageError(t, err, "mirror with no --identity")
}

func TestReportArgs_RejectsInvalidHash(t *testing.T) {
	err := runArgs(t, "report", "not-a-hash", "--identity", "nsec1xxxxx", "--type", "spam", "--reason", "x")
	wantUsageError(t, err, "report with a malformed hash")
}

func TestReportArgs_RequiresTypeAndReason(t *testing.T) {
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	err := runArgs(t, "report", hash, "--identity", "nsec1xxxxx")
	if err == nil {
		t.Fatal("report with no --type/--reason: error = nil, want an error")
	}
}

func TestServersAddArgs_RequiresExactlyOneArg(t *testing.T) {
	if err := runArgs(t, "servers", "add"); err == nil {
		t.Error("servers add with no args: error = nil, want an error")
	}
}

func TestBareBlossomRequiresSubcommand(t *testing.T) {
	err := runArgs(t)
	wantUsageError(t, err, "bare `ncli blossom` with no subcommand")
}

func TestBareServersRequiresSubcommand(t *testing.T) {
	err := runArgs(t, "servers")
	wantUsageError(t, err, "bare `ncli blossom servers` with no subcommand")
}
