package ncli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/ncli/signer"
	"github.com/ohstr/nmilat/nip01"
	"github.com/spf13/cobra"
)

var idSignCmd = &cobra.Command{
	Use:   "sign",
	Short: "Sign unsigned events with a Nostr identity",
	Long: `Sign an unsigned event, or an array of them, with --identity's private
key, or through --signer. --out is written in the shape it was read.

--signer takes a bunker+unix:///path/to.sock URI (an "ncli signer serve"
socket; the key never enters this process) or an identity like
--identity. --attestations passes signed events a socket signer's policy
may require (e.g. an approval); a denial exits 7.

Fails if an event already declares a different pubkey.`,
	Example: `  ncli id sign -e events.json -o signed.json --identity satoshi
  ncli id sign -e event.json -o signed.json --signer bunker+unix:///run/signer/agent.sock
  ncli id sign -e state.json -o signed.json --signer bunker+unix:///run/signer/agent.sock --attestations approval.json`,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := cmd.ValidateRequiredFlags(); err != nil {
			return common.InvocationOrHelp(cmd, args, err)
		}
		if _, err := validateArgFile(cmd, "events", true, ".json", ".jsonp", ".yaml", ".yml"); err != nil {
			return common.InvocationOrHelp(cmd, args, err)
		}
		if _, err := validateArgFile(cmd, "out", false, ".json", ".jsonp", ".yaml", ".yml"); err != nil {
			return common.InvocationOrHelp(cmd, args, err)
		}
		identity, _ := cmd.Flags().GetString("identity")
		signerFlag, _ := cmd.Flags().GetString("signer")
		attestations, _ := cmd.Flags().GetString("attestations")
		switch {
		case identity == "" && signerFlag == "":
			return common.InvocationOrHelp(cmd, args, errors.New(`required flag(s) "identity" or "signer" not set`))
		case identity != "" && signerFlag != "":
			return common.InvocationError(cmd, errors.New("use --identity or --signer, not both"))
		case attestations != "" && !signer.IsURI(signerFlag):
			return common.InvocationError(cmd, errors.New("--attestations needs a bunker+unix:// --signer"))
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonMode, _ := cmd.Flags().GetBool("json")

		identity, _ := cmd.Flags().GetString("identity")
		signerFlag, _ := cmd.Flags().GetString("signer")
		attPath, _ := cmd.Flags().GetString("attestations")
		if signerFlag != "" {
			identity = signerFlag
		}
		sgn, closeSigner, err := keyresolve.ResolveSigner(cmd, jsonMode, identity)
		if err != nil {
			return err
		}
		defer closeSigner()
		pubKeyHex := sgn.PubKey()

		var attestations []*nip01.Event
		if attPath != "" {
			if attestations, err = client.LoadEvents(attPath); err != nil {
				return common.InvalidInputError(cmd, attPath, err)
			}
		}

		eventsPath, err := validateArgFile(cmd, "events", true, ".json", ".jsonp", ".yaml", ".yml")
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
		events, wasArray, err := client.LoadDraftEvents(eventsPath)
		if err != nil {
			return common.InvalidInputError(cmd, eventsPath, err)
		}
		if len(events) == 0 {
			return common.InvalidInputError(cmd, eventsPath, fmt.Errorf("no events to sign"))
		}

		for _, event := range events {
			if event.PubKey != "" && !strings.EqualFold(event.PubKey, pubKeyHex) {
				return common.InvalidInputError(cmd, eventsPath, fmt.Errorf("event pubkey %q conflicts with the signer's pubkey %q", event.PubKey, pubKeyHex))
			}
		}

		for _, event := range events {
			if sc, ok := sgn.(*signer.Client); ok {
				err = sc.SignWithAttestations(cmd.Context(), event, attestations)
			} else {
				err = sgn.Sign(cmd.Context(), event)
			}
			if err != nil {
				return keyresolve.SignerError(cmd, eventsPath, fmt.Errorf("failed to sign event: %w", err))
			}
		}

		outPath, err := validateArgFile(cmd, "out", false, ".json", ".jsonp", ".yaml", ".yml")
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
		if err := client.WriteDraftEvents(outPath, events, wasArray); err != nil {
			return common.RuntimeError(cmd, err)
		}

		ids := make([]string, len(events))
		for i, e := range events {
			ids[i] = e.ID
		}

		if jsonMode {
			common.PrintJSON(map[string]any{
				"pubkey": pubKeyHex,
				"ids":    ids,
				"count":  len(events),
				"out":    outPath,
			})
			return nil
		}

		// Same stdout-only-holds-the-result convention as "miner mine" (see
		// cli/ncli/miner.go).
		for _, id := range ids {
			fmt.Println("id:    ", id)
		}
		fmt.Println("pubkey:", pubKeyHex)
		fmt.Println("count: ", len(events))
		fmt.Println("out:   ", outPath)
		return nil
	},
}

func init() {
	idCmd.AddCommand(idSignCmd)

	idSignCmd.Flags().String("identity", "", "Vault label/nsec identity to sign with (must resolve to a private key -- a pubkey-only npub/hex/nprofile/nip-05 is rejected)")
	idSignCmd.Flags().String("signer", "", "Sign through a socket signer (bunker+unix:///path/to.sock) instead of --identity")
	idSignCmd.Flags().String("attestations", "", "Signed event(s) to pass to a socket signer's policy (with --signer)")

	idSignCmd.Flags().StringP("events", "e", "", "Path to a single unsigned event object or an array of them (required)")
	_ = idSignCmd.MarkFlagRequired("events")
	_ = idSignCmd.MarkFlagFilename("events", "json", "jsonp", "yaml", "yml")

	idSignCmd.Flags().StringP("out", "o", "", "Output path for the signed event(s) (required)")
	_ = idSignCmd.MarkFlagRequired("out")
	_ = idSignCmd.MarkFlagFilename("out", "json", "jsonp", "yaml", "yml")
}
