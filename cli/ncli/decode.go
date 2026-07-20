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
	Short: "Decode a NIP-19 bech32 entity",
	Long: `Decodes any NIP-19 bech32 entity -- npub, nsec, note, nprofile, nevent, or
naddr -- into its hex key/ID plus any embedded relay hints, author, or
kind.

--json switches to structured JSON output on stdout.`,
	Args: cobra.ExactArgs(1),
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
		return nil
	},
}

func init() {
	RootCmd.AddCommand(decodeCmd)
}
