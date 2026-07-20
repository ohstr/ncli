package client

import (
	"os"
	"path/filepath"
	"testing"
)

func withTempConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestPrefsPath(t *testing.T) {
	dir := withTempConfigDir(t)

	path := PrefsPath()

	want := filepath.Join(dir, ".ncli", "prefs.yaml")
	if path != want {
		t.Fatalf("PrefsPath() = %q, want %q", path, want)
	}
}

func TestLoadPrefs_MissingFile(t *testing.T) {
	withTempConfigDir(t)

	prefs, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() error = %v", err)
	}
	if len(prefs.Relays) != 0 {
		t.Fatalf("LoadPrefs() on a fresh install = %+v, want empty", prefs)
	}
}

func TestAddRemoveListRoundTrip(t *testing.T) {
	withTempConfigDir(t)

	prefs, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() error = %v", err)
	}

	added, err := prefs.AddRelay("wss://relay.damus.io")
	if err != nil {
		t.Fatalf("AddRelay() error = %v", err)
	}
	if !added {
		t.Fatalf("AddRelay() = false on first add, want true")
	}

	// Adding the same relay again is a no-op, not a duplicate entry.
	added, err = prefs.AddRelay("wss://relay.damus.io")
	if err != nil {
		t.Fatalf("AddRelay() error = %v", err)
	}
	if added {
		t.Fatalf("AddRelay() = true on duplicate add, want false")
	}
	if len(prefs.Relays) != 1 {
		t.Fatalf("Relays = %v, want exactly one entry", prefs.Relays)
	}

	if err := SavePrefs(prefs); err != nil {
		t.Fatalf("SavePrefs() error = %v", err)
	}

	reloaded, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() after save error = %v", err)
	}
	if len(reloaded.Relays) != 1 || reloaded.Relays[0] != "wss://relay.damus.io" {
		t.Fatalf("reloaded Relays = %v, want [wss://relay.damus.io]", reloaded.Relays)
	}

	if !reloaded.RemoveRelay("wss://relay.damus.io") {
		t.Fatalf("RemoveRelay() = false, want true")
	}
	if len(reloaded.Relays) != 0 {
		t.Fatalf("Relays after remove = %v, want empty", reloaded.Relays)
	}
	if reloaded.RemoveRelay("wss://relay.damus.io") {
		t.Fatalf("RemoveRelay() on absent relay = true, want false")
	}
}

func TestAddRelay_InvalidURL(t *testing.T) {
	withTempConfigDir(t)

	prefs, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() error = %v", err)
	}

	if _, err := prefs.AddRelay("not-a-relay-url"); err == nil {
		t.Fatalf("AddRelay(%q) error = nil, want an error", "not-a-relay-url")
	}
	if _, err := prefs.AddRelay("https://example.com"); err == nil {
		t.Fatalf("AddRelay() with a non-ws(s) scheme error = nil, want an error")
	}
}

func TestAddRelay_SchemelessHostAccepted(t *testing.T) {
	withTempConfigDir(t)

	prefs, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() error = %v", err)
	}

	added, err := prefs.AddRelay("relay.primal.net")
	if err != nil {
		t.Fatalf("AddRelay(%q) error = %v, want no error for a schemeless host", "relay.primal.net", err)
	}
	if !added {
		t.Fatalf("AddRelay(%q) = false, want true", "relay.primal.net")
	}
	if len(prefs.Relays) != 1 || prefs.Relays[0] != "relay.primal.net" {
		t.Fatalf("Relays = %v, want the raw schemeless entry preserved", prefs.Relays)
	}
}

func TestPrefsRelayURLs_None(t *testing.T) {
	withTempConfigDir(t)

	if _, err := PrefsRelayURLs(); err == nil {
		t.Fatalf("PrefsRelayURLs() with no relays configured error = nil, want an error")
	}
}

func TestPrefsVaultIdentityRoundTrip(t *testing.T) {
	withTempConfigDir(t)

	prefs, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() error = %v", err)
	}
	if prefs.VaultIdentity != nil {
		t.Fatalf("VaultIdentity on a fresh install = %+v, want nil", prefs.VaultIdentity)
	}

	prefs.VaultIdentity = &VaultIdentityRef{
		Npub:          "npub1exampleexampleexampleexampleexampleexampleexampleexamplex",
		EncryptedNsec: "ncryptsec1exampleexampleexampleexampleexampleexampleexample",
	}
	if err := SavePrefs(prefs); err != nil {
		t.Fatalf("SavePrefs() error = %v", err)
	}

	reloaded, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() after save error = %v", err)
	}
	if reloaded.VaultIdentity == nil {
		t.Fatalf("reloaded VaultIdentity = nil, want a value")
	}
	if reloaded.VaultIdentity.Npub != prefs.VaultIdentity.Npub {
		t.Fatalf("reloaded VaultIdentity.Npub = %q, want %q", reloaded.VaultIdentity.Npub, prefs.VaultIdentity.Npub)
	}
	if reloaded.VaultIdentity.EncryptedNsec != prefs.VaultIdentity.EncryptedNsec {
		t.Fatalf("reloaded VaultIdentity.EncryptedNsec = %q, want %q", reloaded.VaultIdentity.EncryptedNsec, prefs.VaultIdentity.EncryptedNsec)
	}
}

func TestTargetsFromPrefs(t *testing.T) {
	withTempConfigDir(t)

	prefs, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() error = %v", err)
	}
	if _, err := prefs.AddRelay("wss://relay.damus.io"); err != nil {
		t.Fatalf("AddRelay() error = %v", err)
	}
	if err := SavePrefs(prefs); err != nil {
		t.Fatalf("SavePrefs() error = %v", err)
	}

	spec, err := TargetsFromPrefs()
	if err != nil {
		t.Fatalf("TargetsFromPrefs() error = %v", err)
	}
	if len(spec.Relays) != 1 {
		t.Fatalf("TargetsFromPrefs() Relays = %v, want exactly one entry", spec.Relays)
	}
	if spec.Relays[0].Type != FlOW_REMOTE {
		t.Fatalf("TargetsFromPrefs() Relays[0].Type = %v, want %v", spec.Relays[0].Type, FlOW_REMOTE)
	}
	if spec.Relays[0].relayURI == nil || spec.Relays[0].relayURI.Host != "relay.damus.io" {
		t.Fatalf("TargetsFromPrefs() Relays[0].relayURI = %v, want host relay.damus.io", spec.Relays[0].relayURI)
	}
	if spec.Relays[0].relayFallbackURI != nil {
		t.Fatalf("TargetsFromPrefs() Relays[0].relayFallbackURI = %v, want nil for an explicit wss:// scheme", spec.Relays[0].relayFallbackURI)
	}
}

func TestTargetsFromPrefs_SchemelessGetsFallback(t *testing.T) {
	withTempConfigDir(t)

	prefs, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() error = %v", err)
	}
	if _, err := prefs.AddRelay("relay.primal.net"); err != nil {
		t.Fatalf("AddRelay() error = %v", err)
	}
	if err := SavePrefs(prefs); err != nil {
		t.Fatalf("SavePrefs() error = %v", err)
	}

	spec, err := TargetsFromPrefs()
	if err != nil {
		t.Fatalf("TargetsFromPrefs() error = %v", err)
	}
	if len(spec.Relays) != 1 {
		t.Fatalf("TargetsFromPrefs() Relays = %v, want exactly one entry", spec.Relays)
	}
	relay := spec.Relays[0]
	if relay.relayURI == nil || relay.relayURI.String() != "wss://relay.primal.net" {
		t.Fatalf("relayURI = %v, want wss://relay.primal.net", relay.relayURI)
	}
	if relay.relayFallbackURI == nil || relay.relayFallbackURI.String() != "ws://relay.primal.net" {
		t.Fatalf("relayFallbackURI = %v, want ws://relay.primal.net", relay.relayFallbackURI)
	}
}

func TestTargetsFromRelayList(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "notes.db")
	if err := os.WriteFile(dbPath, nil, 0644); err != nil {
		t.Fatalf("failed to create notes.db fixture: %v", err)
	}

	spec, err := TargetsFromRelayList([]string{"wss://relay.damus.io", dbPath})
	if err != nil {
		t.Fatalf("TargetsFromRelayList() error = %v", err)
	}
	if len(spec.Relays) != 2 {
		t.Fatalf("TargetsFromRelayList() Relays = %v, want 2 entries", spec.Relays)
	}
	if spec.Relays[0].Type != FlOW_REMOTE {
		t.Fatalf("Relays[0].Type = %v, want %v", spec.Relays[0].Type, FlOW_REMOTE)
	}
	if spec.Relays[1].Type != FlOW_LOCAL {
		t.Fatalf("Relays[1].Type = %v, want %v", spec.Relays[1].Type, FlOW_LOCAL)
	}
}

func TestTargetsFromRelayList_SchemelessHost(t *testing.T) {
	spec, err := TargetsFromRelayList([]string{"wss://relay.damus.io", "relay.primal.net"})
	if err != nil {
		t.Fatalf("TargetsFromRelayList() error = %v", err)
	}
	if len(spec.Relays) != 2 {
		t.Fatalf("Relays = %v, want 2 entries", spec.Relays)
	}

	explicit, schemeless := spec.Relays[0], spec.Relays[1]
	if explicit.relayFallbackURI != nil {
		t.Fatalf("explicit-scheme entry relayFallbackURI = %v, want nil", explicit.relayFallbackURI)
	}
	if schemeless.relayURI == nil || schemeless.relayURI.String() != "wss://relay.primal.net" {
		t.Fatalf("schemeless entry relayURI = %v, want wss://relay.primal.net", schemeless.relayURI)
	}
	if schemeless.relayFallbackURI == nil || schemeless.relayFallbackURI.String() != "ws://relay.primal.net" {
		t.Fatalf("schemeless entry relayFallbackURI = %v, want ws://relay.primal.net", schemeless.relayFallbackURI)
	}
}

// TestTargetsFromRelayList_ExistingFileWinsAmbiguity locks in that a
// schemeless bare string resolves to a local store, not a relay host, when
// a file actually exists at that path -- resolveRelayURL's schemeless
// acceptance must not swallow the pre-existing local-path shorthand.
func TestTargetsFromRelayList_ExistingFileWinsAmbiguity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "notes.db")
	if err := os.WriteFile(dbPath, nil, 0644); err != nil {
		t.Fatalf("failed to create notes.db fixture: %v", err)
	}

	spec, err := TargetsFromRelayList([]string{dbPath})
	if err != nil {
		t.Fatalf("TargetsFromRelayList() error = %v", err)
	}
	if len(spec.Relays) != 1 || spec.Relays[0].Type != FlOW_LOCAL {
		t.Fatalf("Relays = %+v, want a single FlOW_LOCAL entry for an existing file", spec.Relays)
	}
}

func TestTargetsFromRelayList_Empty(t *testing.T) {
	if _, err := TargetsFromRelayList(nil); err == nil {
		t.Fatalf("TargetsFromRelayList(nil) error = nil, want an error")
	}
}

func TestTargetsFromRelayList_InvalidEntry(t *testing.T) {
	if _, err := TargetsFromRelayList([]string{"not-a-relay-or-existing-path"}); err == nil {
		t.Fatalf("TargetsFromRelayList(invalid) error = nil, want an error")
	}
}

func TestRelayContextRoundTrip(t *testing.T) {
	withTempConfigDir(t)

	cfgPath := filepath.Join(t.TempDir(), "prod.yaml")
	if err := os.WriteFile(cfgPath, []byte("store: test.db\n"), 0644); err != nil {
		t.Fatalf("failed to create config fixture: %v", err)
	}

	prefs, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() error = %v", err)
	}

	abs, err := prefs.AddRelayContext("prod", cfgPath)
	if err != nil {
		t.Fatalf("AddRelayContext() error = %v", err)
	}
	if abs != cfgPath {
		t.Fatalf("AddRelayContext() = %q, want %q (already absolute)", abs, cfgPath)
	}
	if prefs.RelayContexts["prod"] != cfgPath {
		t.Fatalf("RelayContexts[prod] = %q, want %q", prefs.RelayContexts["prod"], cfgPath)
	}

	if err := prefs.UseRelayContext("prod"); err != nil {
		t.Fatalf("UseRelayContext(prod) error = %v", err)
	}
	if prefs.CurrentRelayContext != "prod" {
		t.Fatalf("CurrentRelayContext = %q, want prod", prefs.CurrentRelayContext)
	}

	if err := SavePrefs(prefs); err != nil {
		t.Fatalf("SavePrefs() error = %v", err)
	}

	reloaded, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() after save error = %v", err)
	}
	path, ok := reloaded.CurrentRelayContextPath()
	if !ok || path != cfgPath {
		t.Fatalf("CurrentRelayContextPath() = (%q, %v), want (%q, true)", path, ok, cfgPath)
	}

	if !reloaded.RemoveRelayContext("prod") {
		t.Fatalf("RemoveRelayContext(prod) = false, want true")
	}
	if reloaded.CurrentRelayContext != "" {
		t.Fatalf("CurrentRelayContext after removing the current context = %q, want empty", reloaded.CurrentRelayContext)
	}
	if _, ok := reloaded.CurrentRelayContextPath(); ok {
		t.Fatalf("CurrentRelayContextPath() after removal ok = true, want false")
	}
	if reloaded.RemoveRelayContext("prod") {
		t.Fatalf("RemoveRelayContext(prod) on an already-removed context = true, want false")
	}
}

func TestAddRelayContext_MissingFile(t *testing.T) {
	withTempConfigDir(t)

	prefs, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() error = %v", err)
	}

	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	if _, err := prefs.AddRelayContext("prod", missing); err == nil {
		t.Fatalf("AddRelayContext() with a missing config file error = nil, want an error")
	}
}

func TestAddRelayContext_DirectoryRejected(t *testing.T) {
	withTempConfigDir(t)

	prefs, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() error = %v", err)
	}

	if _, err := prefs.AddRelayContext("prod", t.TempDir()); err == nil {
		t.Fatalf("AddRelayContext() with a directory error = nil, want an error")
	}
}

func TestAddRelayContext_EmptyName(t *testing.T) {
	withTempConfigDir(t)

	cfgPath := filepath.Join(t.TempDir(), "prod.yaml")
	if err := os.WriteFile(cfgPath, []byte("store: test.db\n"), 0644); err != nil {
		t.Fatalf("failed to create config fixture: %v", err)
	}

	prefs, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() error = %v", err)
	}
	if _, err := prefs.AddRelayContext("", cfgPath); err == nil {
		t.Fatalf("AddRelayContext(\"\", ...) error = nil, want an error")
	}
}

func TestUseRelayContext_Unknown(t *testing.T) {
	withTempConfigDir(t)

	prefs, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() error = %v", err)
	}
	if err := prefs.UseRelayContext("does-not-exist"); err == nil {
		t.Fatalf("UseRelayContext(unknown) error = nil, want an error")
	}
	if prefs.CurrentRelayContext != "" {
		t.Fatalf("CurrentRelayContext after a failed UseRelayContext = %q, want empty", prefs.CurrentRelayContext)
	}
}

func TestCurrentRelayContextPath_NoneSet(t *testing.T) {
	withTempConfigDir(t)

	prefs, err := LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs() error = %v", err)
	}
	if _, ok := prefs.CurrentRelayContextPath(); ok {
		t.Fatalf("CurrentRelayContextPath() on a fresh Prefs ok = true, want false")
	}
}
