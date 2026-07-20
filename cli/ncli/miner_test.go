package ncli

import (
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
)

const testMinerEventYAML = `pubkey: "3c1db3dd55e2ff09ba5317dd8eec2339797e9e2ddf74591172735c47f3a2ad6e"
created_at: 1719759720
kind: 1
tags: []
content: "hello from a test"
`

var (
	testBinOnce sync.Once
	testBinPath string
	testBinErr  error
)

// buildTestBinary builds the real ncli binary once and shares it across
// every test in this file -- these tests are specifically about
// process-level behavior (actual exit codes, actual stdout), which can't be
// exercised by calling RunE in-process (that only returns an error value,
// never an os.Exit code).
func buildTestBinary(t *testing.T) string {
	t.Helper()
	testBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ncli-miner-test-bin")
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

// TestMinerMineThenCheckExitCodes is the exit-code regression test:
// `miner check` used to always exit 0 regardless of whether any event
// actually passed PoW verification, which broke the one scripted/automation
// scenario this command is positioned for.
func TestMinerMineThenCheckExitCodes(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()

	eventPath := filepath.Join(dir, "event.yaml")
	if err := os.WriteFile(eventPath, []byte(testMinerEventYAML), 0644); err != nil {
		t.Fatal(err)
	}

	minedPath := filepath.Join(dir, "mined.json")
	mineCmd := exec.Command(bin, "miner", "mine", "-e", eventPath, "-o", minedPath, "-d", "8")
	if out, err := mineCmd.CombinedOutput(); err != nil {
		t.Fatalf("miner mine failed: %v\n%s", err, out)
	}

	minedData, err := os.ReadFile(minedPath)
	if err != nil {
		t.Fatal(err)
	}

	eventsArrayPath := filepath.Join(dir, "events.json")
	if err := os.WriteFile(eventsArrayPath, []byte("["+string(minedData)+"]"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("valid PoW exits 0", func(t *testing.T) {
		checkCmd := exec.Command(bin, "miner", "check", "-e", eventsArrayPath)
		if out, err := checkCmd.CombinedOutput(); err != nil {
			t.Fatalf("expected exit 0 for a valid PoW file, got error: %v\n%s", err, out)
		}
	})

	t.Run("mine text-mode result is on stdout, not stderr", func(t *testing.T) {
		// Regression test: mine's text-mode (non --json) result used to be
		// emitted entirely via log.Info (stderr), leaving stdout empty on
		// success -- breaking the same "stdout is only ever the clean
		// result" contract every other command follows (see AGENTS.md).
		freshOut := filepath.Join(dir, "mine_stdout_check.json")
		mineCmd := exec.Command(bin, "miner", "mine", "-e", eventPath, "-o", freshOut, "-d", "8")
		out, err := mineCmd.Output()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, want := range []string{"id:", "nonce:", "difficulty:", "signed:", "out:"} {
			if !strings.Contains(string(out), want) {
				t.Errorf("expected stdout to contain %q, got: %q", want, out)
			}
		}
	})

	t.Run("check text-mode result is on stdout, not stderr", func(t *testing.T) {
		// Same regression as mine's text-mode case above.
		checkCmd := exec.Command(bin, "miner", "check", "-e", eventsArrayPath)
		out, err := checkCmd.Output()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(string(out), "checked 1, valid 1, invalid 0") {
			t.Fatalf("expected the check summary on stdout, got: %q", out)
		}
	})

	t.Run("json output shape", func(t *testing.T) {
		checkCmd := exec.Command(bin, "miner", "check", "-e", eventsArrayPath, "--json")
		out, err := checkCmd.Output()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var report struct {
			Checked int `json:"checked"`
			Valid   int `json:"valid"`
			Invalid int `json:"invalid"`
		}
		if err := json.Unmarshal(out, &report); err != nil {
			t.Fatalf("--json output is not valid JSON: %v\nraw: %s", err, out)
		}
		if report.Checked != 1 || report.Valid != 1 || report.Invalid != 0 {
			t.Fatalf("unexpected report: %+v", report)
		}
	})

	t.Run("invalid PoW exits 1", func(t *testing.T) {
		var event map[string]any
		if err := json.Unmarshal(minedData, &event); err != nil {
			t.Fatal(err)
		}
		event["id"] = strings.Repeat("f", 64) // deliberately wrong: won't match the recomputed hash
		badData, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		badPath := filepath.Join(dir, "bad_events.json")
		if err := os.WriteFile(badPath, []byte("["+string(badData)+"]"), 0644); err != nil {
			t.Fatal(err)
		}

		checkCmd := exec.Command(bin, "miner", "check", "-e", badPath)
		err = checkCmd.Run()
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("expected a *exec.ExitError (non-zero exit), got %v (nil means it exited 0)", err)
		}
		if exitErr.ExitCode() != 1 {
			t.Fatalf("expected exit code 1, got %d", exitErr.ExitCode())
		}
	})
}

// TestMinerMineRequiresOutOrInPlace proves `mine` refuses to run (and
// doesn't touch --event) when neither --out nor --in-place is given -- the
// no-silent-overwrite regression test at the process level.
func TestMinerMineRequiresOutOrInPlace(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()

	eventPath := filepath.Join(dir, "event.yaml")
	if err := os.WriteFile(eventPath, []byte(testMinerEventYAML), 0644); err != nil {
		t.Fatal(err)
	}

	mineCmd := exec.Command(bin, "miner", "mine", "-e", eventPath, "-d", "8")
	if err := mineCmd.Run(); err == nil {
		t.Fatal("expected an error when neither --out nor --in-place is given")
	}

	after, err := os.ReadFile(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != testMinerEventYAML {
		t.Error("event file was modified despite the command being rejected for missing --out/--in-place")
	}
}

// exitErrStderr extracts stderr from an *exec.ExitError (as populated by
// Cmd.Output(), unlike Cmd.CombinedOutput()'s merged stream) for a test
// failure message -- "" if err isn't an *exec.ExitError.
func exitErrStderr(err error) string {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return string(exitErr.Stderr)
	}
	return ""
}

// generateTestIdentity spawns `ncli id --json` to mint a fresh, never-saved
// keypair for a test -- process-level, matching this file's own style,
// rather than duplicating key-generation logic via nip19/utils directly.
func generateTestIdentity(t *testing.T, bin string) (nsec, npub string) {
	t.Helper()
	out, err := exec.Command(bin, "id", "--json").Output()
	if err != nil {
		t.Fatalf("failed to generate a test identity: %v", err)
	}
	var id struct {
		Nsec string `json:"nsec"`
		Npub string `json:"npub"`
	}
	if err := json.Unmarshal(out, &id); err != nil {
		t.Fatalf("ncli id --json output is not valid JSON: %v\nraw: %s", err, out)
	}
	return id.Nsec, id.Npub
}

// TestMinerMineEventAndContentMutuallyExclusive proves --event can't be
// combined with --content/--content-file -- a structured event file already
// declares kind/content/tags, so mixing in the inline-authoring flags is
// ambiguous rather than additive.
func TestMinerMineEventAndContentMutuallyExclusive(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()

	eventPath := filepath.Join(dir, "event.yaml")
	if err := os.WriteFile(eventPath, []byte(testMinerEventYAML), 0644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "mined.json")

	cmd := exec.Command(bin, "miner", "mine", "-e", eventPath, "--content", "hello", "-o", outPath, "-d", "4")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected an error when --event and --content are both given")
	}
}

// TestMinerMineContentAndContentFileMutuallyExclusive proves --content and
// --content-file can't be combined -- exactly one content source, not both.
func TestMinerMineContentAndContentFileMutuallyExclusive(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()

	contentFile := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(contentFile, []byte("hello from a file"), 0644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "mined.json")

	cmd := exec.Command(bin, "miner", "mine", "--content", "hello", "--content-file", contentFile, "-o", outPath, "-d", "4")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected an error when --content and --content-file are both given")
	}
}

// TestMinerMineRequiresEventOrContent proves mine refuses to run when none
// of --event/--content/--content-file are given -- there's no draft to mine.
func TestMinerMineRequiresEventOrContent(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "mined.json")

	cmd := exec.Command(bin, "miner", "mine", "-o", outPath, "-d", "4")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected an error when neither --event nor --content/--content-file is given")
	}
}

// TestMinerMineKindRejectedWithEvent proves --kind (content-mode only) is
// rejected alongside --event, whose file already declares its own kind.
func TestMinerMineKindRejectedWithEvent(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()

	eventPath := filepath.Join(dir, "event.yaml")
	if err := os.WriteFile(eventPath, []byte(testMinerEventYAML), 0644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "mined.json")

	cmd := exec.Command(bin, "miner", "mine", "-e", eventPath, "--kind", "7", "-o", outPath, "-d", "4")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected an error when --kind is combined with --event")
	}
}

// TestMinerMineInPlaceRequiresEvent proves --in-place is rejected in
// --content/--content-file mode -- there is no input file to overwrite.
func TestMinerMineInPlaceRequiresEvent(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)

	cmd := exec.Command(bin, "miner", "mine", "--content", "hello", "--in-place", "-d", "4")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected an error when --in-place is used without --event")
	}
}

// TestMinerMineContentModeRequiresIdentity proves --content/--content-file
// mode fails clearly when there's no --identity to source a pubkey from,
// rather than mining a event with a permanently empty pubkey.
func TestMinerMineContentModeRequiresIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "mined.json")

	cmd := exec.Command(bin, "miner", "mine", "--content", "hello", "-o", outPath, "-d", "4")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected an error when --content mode has no --identity to source a pubkey from")
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Error("expected no output file to be written when mining is rejected for missing --identity")
	}
}

// TestMinerMineContentModeAutoSignsWithNsecIdentity proves --content mode
// with an --identity that's a raw nsec mines AND signs in one call, with no
// vault/password involved -- ResolveIdentifier already has the private key
// directly for an nsec identity.
func TestMinerMineContentModeAutoSignsWithNsecIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "mined.json")

	nsec, _ := generateTestIdentity(t, bin)

	cmd := exec.Command(bin, "miner", "mine", "--content", "hello from a test", "--tag", "t=nostr",
		"--identity", nsec, "-d", "4", "-o", outPath, "--json")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("miner mine failed: %v\nstderr: %s", err, exitErrStderr(err))
	}

	var result struct {
		Signed bool `json:"signed"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\nraw: %s", err, out)
	}
	if !result.Signed {
		t.Error("expected \"signed\": true when --identity is a raw nsec")
	}

	var event struct {
		Sig     string     `json:"sig"`
		Kind    int        `json:"kind"`
		Tags    [][]string `json:"tags"`
		Content string     `json:"content"`
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("mined output is not valid JSON: %v", err)
	}
	if event.Sig == "" {
		t.Error("expected the output event's sig to be populated")
	}
	if event.Kind != 1 {
		t.Errorf("expected default kind 1, got %d", event.Kind)
	}
	if event.Content != "hello from a test" {
		t.Errorf("expected content %q, got %q", "hello from a test", event.Content)
	}

	// miner check must accept this single-object file directly (the
	// LoadEvents fallback), not just a JSON array.
	checkCmd := exec.Command(bin, "miner", "check", "-e", outPath, "--json")
	if checkOut, err := checkCmd.CombinedOutput(); err != nil {
		t.Fatalf("miner check on a single mined event object failed: %v\n%s", err, checkOut)
	}
}

// TestMinerMineContentModeUnsignedWithPubkeyOnlyIdentity proves --content
// mode with a pubkey-only identity (an npub that was never saved to the
// vault) still mines successfully, but leaves the event unsigned rather
// than erroring or silently pretending it signed.
func TestMinerMineContentModeUnsignedWithPubkeyOnlyIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "mined.json")

	_, npub := generateTestIdentity(t, bin)

	cmd := exec.Command(bin, "miner", "mine", "--content", "hello from a test",
		"--identity", npub, "-d", "4", "-o", outPath, "--json")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("miner mine failed: %v\nstderr: %s", err, exitErrStderr(err))
	}

	var result struct {
		Signed bool `json:"signed"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\nraw: %s", err, out)
	}
	if result.Signed {
		t.Error("expected \"signed\": false when --identity is a pubkey-only npub not saved in the vault")
	}

	var event struct {
		Sig string `json:"sig"`
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("mined output is not valid JSON: %v", err)
	}
	if event.Sig != "" {
		t.Errorf("expected an empty sig, got %q", event.Sig)
	}
}

// TestWithThousands covers the grouping helper behind mine's progress
// narration (hashes tried) -- in particular the boundary between a leading
// partial group and a leading full group of 3 digits, which is the easiest
// spot to get an off-by-one in the separator placement.
func TestWithThousands(t *testing.T) {
	cases := map[string]string{
		"0":            "0",
		"7":            "7",
		"42":           "42",
		"999":          "999",
		"1000":         "1,000",
		"1804600":      "1,804,600",
		"6014571":      "6,014,571",
		"100000000000": "100,000,000,000",
		"-1234":        "-1,234",
	}
	for in, want := range cases {
		if got := withThousands(in); got != want {
			t.Errorf("withThousands(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestHumanRate covers the unit-scaling helper behind mine's progress
// narration (h/s) -- in particular the boundaries right at each unit
// threshold, the easiest spot for an off-by-one between e.g. "999.99 KH/s"
// and "1.00 MH/s".
func TestHumanRate(t *testing.T) {
	cases := map[float64]string{
		0:             "0 H/s",
		7:             "7 H/s",
		999:           "999 H/s",
		1000:          "1.00 KH/s",
		6014571:       "6.01 MH/s",
		999_999_999:   "1000.00 MH/s",
		1_000_000_000: "1.00 GH/s",
	}
	for in, want := range cases {
		if got := humanRate(in); got != want {
			t.Errorf("humanRate(%v) = %q, want %q", in, got, want)
		}
	}
}
