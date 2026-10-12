package event

import (
	"errors"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/spf13/cobra"
)

func newRebroadcastCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rebroadcast [ref...] --to <relays>",
		Short: "Copy signed events to other relays unchanged",
		Long: `Fetches each event (or takes it in full) and publishes it, as signed,
to every --to relay. No key is needed: nothing is re-signed. An event
that doesn't verify is refused before anything is sent.

--relays is where events are looked up; --to is where they go.
--auth-identity logs in (NIP-42) on either side when a relay asks.
Exits non-zero if any event fails on any relay, like "publish".`,
		Example: `  ncli event rebroadcast nevent1... --to wss://relay.mine.example
  ncli event get naddr1... | ncli event rebroadcast --to relay.a.example,relay.b.example`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			toVal, _ := cmd.Flags().GetString("to")
			to := splitComma(toVal)
			if len(to) == 0 {
				return common.InvocationOrHelp(cmd, args, errors.New(`required flag "to" not set`))
			}
			dest, err := client.TargetsFromRelayList(to)
			if err != nil {
				return common.InvalidInputError(cmd, toVal, err)
			}

			refs, _, err := collectRefs(cmd, args)
			if err != nil {
				return err
			}
			res, closeAuth, err := newResolver(cmd)
			if err != nil {
				return err
			}
			defer closeAuth()

			var lookups []*lookup
			_ = common.WithSpinner(cmd, fmt.Sprintf("looking up %d event(s)", len(refs)), func() error {
				lookups = res.resolve(ctx, refs, false)
				return nil
			})
			hits, missing := found(lookups)
			if err := missingError(cmd, res, missing); err != nil {
				return err
			}

			events := make([]*nip01.Event, 0, len(hits))
			for _, l := range hits {
				ev := l.best().Event
				if err := ev.Verify(nip01.WithoutPowCheck()); err != nil {
					return common.InvalidInputError(cmd, l.Ref.Input, fmt.Errorf("refusing to rebroadcast %s: %w", ev.ID, err))
				}
				events = append(events, ev)
			}

			var report *client.PublishReport
			err = common.WithSpinner(cmd, fmt.Sprintf("publishing %d event(s) to %d relay(s)", len(events), len(dest.Relays)), func() error {
				var pErr error
				report, pErr = client.PublishToTargets(ctx, dest, events, res.auth)
				return pErr
			})
			if err != nil {
				if errors.Is(err, client.ErrNoReachableTargets) {
					return common.NetworkError(cmd, "", err)
				}
				return common.RuntimeError(cmd, err)
			}

			if jsonMode(cmd) {
				if err := printJSON(cmd, report); err != nil {
					return err
				}
			} else {
				for _, r := range report.Results {
					if r.Accepted {
						outf(cmd, "published %s to %s\n", r.ID, r.Relay)
					} else {
						outf(cmd, "failed %s to %s: %s\n", r.ID, r.Relay, r.Error)
					}
				}
				outf(cmd, "attempted %d, succeeded %d, failed %d\n", report.Attempted, report.Succeeded, report.Failed)
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
	cmd.Flags().String("to", "", "Comma-separated relays to publish to (required)")
	return cmd
}
