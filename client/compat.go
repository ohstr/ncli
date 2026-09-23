package client

import (
	"net/url"

	"github.com/ohstr/ncli/client/prefs"
	"github.com/ohstr/ncli/client/vault"
)

// The vault, prefs and keypair code moved into client/vault and client/prefs
// so a consumer that only wants programmatic vault access stops inheriting
// this package's TUI, bbolt and viper closure (issue #59). Everything they
// exported is re-exported here unchanged, permanently -- these are the
// spellings the CLI and any existing consumer already use, and there is no
// deprecation planned.
//
// Types are aliases (=), not definitions, so a *client.VaultEntry and a
// *vault.Entry are the same type and callers can mix the two freely.

// --- client/vault ---

type (
	// VaultEntry is vault.Entry.
	VaultEntry = vault.Entry
	// Identity is vault.Identity.
	Identity = vault.Identity
)

// ErrLabelExists reports that a vault label is already taken. Same value as
// vault.ErrLabelExists, so errors.Is works across both spellings.
var ErrLabelExists = vault.ErrLabelExists

// GenerateIdentity mints a new keypair. See vault.GenerateIdentity.
func GenerateIdentity() (*Identity, error) { return vault.GenerateIdentity() }

// VaultPath returns the path to vault.yaml. See vault.Path.
func VaultPath() string { return vault.Path() }

// VaultExists reports whether the vault identity exists. See vault.Exists.
func VaultExists() (bool, error) { return vault.Exists() }

// CreateVaultIdentity creates the vault's own keypair. See vault.CreateIdentity.
func CreateVaultIdentity(password string) (npub, privKeyHex string, err error) {
	return vault.CreateIdentity(password)
}

// UnlockVaultIdentity decrypts the vault key. See vault.Unlock.
func UnlockVaultIdentity(password string) (string, error) { return vault.Unlock(password) }

// LoadVaultEntries reads vault.yaml. See vault.LoadEntries.
func LoadVaultEntries() ([]VaultEntry, error) { return vault.LoadEntries() }

// SaveVaultEntries writes vault.yaml. See vault.SaveEntries.
func SaveVaultEntries(entries []VaultEntry) error { return vault.SaveEntries(entries) }

// AddVaultEntry saves a new identity into the vault. See vault.AddEntry.
func AddVaultEntry(vaultPrivKeyHex, label, entryPrivKeyHex string) (*VaultEntry, error) {
	return vault.AddEntry(vaultPrivKeyHex, label, entryPrivKeyHex)
}

// DecryptVaultEntry reverses AddVaultEntry. See vault.DecryptEntry.
func DecryptVaultEntry(vaultPrivKeyHex string, entry VaultEntry) (string, error) {
	return vault.DecryptEntry(vaultPrivKeyHex, entry)
}

// FindVaultEntry looks a vault entry up by label or key. See vault.FindEntry.
func FindVaultEntry(labelOrNpub string) (*VaultEntry, bool, error) {
	return vault.FindEntry(labelOrNpub)
}

// --- client/prefs ---

type (
	// Prefs is prefs.Prefs.
	Prefs = prefs.Prefs
	// VaultIdentityRef is prefs.VaultIdentityRef.
	VaultIdentityRef = prefs.VaultIdentityRef
)

// PrefsPath returns the path to prefs.yaml. See prefs.Path.
func PrefsPath() string { return prefs.Path() }

// LoadPrefs reads prefs.yaml. See prefs.Load.
func LoadPrefs() (*Prefs, error) { return prefs.Load() }

// SavePrefs writes prefs.yaml. See prefs.Save.
func SavePrefs(p *Prefs) error { return prefs.Save(p) }

// BlossomServersFromPrefs returns the configured Blossom servers. See
// prefs.BlossomServers.
func BlossomServersFromPrefs() ([]string, error) { return prefs.BlossomServers() }

// PrefsRelayURLs returns the configured relays as URLs. See prefs.RelayURLs.
func PrefsRelayURLs() ([]*url.URL, error) { return prefs.RelayURLs() }

// ResolveRelayURL parses a relay input into its primary and fallback URLs.
// See prefs.ResolveRelayURL.
func ResolveRelayURL(raw string) (primary *url.URL, fallback *url.URL, err error) {
	return prefs.ResolveRelayURL(raw)
}
