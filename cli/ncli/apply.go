package ncli

import (
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
)

var (
	options = &client.ClientOptions{}

	applyCmd = &cobra.Command{
		Use:   "apply",
		Short: "Run a stream, sync, or inspect workflow",
		Example: `  ncli apply -f sync.yaml
  ncli apply -f sync.yaml --strict-pow
  ncli apply -f stream.yaml --auth-signer bunker+unix:///run/signer/agent.sock`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cmd.ValidateRequiredFlags(); err != nil {
				return common.InvocationOrHelp(cmd, args, err)
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

			if _, err := os.Stat(fileName); err != nil {
				return &common.CLIError{Err: err, Code: common.CodeUsage, Input: fileName}
			}

			if authFlag, _ := cmd.Flags().GetString("auth-signer"); authFlag != "" {
				jsonMode, _ := cmd.Flags().GetBool("json")
				sgn, closeSigner, err := keyresolve.ResolveSigner(cmd, jsonMode, authFlag)
				if err != nil {
					return err
				}
				defer closeSigner()
				options.AuthSigner = sgn
			}

			if err := client.Process(ctx, fileName, options); err != nil {
				if errors.Is(err, client.ErrNeedsTerminal) {
					return common.UsageError(cmd, err)
				}
				if le := keyresolve.LoginError(cmd, err); le != nil {
					return le
				}
				return common.RuntimeError(cmd, err)
			}
			return nil
		},
	}
)

func init() {
	RootCmd.AddCommand(applyCmd)

	applyCmd.Flags().StringP("file", "f", "", "Path to the workflow config file (required)")
	_ = applyCmd.MarkFlagRequired("file")
	_ = applyCmd.MarkFlagFilename("file", "yaml", "yml")

	applyCmd.Flags().Bool("strict-pow", false, "Reject stream/sync events whose nonce tag doesn't meet its declared NIP-13 proof-of-work difficulty (default: accept them regardless, since PoW compliance is opt-in per NIP-13, not mandatory). Overrides the spec file's own strictPow field when passed explicitly")
	applyCmd.Flags().String("auth-signer", "", "Log in (NIP-42) to relays that require it: bunker+unix:///path.sock, bunker://..., or an identity (only login events are signed)")
}
