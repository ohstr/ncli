package ncli

import (
	"errors"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
)

var publishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Publish signed events to one or more relays",
	Long: `Send one or more already-signed events (e.g. produced by "ncli miner mine"
--identity, or "ncli dump") to one or more relays, waiting for each relay's
OK. --events accepts either a single event object or a JSON array of
events, and every event is sent to every relay in --relays -- the full
(event, relay) result is reported. Omitting --relays falls back to the
relays configured via "ncli prefs relays add". Exits non-zero if any
(event, relay) pair fails.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := cmd.ValidateRequiredFlags(); err != nil {
			return common.UsageError(cmd, err)
		}
		if _, err := validateArgFile(cmd, "events", true, ".json", ".jsonp"); err != nil {
			return common.UsageError(cmd, err)
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		jsonMode, _ := cmd.Flags().GetBool("json")

		eventsPath, err := validateArgFile(cmd, "events", true, ".json", ".jsonp")
		if err != nil {
			return common.RuntimeError(cmd, err)
		}

		events, err := client.LoadEvents(eventsPath)
		if err != nil {
			return common.InvalidInputError(cmd, eventsPath, err)
		}

		var targetsSpec *client.TargetsSpec
		if relaysVal, _ := cmd.Flags().GetString("relays"); relaysVal != "" {
			targetsSpec, err = client.TargetsFromRelayList(splitCommaSeparated(relaysVal))
			if err != nil {
				return common.InvalidInputError(cmd, relaysVal, err)
			}
		} else {
			targetsSpec, err = client.TargetsFromPrefs()
			if err != nil {
				return common.NotFoundError(cmd, "", err)
			}
		}

		report, err := client.PublishToTargets(ctx, targetsSpec, events)
		if err != nil {
			if errors.Is(err, client.ErrNoReachableTargets) {
				return common.NetworkError(cmd, "", err)
			}
			return common.RuntimeError(cmd, err)
		}

		if jsonMode {
			common.PrintJSON(report)
		} else {
			// The per-(event,relay) outcomes and summary are this command's
			// result, not narration -- same stdout-only-holds-the-result
			// convention as jsonMode's report above (see the equivalent fix
			// in cli/ncli/miner.go).
			for _, r := range report.Results {
				if r.Accepted {
					fmt.Printf("published %s to %s\n", r.ID, r.Relay)
				} else {
					fmt.Printf("failed %s to %s: %s\n", r.ID, r.Relay, r.Error)
				}
			}
			fmt.Printf("attempted %d, succeeded %d, failed %d\n", report.Attempted, report.Succeeded, report.Failed)
		}

		if !report.AllSucceeded() {
			return common.RuntimeError(cmd, fmt.Errorf("%d of %d publish attempts failed", report.Failed, report.Attempted))
		}
		return nil
	},
}

func init() {
	RootCmd.AddCommand(publishCmd)

	publishCmd.Flags().StringP("events", "e", "", "Path to a single event object or a JSON array of events (required)")
	publishCmd.MarkFlagRequired("events")
	publishCmd.MarkFlagFilename("events", "json", "jsonp")

	publishCmd.Flags().StringP("relays", "s", "", "Comma-separated relay URLs to publish to (omit to use the relays configured via \"ncli prefs relays add\")")
}
