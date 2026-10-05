package groups

import (
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip29"
	"github.com/spf13/cobra"
)

func newDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <group-id>",
		Short: "Delete a group",
		Long: `The relay enforces that only a group admin may do this; a non-admin
caller will see the relay's rejection surfaced as the command's error.`,
		Example: `  ncli groups delete standup`,
		Args:    common.ExactArgs(1),
		RunE:    runDelete,
	}
	return cmd
}

func runDelete(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	groupID := args[0]

	relayURL, pubKeyHex, privKeyHex, err := resolveWriteTarget(cmd)
	if err != nil {
		return err
	}

	ev := nip29.NewDeleteGroup(pubKeyHex, groupID)
	return publishGroupEvent(ctx, cmd, relayURL, groupID, ev, privKeyHex)
}
