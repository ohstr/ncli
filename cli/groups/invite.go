package groups

import (
	"fmt"
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip29"
	"github.com/spf13/cobra"
)

// randomInviteCodeBytes is the entropy behind a generated invite code -- 16
// bytes (32 hex characters) matches the pairing-secret strength ncli already
// uses elsewhere (cli/bunker.NewSecret), appropriate here since anyone who
// learns the code can join a closed group.
const randomInviteCodeBytes = 16

func newInviteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "invite <group-id>",
		Short: "Create an invite code for a group",
		Long: `A generated code is printed in the publish report's event, same as any
other published event -- there is no separate "show me the code" step.`,
		Example: `  ncli groups invite standup
  ncli groups invite standup --code my-code`,
		Args: common.ExactArgs(1),
		RunE: runInvite,
	}

	cmd.Flags().String("code", "", "Invite code (generated if omitted)")

	return cmd
}

func runInvite(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	groupID := args[0]

	code, _ := cmd.Flags().GetString("code")
	if code == "" {
		generated, err := randomHex(randomInviteCodeBytes)
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
		code = generated
	}

	relayURL, pubKeyHex, privKeyHex, err := resolveWriteTarget(cmd)
	if err != nil {
		return err
	}

	ev := nip29.NewCreateInvite(pubKeyHex, groupID, code)
	report, err := signAndPublish(ctx, cmd, relayURL, groupID, ev, privKeyHex)
	if err != nil {
		return err
	}

	// The code lives in the event's own tags, which PublishResult doesn't
	// echo back -- without printing it here, a generated code would be
	// unrecoverable the moment this command returns.
	jsonMode, _ := cmd.Flags().GetBool("json")
	if jsonMode {
		common.PrintJSON(map[string]any{"code": code, "publish": report})
	} else {
		fmt.Printf("invite code: %s\n", code)
		printPublishReport(cmd, report)
	}
	return failedPublishErr(cmd, report)
}
