package event

import (
	"fmt"
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/spf13/cobra"
)

type seenReport struct {
	Input  string        `json:"input"`
	ID     string        `json:"id,omitempty"`
	Found  int           `json:"found"`
	Asked  int           `json:"asked"`
	Relays []relayAnswer `json:"relays"`
}

func newSeenCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "seen [ref...]",
		Short: "List which relays hold each event",
		Long: `Asks every candidate relay -- hints, --relays (or prefs) and the
author's NIP-65 write relays -- and reports, per relay, whether it holds
the event, how long it took to answer, and any error.

Works for an event given in full too, e.g. to check where a publish
landed. Exits 4 if no relay holds an event.`,
		Example: `  ncli event seen nevent1...
  ncli event seen -f signed.json -s relay.a.example,relay.b.example
  ncli event seen note1... --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			refs, single, err := collectRefs(cmd, args)
			if err != nil {
				return err
			}
			res, closeAuth, err := newResolver(cmd)
			if err != nil {
				return err
			}
			defer closeAuth()
			if res.offline {
				return common.InvocationError(cmd, fmt.Errorf("seen asks relays; drop --offline"))
			}

			var lookups []*lookup
			_ = common.WithSpinner(cmd, fmt.Sprintf("asking relays about %d event(s)", len(refs)), func() error {
				lookups = res.resolve(ctx, refs, true)
				return nil
			})

			reports := make([]seenReport, 0, len(lookups))
			var nowhere []*lookup
			for _, l := range lookups {
				rep := seenReport{Input: l.Ref.Input, Relays: l.Asked, Asked: len(l.Asked), Found: len(l.foundOn())}
				if rep.Relays == nil {
					rep.Relays = []relayAnswer{}
				}
				switch {
				case l.Ref.inline():
					rep.ID = l.Ref.Event.ID
				case l.best() != nil:
					rep.ID = l.best().Event.ID
				default:
					rep.ID = l.Ref.ID
				}
				if rep.Found == 0 {
					nowhere = append(nowhere, l)
				}
				reports = append(reports, rep)
			}

			if jsonMode(cmd) {
				if err := printShaped(cmd, reports, single); err != nil {
					return err
				}
			} else {
				for i, rep := range reports {
					if i > 0 {
						outf(cmd, "\n")
					}
					outf(cmd, "%s  on %d of %d relay(s)\n", rep.ID, rep.Found, rep.Asked)
					for _, a := range rep.Relays {
						mark := "-"
						if a.Found {
							mark = "✓"
						}
						line := fmt.Sprintf("  %s %s  %dms", mark, a.Relay, a.ElapsedMs)
						if a.Restricted {
							line += "  restricted"
						}
						if a.Error != "" {
							line += "  " + a.Error
						}
						outf(cmd, "%s\n", line)
					}
				}
			}

			return missingError(cmd, res, nowhere)
		},
	}
}
