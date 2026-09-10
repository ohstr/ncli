package ncli

import (
	"fmt"
	"strings"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
)

var decodeCmd = &cobra.Command{
	Use:   "decode <entity>",
	Short: "Decode a NIP-19 entity, cash token, or circlehub1... connection",
	Long: `Decodes whichever bech32 shape you paste in:

  - a NIP-19 entity -- npub, nsec, note, nprofile, nevent, or naddr --
    into its hex key/ID plus any embedded relay hints, author, or kind
  - a NIP-CASH cash-token-family string (lokicash1..., satscash1..., ...)
    into its wallet pubkey, relay hints, and optional identity-required/
    mint-provenance fields
  - a NIP-CW circlehub1... Circle Hub connection into its wallet pubkey,
    relay hints, and optional label

A pairing secret is never included in the output, for either of the two
connection shapes. --json switches to structured JSON output on stdout.`,
	Args: common.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonMode, _ := cmd.Flags().GetBool("json")

		entity, err := client.DecodeEntity(args[0])
		if err != nil {
			return common.InvalidInputError(cmd, common.RedactSecretInput(args[0]), err)
		}

		if jsonMode {
			common.PrintJSON(entity)
			return nil
		}

		fmt.Println("type:      ", entity.Type)
		if entity.HRP != "" {
			fmt.Println("hrp:       ", entity.HRP)
		}
		if entity.PubKeyHex != "" {
			fmt.Println("pubkey:    ", entity.PubKeyHex)
		}
		if entity.PrivKeyHex != "" {
			fmt.Println("privkey:   ", entity.PrivKeyHex)
		}
		if entity.EventID != "" {
			fmt.Println("event id:  ", entity.EventID)
		}
		if entity.Identifier != "" {
			fmt.Println("identifier:", entity.Identifier)
		}
		if entity.Kind != nil {
			fmt.Println("kind:      ", *entity.Kind)
		}
		if len(entity.Relays) > 0 {
			fmt.Println("relays:    ", strings.Join(entity.Relays, ", "))
		}
		if entity.IdentityRequired != nil {
			fmt.Println("identity_required:", *entity.IdentityRequired)
		}
		if entity.MintSignatureHex != "" {
			fmt.Println("mint_signature: present")
			fmt.Println("attested_amount_millis:", *entity.AttestedAmountMillis)
		}
		if entity.Label != "" {
			fmt.Println("label:     ", entity.Label)
		}
		return nil
	},
}

func init() {
	RootCmd.AddCommand(decodeCmd)
}
