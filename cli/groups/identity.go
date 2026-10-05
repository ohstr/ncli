package groups

import (
	"errors"
	"fmt"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// resolveIdentity resolves the key that signs a group event: the --identity
// flag, then the "groups.identity" config key, then the vault's sole entry
// when there is exactly one. A pubkey-only identity is rejected --
// authenticating means signing. Returns both the public and private key hex
// since every nip29.NewXxx constructor needs the actor's pubkey, not just a
// key to sign with.
//
// This mirrors cli/huddle/command.go's resolveIdentity rather than importing
// a shared helper -- see docs/private/nip29-groups-commands-plan.md for why
// this worktree stays independent of the identity-resolution refactor in
// progress on the nip42-generic-auth worktree.
func resolveIdentity(cmd *cobra.Command, identityFlag string) (pubKeyHex, privKeyHex string, err error) {
	identity := identityFlag
	if identity == "" {
		identity = viper.GetString("groups.identity")
	}

	if identity == "" {
		entries, err := client.LoadVaultEntries()
		if err != nil {
			return "", "", common.RuntimeError(cmd, err)
		}
		switch len(entries) {
		case 0:
			return "", "", common.InvocationError(cmd, errors.New("--identity is required (or set NCLI_GROUPS_IDENTITY/groups.identity): no vault identity to fall back to"))
		case 1:
			identity = entries[0].Label
		default:
			return "", "", common.InvocationError(cmd, fmt.Errorf("--identity is required (or set NCLI_GROUPS_IDENTITY/groups.identity): the vault has %d saved identities, none chosen by default", len(entries)))
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
