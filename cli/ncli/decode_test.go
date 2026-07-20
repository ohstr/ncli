package ncli

import (
	"encoding/json"
	"os/exec"
	"testing"
)

// TestDecodeCmd_NpubJSON drives the real binary end to end: generate an
// identity via `ncli id --json`, decode its npub back via `ncli decode
// --json`, and confirm the round trip -- this is what buildTestBinary
// (miner_test.go) exists for: process-level behavior (actual stdout,
// actual exit codes) that calling RunE in-process can't exercise.
func TestDecodeCmd_NpubJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)

	idOut, err := exec.Command(bin, "id", "--json").Output()
	if err != nil {
		t.Fatalf("ncli id --json failed: %v", err)
	}
	var generated struct {
		Npub   string `json:"npub"`
		PubHex string `json:"pub_hex"`
	}
	if err := json.Unmarshal(idOut, &generated); err != nil {
		t.Fatalf("failed to parse ncli id --json output: %v\nraw: %s", err, idOut)
	}

	out, err := exec.Command(bin, "decode", generated.Npub, "--json").Output()
	if err != nil {
		t.Fatalf("ncli decode --json failed: %v", err)
	}
	var decoded struct {
		Type   string `json:"type"`
		PubHex string `json:"pub_hex"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("ncli decode --json output is not valid JSON: %v\nraw: %s", err, out)
	}
	if decoded.Type != "npub" {
		t.Fatalf("decoded.Type = %q, want npub", decoded.Type)
	}
	if decoded.PubHex != generated.PubHex {
		t.Fatalf("decoded.PubHex = %q, want %q", decoded.PubHex, generated.PubHex)
	}
}

// TestDecodeCmd_InvalidExitsNonZero proves a garbage argument fails the
// command (non-zero exit) instead of printing an empty/zero-value result.
func TestDecodeCmd_InvalidExitsNonZero(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)

	err := exec.Command(bin, "decode", "not-a-nip19-entity").Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected a *exec.ExitError for invalid input, got %v (nil means it exited 0)", err)
	}
	if exitErr.ExitCode() == 0 {
		t.Fatal("expected a non-zero exit code for invalid input")
	}
}

// TestDecodeCmd_WrongArgCount proves decode requires exactly one argument.
func TestDecodeCmd_WrongArgCount(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)

	if err := exec.Command(bin, "decode").Run(); err == nil {
		t.Fatal("expected an error when no argument is given")
	}
	if err := exec.Command(bin, "decode", "a", "b").Run(); err == nil {
		t.Fatal("expected an error when more than one argument is given")
	}
}
