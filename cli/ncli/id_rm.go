package ncli

import (
	"bufio"
	"errors"
	"fmt"
	"os"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var idRmCmd = &cobra.Command{
	Use:     "rm <label|npub>",
	Aliases: []string{"remove"},
	Short:   "Remove a saved vault identity",
	Long: `Remove a saved vault identity, found by its label, npub or hex pubkey.
This deletes the vault's copy of the private key: back it up first
(ncli id <label> --reveal) if it isn't stored anywhere else.

Asks for confirmation; --yes skips it, and is required with --json or
without a terminal.`,
	Example: `  ncli id rm alice
  ncli id rm npub1... --yes --json`,
	Args: common.ExactArgs(1),
	RunE: runIDRm,
}

func init() {
	idCmd.AddCommand(idRmCmd)
	idRmCmd.Flags().Bool("yes", false, "Skip the confirmation prompt")
}

func runIDRm(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	yes, _ := cmd.Flags().GetBool("yes")
	target := args[0]

	entry, found, err := client.FindVaultEntry(target)
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if !found {
		return common.NotFoundError(cmd, common.RedactSecretInput(target), errors.New("identity not saved in vault"))
	}

	interactive := !jsonMode && term.IsTerminal(int(os.Stdin.Fd()))
	if !yes {
		if !interactive {
			return common.UsageError(cmd, fmt.Errorf("refusing to remove %q without --yes in a non-interactive session", entry.Label))
		}
		prompt := fmt.Sprintf("Remove %s (%s)? This deletes the vault's copy of its private key. [y/N] ", entry.Label, entry.Npub)
		if !common.PromptYesNo(bufio.NewReader(os.Stdin), prompt) {
			return common.RuntimeError(cmd, errors.New("aborted"))
		}
	}

	if _, err := keyresolve.UnlockOrCreateVault(cmd, !interactive); err != nil {
		return err
	}
	removed, err := client.RemoveVaultEntry(entry.Npub)
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	if jsonMode {
		common.PrintJSON(map[string]any{"npub": removed.Npub, "label": removed.Label, "status": "removed"})
		return nil
	}
	fmt.Printf("removed %s (%s)\n", removed.Label, removed.Npub)
	return nil
}
