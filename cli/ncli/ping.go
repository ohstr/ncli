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
	Long: `Probe reachability of every target relay by connecting and issuing a
Limit-1 subscription -- a bare TCP/WebSocket connect isn't enough to
confirm a relay actually speaks the protocol. Local store paths (if any
end up in the target list) are skipped -- there's nothing to dial. There
are no filter flags: ping never reads a relay's response to judge
reachability, so a filter's content wouldn't change the result either way.

Relays are given directly as positional arguments -- "ncli ping
relay.primal.net" just works, no flag required -- or, for a relay list
kept in a file (the same shape as find/dump's --targets, though ping only
looks at its relays, not its filters), --targets <file.yaml>; pick one,
not a mix. Omitting both falls back to every relay configured via
"ncli prefs relays add".

In an interactive terminal (and without --json/--quiet), results render as
a live board; otherwise (piped, --quiet, or --json) they narrate as plain
log lines on stderr instead. --json additionally suppresses that narration
and prints a structured { results, checked, reachable, unreachable }
report to stdout, for scripting. Exits non-zero if any relay was
unreachable.`,
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
}
