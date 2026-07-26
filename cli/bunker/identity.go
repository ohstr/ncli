package bunker

import (
	"fmt"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// ResolveSignerKey resolves the identity the bunker daemon signs on behalf
// of: identityFlag (a vault label, nsec, npub, hex, nprofile, or nip-05 --
// the same identifier shapes "id sign --identity"/"id delegate" accept),
// falling back to the viper config key "bunker.identity" (settable via
// ncli.yaml/relay.yaml or NCLI_BUNKER_IDENTITY, the same precedent
// "delegate.issuer" already establishes), falling back to the vault's sole
// entry if exactly one exists (zero-config for the common single-identity
// case), or else a clear usage error. Like "id sign"/"id delegate", this
// must resolve to a **private** key -- a pubkey-only identity has nothing
// to sign with and is rejected. vaultLabel is non-empty whenever the
// resolved pubkey is also a saved vault entry -- not just when identityFlag
// was literally typed as a label (client.ResolveIdentifier looks it up by
// the *resolved* pubkey too, so an npub/nsec/nip-05 that happens to match
// a saved entry still reports its label) -- so the TUI/status output can
// show it alongside name/nip05 when available (see board.go's
// formatIdentity/identityLabel).
func ResolveSignerKey(cmd *cobra.Command, jsonMode bool, identityFlag string) (privKeyHex, pubKeyHex, vaultLabel string, err error) {
	identity := identityFlag
	if identity == "" {
		identity = viper.GetString("bunker.identity")
	}

	if identity == "" {
		entries, loadErr := client.LoadVaultEntries()
		if loadErr != nil {
			return "", "", "", common.RuntimeError(cmd, loadErr)
		}
		switch len(entries) {
		case 0:
			return "", "", "", common.UsageError(cmd, fmt.Errorf("--identity is required (or set NCLI_BUNKER_IDENTITY/bunker.identity): no vault identity to fall back to"))
		case 1:
			identity = entries[0].Label
		default:
			return "", "", "", common.UsageError(cmd, fmt.Errorf("--identity is required (or set NCLI_BUNKER_IDENTITY/bunker.identity): the vault has %d saved identities, none chosen by default", len(entries)))
		}
	}

	resolved, err := client.ResolveIdentifier(identity)
	if err != nil {
		return "", "", "", keyresolve.ClassifyIdentifierError(cmd, identity, err)
	}

	privKeyHex, err = keyresolve.ResolveSigningKey(cmd, jsonMode, resolved)
	if err != nil {
		return "", "", "", err
	}
	if privKeyHex == "" {
		return "", "", "", common.AuthError(cmd, fmt.Errorf("identity %q has no private key available", common.RedactSecretInput(identity)))
	}
	return privKeyHex, resolved.PubKeyHex, resolved.VaultLabel, nil
}
