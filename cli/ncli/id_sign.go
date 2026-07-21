package ncli

import (
	"fmt"
	"strings"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
)

var idSignCmd = &cobra.Command{
	Use:   "sign",
	Short: "Sign one or more unsigned events with a Nostr identity",
	Long: `Sign an unsigned event (or an array of them) with the private key behind
--identity -- a saved vault label or an nsec resolves to a private key
directly; a pubkey-only identity (npub/hex/nprofile/nip-05, not saved in
the vault) has no private key to sign with and is rejected.

--events accepts a single event object or an array, the same shapes "ncli
publish"'s --events and "ncli miner check"'s --events already accept -- and
--out is written back in the same shape it was read in, so the result
chains straight into "ncli publish --events <out>" or "ncli miner check
--events <out>" with no reshaping.

If an event already declares a pubkey that conflicts with --identity's
resolved pubkey, this fails rather than silently re-signing it under a
different key.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := cmd.ValidateRequiredFlags(); err != nil {
			return common.UsageError(cmd, err)
		}
		if _, err := validateArgFile(cmd, "events", true, ".json", ".jsonp", ".yaml", ".yml"); err != nil {
			return common.UsageError(cmd, err)
		}
		if _, err := validateArgFile(cmd, "out", false, ".json", ".jsonp", ".yaml", ".yml"); err != nil {
			return common.UsageError(cmd, err)
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonMode, _ := cmd.Flags().GetBool("json")

		identity, _ := cmd.Flags().GetString("identity")
		resolved, err := client.ResolveIdentifier(identity)
		if err != nil {
			return keyresolve.ClassifyIdentifierError(cmd, identity, err)
		}

		privKeyHex, err := keyresolve.ResolveSigningKey(cmd, jsonMode, resolved)
		if err != nil {
			return err
		}
		if privKeyHex == "" {
			return common.AuthError(cmd, fmt.Errorf("identity %q has no private key available to sign with", common.RedactSecretInput(identity)))
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
			if event.PubKey != "" && !strings.EqualFold(event.PubKey, resolved.PubKeyHex) {
				return common.InvalidInputError(cmd, eventsPath, fmt.Errorf("event pubkey %q conflicts with --identity's resolved pubkey %q", event.PubKey, resolved.PubKeyHex))
			}
		}

		for _, event := range events {
			if err := event.Sign(privKeyHex); err != nil {
				return common.RuntimeError(cmd, fmt.Errorf("failed to sign event: %w", err))
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
				"pubkey": resolved.PubKeyHex,
				"ids":    ids,
				"count":  len(events),
				"out":    outPath,
			})
			return nil
		}

		// The signed result is this command's output, not narration -- same
		// stdout-only-holds-the-result convention as miner mine/check and
		// publish's text mode.
		for _, id := range ids {
			fmt.Println("id:    ", id)
		}
		fmt.Println("pubkey:", resolved.PubKeyHex)
		fmt.Println("count: ", len(events))
		fmt.Println("out:   ", outPath)
		return nil
	},
}

func init() {
	idCmd.AddCommand(idSignCmd)

	idSignCmd.Flags().String("identity", "", "Vault label/nsec identity to sign with (required; must resolve to a private key -- a pubkey-only npub/hex/nprofile/nip-05 is rejected)")
	idSignCmd.MarkFlagRequired("identity")

	idSignCmd.Flags().StringP("events", "e", "", "Path to a single unsigned event object or an array of them (required)")
	idSignCmd.MarkFlagRequired("events")
	idSignCmd.MarkFlagFilename("events", "json", "jsonp", "yaml", "yml")

	idSignCmd.Flags().StringP("out", "o", "", "Output path for the signed event(s) (required)")
	idSignCmd.MarkFlagRequired("out")
	idSignCmd.MarkFlagFilename("out", "json", "jsonp", "yaml", "yml")
}
