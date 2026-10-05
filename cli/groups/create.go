package groups

import (
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip29"
	"github.com/spf13/cobra"
)

// randomGroupIDBytes is the entropy behind a generated group id: 8 bytes
// (16 hex characters) is short enough to type back if needed, long enough
// that two independently generated ids won't collide in practice.
const randomGroupIDBytes = 8

func newCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create [group-id]",
		Short: "Create a new NIP-29 group",
		Long: `Existence is established by the relay reacting to this event -- there is
no separate "create" step beyond publishing it, and any id the creator
picks is valid. A random id is generated if group-id is omitted.

The group is created private and closed by default on a relay following
the nmilat NIP-29 implementation; use "groups edit" afterwards to open it
up.`,
		Example: `  ncli groups create standup
  ncli groups create`,
		Args: common.MaximumNArgs(1),
		RunE: runCreate,
	}
	return cmd
}

func runCreate(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	groupID := ""
	if len(args) == 1 {
		groupID = args[0]
	}
	if groupID == "" {
		id, err := randomHex(randomGroupIDBytes)
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
		groupID = id
	}

	relayURL, pubKeyHex, privKeyHex, err := resolveWriteTarget(cmd)
	if err != nil {
		return err
	}

	ev := nip29.NewCreateGroup(pubKeyHex, groupID)
	return publishGroupEvent(ctx, cmd, relayURL, groupID, ev, privKeyHex)
}
