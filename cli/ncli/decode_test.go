package ncli

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/ohstr/nmilat/utils"
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

// TestDecodeCmd_CircleHubJSON drives the real binary on a circlehub1...
// Circle Hub connection -- the NIP-CW counterpart to
// TestDecodeCmd_NpubJSON's NIP-19 round trip, proving the secret never
// makes it into the CLI's own JSON encoding (not just DecodeEntity's Go
// struct, which client/decode_test.go already covers in-process).
func TestDecodeCmd_CircleHubJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)

	pubkey := strings.Repeat("a", 64)
	conn, err := signedCircleHub(nipcw.CircleHubConnection{
		Pubkey:   pubkey,
		Secret:   strings.Repeat("b", 64),
		Relays:   []string{"wss://relay.example.com"},
		Label:    "test-hub",
		HasLabel: true,
	})
	if err != nil {
		t.Fatalf("nipcw.EncodeCircleHubConnection() error = %v", err)
	}

	out, err := exec.Command(bin, "decode", conn, "--json").Output()
	if err != nil {
		t.Fatalf("ncli decode --json failed: %v", err)
	}
	if strings.Contains(string(out), strings.Repeat("b", 64)) {
		t.Fatalf("decode output leaked the pairing secret: %s", out)
	}
	var decoded struct {
		Type   string `json:"type"`
		PubHex string `json:"pub_hex"`
		Label  string `json:"label"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("ncli decode --json output is not valid JSON: %v\nraw: %s", err, out)
	}
	if decoded.Type != "circlehub" {
		t.Fatalf("decoded.Type = %q, want circlehub", decoded.Type)
	}
	if decoded.PubHex != pubkey {
		t.Fatalf("decoded.PubHex = %q, want %q", decoded.PubHex, pubkey)
	}
	if decoded.Label != "test-hub" {
		t.Fatalf("decoded.Label = %q, want test-hub", decoded.Label)
	}
}

// TestDecodeCmd_CashTokenJSON is the cash-token-family counterpart -- same
// secret-never-leaks guarantee, plus the mint-provenance pair round trip.
func TestDecodeCmd_CashTokenJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)

	pubkey := strings.Repeat("a", 64)
	amount := uint64(5000)
	tok, err := signedCashToken(nipcash.Token{
		HRP:                  "lokicash",
		WalletPubkey:         pubkey,
		Secret:               strings.Repeat("b", 64),
		RelayURLs:            []string{"wss://relay.example.com"},
		AttestedAmountMillis: &amount,
	})
	if err != nil {
		t.Fatalf("signedCashToken() error = %v", err)
	}

	out, err := exec.Command(bin, "decode", tok, "--json").Output()
	if err != nil {
		t.Fatalf("ncli decode --json failed: %v", err)
	}
	if strings.Contains(string(out), strings.Repeat("b", 64)) {
		t.Fatalf("decode output leaked the pairing secret: %s", out)
	}
	var decoded struct {
		Type                 string `json:"type"`
		HRP                  string `json:"hrp"`
		PubHex               string `json:"pub_hex"`
		AttestedAmountMillis uint64 `json:"attested_amount_millis"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("ncli decode --json output is not valid JSON: %v\nraw: %s", err, out)
	}
	if decoded.Type != "cash_token" {
		t.Fatalf("decoded.Type = %q, want cash_token", decoded.Type)
	}
	if decoded.HRP != "lokicash" {
		t.Fatalf("decoded.HRP = %q, want lokicash", decoded.HRP)
	}
	if decoded.PubHex != pubkey {
		t.Fatalf("decoded.PubHex = %q, want %q", decoded.PubHex, pubkey)
	}
	if decoded.AttestedAmountMillis != amount {
		t.Fatalf("decoded.AttestedAmountMillis = %d, want %d", decoded.AttestedAmountMillis, amount)
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

// signedCircleHub signs c as a Hub whose node key is 1 on bitcoin: since nmilat
// v0.5.0-rc.45 a circlehub1 string carries TLV 4-6 (the Hub's identity, the
// connection signature, the chain) and the encoder refuses an unsigned one.
func signedCircleHub(c nipcw.CircleHubConnection) (string, error) {
	const nodeKey = "0000000000000000000000000000000000000000000000000000000000000001"
	const identity = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	c.HubIdentity, c.Chain = identity, "bitcoin"
	sign, err := nipcash.LocalChainSigner(nodeKey, "bitcoin")
	if err != nil {
		return "", err
	}
	if c, err = nipcw.SignCircleHubConnection(c, sign, identity); err != nil {
		return "", err
	}
	return nipcw.EncodeCircleHubConnection(c)
}

// signedCashToken signs t as a mint whose node key is 0x01…01 (scheme 1): since
// nmilat v0.5.0-rc.45 nipcash.Encode refuses a token without its mint
// signature, attested amount and signature scheme.
func signedCashToken(t nipcash.Token) (string, error) {
	const minterKey = "0101010101010101010101010101010101010101010101010101010101010101"
	sign, err := nipcash.LocalMintSigner(minterKey, t.HRP)
	if err != nil {
		return "", err
	}
	minter, err := utils.GetPublicKey(minterKey)
	if err != nil {
		return "", err
	}
	if t, err = nipcash.SignToken(t, nipcash.SchemeMintV1, nil, minter, sign); err != nil {
		return "", err
	}
	return nipcash.Encode(t)
}
