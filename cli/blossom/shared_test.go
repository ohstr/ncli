package blossom

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nipB7"
	bclient "github.com/ohstr/nmilat/nipB7/client"
	"github.com/spf13/cobra"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = orig
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestPrintFanoutReport_TextMode_NoURLDangling is the regression test for
// a manual-smoke-test-caught bug: rm's serverResult never populates URL
// (only upload/mirror do), and the text-mode line used to print it
// unconditionally, producing a dangling "ok   <hash> -> <server>: " with
// nothing after the colon.
func TestPrintFanoutReport_TextMode_NoURLDangling(t *testing.T) {
	report := &fanoutReport{}
	report.add(serverResult{Item: "deadbeef", Server: "https://s.example", OK: true}) // rm-shaped: no URL
	report.add(serverResult{Item: "f.txt", Server: "https://s.example", OK: true, URL: "https://s.example/deadbeef"})
	report.add(serverResult{Item: "g.txt", Server: "https://s.example", OK: false, Error: "boom"})

	out := captureStdout(t, func() { printFanoutReport(false, report) })

	if strings.Contains(out, ": \n") || strings.Contains(out, ":\n") {
		t.Errorf("output has a dangling empty-value line: %q", out)
	}
	if !strings.Contains(out, "ok   deadbeef -> https://s.example\n") {
		t.Errorf("output = %q, want a clean \"ok   deadbeef -> https://s.example\" line with no trailing colon", out)
	}
	if !strings.Contains(out, "ok   f.txt -> https://s.example: https://s.example/deadbeef\n") {
		t.Errorf("output = %q, want the upload-shaped line to still include its URL", out)
	}
	if !strings.Contains(out, "fail g.txt -> https://s.example: boom\n") {
		t.Errorf("output = %q, want the failure line to include its error", out)
	}
}

func testCmdWithServerFlag(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().StringArray("server", nil, "")
	return cmd
}

func TestResolveServers_ExplicitFlagWins(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cmd := testCmdWithServerFlag(t)
	if err := cmd.Flags().Set("server", "https://explicit.example"); err != nil {
		t.Fatal(err)
	}

	servers, err := resolveServers(cmd)
	if err != nil {
		t.Fatalf("resolveServers() error = %v", err)
	}
	if len(servers) != 1 || servers[0] != "https://explicit.example" {
		t.Errorf("servers = %v, want [https://explicit.example]", servers)
	}
}

func TestResolveServers_FallsBackToPrefs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	prefs, err := client.LoadPrefs()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prefs.AddBlossomServer("https://from-prefs.example"); err != nil {
		t.Fatal(err)
	}
	if err := client.SavePrefs(prefs); err != nil {
		t.Fatal(err)
	}

	cmd := testCmdWithServerFlag(t)
	servers, err := resolveServers(cmd)
	if err != nil {
		t.Fatalf("resolveServers() error = %v", err)
	}
	if len(servers) != 1 || servers[0] != "https://from-prefs.example" {
		t.Errorf("servers = %v, want [https://from-prefs.example]", servers)
	}
}

func TestResolveServers_ErrorsWhenNoneConfigured(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cmd := testCmdWithServerFlag(t)
	if _, err := resolveServers(cmd); err == nil {
		t.Fatal("resolveServers() error = nil, want an error naming `ncli blossom servers add`")
	}
}

func TestBuildAuth_ProducesAValidSignedToken(t *testing.T) {
	identity, err := client.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	event, err := buildAuth(identity.PrivKeyHex, identity.PubKeyHex, nipB7.VerbUpload, nil, defaultAuthTTL)
	if err != nil {
		t.Fatalf("buildAuth() error = %v", err)
	}

	if err := event.Verify(); err != nil {
		t.Errorf("signed authorization failed Verify(): %v", err)
	}
	auth, err := nipB7.ParseAuthorization(event)
	if err != nil {
		t.Fatalf("ParseAuthorization() error = %v", err)
	}
	if auth.Verb != nipB7.VerbUpload {
		t.Errorf("verb = %q, want %q", auth.Verb, nipB7.VerbUpload)
	}
	if len(auth.Hashes) != 0 {
		t.Errorf("hashes = %v, want none (unscoped)", auth.Hashes)
	}
}

func TestBuildAuth_ScopesToGivenHashes(t *testing.T) {
	identity, err := client.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	hash := strings.Repeat("a", 64)
	event, err := buildAuth(identity.PrivKeyHex, identity.PubKeyHex, nipB7.VerbDelete, []string{hash}, defaultAuthTTL)
	if err != nil {
		t.Fatalf("buildAuth() error = %v", err)
	}

	auth, err := nipB7.ParseAuthorization(event)
	if err != nil {
		t.Fatalf("ParseAuthorization() error = %v", err)
	}
	if !auth.HasHash(hash) {
		t.Errorf("token does not cover hash %q", hash)
	}
}

func TestDescribeError_PlainHTTPError(t *testing.T) {
	err := &bclient.HTTPError{Server: "https://s.example", StatusCode: http.StatusNotFound, Reason: "gone"}
	got := describeError(err)
	if !strings.Contains(got, "404") {
		t.Errorf("describeError(%v) = %q, want it to mention the status", err, got)
	}
}

func TestDescribeError_PaymentRequiredIncludesCashuAndLightning(t *testing.T) {
	err := &bclient.PaymentRequiredError{
		HTTPError: bclient.HTTPError{Server: "https://s.example", StatusCode: http.StatusPaymentRequired},
		Payment:   nipB7.PaymentRequest{Cashu: "cashuAxyz", Lightning: "lnbc1xyz"},
	}
	got := describeError(err)
	if !strings.Contains(got, "cashuAxyz") || !strings.Contains(got, "lnbc1xyz") {
		t.Errorf("describeError(%v) = %q, want it to include both payment methods", err, got)
	}
}

func TestClassifyHTTPError_MapsStatusToCode(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode common.ErrorCode
	}{
		{"401 -> auth", &bclient.HTTPError{StatusCode: http.StatusUnauthorized}, common.CodeAuth},
		{"403 -> auth", &bclient.HTTPError{StatusCode: http.StatusForbidden}, common.CodeAuth},
		{"404 -> not_found", &bclient.HTTPError{StatusCode: http.StatusNotFound}, common.CodeNotFound},
		{"500 -> network", &bclient.HTTPError{StatusCode: http.StatusInternalServerError}, common.CodeNetwork},
		{"402 payment required -> network", &bclient.PaymentRequiredError{HTTPError: bclient.HTTPError{StatusCode: http.StatusPaymentRequired}}, common.CodeNetwork},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "test"}
			got := classifyHTTPError(cmd, "input", tc.err)
			ce, ok := got.(*common.CLIError)
			if !ok {
				t.Fatalf("classifyHTTPError() returned %T, want *common.CLIError", got)
			}
			if ce.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", ce.Code, tc.wantCode)
			}
		})
	}
}

func TestNormalizeDownloadTarget(t *testing.T) {
	hash := strings.Repeat("b", 64)

	cases := []struct {
		name     string
		input    string
		wantHash string
		wantExt  string
		wantErr  bool
	}{
		{"bare hash", hash, hash, "", false},
		{"blossom uri with ext", "blossom:" + hash + ".png", hash, "png", false},
		{"hash-shaped url", "https://server.example/" + hash + ".jpg", hash, "jpg", false},
		{"garbage", "not-a-hash-or-uri", "", "", true},
		// Regression case: a blossom: URI can put a "/" into what
		// LastIndex-based splitting treats as "ext" (see sanitizeExt's
		// doc comment) -- confirmed separately that "../" specifically
		// can't reach here (it breaks IsSHA256Hex first), but a plain
		// "/" without ".." does parse, so ext must be sanitized down to
		// "" rather than smuggled into the default output filename.
		{"blossom uri with a slash in ext", "blossom:" + hash + ".txt/subdir/evil", hash, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotHash, gotExt, err := normalizeDownloadTarget(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("normalizeDownloadTarget(%q) error = nil, want an error", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeDownloadTarget(%q) error = %v", tc.input, err)
			}
			if gotHash != tc.wantHash || gotExt != tc.wantExt {
				t.Errorf("normalizeDownloadTarget(%q) = (%q, %q), want (%q, %q)", tc.input, gotHash, gotExt, tc.wantHash, tc.wantExt)
			}
		})
	}
}
