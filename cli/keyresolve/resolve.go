// Package keyresolve holds identity-resolution helpers shared by every
// command that turns a vault label/nsec/npub/hex/nprofile/nip-05 identifier
// into actual key material -- id, id sign, id delegate, and miner mine.
package keyresolve

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"
)

// ClassifyIdentifierError picks InvalidInputError (a malformed npub/nsec/
// hex/nprofile string) or NetworkError (a nip-05 "name@domain" lookup that
// failed to resolve, e.g. DNS/HTTP) for a client.ResolveIdentifier failure
// -- both funnel into one error return without a distinguishable type, and
// a "name@domain"-shaped identifier's failure is overwhelmingly a network
// problem, not a malformed-string one.
func ClassifyIdentifierError(cmd *cobra.Command, identifier string, err error) error {
	input := common.RedactSecretInput(identifier)
	if strings.Contains(identifier, "@") {
		return common.NetworkError(cmd, input, err)
	}
	return common.InvalidInputError(cmd, input, err)
}

// ResolveVaultPassword sources the vault password from NCLI_VAULT_PASSWORD
// if set (a universal override, in any mode), otherwise errors in JSON
// mode (never prompts -- an agent driving this over a pipe has no TTY to
// prompt), otherwise prompts interactively with promptText.
func ResolveVaultPassword(jsonMode bool, promptText string) (string, error) {
	if pw := viper.GetString("vault.password"); pw != "" {
		return pw, nil
	}
	if jsonMode {
		return "", errors.New("vault password required; set NCLI_VAULT_PASSWORD")
	}
	return PromptPassword(promptText)
}

// PromptPassword reads a password with echo disabled. This requires stdin
// to be an actual terminal (it issues a termios ioctl); piped/non-
// interactive stdin will error here -- only reached when --json is unset
// and NCLI_VAULT_PASSWORD is unset, i.e. the deliberately-interactive-only
// path (the same trade-off ssh-keygen/sudo make).
func PromptPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return string(pw), nil
}

// UnlockOrCreateVault returns the vault's own decrypted private key --
// unlocking the existing vault (NCLI_VAULT_PASSWORD, or an interactive
// prompt), or creating a brand-new one first (set-and-confirm prompt, or
// NCLI_VAULT_PASSWORD again) if none exists yet. Shared by every command
// that saves a freshly generated identity into the vault as a step of its
// own flow, rather than "id"'s top-level interactive save (id.go's
// saveIdentity narrates around this same exists/unlock/create sequence
// itself).
func UnlockOrCreateVault(cmd *cobra.Command, jsonMode bool) (string, error) {
	exists, err := client.VaultExists()
	if err != nil {
		return "", err
	}
	if !exists {
		password, err := ResolveNewVaultPassword(jsonMode)
		if err != nil {
			return "", common.UsageError(cmd, err)
		}
		_, privHex, err := client.CreateVaultIdentity(password)
		if err != nil {
			return "", fmt.Errorf("failed to create vault: %w", err)
		}
		return privHex, nil
	}

	password, err := ResolveVaultPassword(jsonMode, "Vault password: ")
	if err != nil {
		return "", common.UsageError(cmd, err)
	}
	privHex, err := client.UnlockVaultIdentity(password)
	if err != nil {
		return "", common.AuthError(cmd, err)
	}
	return privHex, nil
}

// ResolveNewVaultPassword sources the password to protect a brand-new
// vault identity. When it comes from NCLI_VAULT_PASSWORD, the
// confirm-by-retyping step is skipped -- there's no risk of a mistyped
// confirmation when the value came from one authoritative source rather
// than two rounds of human typing.
func ResolveNewVaultPassword(jsonMode bool) (string, error) {
	if pw := viper.GetString("vault.password"); pw != "" {
		return pw, nil
	}
	if jsonMode {
		return "", errors.New("vault password required; set NCLI_VAULT_PASSWORD")
	}
	return promptNewPassword()
}

func promptNewPassword() (string, error) {
	pw, err := PromptPassword("Set a vault password: ")
	if err != nil {
		return "", err
	}
	if pw == "" {
		return "", errors.New("password must not be empty")
	}
	confirm, err := PromptPassword("Confirm vault password: ")
	if err != nil {
		return "", err
	}
	if pw != confirm {
		return "", errors.New("passwords did not match")
	}
	return pw, nil
}

// ResolveSigningKey returns the private key behind resolved -- directly,
// for an nsec identity (client.ResolveIdentifier already decoded it), or
// via the vault, for a saved vault label (ResolveVaultPassword ->
// client.UnlockVaultIdentity -> client.FindVaultEntry ->
// client.DecryptVaultEntry, the same sequence "id --reveal" uses). Returns
// "", nil (not an error) for a pubkey-only identity (npub/hex/nprofile/
// nip-05) that has no private key at all -- callers that tolerate an
// unsigned/pubkey-only result (like "miner mine --identity") treat "" as
// "nothing to sign with, carry on"; callers that require a private key
// (like "id sign", "id delegate") turn "" into a hard failure themselves.
func ResolveSigningKey(cmd *cobra.Command, jsonMode bool, resolved *client.IdentityInspection) (string, error) {
	if resolved.PrivKeyHex != "" {
		return resolved.PrivKeyHex, nil
	}
	if !resolved.InVault {
		return "", nil
	}

	password, err := ResolveVaultPassword(jsonMode, "Vault password: ")
	if err != nil {
		return "", common.UsageError(cmd, err)
	}
	vaultPrivKeyHex, err := client.UnlockVaultIdentity(password)
	if err != nil {
		return "", common.AuthError(cmd, err)
	}
	entry, found, err := client.FindVaultEntry(resolved.Npub)
	if err != nil {
		return "", common.RuntimeError(cmd, err)
	}
	if !found {
		return "", common.NotFoundError(cmd, resolved.Npub, fmt.Errorf("vault entry disappeared during signing"))
	}
	privKeyHex, err := client.DecryptVaultEntry(vaultPrivKeyHex, *entry)
	if err != nil {
		return "", common.RuntimeError(cmd, err)
	}
	return privKeyHex, nil
}
