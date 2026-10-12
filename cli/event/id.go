package event

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func newIDCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "id [event-json...]",
		Short: "Compute the id of unsigned events",
		Long: `Reads events in full (argument, --file or stdin), fills in what is
missing and computes each id, printing the events back in the shape they
were read. Never signs and never touches the network.

--pubkey fills an empty pubkey (an npub, hex, nprofile, nip-05 or vault
label; no private key needed) and refuses an event that declares a
different one. created_at 0 becomes now. A sig that no longer matches
the new id is dropped, with a warning.

--id-only prints just the ids, one per line.`,
		Example: `  echo '{"kind":1,"content":"hi","tags":[]}' | ncli event id --pubkey npub1...
  ncli event id -f draft.json --id-only`,
		RunE: func(cmd *cobra.Command, args []string) error {
			refs, single, err := collectRefs(cmd, args)
			if err != nil {
				return err
			}
			pubkey := ""
			if p, _ := cmd.Flags().GetString("pubkey"); p != "" {
				insp, err := client.ResolveIdentifier(p)
				if err != nil {
					return keyresolve.ClassifyIdentifierError(cmd, p, err)
				}
				pubkey = insp.PubKeyHex
			}

			events := make([]*nip01.Event, 0, len(refs))
			for _, rf := range refs {
				if !rf.inline() {
					return common.InvalidInputError(cmd, rf.Input, errors.New("event id needs the event itself (JSON), not a reference to it"))
				}
				ev := rf.Event
				if err := fillID(ev, pubkey, time.Now()); err != nil {
					return common.InvalidInputError(cmd, rf.Input, err)
				}
				events = append(events, ev)
			}

			if only, _ := cmd.Flags().GetBool("id-only"); only {
				for _, ev := range events {
					outf(cmd, "%s\n", ev.ID)
				}
				return nil
			}
			return printShaped(cmd, events, single)
		},
	}
	cmd.Flags().String("pubkey", "", "Fill an empty pubkey: npub, hex, nprofile, nip-05 or vault label")
	cmd.Flags().Bool("id-only", false, "Print only the ids, one per line")
	return cmd
}

// fillID completes an unsigned event and sets its id.
func fillID(ev *nip01.Event, pubkey string, now time.Time) error {
	switch {
	case ev.PubKey == "" && pubkey == "":
		return errors.New("event has no pubkey; pass --pubkey")
	case ev.PubKey == "":
		ev.PubKey = pubkey
	case pubkey != "" && !strings.EqualFold(ev.PubKey, pubkey):
		return fmt.Errorf("event pubkey %s conflicts with --pubkey %s", ev.PubKey, pubkey)
	}
	ev.PubKey = strings.ToLower(ev.PubKey)
	if ev.CreatedAt == 0 {
		ev.CreatedAt = uint64(now.Unix())
	}
	if ev.Tags == nil {
		ev.Tags = [][]string{}
	}
	hash, err := ev.HashID()
	if err != nil {
		return err
	}
	id := hex.EncodeToString(hash)
	if ev.Sig != "" && id != ev.ID {
		log.Warn().Msgf("dropping the sig of %s: it signed a different id (%s)", id, ev.ID)
		ev.Sig = ""
	}
	ev.ID = id
	return nil
}
