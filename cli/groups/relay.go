package groups

import (
	"errors"
	"net/url"
	"strings"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
)

// resolveRelay honors --relay, falling back to the first configured prefs
// relay -- a group lives on one relay, so unlike find/dump's repeatable
// -s/--relays, there is nothing to fan out to here.
func resolveRelay(cmd *cobra.Command, relayFlag string) (*url.URL, error) {
	if relayFlag != "" {
		u, _, err := client.ResolveRelayURL(relayFlag)
		if err != nil {
			return nil, common.InvalidInputError(cmd, relayFlag, err)
		}
		return u, nil
	}

	urls, err := client.PrefsRelayURLs()
	if err != nil {
		return nil, common.UsageError(cmd, err)
	}
	if len(urls) == 0 {
		return nil, common.InvocationError(cmd, errors.New("--relay is required: no relays are configured"))
	}
	return urls[0], nil
}

// resolveWriteTarget resolves everything a group write command needs beyond
// its own arguments: the relay to publish to and what signs: --signer (a
// socket signer, bunker or identity), else the --identity key. Call
// closeSigner when done.
func resolveWriteTarget(cmd *cobra.Command) (relayURL *url.URL, pubKeyHex string, signer client.Signer, closeSigner func(), err error) {
	relayFlag, _ := cmd.Flags().GetString("relay")
	relayURL, err = resolveRelay(cmd, relayFlag)
	if err != nil {
		return nil, "", nil, nil, err
	}

	identityFlag, _ := cmd.Flags().GetString("identity")
	if signerFlag := groupSigner(cmd, identityFlag); signerFlag != "" {
		if identityFlag != "" {
			return nil, "", nil, nil, common.InvocationError(cmd, errors.New("pass --identity or --signer, not both"))
		}
		jsonMode, _ := cmd.Flags().GetBool("json")
		signer, closeSigner, err = keyresolve.ResolveSigner(cmd, jsonMode, signerFlag)
		if err != nil {
			return nil, "", nil, nil, err
		}
		return relayURL, strings.ToLower(signer.PubKey()), signer, closeSigner, nil
	}

	pubKeyHex, privKeyHex, err := resolveIdentity(cmd, identityFlag)
	if err != nil {
		return nil, "", nil, nil, err
	}
	local, err := client.NewLocalSigner(privKeyHex)
	if err != nil {
		return nil, "", nil, nil, common.RuntimeError(cmd, err)
	}
	return relayURL, pubKeyHex, local, func() {}, nil
}
