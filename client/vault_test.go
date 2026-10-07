package client

import (
	"errors"
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/utils"
)

func TestVaultExists_NoVault(t *testing.T) {
	withTempConfigDir(t)

	exists, err := VaultExists()
	if err != nil {
		t.Fatalf("VaultExists() error = %v", err)
	}
	if exists {
		t.Fatalf("VaultExists() on a fresh install = true, want false")
	}
}

func TestCreateVaultIdentity_RoundTrip(t *testing.T) {
	withTempConfigDir(t)

	npub, privHex, err := CreateVaultIdentity("hunter2")
	if err != nil {
		t.Fatalf("CreateVaultIdentity() error = %v", err)
	}
	if npub == "" || privHex == "" {
		t.Fatalf("CreateVaultIdentity() returned empty npub/privHex")
	}

	exists, err := VaultExists()
	if err != nil {
		t.Fatalf("VaultExists() error = %v", err)
	}
	if !exists {
		t.Fatalf("VaultExists() after create = false, want true")
	}

	prefs, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() error = %v", err)
	}
	if prefs.VaultIdentity == nil {
		t.Fatalf("prefs.VaultIdentity = nil after create")
	}
	if prefs.VaultIdentity.Npub != npub {
		t.Fatalf("prefs.VaultIdentity.Npub = %q, want %q", prefs.VaultIdentity.Npub, npub)
	}
	if !strings.HasPrefix(prefs.VaultIdentity.EncryptedNsec, "ncryptsec1") {
		t.Fatalf("prefs.VaultIdentity.EncryptedNsec = %q, want ncryptsec1 prefix", prefs.VaultIdentity.EncryptedNsec)
	}
}

func TestCreateVaultIdentity_AlreadyExists(t *testing.T) {
	withTempConfigDir(t)

	if _, _, err := CreateVaultIdentity("hunter2"); err != nil {
		t.Fatalf("first CreateVaultIdentity() error = %v", err)
	}
	if _, _, err := CreateVaultIdentity("hunter2"); err == nil {
		t.Fatalf("second CreateVaultIdentity() error = nil, want an error")
	}
}

func TestUnlockVaultIdentity_RoundTrip(t *testing.T) {
	withTempConfigDir(t)

	npub, privHex, err := CreateVaultIdentity("hunter2")
	if err != nil {
		t.Fatalf("CreateVaultIdentity() error = %v", err)
	}

	unlockedHex, err := UnlockVaultIdentity("hunter2")
	if err != nil {
		t.Fatalf("UnlockVaultIdentity() error = %v", err)
	}
	if unlockedHex != privHex {
		t.Fatalf("UnlockVaultIdentity() = %q, want %q", unlockedHex, privHex)
	}

	pubHex, err := utils.GetPublicKey(unlockedHex)
	if err != nil {
		t.Fatalf("derive pubkey error = %v", err)
	}
	gotNpub, err := nip19.EncodePublicKey(pubHex)
	if err != nil {
		t.Fatalf("encode npub error = %v", err)
	}
	if gotNpub != npub {
		t.Fatalf("derived npub = %q, want %q", gotNpub, npub)
	}
}

func TestUnlockVaultIdentity_WrongPassword(t *testing.T) {
	withTempConfigDir(t)

	if _, _, err := CreateVaultIdentity("hunter2"); err != nil {
		t.Fatalf("CreateVaultIdentity() error = %v", err)
	}

	if _, err := UnlockVaultIdentity("wrong-password"); err == nil {
		t.Fatalf("UnlockVaultIdentity() with wrong password error = nil, want an error")
	}
}

func TestUnlockVaultIdentity_NoVault(t *testing.T) {
	withTempConfigDir(t)

	if _, err := UnlockVaultIdentity("hunter2"); err == nil {
		t.Fatalf("UnlockVaultIdentity() with no vault error = nil, want an error")
	}
}

func TestAddVaultEntry_And_DecryptRoundTrip(t *testing.T) {
	withTempConfigDir(t)

	_, vaultPrivHex, err := CreateVaultIdentity("hunter2")
	if err != nil {
		t.Fatalf("CreateVaultIdentity() error = %v", err)
	}

	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity() error = %v", err)
	}

	entry, err := AddVaultEntry(vaultPrivHex, "alice", id.PrivKeyHex)
	if err != nil {
		t.Fatalf("AddVaultEntry() error = %v", err)
	}
	if entry.Label != "alice" {
		t.Fatalf("entry.Label = %q, want %q", entry.Label, "alice")
	}
	if entry.Npub != id.Npub {
		t.Fatalf("entry.Npub = %q, want %q", entry.Npub, id.Npub)
	}

	decrypted, err := DecryptVaultEntry(vaultPrivHex, *entry)
	if err != nil {
		t.Fatalf("DecryptVaultEntry() error = %v", err)
	}
	if decrypted != id.PrivKeyHex {
		t.Fatalf("DecryptVaultEntry() = %q, want %q", decrypted, id.PrivKeyHex)
	}
}

func TestAddVaultEntry_BlankLabelDefaultsToNpub(t *testing.T) {
	withTempConfigDir(t)

	_, vaultPrivHex, err := CreateVaultIdentity("hunter2")
	if err != nil {
		t.Fatalf("CreateVaultIdentity() error = %v", err)
	}

	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity() error = %v", err)
	}

	entry, err := AddVaultEntry(vaultPrivHex, "  ", id.PrivKeyHex)
	if err != nil {
		t.Fatalf("AddVaultEntry() with blank label error = %v, want success", err)
	}
	if entry.Label != id.Npub {
		t.Fatalf("entry.Label = %q, want fallback to npub %q", entry.Label, id.Npub)
	}
}

func TestAddVaultEntry_DuplicateLabel(t *testing.T) {
	withTempConfigDir(t)

	_, vaultPrivHex, err := CreateVaultIdentity("hunter2")
	if err != nil {
		t.Fatalf("CreateVaultIdentity() error = %v", err)
	}

	id1, _ := GenerateIdentity()
	id2, _ := GenerateIdentity()

	if _, err := AddVaultEntry(vaultPrivHex, "alice", id1.PrivKeyHex); err != nil {
		t.Fatalf("first AddVaultEntry() error = %v", err)
	}
	if _, err := AddVaultEntry(vaultPrivHex, "ALICE", id2.PrivKeyHex); err == nil {
		t.Fatalf("second AddVaultEntry() with case-different duplicate label error = nil, want an error")
	}
}

func TestAddVaultEntry_DuplicateKey(t *testing.T) {
	withTempConfigDir(t)

	_, vaultPrivHex, err := CreateVaultIdentity("hunter2")
	if err != nil {
		t.Fatalf("CreateVaultIdentity() error = %v", err)
	}

	id, _ := GenerateIdentity()
	if _, err := AddVaultEntry(vaultPrivHex, "alice", id.PrivKeyHex); err != nil {
		t.Fatalf("first AddVaultEntry() error = %v", err)
	}
	_, err = AddVaultEntry(vaultPrivHex, "bob", id.PrivKeyHex)
	if !errors.Is(err, ErrKeyExists) {
		t.Fatalf("AddVaultEntry() same key, new label error = %v, want ErrKeyExists", err)
	}
	entries, _ := LoadVaultEntries()
	if len(entries) != 1 {
		t.Fatalf("vault holds %d entries, want 1", len(entries))
	}
}

func TestRelabelVaultEntry(t *testing.T) {
	withTempConfigDir(t)

	_, vaultPrivHex, err := CreateVaultIdentity("hunter2")
	if err != nil {
		t.Fatalf("CreateVaultIdentity() error = %v", err)
	}
	id1, _ := GenerateIdentity()
	id2, _ := GenerateIdentity()
	if _, err := AddVaultEntry(vaultPrivHex, "alice", id1.PrivKeyHex); err != nil {
		t.Fatal(err)
	}
	if _, err := AddVaultEntry(vaultPrivHex, "bob", id2.PrivKeyHex); err != nil {
		t.Fatal(err)
	}

	if _, err := RelabelVaultEntry(id1.Npub, "BOB"); !errors.Is(err, ErrLabelExists) {
		t.Fatalf("RelabelVaultEntry() onto a taken label error = %v, want ErrLabelExists", err)
	}

	entry, err := RelabelVaultEntry(id1.Npub, "carol")
	if err != nil || entry.Label != "carol" {
		t.Fatalf("RelabelVaultEntry() = (%+v, %v), want label carol", entry, err)
	}
	got, found, _ := FindVaultEntry("carol")
	if !found || got.Npub != id1.Npub {
		t.Fatalf("FindVaultEntry(carol) = (%+v, %v), want id1", got, found)
	}
	privHex, err := DecryptVaultEntry(vaultPrivHex, *got)
	if err != nil || privHex != id1.PrivKeyHex {
		t.Fatalf("DecryptVaultEntry() after relabel = (%q, %v), want id1's key", privHex, err)
	}
}

func TestRemoveVaultEntry(t *testing.T) {
	withTempConfigDir(t)

	_, vaultPrivHex, err := CreateVaultIdentity("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]*Identity, 3)
	for i, label := range []string{"a", "b", "c"} {
		ids[i], _ = GenerateIdentity()
		if _, err := AddVaultEntry(vaultPrivHex, label, ids[i].PrivKeyHex); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := RemoveVaultEntry(ids[1].Npub)
	if err != nil || removed.Label != "b" {
		t.Fatalf("RemoveVaultEntry() = (%+v, %v), want b", removed, err)
	}
	entries, _ := LoadVaultEntries()
	if len(entries) != 2 || entries[0].Label != "a" || entries[1].Label != "c" {
		t.Fatalf("entries after remove = %+v, want a, c", entries)
	}
	if _, err := RemoveVaultEntry(ids[1].Npub); err == nil {
		t.Fatal("second RemoveVaultEntry() error = nil")
	}
	// The removed key can be saved again.
	if _, err := AddVaultEntry(vaultPrivHex, "b", ids[1].PrivKeyHex); err != nil {
		t.Fatalf("re-add after remove: %v", err)
	}
}

func TestIdentityFromPrivKey(t *testing.T) {
	id, _ := GenerateIdentity()
	got, err := IdentityFromPrivKey(id.PrivKeyHex)
	if err != nil || *got != *id {
		t.Fatalf("IdentityFromPrivKey() = (%+v, %v), want %+v", got, err, id)
	}

	for _, bad := range []string{
		"",
		"zz",
		strings.Repeat("0", 64),
		strings.Repeat("f", 64), // >= curve order
		strings.Repeat("1", 62),
	} {
		if _, err := IdentityFromPrivKey(bad); err == nil {
			t.Errorf("IdentityFromPrivKey(%q) error = nil, want an error", bad)
		}
	}
}

func TestFindVaultEntry_ByLabelAndByNpub(t *testing.T) {
	withTempConfigDir(t)

	_, vaultPrivHex, err := CreateVaultIdentity("hunter2")
	if err != nil {
		t.Fatalf("CreateVaultIdentity() error = %v", err)
	}

	id, _ := GenerateIdentity()
	if _, err := AddVaultEntry(vaultPrivHex, "alice", id.PrivKeyHex); err != nil {
		t.Fatalf("AddVaultEntry() error = %v", err)
	}

	entry, found, err := FindVaultEntry("alice")
	if err != nil {
		t.Fatalf("FindVaultEntry(label) error = %v", err)
	}
	if !found || entry.Npub != id.Npub {
		t.Fatalf("FindVaultEntry(label) = (%+v, %v), want the alice entry", entry, found)
	}

	entry, found, err = FindVaultEntry(id.Npub)
	if err != nil {
		t.Fatalf("FindVaultEntry(npub) error = %v", err)
	}
	if !found || entry.Label != "alice" {
		t.Fatalf("FindVaultEntry(npub) = (%+v, %v), want the alice entry", entry, found)
	}

	entry, found, err = FindVaultEntry(id.PubKeyHex)
	if err != nil {
		t.Fatalf("FindVaultEntry(hex) error = %v", err)
	}
	if !found || entry.Label != "alice" {
		t.Fatalf("FindVaultEntry(hex) = (%+v, %v), want the alice entry", entry, found)
	}

	_, found, err = FindVaultEntry("nonexistent")
	if err != nil {
		t.Fatalf("FindVaultEntry(nonexistent) error = %v", err)
	}
	if found {
		t.Fatalf("FindVaultEntry(nonexistent) found = true, want false")
	}
}

func TestLoadVaultEntries_MissingFile(t *testing.T) {
	withTempConfigDir(t)

	entries, err := LoadVaultEntries()
	if err != nil {
		t.Fatalf("LoadVaultEntries() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("LoadVaultEntries() on a fresh install = %v, want empty", entries)
	}
}
