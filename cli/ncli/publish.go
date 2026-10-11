package ncli

import (
	"errors"
	"fmt"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/spf13/cobra"
)

var publishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Publish signed events to one or more relays",
	Long: `--events accepts a single event or an array. Exits non-zero if any
event fails on any relay.

With --signer, events that have no sig are signed first: through a
bunker+unix:///path/to.sock socket signer, or with an identity like
"id sign --identity". A policy denial exits 7 before anything is sent.

Relays that require login (NIP-42): on an AUTH challenge ncli signs one
kind 22242 for the relay URL it dialed, with --signer (or --auth-signer
when the login identity differs, e.g. publishing events someone else
signed), then retries the refused event once. A failed login exits 7
with reason login_required, signer_refused or relay_refused.

Omit --relays to use the relays from "ncli prefs relays".`,
	Example: `  ncli publish -e signed.json
  ncli publish -e signed.json -s wss://relay.example.com
  ncli publish -e draft.json --signer bunker+unix:///run/signer/agent.sock
  ncli publish -e signed.json --auth-signer bunker+unix:///run/signer/agent.sock -s wss://members.example`,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := cmd.ValidateRequiredFlags(); err != nil {
			return common.InvocationOrHelp(cmd, args, err)
		}
		if _, err := validateArgFile(cmd, "events", true, ".json", ".jsonp"); err != nil {
			return common.InvocationOrHelp(cmd, args, err)
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
		signerFlag, _ := cmd.Flags().GetString("signer")
		var sgn client.Signer
		if signerFlag != "" {
			s, closeSigner, err := keyresolve.ResolveSigner(cmd, jsonMode, signerFlag)
			if err != nil {
				return err
			}
			defer closeSigner()
			sgn = s
			if err := signUnsigned(cmd, sgn, eventsPath, events); err != nil {
				return err
			}
		}
		authSigner := sgn
		if authFlag, _ := cmd.Flags().GetString("auth-signer"); authFlag != "" {
			s, closeAuth, err := keyresolve.ResolveSigner(cmd, jsonMode, authFlag)
			if err != nil {
				return err
			}
			defer closeAuth()
			authSigner = s
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

		var report *client.PublishReport
		err = common.WithSpinner(cmd, targetsMessage(fmt.Sprintf("publishing %d event(s) to", len(events)), targetsSpec), func() error {
			var pErr error
			report, pErr = client.PublishToTargets(ctx, targetsSpec, events, authSigner)
			return pErr
		})
		if err != nil {
			if errors.Is(err, client.ErrNoReachableTargets) {
				return common.NetworkError(cmd, "", err)
			}
			return common.RuntimeError(cmd, err)
		}

		if jsonMode {
			common.PrintJSON(report)
		} else {
			// Same stdout-only-holds-the-result convention as "miner mine"
			// (see cli/ncli/miner.go).
			for _, r := range report.Results {
				if r.Accepted {
					fmt.Printf("published %s to %s\n", r.ID, r.Relay)
				} else {
					fmt.Printf("failed %s to %s: %s\n", r.ID, r.Relay, r.Error)
				}
			}
			fmt.Printf("attempted %d, succeeded %d, failed %d\n", report.Attempted, report.Succeeded, report.Failed)
		}

		if report.LoginError != nil {
			return keyresolve.LoginError(cmd, report.LoginError)
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
	_ = publishCmd.MarkFlagRequired("events")
	_ = publishCmd.MarkFlagFilename("events", "json", "jsonp")

	publishCmd.Flags().String("signer", "", "Sign events that have no sig first, and log in (NIP-42) with it: bunker+unix:///path/to.sock, bunker://..., or an identity")
	publishCmd.Flags().String("auth-signer", "", "Log in (NIP-42) with this signer instead of --signer: bunker+unix:///path/to.sock, bunker://..., or an identity")
	publishCmd.Flags().StringP("relays", "s", "", "Comma-separated relay URLs to publish to (omit to use the relays configured via \"ncli prefs relays add\")")
}

// signUnsigned signs, in place, every event that has no sig yet.
func signUnsigned(cmd *cobra.Command, sgn client.Signer, eventsPath string, events []*nip01.Event) error {
	for _, ev := range events {
		if ev.Sig != "" {
			continue
		}
		if ev.PubKey != "" && !strings.EqualFold(ev.PubKey, sgn.PubKey()) {
			return common.InvalidInputError(cmd, eventsPath, fmt.Errorf("event pubkey %q conflicts with the signer's pubkey %q", ev.PubKey, sgn.PubKey()))
		}
		if err := sgn.Sign(cmd.Context(), ev); err != nil {
			return keyresolve.SignerError(cmd, eventsPath, fmt.Errorf("failed to sign event: %w", err))
		}
	}
	return nil
}
