package ncli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var idRelabelCmd = &cobra.Command{
	Use:   "relabel <identifier> <new-label>",
	Short: "Rename a saved vault identity",
	Long: `Rename a saved vault identity, found by its current label, npub, hex
pubkey, nprofile or nip-05 address. No key needed; the vault password is,
as for any vault write. An empty new label resets it to the npub.

--json or a non-terminal stdin never prompts: set NCLI_VAULT_PASSWORD.`,
	Example: `  ncli id relabel alice bob
  ncli id relabel alice@example.com work
  NCLI_VAULT_PASSWORD=pw ncli id relabel npub1... work --json`,
	Args: common.ExactArgs(2),
	RunE: runIDRelabel,
}

func init() {
	idCmd.AddCommand(idRelabelCmd)
}

func runIDRelabel(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	target, label := args[0], strings.TrimSpace(args[1])

	entry, err := findVaultTarget(cmd, target)
	if err != nil {
		return err
	}

	prev := entry.Label
	status := "unchanged"
	if label == "" {
		label = entry.Npub
	}
	if label != prev {
		// Prompts need a terminal on stdin.
		nonInteractive := jsonMode || !term.IsTerminal(int(os.Stdin.Fd()))
		if _, err := keyresolve.UnlockOrCreateVault(cmd, nonInteractive); err != nil {
			return err
		}
		entry, err = client.RelabelVaultEntry(entry.Npub, label)
		if err != nil {
			if errors.Is(err, client.ErrLabelExists) {
				return common.ConflictError(cmd, label, err)
			}
			return common.RuntimeError(cmd, err)
		}
		status = "relabeled"
	}

	if jsonMode {
		common.PrintJSON(map[string]any{
			"npub": entry.Npub, "label": entry.Label,
			"previous_label": prev, "status": status,
		})
		return nil
	}
	if status == "unchanged" {
		fmt.Printf("%s already labeled %s\n", entry.Npub, entry.Label)
	} else {
		fmt.Printf("relabeled %s -> %s (%s)\n", prev, entry.Label, entry.Npub)
	}
	return nil
}
