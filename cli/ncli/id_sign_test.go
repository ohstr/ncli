package ncli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testSignConflictPubKey is an arbitrary 32-byte-hex pubkey (never a real
// signer here) used only to give a draft a pre-declared pubkey that's
// guaranteed to conflict with whatever identity a test signs with.
const testSignConflictPubKey = "3c1db3dd55e2ff09ba5317dd8eec2339797e9e2ddf74591172735c47f3a2ad6e"

func writeSignTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestIDSignSingleEvent proves the happy path: an nsec identity signs a
// lone unsigned event, the result is a single JSON object (not wrapped in
// an array), and its sig/pubkey/id are all populated and self-consistent.
func TestIDSignSingleEvent(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()
	nsec, npub := generateTestIdentity(t, bin)

	draftPath := writeSignTestFile(t, dir, "draft.json",
		`{"kind":1,"created_at":1719759720,"tags":[],"content":"hello from id sign"}`)
	outPath := filepath.Join(dir, "signed.json")

	cmd := exec.Command(bin, "id", "sign", "--identity", nsec, "-e", draftPath, "-o", outPath, "--json")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("id sign failed: %v\nstderr: %s", err, exitErrStderr(err))
	}

	var result struct {
		Pubkey string   `json:"pubkey"`
		IDs    []string `json:"ids"`
		Count  int      `json:"count"`
		Out    string   `json:"out"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\nraw: %s", err, out)
	}
	if result.Count != 1 || len(result.IDs) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Out != outPath {
		t.Errorf("out = %q, want %q", result.Out, outPath)
	}

	var event struct {
		ID      string `json:"id"`
		PubKey  string `json:"pubkey"`
		Sig     string `json:"sig"`
		Content string `json:"content"`
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatalf("signed output is not a single JSON object: %v\nraw: %s", err, raw)
	}
	if event.Sig == "" {
		t.Error("expected the output event's sig to be populated")
	}
	if event.ID != result.IDs[0] {
		t.Errorf("event id %q does not match reported id %q", event.ID, result.IDs[0])
	}
	if event.Content != "hello from id sign" {
		t.Errorf("content = %q, want %q", event.Content, "hello from id sign")
	}

	wantPubHex := npubToHex(t, bin, npub)
	if event.PubKey != wantPubHex {
		t.Errorf("event pubkey = %q, want %q (from identity %s)", event.PubKey, wantPubHex, npub)
	}
	if result.Pubkey != wantPubHex {
		t.Errorf("reported pubkey = %q, want %q", result.Pubkey, wantPubHex)
	}
}

// TestIDSignArrayRoundTrips proves an array of unsigned events comes back
// out as an array (not flattened to one object), every element signed.
func TestIDSignArrayRoundTrips(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()
	nsec, _ := generateTestIdentity(t, bin)

	draftPath := writeSignTestFile(t, dir, "drafts.json",
		`[{"kind":1,"created_at":1,"tags":[],"content":"a"},{"kind":1,"created_at":2,"tags":[],"content":"b"}]`)
	outPath := filepath.Join(dir, "signed.json")

	cmd := exec.Command(bin, "id", "sign", "--identity", nsec, "-e", draftPath, "-o", outPath, "--json")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("id sign failed: %v\n%s", err, out)
	}

	var events []struct {
		Sig     string `json:"sig"`
		Content string `json:"content"`
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatalf("signed output is not a JSON array: %v\nraw: %s", err, raw)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	for i, e := range events {
		if e.Sig == "" {
			t.Errorf("event %d: expected a populated sig", i)
		}
	}

	// The output must chain straight into "miner check --events" with no
	// reshaping -- that's the whole point of matching LoadEvents' shape.
	// These drafts were never PoW-mined, so "miner check" is expected to
	// report them invalid (exit non-zero) -- what this proves is that the
	// array shape itself was accepted and both elements were evaluated
	// individually, not rejected as a malformed/unparseable file.
	checkCmd := exec.Command(bin, "miner", "check", "-e", outPath, "--json")
	checkOut, _ := checkCmd.Output()
	var report struct {
		Checked int `json:"checked"`
	}
	if err := json.Unmarshal(checkOut, &report); err != nil {
		t.Fatalf("miner check did not accept id sign's array output as valid input shape: %v\nraw: %s", err, checkOut)
	}
	if report.Checked != 2 {
		t.Errorf("miner check saw %d events, want 2 -- the array shape wasn't preserved", report.Checked)
	}
}

// TestIDSignTextModeOnStdout proves the text-mode result (id/pubkey/count/
// out) lands on stdout, not stderr -- the same stdout-only-holds-the-result
// convention as "miner mine"/"miner check"/"publish".
func TestIDSignTextModeOnStdout(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()
	nsec, _ := generateTestIdentity(t, bin)

	draftPath := writeSignTestFile(t, dir, "draft.json",
		`{"kind":1,"created_at":1719759720,"tags":[],"content":"hello"}`)
	outPath := filepath.Join(dir, "signed.json")

	cmd := exec.Command(bin, "id", "sign", "--identity", nsec, "-e", draftPath, "-o", outPath)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("id sign failed: %v\nstderr: %s", err, exitErrStderr(err))
	}
	for _, want := range []string{"id:", "pubkey:", "count:", "out:"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("expected stdout to contain %q, got: %q", want, out)
		}
	}
}

// TestIDSignRejectsPubkeyOnlyIdentity proves an identity with no available
// private key (an npub never saved to the vault) is a hard AuthError, not a
// silently-unsigned result -- unlike "miner mine --identity", where an
// unsigned-but-mined result is still useful. Signing with no key at all
// isn't a partial result, so it must fail loudly instead.
func TestIDSignRejectsPubkeyOnlyIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()
	_, npub := generateTestIdentity(t, bin)

	draftPath := writeSignTestFile(t, dir, "draft.json",
		`{"kind":1,"created_at":1719759720,"tags":[],"content":"hello"}`)
	outPath := filepath.Join(dir, "signed.json")

	cmd := exec.Command(bin, "id", "sign", "--identity", npub, "-e", draftPath, "-o", outPath, "--json")
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected a *exec.ExitError (non-zero exit), got %v", err)
	}
	if exitErr.ExitCode() != 7 {
		t.Fatalf("expected exit code 7 (auth), got %d", exitErr.ExitCode())
	}
	if _, statErr := os.Stat(outPath); statErr == nil {
		t.Error("expected no output file to be written when signing is rejected")
	}
}

// TestIDSignRejectsConflictingPubkey proves an event that already declares
// a pubkey different from --identity's resolved pubkey is rejected rather
// than silently re-signed under a different key -- mirroring "miner mine
// --identity"'s own conflict guard (client/miner.go's Mine).
func TestIDSignRejectsConflictingPubkey(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()
	nsec, _ := generateTestIdentity(t, bin)

	draftPath := writeSignTestFile(t, dir, "draft.json",
		`{"pubkey":"`+testSignConflictPubKey+`","kind":1,"created_at":1719759720,"tags":[],"content":"hello"}`)
	outPath := filepath.Join(dir, "signed.json")

	cmd := exec.Command(bin, "id", "sign", "--identity", nsec, "-e", draftPath, "-o", outPath, "--json")
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected a *exec.ExitError (non-zero exit), got %v", err)
	}
	if exitErr.ExitCode() != 3 {
		t.Fatalf("expected exit code 3 (invalid_input), got %d", exitErr.ExitCode())
	}
	if _, statErr := os.Stat(outPath); statErr == nil {
		t.Error("expected no output file to be written when signing is rejected")
	}
}

// TestIDSignRejectsEmptyArray proves an empty events array is a usage
// mistake worth reporting, not a silent no-op success.
func TestIDSignRejectsEmptyArray(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()
	nsec, _ := generateTestIdentity(t, bin)

	draftPath := writeSignTestFile(t, dir, "empty.json", `[]`)
	outPath := filepath.Join(dir, "signed.json")

	cmd := exec.Command(bin, "id", "sign", "--identity", nsec, "-e", draftPath, "-o", outPath)
	if err := cmd.Run(); err == nil {
		t.Fatal("expected an error for an empty events array")
	}
}

// TestIDSignRequiresAllFlags proves --identity/--events/--out are all
// enforced as required, exiting with the usage code (2), not a help dump
// with exit 0 the way a bare group command used to (see AGENTS.md's
// RequireSubcommand pattern -- this is the same discipline for required
// flags on a leaf command).
func TestIDSignRequiresAllFlags(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()
	nsec, _ := generateTestIdentity(t, bin)
	draftPath := writeSignTestFile(t, dir, "draft.json",
		`{"kind":1,"created_at":1719759720,"tags":[],"content":"hello"}`)
	outPath := filepath.Join(dir, "signed.json")

	cases := map[string][]string{
		"missing --identity": {"id", "sign", "-e", draftPath, "-o", outPath},
		"missing --events":   {"id", "sign", "--identity", nsec, "-o", outPath},
		"missing --out":      {"id", "sign", "--identity", nsec, "-e", draftPath},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(bin, args...)
			err := cmd.Run()
			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("expected a *exec.ExitError (non-zero exit), got %v", err)
			}
			if exitErr.ExitCode() != 2 {
				t.Errorf("expected exit code 2 (usage), got %d", exitErr.ExitCode())
			}
		})
	}
}

// TestIDSignWithVaultIdentity proves signing works against a vault-saved
// label (not just a raw nsec), exercising the same resolveVaultPassword ->
// UnlockVaultIdentity -> DecryptVaultEntry sequence "miner mine --identity"
// already relies on, via NCLI_VAULT_PASSWORD (never prompts under --json).
func TestIDSignWithVaultIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	dir := t.TempDir()
	env := append(prefsTestEnv(t), "NCLI_VAULT_PASSWORD=test-password-123")

	saveCmd := exec.Command(bin, "id", "--save", "--label", "signer", "--json")
	saveCmd.Env = env
	saveOut, err := saveCmd.Output()
	if err != nil {
		t.Fatalf("failed to save a vault identity: %v\nstderr: %s", err, exitErrStderr(err))
	}
	var saved struct {
		PubHex string `json:"pub_hex"`
	}
	if err := json.Unmarshal(saveOut, &saved); err != nil {
		t.Fatalf("id --save --json output is not valid JSON: %v\nraw: %s", err, saveOut)
	}

	draftPath := writeSignTestFile(t, dir, "draft.json",
		`{"kind":1,"created_at":1719759720,"tags":[],"content":"vault-signed"}`)
	outPath := filepath.Join(dir, "signed.json")

	signCmd := exec.Command(bin, "id", "sign", "--identity", "signer", "-e", draftPath, "-o", outPath, "--json")
	signCmd.Env = env
	signOut, err := signCmd.Output()
	if err != nil {
		t.Fatalf("id sign against a vault label failed: %v\nstderr: %s", err, exitErrStderr(err))
	}

	var result struct {
		Pubkey string `json:"pubkey"`
	}
	if err := json.Unmarshal(signOut, &result); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\nraw: %s", err, signOut)
	}
	if result.Pubkey != saved.PubHex {
		t.Errorf("signed with pubkey %q, want the vault entry's %q", result.Pubkey, saved.PubHex)
	}

	var event struct {
		Sig string `json:"sig"`
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatalf("signed output is not valid JSON: %v", err)
	}
	if event.Sig == "" {
		t.Error("expected the output event's sig to be populated")
	}
}

// npubToHex resolves npub to its hex pubkey via "ncli decode --json", so
// tests can compare a signed event's pubkey field against the identity that
// produced it without duplicating NIP-19 decoding logic.
func npubToHex(t *testing.T, bin, npub string) string {
	t.Helper()
	out, err := exec.Command(bin, "decode", npub, "--json").Output()
	if err != nil {
		t.Fatalf("failed to decode %s: %v", npub, err)
	}
	var decoded struct {
		PubKeyHex string `json:"pub_hex"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("ncli decode --json output is not valid JSON: %v\nraw: %s", err, out)
	}
	return decoded.PubKeyHex
}
