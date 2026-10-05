package space

import (
	"errors"
	"fmt"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// resolveIdentity resolves the key that signs a space event: the --identity
// flag, then the "space.identity" config key, then the vault's sole entry
// when there is exactly one. A pubkey-only identity is rejected --
// publishing means signing. Returns both the public and private key hex
// since nip53.NewMeetingSpace needs the creator's pubkey for its Host
// provider tag, not just a key to sign with.
//
// This is its own local copy rather than a shared helper -- same precedent
// cli/groups/cli/huddle/cli/blossom/cli/bunker each already independently
// have.
func resolveIdentity(cmd *cobra.Command, identityFlag string) (pubKeyHex, privKeyHex string, err error) {
	identity := identityFlag
	if identity == "" {
		identity = viper.GetString("space.identity")
	}

	if identity == "" {
		entries, err := client.LoadVaultEntries()
		if err != nil {
			return "", "", common.RuntimeError(cmd, err)
		}
		switch len(entries) {
		case 0:
			return "", "", common.InvocationError(cmd, errors.New("--identity is required (or set NCLI_SPACE_IDENTITY/space.identity): no vault identity to fall back to"))
		case 1:
			identity = entries[0].Label
		default:
			return "", "", common.InvocationError(cmd, fmt.Errorf("--identity is required (or set NCLI_SPACE_IDENTITY/space.identity): the vault has %d saved identities, none chosen by default", len(entries)))
		}
	}

	resolved, err := client.ResolveIdentifier(identity)
	if err != nil {
		return "", "", keyresolve.ClassifyIdentifierError(cmd, identity, err)
	}

	privKeyHex, err = keyresolve.ResolveSigningKey(cmd, false, resolved)
	if err != nil {
		return "", "", err
	}
	if privKeyHex == "" {
		return "", "", common.AuthError(cmd, fmt.Errorf("identity %q has no private key available", common.RedactSecretInput(identity)))
	}
	return resolved.PubKeyHex, privKeyHex, nil
}
