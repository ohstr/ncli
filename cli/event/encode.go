package event

import (
	"fmt"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip19"
	"github.com/spf13/cobra"
)

// maxHints caps the relay hints put into an nevent/naddr.
const maxHints = 3

// links are an event's NIP-19 spellings.
type links struct {
	Note   string `json:"note"`
	Nevent string `json:"nevent"`
	Naddr  string `json:"naddr,omitempty"`
}

type encodeResult struct {
	Input string   `json:"input"`
	ID    string   `json:"id"`
	Hints []string `json:"hints"`
	links
}

func newEncodeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "encode [ref...]",
		Short: "Spell events as note, nevent and naddr with relay hints",
		Long: `Prints each event's note1, its nevent1 (id, author, kind and relay
hints) and, for a replaceable or addressable event, its naddr1.

Hints are --hint values when given; otherwise the relays the event was
found on (up to 3). An event given in full has none unless --hint is set.
The counterpart of "ncli decode".`,
		Example: `  ncli event encode <hex-id> -s wss://relay.example
  ncli event encode -f event.json --hint wss://relay.example
  ncli event encode note1... --json`,
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
			hintFlag, _ := cmd.Flags().GetStringArray("hint")

			var lookups []*lookup
			_ = common.WithSpinner(cmd, fmt.Sprintf("looking up %d event(s)", len(refs)), func() error {
				lookups = res.resolve(ctx, refs, false)
				return nil
			})
			hits, missing := found(lookups)
			results := make([]encodeResult, 0, len(hits))
			for _, l := range hits {
				ev := l.best().Event
				hints := hintsFor(l, hintFlag)
				results = append(results, encodeResult{Input: l.Ref.Input, ID: ev.ID, Hints: hints, links: eventLinks(ev, hints)})
			}
			if jsonMode(cmd) {
				if len(results) > 0 || len(missing) == 0 {
					if err := printShaped(cmd, results, single); err != nil {
						return err
					}
				}
			} else {
				for i, r := range results {
					if i > 0 {
						outf(cmd, "\n")
					}
					outf(cmd, "note:   %s\nnevent: %s\n", r.Note, r.Nevent)
					if r.Naddr != "" {
						outf(cmd, "naddr:  %s\n", r.Naddr)
					}
				}
			}
			return missingError(cmd, res, missing)
		},
	}
	cmd.Flags().StringArray("hint", nil, "Relay hint to embed (repeatable; default: where the event was found)")
	return cmd
}

// hintsFor picks relay hints: explicit ones, else remote relays the event
// was found on.
func hintsFor(l *lookup, explicit []string) []string {
	if len(explicit) > 0 {
		return explicit
	}
	hints := []string{}
	for _, r := range l.foundOn() {
		if strings.HasPrefix(r, "ws://") || strings.HasPrefix(r, "wss://") {
			hints = append(hints, r)
		}
		if len(hints) == maxHints {
			break
		}
	}
	return hints
}

func eventLinks(ev *nip01.Event, hints []string) links {
	var out links
	out.Note, _ = nip19.EncodeNote(ev.ID)
	out.Nevent, _ = nip19.EncodeEvent(nip19.EventPointer{ID: ev.ID, Relays: hints, Author: ev.PubKey, Kind: ev.Kind})
	if coord := coordinate(ev); coord != "" {
		out.Naddr, _ = nip19.EncodeAddr(nip19.EntityPointer{Identifier: dTag(ev), PublicKey: ev.PubKey, Kind: ev.Kind, Relays: hints})
	}
	return out
}
