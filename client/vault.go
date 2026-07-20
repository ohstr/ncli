package client

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/go-flokicoin/crypto/schnorr"
	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip44"
	"github.com/ohstr/nmilat/nip49"
	"github.com/ohstr/nmilat/utils"
	"sigs.k8s.io/yaml"
)

const vaultFileName = "vault.yaml"

// VaultEntry is one identity saved in the local vault: label and npub are
// plaintext (so listing/inspecting needs no password), but EncryptedNsec is
// a NIP-44 payload only the unlocked vault identity can decrypt.
type VaultEntry struct {
	Label         string `json:"label" yaml:"label"`
	Npub          string `json:"npub" yaml:"npub"`
	EncryptedNsec string `json:"encrypted_nsec" yaml:"encrypted_nsec"`
	CreatedAt     string `json:"created_at" yaml:"created_at"`
}

// vaultFile is the on-disk envelope for vault.yaml.
type vaultFile struct {
	Entries []VaultEntry `json:"entries,omitempty" yaml:"entries,omitempty"`
}

// VaultPath returns the OS-appropriate path to vault.yaml, next to
// prefs.yaml under ncli's shared app config directory.
func VaultPath() string {
	return filepath.Join(common.AppConfigDir(), vaultFileName)
}

// VaultExists reports whether the vault identity has been created yet.
// This is the authoritative check (not vault.yaml's mere presence, which
// mirrors prefs.yaml's "missing file means empty" convention).
func VaultExists() (bool, error) {
	prefs, err := LoadPrefs()
	if err != nil {
		return false, err
	}
	return prefs.VaultIdentity != nil, nil
}

// CreateVaultIdentity generates the vault's own keypair, encrypts its
// private key under password (NIP-49), and persists the reference in
// prefs.yaml. It returns the plaintext private key hex too, so callers
// that just created the vault don't need to immediately pay for a second,
// redundant scrypt-based unlock (NIP-49's scrypt is deliberately slow).
func CreateVaultIdentity(password string) (npub, privKeyHex string, err error) {
	prefs, err := LoadPrefs()
	if err != nil {
		return "", "", err
	}
	if prefs.VaultIdentity != nil {
		return "", "", errors.New("vault identity already exists")
	}

	id, err := GenerateIdentity()
	if err != nil {
		return "", "", fmt.Errorf("failed to generate vault identity: %w", err)
	}

	encryptedNsec, err := nip49.Encrypt(id.PrivKeyHex, password)
	if err != nil {
		return "", "", fmt.Errorf("failed to encrypt vault identity key: %w", err)
	}

	prefs.VaultIdentity = &VaultIdentityRef{Npub: id.Npub, EncryptedNsec: encryptedNsec}
	if err := SavePrefs(prefs); err != nil {
		return "", "", err
	}
	return id.Npub, id.PrivKeyHex, nil
}

// UnlockVaultIdentity decrypts the vault's private key with password.
func UnlockVaultIdentity(password string) (string, error) {
	prefs, err := LoadPrefs()
	if err != nil {
		return "", err
	}
	if prefs.VaultIdentity == nil {
		return "", errors.New("no vault identity yet; save an identity with `ncli id` to create one")
	}

	privHex, err := nip49.Decrypt(prefs.VaultIdentity.EncryptedNsec, password)
	if err != nil {
		return "", err // "decryption failed (bad password?)" -- already AEAD-authenticated
	}

	// NIP-49's AEAD tag already rules out a wrong password producing this
	// plaintext; this check instead guards against prefs.yaml having been
	// hand-edited/corrupted into a (npub, encrypted_nsec) pair that no
	// longer agree with each other.
	pubHex, err := utils.GetPublicKey(privHex)
	if err != nil {
		return "", fmt.Errorf("vault identity corrupted: %w", err)
	}
	npub, err := nip19.EncodePublicKey(pubHex)
	if err != nil {
		return "", err
	}
	if npub != prefs.VaultIdentity.Npub {
		return "", errors.New("vault identity corrupted: decrypted key does not match stored npub")
	}

	return privHex, nil
}

// LoadVaultEntries reads vault.yaml, returning a nil slice (not an error)
// if it doesn't exist yet.
func LoadVaultEntries() ([]VaultEntry, error) {
	path := VaultPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	var vf vaultFile
	if err := yaml.UnmarshalStrict(data, &vf); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	return vf.Entries, nil
}

// SaveVaultEntries writes entries to vault.yaml, creating its parent
// directory if needed.
func SaveVaultEntries(entries []VaultEntry) error {
	path := VaultPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(&vaultFile{Entries: entries})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// AddVaultEntry encrypts entryPrivKeyHex under a key derived from the
// (already-unlocked) vault private key and that entry's own public key,
// then appends and saves it as a new vault entry. A blank label defaults
// to the entry's own npub (guaranteed unique), so leaving the label prompt
// empty never blocks a save.
func AddVaultEntry(vaultPrivKeyHex, label, entryPrivKeyHex string) (*VaultEntry, error) {
	entryPubHex, err := utils.GetPublicKey(entryPrivKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid identity private key: %w", err)
	}
	npub, err := nip19.EncodePublicKey(entryPubHex)
	if err != nil {
		return nil, err
	}

	label = strings.TrimSpace(label)
	if label == "" {
		label = npub
	}

	entries, err := LoadVaultEntries()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if strings.EqualFold(e.Label, label) {
			return nil, fmt.Errorf("label %q already exists in vault", label)
		}
	}

	convKey, err := deriveEntryConversationKey(vaultPrivKeyHex, npub)
	if err != nil {
		return nil, err
	}
	encryptedNsec, err := nip44.Encrypt(entryPrivKeyHex, convKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt identity key: %w", err)
	}

	entry := VaultEntry{
		Label:         label,
		Npub:          npub,
		EncryptedNsec: encryptedNsec,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	entries = append(entries, entry)
	if err := SaveVaultEntries(entries); err != nil {
		return nil, err
	}
	return &entry, nil
}

// DecryptVaultEntry reverses AddVaultEntry, given the already-unlocked
// vault private key.
func DecryptVaultEntry(vaultPrivKeyHex string, entry VaultEntry) (string, error) {
	convKey, err := deriveEntryConversationKey(vaultPrivKeyHex, entry.Npub)
	if err != nil {
		return "", err
	}
	return nip44.Decrypt(entry.EncryptedNsec, convKey)
}

// FindVaultEntry looks up a vault entry by exact label (case-insensitive)
// first, then by npub or hex pubkey.
func FindVaultEntry(labelOrNpub string) (*VaultEntry, bool, error) {
	trimmed := strings.TrimSpace(labelOrNpub)
	if trimmed == "" {
		return nil, false, nil
	}

	entries, err := LoadVaultEntries()
	if err != nil {
		return nil, false, err
	}

	for i := range entries {
		if strings.EqualFold(entries[i].Label, trimmed) {
			return &entries[i], true, nil
		}
	}

	targetHex := strings.ToLower(common.NormalizeKey(trimmed))
	for i := range entries {
		entryHex, err := nip19.DecodePublicKey(entries[i].Npub)
		if err != nil {
			continue // corrupt entry -- skip rather than fail the whole lookup
		}
		if strings.EqualFold(entryHex, targetHex) {
			return &entries[i], true, nil
		}
	}

	return nil, false, nil
}

// deriveEntryConversationKey computes the NIP-44 conversation key shared by
// the vault's private key and an entry's own public key. ECDH is
// symmetric, so this is called identically at encrypt time (AddVaultEntry)
// and decrypt time (DecryptVaultEntry).
func deriveEntryConversationKey(vaultPrivKeyHex, entryNpub string) ([]byte, error) {
	privBytes, err := hex.DecodeString(vaultPrivKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid vault private key: %w", err)
	}
	privKey, _ := btcec.PrivKeyFromBytes(privBytes)

	entryPubHex, err := nip19.DecodePublicKey(entryNpub)
	if err != nil {
		return nil, fmt.Errorf("invalid entry npub: %w", err)
	}
	pubBytes, err := hex.DecodeString(entryPubHex)
	if err != nil {
		return nil, fmt.Errorf("invalid entry pubkey: %w", err)
	}
	pubKey, err := schnorr.ParsePubKey(pubBytes)
	if err != nil {
		return nil, fmt.Errorf("unable to parse entry pubkey: %w", err)
	}

	return nip44.GenerateConversationKey(privKey, pubKey)
}
