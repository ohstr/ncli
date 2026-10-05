package groups

import (
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip29"
	"github.com/ohstr/nmilat/utils"
	"github.com/spf13/cobra"
)

func newMembersCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "members",
		Short: "Add or remove a group member",
		RunE:  common.RequireSubcommand,
	}

	cmd.AddCommand(newMembersAddCommand())
	cmd.AddCommand(newMembersRemoveCommand())

	return cmd
}

func newMembersAddCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "add <group-id> <pubkey> [role...]",
		Short:   "Add a pubkey to a group, with optional roles",
		Example: `  ncli groups members add standup <pubkey> admin`,
		Args:    common.MinimumNArgs(2),
		RunE:    runMembersAdd,
	}
	return cmd
}

func runMembersAdd(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	groupID, memberPubkey, roles := args[0], args[1], args[2:]
	if err := utils.Validate32Key(memberPubkey); err != nil {
		return common.InvalidInputError(cmd, memberPubkey, err)
	}

	relayURL, pubKeyHex, privKeyHex, err := resolveWriteTarget(cmd)
	if err != nil {
		return err
	}

	ev := nip29.NewPutUser(pubKeyHex, groupID, memberPubkey, roles...)
	return publishGroupEvent(ctx, cmd, relayURL, groupID, ev, privKeyHex)
}

func newMembersRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "remove <group-id> <pubkey>",
		Short:   "Remove a pubkey from a group",
		Example: `  ncli groups members remove standup <pubkey>`,
		Args:    common.ExactArgs(2),
		RunE:    runMembersRemove,
	}
	return cmd
}

func runMembersRemove(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	groupID, memberPubkey := args[0], args[1]
	if err := utils.Validate32Key(memberPubkey); err != nil {
		return common.InvalidInputError(cmd, memberPubkey, err)
	}

	relayURL, pubKeyHex, privKeyHex, err := resolveWriteTarget(cmd)
	if err != nil {
		return err
	}

	ev := nip29.NewRemoveUser(pubKeyHex, groupID, memberPubkey)
	return publishGroupEvent(ctx, cmd, relayURL, groupID, ev, privKeyHex)
}
