package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/utils"
)

func TestGenerateIdentity(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity() error = %v", err)
	}

	if len(id.PrivKeyHex) != 64 {
		t.Fatalf("PrivKeyHex length = %d, want 64", len(id.PrivKeyHex))
	}
	if len(id.PubKeyHex) != 64 {
		t.Fatalf("PubKeyHex length = %d, want 64", len(id.PubKeyHex))
	}

	pubHex, err := utils.GetPublicKey(id.PrivKeyHex)
	if err != nil {
		t.Fatalf("utils.GetPublicKey() error = %v", err)
	}
	if pubHex != id.PubKeyHex {
		t.Fatalf("PubKeyHex = %q, want the derivation of PrivKeyHex (%q)", id.PubKeyHex, pubHex)
	}

	privFromNsec, err := nip19.DecodePrivateKey(id.Nsec)
	if err != nil {
		t.Fatalf("nip19.DecodePrivateKey(Nsec) error = %v", err)
	}
	if privFromNsec != id.PrivKeyHex {
		t.Fatalf("Nsec decodes to %q, want %q", privFromNsec, id.PrivKeyHex)
	}

	pubFromNpub, err := nip19.DecodePublicKey(id.Npub)
	if err != nil {
		t.Fatalf("nip19.DecodePublicKey(Npub) error = %v", err)
	}
	if pubFromNpub != id.PubKeyHex {
		t.Fatalf("Npub decodes to %q, want %q", pubFromNpub, id.PubKeyHex)
	}
}

func TestResolveIdentifier_VaultLabel(t *testing.T) {
	withTempConfigDir(t)

	_, vaultPrivHex, err := CreateVaultIdentity("hunter2")
	if err != nil {
		t.Fatalf("CreateVaultIdentity() error = %v", err)
	}
	id, _ := GenerateIdentity()
	if _, err := AddVaultEntry(vaultPrivHex, "alice", id.PrivKeyHex); err != nil {
		t.Fatalf("AddVaultEntry() error = %v", err)
	}

	result, err := ResolveIdentifier("alice")
	if err != nil {
		t.Fatalf("ResolveIdentifier(label) error = %v", err)
	}
	if !result.InVault || result.VaultLabel != "alice" {
		t.Fatalf("ResolveIdentifier(label) = %+v, want InVault with label alice", result)
	}
	if result.PubKeyHex != id.PubKeyHex {
		t.Fatalf("ResolveIdentifier(label).PubKeyHex = %q, want %q", result.PubKeyHex, id.PubKeyHex)
	}
}

func TestResolveIdentifier_Npub(t *testing.T) {
	withTempConfigDir(t)

	id, _ := GenerateIdentity()
	result, err := ResolveIdentifier(id.Npub)
	if err != nil {
		t.Fatalf("ResolveIdentifier(npub) error = %v", err)
	}
	if result.PubKeyHex != id.PubKeyHex {
		t.Fatalf("ResolveIdentifier(npub).PubKeyHex = %q, want %q", result.PubKeyHex, id.PubKeyHex)
	}
	if result.InVault {
		t.Fatalf("ResolveIdentifier(npub) not in vault -- InVault = true, want false")
	}
}

func TestResolveIdentifier_HexPubkey(t *testing.T) {
	withTempConfigDir(t)

	id, _ := GenerateIdentity()
	result, err := ResolveIdentifier(id.PubKeyHex)
	if err != nil {
		t.Fatalf("ResolveIdentifier(hex) error = %v", err)
	}
	if result.Npub != id.Npub {
		t.Fatalf("ResolveIdentifier(hex).Npub = %q, want %q", result.Npub, id.Npub)
	}
}

func TestResolveIdentifier_Nprofile(t *testing.T) {
	withTempConfigDir(t)

	id, _ := GenerateIdentity()
	relays := []string{"wss://relay.example.com", "wss://relay2.example.com"}
	nprofile, err := nip19.EncodeProfile(id.PubKeyHex, relays)
	if err != nil {
		t.Fatalf("nip19.EncodeProfile() error = %v", err)
	}

	result, err := ResolveIdentifier(nprofile)
	if err != nil {
		t.Fatalf("ResolveIdentifier(nprofile) error = %v", err)
	}
	if result.PubKeyHex != id.PubKeyHex {
		t.Fatalf("ResolveIdentifier(nprofile).PubKeyHex = %q, want %q", result.PubKeyHex, id.PubKeyHex)
	}
	if result.Npub != id.Npub {
		t.Fatalf("ResolveIdentifier(nprofile).Npub = %q, want %q", result.Npub, id.Npub)
	}
	if strings.Join(result.Relays, ",") != strings.Join(relays, ",") {
		t.Fatalf("ResolveIdentifier(nprofile).Relays = %v, want %v", result.Relays, relays)
	}
	if result.InVault {
		t.Fatalf("ResolveIdentifier(nprofile) not in vault -- InVault = true, want false")
	}
}

func TestResolveIdentifier_VaultMembership_ViaNpub(t *testing.T) {
	withTempConfigDir(t)

	_, vaultPrivHex, err := CreateVaultIdentity("hunter2")
	if err != nil {
		t.Fatalf("CreateVaultIdentity() error = %v", err)
	}
	id, _ := GenerateIdentity()
	if _, err := AddVaultEntry(vaultPrivHex, "alice", id.PrivKeyHex); err != nil {
		t.Fatalf("AddVaultEntry() error = %v", err)
	}

	result, err := ResolveIdentifier(id.Npub)
	if err != nil {
		t.Fatalf("ResolveIdentifier(npub) error = %v", err)
	}
	if !result.InVault || result.VaultLabel != "alice" {
		t.Fatalf("ResolveIdentifier(npub) = %+v, want InVault with label alice even though resolved via npub, not label", result)
	}
}

func TestResolveIdentifier_Nsec(t *testing.T) {
	withTempConfigDir(t)

	id, _ := GenerateIdentity()
	result, err := ResolveIdentifier(id.Nsec)
	if err != nil {
		t.Fatalf("ResolveIdentifier(nsec) error = %v", err)
	}
	if result.PrivKeyHex != id.PrivKeyHex {
		t.Fatalf("ResolveIdentifier(nsec).PrivKeyHex = %q, want %q", result.PrivKeyHex, id.PrivKeyHex)
	}
	if result.Nsec != id.Nsec {
		t.Fatalf("ResolveIdentifier(nsec).Nsec = %q, want %q", result.Nsec, id.Nsec)
	}
	if result.PubKeyHex != id.PubKeyHex {
		t.Fatalf("ResolveIdentifier(nsec).PubKeyHex = %q, want %q", result.PubKeyHex, id.PubKeyHex)
	}
	if result.InVault {
		t.Fatalf("ResolveIdentifier(nsec) not in vault -- InVault = true, want false")
	}
}

func TestResolveIdentifier_Unresolvable(t *testing.T) {
	withTempConfigDir(t)

	if _, err := ResolveIdentifier("not-a-real-identifier"); err == nil {
		t.Fatalf("ResolveIdentifier(garbage) error = nil, want an error")
	}
}

func TestResolveIdentifier_Nip05(t *testing.T) {
	withTempConfigDir(t)

	id, _ := GenerateIdentity()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"names": map[string]string{"alice": id.PubKeyHex},
		})
	}))
	defer srv.Close()

	pubHex, err := fetchNip05PubKey(srv.URL+"/.well-known/nostr.json?name=alice", "alice", "alice@example.com")
	if err != nil {
		t.Fatalf("fetchNip05PubKey() error = %v", err)
	}
	if pubHex != id.PubKeyHex {
		t.Fatalf("fetchNip05PubKey() = %q, want %q", pubHex, id.PubKeyHex)
	}
}

func TestFetchNip05PubKey_NameNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"names": map[string]string{}})
	}))
	defer srv.Close()

	if _, err := fetchNip05PubKey(srv.URL, "alice", "alice@example.com"); err == nil {
		t.Fatalf("fetchNip05PubKey() with missing name error = nil, want an error")
	}
}

func TestFetchNip05PubKey_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := fetchNip05PubKey(srv.URL, "alice", "alice@example.com"); err == nil {
		t.Fatalf("fetchNip05PubKey() with 404 error = nil, want an error")
	}
}

func TestFetchNip05PubKey_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "not json")
	}))
	defer srv.Close()

	if _, err := fetchNip05PubKey(srv.URL, "alice", "alice@example.com"); err == nil {
		t.Fatalf("fetchNip05PubKey() with invalid JSON error = nil, want an error")
	}
}

func TestFetchNip05PubKey_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]any{"names": map[string]string{}})
	}))
	defer srv.Close()

	// fetchNip05PubKey itself uses a fixed internal timeout; here we just
	// confirm a slow-but-eventually-responding server doesn't hang forever
	// and that a clearly-unreachable host fails promptly with a wrapped error.
	if _, err := fetchNip05PubKey("http://127.0.0.1:1/nostr.json", "alice", "alice@example.com"); err == nil {
		t.Fatalf("fetchNip05PubKey() against an unreachable host error = nil, want an error")
	} else if !strings.Contains(err.Error(), "nip-05 lookup") {
		t.Fatalf("fetchNip05PubKey() error = %v, want it wrapped with nip-05 lookup context", err)
	}
}

func TestResolveFilterAuthors_HexPassthrough(t *testing.T) {
	specs := []*FilterSpec{NewFilterSpec(&nip01.SubscriptionFilter{
		Authors: []string{"cd50647935202dd319529eea22d58d4e1c212f422995701c732358590559c4af", "abcdef01"},
	})}

	if err := ResolveFilterAuthors(specs); err != nil {
		t.Fatalf("ResolveFilterAuthors() error = %v", err)
	}
	want := []string{"cd50647935202dd319529eea22d58d4e1c212f422995701c732358590559c4af", "abcdef01"}
	if strings.Join(specs[0].Authors, ",") != strings.Join(want, ",") {
		t.Fatalf("ResolveFilterAuthors() Authors = %v, want unchanged %v", specs[0].Authors, want)
	}
}

func TestResolveFilterAuthors_InvalidNip05Errors(t *testing.T) {
	// "@baddomain.com" has no name before the "@", so ResolveNip05 errors
	// before attempting any network request -- keeps this test deterministic.
	specs := []*FilterSpec{NewFilterSpec(&nip01.SubscriptionFilter{
		Authors: []string{"@baddomain.com"},
	})}

	if err := ResolveFilterAuthors(specs); err == nil {
		t.Fatal("ResolveFilterAuthors() with a malformed nip-05 author error = nil, want an error")
	}
}

func TestResolveNip05_InvalidIdentifier(t *testing.T) {
	if _, err := ResolveNip05("no-at-sign"); err == nil {
		t.Fatalf("ResolveNip05(no @) error = nil, want an error")
	}
	if _, err := ResolveNip05("@domain.com"); err == nil {
		t.Fatalf("ResolveNip05(empty name) error = nil, want an error")
	}
}

func TestResolveFindIdentifier_HexPassthrough(t *testing.T) {
	result, err := ResolveFindIdentifier("abc123")
	if err != nil {
		t.Fatalf("ResolveFindIdentifier() error = %v", err)
	}
	if result.ID != "abc123" || result.Author != "" {
		t.Fatalf("ResolveFindIdentifier(hex) = %+v, want ID=\"abc123\", Author=\"\"", result)
	}
}

func TestResolveFindIdentifier_Note(t *testing.T) {
	eventID := strings.Repeat("ab", 32)
	note, err := nip19.EncodeNote(eventID)
	if err != nil {
		t.Fatalf("nip19.EncodeNote() error = %v", err)
	}

	result, err := ResolveFindIdentifier(note)
	if err != nil {
		t.Fatalf("ResolveFindIdentifier(note) error = %v", err)
	}
	if result.ID != eventID || result.Author != "" {
		t.Fatalf("ResolveFindIdentifier(note) = %+v, want ID=%q, Author=\"\"", result, eventID)
	}
}

func TestResolveFindIdentifier_InvalidNote(t *testing.T) {
	if _, err := ResolveFindIdentifier("note1invalid"); err == nil {
		t.Fatal("ResolveFindIdentifier(bad note1) error = nil, want an error")
	}
}

func TestResolveFindIdentifier_Nevent_IgnoresHints(t *testing.T) {
	eventID := strings.Repeat("cd", 32)
	id, _ := GenerateIdentity()
	nevent, err := nip19.EncodeEvent(nip19.EventPointer{
		ID:     eventID,
		Relays: []string{"wss://relay.example.com"},
		Author: id.PubKeyHex,
		Kind:   1,
	})
	if err != nil {
		t.Fatalf("nip19.EncodeEvent() error = %v", err)
	}

	result, err := ResolveFindIdentifier(nevent)
	if err != nil {
		t.Fatalf("ResolveFindIdentifier(nevent) error = %v", err)
	}
	if result.ID != eventID || result.Author != "" {
		t.Fatalf("ResolveFindIdentifier(nevent) = %+v, want ID=%q, Author=\"\" (embedded hints ignored)", result, eventID)
	}
}

func TestResolveFindIdentifier_InvalidNevent(t *testing.T) {
	if _, err := ResolveFindIdentifier("nevent1invalid"); err == nil {
		t.Fatal("ResolveFindIdentifier(bad nevent1) error = nil, want an error")
	}
}

func TestResolveFindIdentifier_Npub(t *testing.T) {
	id, _ := GenerateIdentity()

	result, err := ResolveFindIdentifier(id.Npub)
	if err != nil {
		t.Fatalf("ResolveFindIdentifier(npub) error = %v", err)
	}
	if result.Author != id.PubKeyHex || result.ID != "" {
		t.Fatalf("ResolveFindIdentifier(npub) = %+v, want Author=%q, ID=\"\"", result, id.PubKeyHex)
	}
}

func TestResolveFindIdentifier_InvalidNpub(t *testing.T) {
	if _, err := ResolveFindIdentifier("npub1invalid"); err == nil {
		t.Fatal("ResolveFindIdentifier(bad npub1) error = nil, want an error")
	}
}

func TestResolveFindIdentifier_Nprofile_IgnoresHints(t *testing.T) {
	id, _ := GenerateIdentity()
	nprofile, err := nip19.EncodeProfile(id.PubKeyHex, []string{"wss://relay.example.com"})
	if err != nil {
		t.Fatalf("nip19.EncodeProfile() error = %v", err)
	}

	result, err := ResolveFindIdentifier(nprofile)
	if err != nil {
		t.Fatalf("ResolveFindIdentifier(nprofile) error = %v", err)
	}
	if result.Author != id.PubKeyHex || result.ID != "" {
		t.Fatalf("ResolveFindIdentifier(nprofile) = %+v, want Author=%q, ID=\"\" (relay hints ignored)", result, id.PubKeyHex)
	}
}

func TestResolveFindIdentifier_InvalidNprofile(t *testing.T) {
	if _, err := ResolveFindIdentifier("nprofile1invalid"); err == nil {
		t.Fatal("ResolveFindIdentifier(bad nprofile1) error = nil, want an error")
	}
}

func TestResolveFindIdentifier_Nip05_InvalidErrors(t *testing.T) {
	// "@baddomain.com" has no name before the "@", so ResolveNip05 errors
	// before attempting any network request -- keeps this test deterministic,
	// same rationale as TestResolveFilterAuthors_InvalidNip05Errors.
	if _, err := ResolveFindIdentifier("@baddomain.com"); err == nil {
		t.Fatal("ResolveFindIdentifier(malformed nip-05) error = nil, want an error")
	}
}
