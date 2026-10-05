package groups

import (
	"fmt"
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip29"
	"github.com/spf13/cobra"
)

func newLeaveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "leave <group-id>",
		Short: "Request to leave a group",
		Long: `There is no second step: on success the relay auto-issues its own
kind:9001 remove-user for the caller.`,
		Example: `  ncli groups leave standup
  ncli groups leave standup --reason "moving teams"`,
		Args: common.ExactArgs(1),
		RunE: runLeave,
	}

	cmd.Flags().String("reason", "", "Message to include with the request")

	return cmd
}

func runLeave(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	groupID := args[0]
	reason, _ := cmd.Flags().GetString("reason")

	relayURL, pubKeyHex, privKeyHex, err := resolveWriteTarget(cmd)
	if err != nil {
		return err
	}

	ev := nip29.NewLeaveRequest(pubKeyHex, groupID, reason)
	report, err := signAndPublish(ctx, cmd, relayURL, groupID, ev, privKeyHex)
	if err != nil {
		return err
	}

	jsonMode, _ := cmd.Flags().GetBool("json")
	if !jsonMode && report.AllSucceeded() {
		fmt.Println("leave request sent -- the relay removes membership automatically, no further step needed")
	}
	printPublishReport(cmd, report)
	return failedPublishErr(cmd, report)
}
