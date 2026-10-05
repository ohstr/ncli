package groups

import (
	"fmt"
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip29"
	"github.com/spf13/cobra"
)

func newJoinCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "join <group-id>",
		Short: "Request to join a group",
		Long: `There is no second step: on success the relay auto-grants membership
itself (issuing its own kind:9000 put-user), so this request either worked
or it didn't -- nothing further to poll or confirm.`,
		Example: `  ncli groups join standup
  ncli groups join standup --invite-code abc123 --reason "here for the standup"`,
		Args: common.ExactArgs(1),
		RunE: runJoin,
	}

	cmd.Flags().String("invite-code", "", "Invite code, if the group requires one")
	cmd.Flags().String("reason", "", "Message to include with the request")

	return cmd
}

func runJoin(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	groupID := args[0]
	code, _ := cmd.Flags().GetString("invite-code")
	reason, _ := cmd.Flags().GetString("reason")

	relayURL, pubKeyHex, privKeyHex, err := resolveWriteTarget(cmd)
	if err != nil {
		return err
	}

	ev := nip29.NewJoinRequest(pubKeyHex, groupID, code, reason)
	report, err := signAndPublish(ctx, cmd, relayURL, groupID, ev, privKeyHex)
	if err != nil {
		return err
	}

	jsonMode, _ := cmd.Flags().GetBool("json")
	if !jsonMode && report.AllSucceeded() {
		fmt.Println("join request sent -- the relay grants membership automatically on acceptance, no further step needed")
	}
	printPublishReport(cmd, report)
	return failedPublishErr(cmd, report)
}
