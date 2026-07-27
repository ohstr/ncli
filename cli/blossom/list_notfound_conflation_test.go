package blossom

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ohstr/ncli/client"
)

// TestBlossomList_DisabledEndpointGetsDistinctCode is the regression test
// for followup issue ("`code: \"not_found\"` conflates \"resource doesn't
// exist\" with \"capability disabled on this server\"",
// integration/agent-eval/followup/issues.md): a real Blossom server with
// BUD-02 `list` disabled entirely (hzrd149/blossom-server) answers GET
// /list/<pubkey> with 404 and an X-Reason of "List endpoint is disabled on
// this server". classifyHTTPError (shared by list/download/rm/etc.) used
// to map any 404 to common.NotFoundError regardless of *why* it was
// 404'd, so this came back with the exact same code ("not_found", exit 4)
// as a genuine "this one specific blob doesn't exist" 404 (e.g. from
// `blossom download` against a real missing hash).
//
// Fix: list.go's classifyListError checks the 404's X-Reason for
// disabled-capability phrasing (disabledCapabilityReasonKeywords) before
// falling back to the shared classifyHTTPError, and returns the new
// common.CodeUnsupported (exit 8) instead. Deliberately narrow and
// best-effort -- BUD-01 defines X-Reason as diagnostic-only, not a
// control-flow signal, so a differently-worded server still falls back to
// not_found (covered below), and this heuristic is scoped to list.go
// alone, not the shared classifier every other blossom command also uses.
func TestBlossomList_DisabledEndpointGetsDistinctCode(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	identity, err := client.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	r := &ncliRunner{t: t, bin: buildTestBinary(t), env: blossomTestEnv(t), nsec: identity.Nsec}

	// Case 1: list disabled entirely, with the same X-Reason text the real
	// reference server uses.
	listDisabledServer := newFakeBlossomServer()
	defer listDisabledServer.Close()
	listDisabledServer.SetHooks(fakeServerHooks{ListDisabled: true})

	_, listErrOut, listCode := r.run("blossom", "list", "--server", listDisabledServer.URL, identity.PubKeyHex, "--json")
	if listCode != 8 {
		t.Errorf("disabled-list exit code = %d, want 8 (unsupported)", listCode)
	}
	var listPayload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(listErrOut), &listPayload); err != nil {
		t.Fatalf("stderr is not valid JSON: %v\nstderr: %s", err, listErrOut)
	}
	if listPayload.Code != "unsupported" {
		t.Errorf("disabled-list code = %q, want %q", listPayload.Code, "unsupported")
	}

	// Case 2: a genuine "this one specific blob doesn't exist" 404 (a real
	// download against a hash the server never stored) must still be
	// not_found, not swept up by the same heuristic.
	realServer := newFakeBlossomServer()
	defer realServer.Close()
	missingHash := strings.Repeat("0", 63) + "f"

	_, dlErrOut, dlCode := r.run("blossom", "download", missingHash, "--server", realServer.URL, "-o", "-", "--json")
	if dlCode != 4 {
		t.Errorf("genuine-missing-blob exit code = %d, want 4 (not_found)", dlCode)
	}
	var dlPayload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(dlErrOut), &dlPayload); err != nil {
		t.Fatalf("stderr is not valid JSON: %v\nstderr: %s", err, dlErrOut)
	}
	if dlPayload.Code != "not_found" {
		t.Errorf("genuine-missing-blob code = %q, want %q", dlPayload.Code, "not_found")
	}

	if listPayload.Code == dlPayload.Code {
		t.Errorf("disabled-capability and genuine-not-found reported the same code %q -- the two cases are no longer distinguishable", listPayload.Code)
	}

	// Case 3: a 404 from a server that phrases it differently (no
	// recognized keyword) must still fall back to not_found -- the
	// heuristic must not over-fire on every bare 404.
	genericServer := newFakeBlossomServer()
	defer genericServer.Close()
	genericServer.SetHooks(fakeServerHooks{FailStatus: 404})

	_, genErrOut, genCode := r.run("blossom", "list", "--server", genericServer.URL, identity.PubKeyHex, "--json")
	if genCode != 4 {
		t.Errorf("generic 404 list exit code = %d, want 4 (not_found, no recognized disabled-capability phrasing)", genCode)
	}
	var genPayload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(genErrOut), &genPayload); err != nil {
		t.Fatalf("stderr is not valid JSON: %v\nstderr: %s", err, genErrOut)
	}
	if genPayload.Code != "not_found" {
		t.Errorf("generic 404 list code = %q, want %q", genPayload.Code, "not_found")
	}
}
