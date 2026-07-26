package blossom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/ncli/client"
)

var (
	testBinOnce sync.Once
	testBinPath string
	testBinErr  error
)

// buildTestBinary builds the real ncli binary once and shares it across
// every test in this file -- these are process-level "as a user" tests
// (actual exit codes, actual stdout/stderr, actual argv parsing), which
// can't be exercised by calling RunE in-process. Mirrors the same-named
// helper in cli/ncli/miner_test.go.
func buildTestBinary(t *testing.T) string {
	t.Helper()
	testBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ncli-blossom-test-bin")
		if err != nil {
			testBinErr = err
			return
		}
		testBinPath = filepath.Join(dir, "ncli")
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		buildCmd := exec.CommandContext(ctx, "go", "build", "-o", testBinPath, "github.com/ohstr/ncli/cmd/ncli")
		if out, err := buildCmd.CombinedOutput(); err != nil {
			testBinErr = fmt.Errorf("failed to build ncli binary: %w\n%s", err, out)
		}
	})
	if testBinErr != nil {
		t.Fatalf("%v", testBinErr)
	}
	return testBinPath
}

// blossomTestEnv isolates a spawned ncli process's prefs.yaml to a fresh
// temp dir, so these tests never touch the real user's config -- mirrors
// cli/ncli/prefs_test.go's prefsTestEnv.
func blossomTestEnv(t *testing.T) []string {
	t.Helper()
	return append(os.Environ(), "XDG_CONFIG_HOME="+t.TempDir())
}

// ncliRunner runs the built binary against a single isolated prefs.yaml
// (one identity/server config per runner), the way one real user session
// would.
type ncliRunner struct {
	t    *testing.T
	bin  string
	env  []string
	nsec string
}

func newRunner(t *testing.T) *ncliRunner {
	t.Helper()
	identity, err := client.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return &ncliRunner{t: t, bin: buildTestBinary(t), env: blossomTestEnv(t), nsec: identity.Nsec}
}

// run executes args and returns combined stdout/stderr separately, along
// with the process's exit code (0 if it exited cleanly).
func (r *ncliRunner) run(args ...string) (stdout, stderr string, exitCode int) {
	r.t.Helper()
	cmd := exec.Command(r.bin, args...)
	cmd.Env = r.env
	cmd.Stdin = nil // explicitly non-interactive: reading stdin must never block
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err == nil {
		return outBuf.String(), errBuf.String(), 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return outBuf.String(), errBuf.String(), exitErr.ExitCode()
	}
	r.t.Fatalf("%v failed to run: %v\nstderr: %s", args, err, errBuf.String())
	return "", "", -1
}

func (r *ncliRunner) mustRun(args ...string) (stdout, stderr string) {
	r.t.Helper()
	stdout, stderr, code := r.run(args...)
	if code != 0 {
		r.t.Fatalf("%v exited %d\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
	}
	return stdout, stderr
}

// TestBlossomFullUserJourney drives every subcommand back-to-back the way
// a real user (or an agent scripting against --json) would: configure
// servers, upload a file, see it in list, download it back byte-for-byte,
// mirror it to a second server, delete it, and confirm it's really gone.
func TestBlossomFullUserJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}

	server1 := newFakeBlossomServer()
	defer server1.Close()
	server2 := newFakeBlossomServer()
	defer server2.Close()

	r := newRunner(t)

	t.Run("servers add persists the default list", func(t *testing.T) {
		out, _ := r.mustRun("blossom", "servers", "add", server1.URL, "--json")
		var got struct {
			Added bool `json:"added"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
		}
		if !got.Added {
			t.Error("added = false, want true")
		}

		out, _ = r.mustRun("blossom", "servers", "list", "--json")
		var list struct {
			Servers []string `json:"servers"`
		}
		if err := json.Unmarshal([]byte(out), &list); err != nil {
			t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
		}
		if len(list.Servers) != 1 || list.Servers[0] != server1.URL {
			t.Errorf("servers = %v, want [%s]", list.Servers, server1.URL)
		}
	})

	dir := t.TempDir()
	filePath := filepath.Join(dir, "hello.txt")
	content := []byte("hello from ncli blossom's black-box test\n")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatal(err)
	}

	var hash string
	t.Run("upload reports a url and hash", func(t *testing.T) {
		out, _ := r.mustRun("blossom", "upload", "--identity", r.nsec, filePath, "--json")
		var got struct {
			Attempted int `json:"attempted"`
			Succeeded int `json:"succeeded"`
			Failed    int `json:"failed"`
			Results   []struct {
				OK     bool   `json:"ok"`
				URL    string `json:"url"`
				Sha256 string `json:"sha256"`
			} `json:"results"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
		}
		if got.Attempted != 1 || got.Succeeded != 1 || got.Failed != 0 {
			t.Fatalf("report = %+v, want attempted=1 succeeded=1 failed=0", got)
		}
		if len(got.Results) != 1 || !got.Results[0].OK || got.Results[0].URL == "" {
			t.Fatalf("results = %+v, want one ok result with a url", got.Results)
		}
		hash = got.Results[0].Sha256
		if hash == "" {
			t.Fatal("uploaded blob has no sha256 in the report")
		}
	})

	t.Run("list shows the uploaded blob", func(t *testing.T) {
		out, _ := r.mustRun("blossom", "list", "--identity", r.nsec, "--json")
		var descriptors []struct {
			Sha256 string `json:"sha256"`
		}
		if err := json.Unmarshal([]byte(out), &descriptors); err != nil {
			t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
		}
		if len(descriptors) != 1 || descriptors[0].Sha256 != hash {
			t.Errorf("list = %+v, want one descriptor with sha256 %s", descriptors, hash)
		}
	})

	t.Run("download recovers the exact original bytes", func(t *testing.T) {
		outPath := filepath.Join(dir, "downloaded.txt")
		r.mustRun("blossom", "download", hash, "-o", outPath)
		got, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, content) {
			t.Errorf("downloaded content = %q, want %q", got, content)
		}
	})

	t.Run("mirror copies the blob onto a second server", func(t *testing.T) {
		sourceURL := server1.URL + "/" + hash
		out, _ := r.mustRun("blossom", "mirror", "--identity", r.nsec, "--server", server2.URL, sourceURL, "--json")
		var got struct {
			Succeeded int `json:"succeeded"`
			Failed    int `json:"failed"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
		}
		if got.Succeeded != 1 || got.Failed != 0 {
			t.Fatalf("mirror report = %+v, want succeeded=1 failed=0", got)
		}

		server2.mu.Lock()
		_, exists := server2.blobs[hash]
		server2.mu.Unlock()
		if !exists {
			t.Error("blob was not actually stored on server2")
		}
	})

	t.Run("report submits without error", func(t *testing.T) {
		r.mustRun("blossom", "report", hash, "--identity", r.nsec, "--type", "spam", "--reason", "test report", "--server", server1.URL)
	})

	t.Run("rm deletes the blob, and a follow-up download 404s", func(t *testing.T) {
		out, _ := r.mustRun("blossom", "rm", hash, "--identity", r.nsec, "--yes", "--json")
		var got struct {
			Succeeded int `json:"succeeded"`
			Failed    int `json:"failed"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
		}
		if got.Succeeded != 1 || got.Failed != 0 {
			t.Fatalf("rm report = %+v, want succeeded=1 failed=0", got)
		}

		_, _, code := r.run("blossom", "download", hash, "-o", filepath.Join(dir, "should-not-exist.txt"))
		if code != 4 { // common.CodeNotFound's exit code
			t.Errorf("download of a deleted blob exited %d, want 4 (not_found)", code)
		}
	})
}

// TestBlossomUpload_NoServersConfigured proves upload fails clearly (not
// with a confusing panic or a bare "no such host") when nothing has been
// configured yet, and exits with the not_found code, not a generic 1.
func TestBlossomUpload_NoServersConfigured(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)

	dir := t.TempDir()
	filePath := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := r.run("blossom", "upload", "--identity", r.nsec, filePath, "--json")
	if code != 4 {
		t.Errorf("exit code = %d, want 4 (not_found)", code)
	}
	if !strings.Contains(stderr, "blossom servers add") {
		t.Errorf("stderr = %q, want it to mention `ncli blossom servers add`", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
}

// TestBlossomRm_RefusesWithoutYesNonInteractively proves rm fails fast
// instead of hanging when --yes is omitted and stdin isn't a terminal --
// the scenario an agent driving this over a pipe would otherwise hit as
// an indefinite hang.
func TestBlossomRm_RefusesWithoutYesNonInteractively(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)
	server := newFakeBlossomServer()
	defer server.Close()
	r.mustRun("blossom", "servers", "add", server.URL)

	hash := strings.Repeat("a", 64)
	done := make(chan struct{})
	var stderr string
	var code int
	go func() {
		_, stderr, code = r.run("blossom", "rm", hash, "--identity", r.nsec)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("rm without --yes hung instead of failing fast on non-interactive stdin")
	}

	if code == 0 {
		t.Error("exit code = 0, want a non-zero refusal")
	}
	if !strings.Contains(stderr, "--yes") {
		t.Errorf("stderr = %q, want it to mention --yes", stderr)
	}
}

// TestBlossomUpload_PartialMultiServerFailure proves that when one of
// several target servers fails, the report shows exactly which one, the
// upload to the healthy server still succeeds, and the process exits
// non-zero overall.
func TestBlossomUpload_PartialMultiServerFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)

	healthy := newFakeBlossomServer()
	defer healthy.Close()
	broken := newFakeBlossomServer()
	broken.SetHooks(fakeServerHooks{FailStatus: 500})
	defer broken.Close()

	r.mustRun("blossom", "servers", "add", healthy.URL)
	r.mustRun("blossom", "servers", "add", broken.URL)

	dir := t.TempDir()
	filePath := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(filePath, []byte("partial failure test"), 0644); err != nil {
		t.Fatal(err)
	}

	out, _, code := r.run("blossom", "upload", "--identity", r.nsec, filePath, "--json")
	if code == 0 {
		t.Fatal("exit code = 0, want non-zero since one server failed")
	}
	var got struct {
		Attempted int `json:"attempted"`
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
		Results   []struct {
			Server string `json:"server"`
			OK     bool   `json:"ok"`
			Error  string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	if got.Attempted != 2 || got.Succeeded != 1 || got.Failed != 1 {
		t.Fatalf("report = %+v, want attempted=2 succeeded=1 failed=1", got)
	}
	for _, res := range got.Results {
		if res.Server == broken.URL && res.OK {
			t.Error("the broken server's result reports ok=true")
		}
		if res.Server == healthy.URL && !res.OK {
			t.Error("the healthy server's result reports ok=false")
		}
	}
}

// TestBlossomUpload_PaymentRequiredSurfacesDetails proves a 402 response
// is reported with its Cashu/Lightning payment details rather than a bare
// "402 Payment Required" a user/agent could do nothing with.
func TestBlossomUpload_PaymentRequiredSurfacesDetails(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)
	server := newFakeBlossomServer()
	server.SetHooks(fakeServerHooks{PaymentRequired: true})
	defer server.Close()
	r.mustRun("blossom", "servers", "add", server.URL)

	dir := t.TempDir()
	filePath := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	out, _, code := r.run("blossom", "upload", "--identity", r.nsec, filePath, "--json")
	if code == 0 {
		t.Fatal("exit code = 0, want non-zero")
	}
	if !strings.Contains(out, "cashuAtest") || !strings.Contains(out, "lnbctest") {
		t.Errorf("report = %s, want it to include the cashu/lightning payment details", out)
	}
}

// TestBlossomUpload_SucceededTransferButInvalidResponse covers the
// SDK-confirmed "bytes sent, error returned" scenario: the PUT succeeds
// but the server's response body is malformed, or well-formed JSON that
// fails BlobDescriptor.Validate(). The CLI can't distinguish this from a
// real transport failure (see shared.go's uploadErrorHint), so it must
// always attach the disclaimer rather than implying a clean, safe-to-
// retry failure.
func TestBlossomUpload_SucceededTransferButInvalidResponse(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}

	for _, hook := range []string{"malformed body", "invalid descriptor"} {
		t.Run(hook, func(t *testing.T) {
			r := newRunner(t)
			server := newFakeBlossomServer()
			defer server.Close()
			if hook == "malformed body" {
				server.SetHooks(fakeServerHooks{MalformedResponseBody: true})
			} else {
				server.SetHooks(fakeServerHooks{InvalidDescriptor: true})
			}
			r.mustRun("blossom", "servers", "add", server.URL)

			dir := t.TempDir()
			filePath := filepath.Join(dir, "f.txt")
			if err := os.WriteFile(filePath, []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}

			out, _, code := r.run("blossom", "upload", "--identity", r.nsec, filePath, "--json")
			if code == 0 {
				t.Fatal("exit code = 0, want non-zero")
			}
			if !strings.Contains(out, "already sent") {
				t.Errorf("report = %s, want it to include the \"already sent to the server\" disclaimer", out)
			}

			// The blob really was stored server-side despite the reported
			// failure -- this is exactly the scenario the disclaimer warns
			// about, confirmed directly against the fake server's store.
			if len(server.blobs) != 1 {
				t.Errorf("server has %d blobs, want 1 (the transfer succeeded even though the CLI reported an error)", len(server.blobs))
			}
		})
	}
}

// TestBlossomListAll_MergesAndDedupesAcrossServers proves --all queries
// every configured server and merges by hash rather than just querying
// the first one.
func TestBlossomListAll_MergesAndDedupesAcrossServers(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)

	server1 := newFakeBlossomServer()
	defer server1.Close()
	server2 := newFakeBlossomServer()
	defer server2.Close()
	r.mustRun("blossom", "servers", "add", server1.URL)
	r.mustRun("blossom", "servers", "add", server2.URL)

	dir := t.TempDir()
	file1 := filepath.Join(dir, "one.txt")
	file2 := filepath.Join(dir, "two.txt")
	if err := os.WriteFile(file1, []byte("one"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file2, []byte("two"), 0644); err != nil {
		t.Fatal(err)
	}
	// Uploading fans out to both configured servers by default, so both
	// blobs land on both servers -- list --all must dedupe them down to
	// two entries, not report four.
	r.mustRun("blossom", "upload", "--identity", r.nsec, file1, file2)

	out, _ := r.mustRun("blossom", "list", "--identity", r.nsec, "--all", "--json")
	var descriptors []struct {
		Sha256 string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(out), &descriptors); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	if len(descriptors) != 2 {
		t.Errorf("list --all returned %d descriptors, want 2 (deduped)", len(descriptors))
	}
}

// TestBlossomListAll_LimitIsGlobalCap is the regression test for a
// code-review finding: "list --all --limit N" used to apply N to each
// server independently before merging, so the deduped total could exceed
// N (up to N * server count). The fix re-applies Limit as a cap on the
// final, newest-first merged result.
func TestBlossomListAll_LimitIsGlobalCap(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)

	server1 := newFakeBlossomServer()
	defer server1.Close()
	server2 := newFakeBlossomServer()
	defer server2.Close()
	r.mustRun("blossom", "servers", "add", server1.URL)
	r.mustRun("blossom", "servers", "add", server2.URL)

	dir := t.TempDir()
	var files []string
	for i := range 4 {
		p := filepath.Join(dir, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(p, fmt.Appendf(nil, "content %d", i), 0644); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	// Fans out to both configured servers, so all 4 files land on both --
	// list --all would merge/dedupe to 4 without a limit.
	uploadArgs := append([]string{"blossom", "upload", "--identity", r.nsec}, files...)
	r.mustRun(uploadArgs...)

	out, _ := r.mustRun("blossom", "list", "--identity", r.nsec, "--all", "--limit", "2", "--json")
	var descriptors []struct {
		Sha256 string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(out), &descriptors); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	if len(descriptors) != 2 {
		t.Errorf("list --all --limit 2 returned %d descriptors, want exactly 2 (global cap, not per-server)", len(descriptors))
	}
}

// TestBlossomUpload_FreshAuthPerCall_SurvivesSlowMultiFileBatch is the
// regression test for a code-review finding: a single BUD-11 auth token
// used to be signed once for the whole upload batch and reused across
// every (file, server) pair, so a batch slow enough to outlive the
// token's TTL would start failing partway through with "authorization
// has expired." The fix signs a fresh token per (file, server) call.
//
// This test uses a short --auth-ttl and an artificial per-request server
// delay so the batch's total elapsed time reliably exceeds the TTL --
// with the old shared-token behavior this would fail on the 2nd file;
// with a fresh token per call it must not. Both --auth-ttl and the delay
// are kept comfortably above one second: BUD-11 timestamps are Unix
// *seconds* (nipB7.NewAuthorization truncates via time.Time.Unix()), so a
// sub-second TTL is inherently flaky -- its truncated expiration can land
// anywhere up to ~1s later than requested depending on where "now" falls
// within its current second.
func TestBlossomUpload_FreshAuthPerCall_SurvivesSlowMultiFileBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)

	server := newFakeBlossomServer()
	server.SetHooks(fakeServerHooks{ResponseDelay: 3 * time.Second})
	defer server.Close()
	r.mustRun("blossom", "servers", "add", server.URL)

	dir := t.TempDir()
	var files []string
	for i := range 2 {
		p := filepath.Join(dir, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(p, fmt.Appendf(nil, "content %d", i), 0644); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}

	// 2 files * 3s server delay = ~6s elapsed by the second file's auth
	// check, well past a 1s TTL (worst-case truncated to ~2s) if the
	// token were shared across the whole batch.
	args := append([]string{"blossom", "upload", "--identity", r.nsec, "--auth-ttl", "1s"}, files...)
	args = append(args, "--json")
	out, _ := r.mustRun(args...)

	var got struct {
		Attempted int `json:"attempted"`
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	if got.Attempted != 2 || got.Succeeded != 2 || got.Failed != 0 {
		t.Fatalf("report = %+v, want attempted=2 succeeded=2 failed=0 -- a shared, batch-wide auth token would fail the 2nd file once the short TTL elapses", got)
	}
}

// TestBlossomDownload_FailedTransferLeavesNoPartialFile is the regression
// test for a code-review finding: a download that fails mid-transfer used
// to leave a truncated file behind at the output path with no cleanup.
// The fix removes the partially-written file when io.Copy fails.
func TestBlossomDownload_FailedTransferLeavesNoPartialFile(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	r := newRunner(t)

	server := newFakeBlossomServer()
	defer server.Close()
	r.mustRun("blossom", "servers", "add", server.URL)

	dir := t.TempDir()
	filePath := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(filePath, []byte("some real content to download"), 0644); err != nil {
		t.Fatal(err)
	}
	out, _ := r.mustRun("blossom", "upload", "--identity", r.nsec, filePath, "--json")
	var uploadReport struct {
		Results []struct {
			Sha256 string `json:"sha256"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &uploadReport); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	hash := uploadReport.Results[0].Sha256

	server.SetHooks(fakeServerHooks{TruncateDownload: true})

	outPath := filepath.Join(dir, "downloaded.txt")
	_, _, code := r.run("blossom", "download", hash, "-o", outPath)
	if code == 0 {
		t.Fatal("exit code = 0, want non-zero: the truncated transfer should have been reported as a failure")
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%s) = %v, want the partially-written file to have been removed", outPath, err)
	}
}
