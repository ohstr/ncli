package event

import (
	"fmt"
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func newGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get [ref...]",
		Short: "Print events exactly as a relay served them",
		Long: `Fetches each referenced event and prints it unchanged, ready to pipe
into another command. One reference prints one event object; several
print an array. Where each came from goes to stderr.

For an id, the first copy that verifies wins. For an naddr or
coordinate every relay is asked and the newest version wins.

Unlike "find", which searches by filter, get resolves references and
uses their relay hints and the author's outbox relays.`,
		Example: `  ncli event get nevent1...
  ncli event get naddr1... | jq .content
  ncli event get note1... note1... -s wss://relay.example > events.json`,
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

			var lookups []*lookup
			_ = common.WithSpinner(cmd, fmt.Sprintf("looking up %d event(s)", len(refs)), func() error {
				lookups = res.resolve(ctx, refs, false)
				return nil
			})
			hits, missing := found(lookups)
			events := make([]*nip01.Event, 0, len(hits))
			for _, l := range hits {
				best := l.best()
				log.Info().Msgf("%s: found on %s", l.Ref.Input, best.Source)
				events = append(events, best.Event)
			}
			if len(events) > 0 || len(missing) == 0 {
				if err := printShaped(cmd, events, single); err != nil {
					return err
				}
			}
			return missingError(cmd, res, missing)
		},
	}
}
