package ncli

import (
	"fmt"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
)

var pingCmd = &cobra.Command{
	Use:   "ping [relay...]",
	Short: "Test relay connectivity",
	Long: `Probe reachability of each target relay with a Limit-1 subscription.
Local store paths in the target list are skipped.

Give relays as positional arguments, or --targets <file.yaml> for a relay
list file -- pick one, not both. Omitting both falls back to the relays
configured via "ncli prefs relays add".

Results narrate as plain log lines on stderr by default. --tui shows a
live interactive board instead (requires a real terminal; ignored with
--json/--quiet). --json prints a structured report to stdout instead of
narrating. Exits non-zero if any relay was unreachable.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := cmd.ValidateRequiredFlags(); err != nil {
			return common.UsageError(cmd, err)
		}
		if cmd.Flags().Changed("targets") && len(args) > 0 {
			return common.UsageError(cmd, fmt.Errorf("--targets is mutually exclusive with relay arguments; a --targets file already declares its own relays"))
		}
		if cmd.Flags().Changed("targets") {
			if _, err := validateArgFile(cmd, "targets", true, ".yaml", ".yml"); err != nil {
				return common.UsageError(cmd, err)
			}
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		timeout, _ := cmd.Flags().GetDuration("timeout")
		jsonMode, _ := cmd.Flags().GetBool("json")
		quiet, _ := cmd.Flags().GetBool("quiet")
		tuiMode, _ := cmd.Flags().GetBool("tui")

		var targetsSpec *client.TargetsSpec
		var err error

		switch {
		case cmd.Flags().Changed("targets"):
			targetsPath, terr := validateArgFile(cmd, "targets", true, ".yaml", ".yml")
			if terr != nil {
				return common.RuntimeError(cmd, terr)
			}
			targetsSpec, err = client.LoadTargetsSpec(targetsPath)
			if err != nil {
				return common.InvalidInputError(cmd, targetsPath, err)
			}

		case len(args) > 0:
			targetsSpec, err = client.TargetsFromRelayList(args)
			if err != nil {
				return common.InvalidInputError(cmd, strings.Join(args, ","), err)
			}

		default:
			targetsSpec, err = client.TargetsFromPrefs()
			if err != nil {
				return common.NotFoundError(cmd, "", err)
			}
		}

		report := client.Ping(ctx, targetsSpec, client.PingOptions{
			JSON:    jsonMode,
			Quiet:   quiet,
			TUI:     tuiMode,
			Timeout: timeout,
		})

		if jsonMode {
			common.PrintJSON(report)
		}

		if !report.AllReachable() {
			return common.RuntimeError(cmd, fmt.Errorf("%d of %d relays unreachable", report.Unreachable, report.Checked))
		}
		return nil
	},
}

func init() {
	RootCmd.AddCommand(pingCmd)

	pingCmd.Flags().StringP("targets", "t", "", "Path to a YAML targets file (only its relays are used)")
	pingCmd.MarkFlagFilename("targets", "yaml", "yml")

	pingCmd.Flags().Duration("timeout", 30*time.Second, "Max time to wait per relay before giving up on it (0 = wait forever)")
	pingCmd.Flags().Bool("tui", false, "Render results as a live interactive board instead of plain log lines (requires a real terminal; ignored otherwise, or with --json/--quiet)")
}
