package groups

import (
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip29"
	"github.com/spf13/cobra"
)

func newPinsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pins",
		Short: "Set a group's pinned events",
		RunE:  common.RequireSubcommand,
	}

	cmd.AddCommand(newPinsSetCommand())

	return cmd
}

func newPinsSetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <group-id>",
		Short: "Replace a group's entire pinned-events list",
		Long: `WHOLE-LIST REPLACE, NOT APPEND: this sets the pin list to exactly
--event/--address's values. Passing neither clears every pin -- there is
no separate "pin one more" operation, because the underlying kind:9010
event always carries the complete desired list.`,
		Example: `  ncli groups pins set standup --event <event-id> --event <event-id-2>
  ncli groups pins set standup   # clears every pin`,
		Args: common.ExactArgs(1),
		RunE: runPinsSet,
	}

	cmd.Flags().StringArray("event", nil, "Event id to pin (repeatable)")
	cmd.Flags().StringArray("address", nil, "Addressable event coordinate to pin, e.g. 30312:<pubkey>:<d> (repeatable)")

	return cmd
}

func runPinsSet(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	groupID := args[0]
	events, _ := cmd.Flags().GetStringArray("event")
	addresses, _ := cmd.Flags().GetStringArray("address")

	relayURL, pubKeyHex, privKeyHex, err := resolveWriteTarget(cmd)
	if err != nil {
		return err
	}

	ev := nip29.NewUpdatePinList(pubKeyHex, groupID, events, addresses)
	return publishGroupEvent(ctx, cmd, relayURL, groupID, ev, privKeyHex)
}
