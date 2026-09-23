package blossom

import (
	"fmt"
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nipB7"
	"github.com/spf13/cobra"
)

func newReportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report <hash>",
		Short: "Report a blob to a Blossom server",
		Long: `Submit a signed kind:1984 report event for a blob (BUD-09). Targets a
single server: --server, or the first configured one.`,
		Example: `  ncli blossom report <hash> --identity satoshi`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return common.InvocationOrHelp(cmd, args, fmt.Errorf("exactly one hash is required"))
			}
			if !nipB7.IsSHA256Hex(args[0]) {
				return common.InvalidInputError(cmd, args[0], fmt.Errorf("not a valid sha256 hash"))
			}
			if err := cmd.ValidateRequiredFlags(); err != nil {
				return common.InvocationOrHelp(cmd, args, err)
			}
			if identity, _ := cmd.Flags().GetString("identity"); identity == "" {
				return common.InvocationOrHelp(cmd, args, fmt.Errorf("--identity is required"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			jsonMode, _ := cmd.Flags().GetBool("json")
			hash := args[0]
			reportType, _ := cmd.Flags().GetString("type")
			reason, _ := cmd.Flags().GetString("reason")

			identity, _ := cmd.Flags().GetString("identity")
			privKeyHex, pubKeyHex, err := resolveIdentity(cmd, jsonMode, identity)
			if err != nil {
				return err
			}

			event, err := nipB7.NewReport(pubKeyHex, []nipB7.ReportedBlob{{Hash: hash, Type: reportType}}, reason)
			if err != nil {
				return common.RuntimeError(cmd, err)
			}
			if err := event.Sign(privKeyHex); err != nil {
				return common.RuntimeError(cmd, err)
			}

			servers, err := resolveServers(cmd)
			if err != nil {
				return common.NotFoundError(cmd, "", err)
			}
			server := servers[0]

			timeout, _ := cmd.Flags().GetDuration("timeout")
			hc := newHTTPClient(timeout)

			err = common.WithSpinner(cmd, fmt.Sprintf("reporting %s to %s", shortHash(hash), server), func() error {
				return hc.Report(ctx, server, event)
			})
			if err != nil {
				return classifyHTTPError(cmd, server, err)
			}

			if jsonMode {
				common.PrintJSON(map[string]any{"hash": hash, "server": server, "event": event.ID})
				return nil
			}
			fmt.Println("reported", hash, "to", server)
			return nil
		},
	}

	cmd.Flags().String("type", "", `Report type (e.g. "nudity", "malware", "illegal", "spam") (required)`)
	_ = cmd.MarkFlagRequired("type")
	cmd.Flags().String("reason", "", "Human-readable reason (required)")
	_ = cmd.MarkFlagRequired("reason")

	return cmd
}
