package ncli

import (
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
)

var (
	options = &client.ClientOptions{}

	applyCmd = &cobra.Command{
		Use:   "apply",
		Short: "Run a client workflow from a config file",
		Long:  `Run a stream, sync, or inspect workflow defined in a YAML config file.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cmd.ValidateRequiredFlags(); err != nil {
				return common.UsageError(cmd, err)
			}
			return nil
		},
		PreRun: func(cmd *cobra.Command, args []string) {
			if LogWriter != nil {
				jsonMode, _ := cmd.Flags().GetBool("json")
				common.ConfigureLogging(common.WithConsole(), common.WithFileWriter(LogWriter), common.WithJSON(jsonMode))
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			fileName, err := cmd.Flags().GetString("file")
			if err != nil {
				return common.RuntimeError(cmd, err)
			}

			if cmd.Flags().Changed("quiet") {
				quiet, _ := cmd.Flags().GetBool("quiet")
				options.Quiet = &quiet
			}

			if cmd.Flags().Changed("strict-pow") {
				strictPow, _ := cmd.Flags().GetBool("strict-pow")
				options.StrictPow = &strictPow
			}

			if err := client.Process(ctx, fileName, options); err != nil {
				return common.RuntimeError(cmd, err)
			}
			return nil
		},
	}
)

func init() {
	RootCmd.AddCommand(applyCmd)

	applyCmd.Flags().StringP("file", "f", "", "Path to the workflow config file (required)")
	applyCmd.MarkFlagRequired("file")
	applyCmd.MarkFlagFilename("file", "yaml", "yml")

	applyCmd.Flags().Bool("strict-pow", false, "Reject stream/sync events whose nonce tag doesn't meet its declared NIP-13 proof-of-work difficulty (default: accept them regardless, since PoW compliance is opt-in per NIP-13, not mandatory). Overrides the spec file's own strictPow field when passed explicitly")
}
