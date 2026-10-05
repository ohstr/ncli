package groups

import (
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip29"
	"github.com/ohstr/nmilat/utils"
	"github.com/spf13/cobra"
)

func newDeleteEventCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "delete-event <group-id> <event-id>",
		Short:   "Delete one event from a group",
		Example: `  ncli groups delete-event standup <event-id>`,
		Args:    common.ExactArgs(2),
		RunE:    runDeleteEvent,
	}
	return cmd
}

func runDeleteEvent(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	groupID, eventID := args[0], args[1]
	if err := utils.Validate32Key(eventID); err != nil {
		return common.InvalidInputError(cmd, eventID, err)
	}

	relayURL, pubKeyHex, privKeyHex, err := resolveWriteTarget(cmd)
	if err != nil {
		return err
	}

	ev := nip29.NewDeleteEvent(pubKeyHex, groupID, eventID)
	return publishGroupEvent(ctx, cmd, relayURL, groupID, ev, privKeyHex)
}
