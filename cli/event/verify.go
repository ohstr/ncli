package event

import (
	"context"
	"fmt"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip09"
	"github.com/spf13/cobra"
)

// verifyReport is verify's result for one input.
type verifyReport struct {
	Input     string        `json:"input"`
	Status    string        `json:"status"` // valid, invalid, missing
	ID        string        `json:"id,omitempty"`
	PubKey    string        `json:"pubkey,omitempty"`
	Kind      *int          `json:"kind,omitempty"`
	CreatedAt uint64        `json:"created_at,omitempty"`
	Source    string        `json:"source,omitempty"`
	Checks    []check       `json:"checks"`
	Relays    []relayAnswer `json:"relays,omitempty"`
}

func newVerifyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify [ref...]",
		Short: "Verify events' ids and signatures, plus whatever they claim",
		Long: `Checks that each event's id is the hash of its content and that its
signature is valid for its pubkey. When fetched by reference, it also
checks the relay returned what was asked for (the id, or the naddr's
coordinate, author and kind).

Present on the event, these are checked too: a NIP-13 nonce, a NIP-40
expiration, a NIP-26 delegation, a NIP-OA owner attestation, and a NIP-57
zap request or receipt.

Requirements: --author, --kind, --min-pow, --max-skew. Network checks:
--deleted looks for the author's own NIP-09 deletion; --cross-relays asks
every relay and fails if any serves a copy that doesn't verify.

Exits 0 only if every event is valid. An invalid event exits 1 (the full
report is on stdout first); an event that couldn't be found exits 4, 6 or
7 like "find".`,
		Example: `  ncli event verify nevent1...
  ncli event verify note1... --author npub1... -s wss://relay.example
  ncli event verify -f signed.json --min-pow 20 --json
  ncli dump ... | ncli event verify --offline
  ncli event verify naddr1... --cross-relays --deleted`,
		RunE: runVerify,
	}
	cmd.Flags().String("author", "", "Require this signer: npub, hex, nprofile, nip-05 or vault label")
	cmd.Flags().Int("kind", -1, "Require this kind")
	cmd.Flags().Int("min-pow", 0, "Require at least this NIP-13 difficulty")
	cmd.Flags().Duration("max-skew", 0, "Fail events dated more than this far in the future (0 = don't check)")
	cmd.Flags().Bool("deleted", false, "Fail events their author has deleted (NIP-09; asks relays)")
	cmd.Flags().Bool("cross-relays", false, "Ask every relay and fail if any serves a copy that doesn't verify")
	return cmd
}

func runVerify(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	refs, single, err := collectRefs(cmd, args)
	if err != nil {
		return err
	}
	want, err := expectFromFlags(cmd)
	if err != nil {
		return err
	}
	res, closeAuth, err := newResolver(cmd)
	if err != nil {
		return err
	}
	defer closeAuth()

	cross, _ := cmd.Flags().GetBool("cross-relays")
	deleted, _ := cmd.Flags().GetBool("deleted")
	if res.offline && (cross || deleted) {
		return common.InvocationError(cmd, fmt.Errorf("--cross-relays and --deleted ask relays; drop --offline"))
	}

	var lookups []*lookup
	err = common.WithSpinner(cmd, fmt.Sprintf("looking up %d event(s)", len(refs)), func() error {
		lookups = res.resolve(ctx, refs, cross)
		return nil
	})
	if err != nil {
		return err
	}

	reports := make([]verifyReport, 0, len(lookups))
	invalid := 0
	var missing []*lookup
	for _, l := range lookups {
		rep := verifyReport{Input: l.Ref.Input, Checks: []check{}}
		if cross || !l.Ref.inline() {
			rep.Relays = l.Asked
		}
		best := l.best()
		if best == nil {
			rep.Status = "missing"
			missing = append(missing, l)
			reports = append(reports, rep)
			continue
		}
		ev := best.Event
		kind := ev.Kind
		rep.ID, rep.PubKey, rep.Kind, rep.CreatedAt, rep.Source = ev.ID, ev.PubKey, &kind, ev.CreatedAt, best.Source
		rep.Checks = checkEvent(ev, l.Ref, want)
		if cross {
			rep.Checks = append(rep.Checks, checkCopies(l))
		}
		if deleted {
			rep.Checks = append(rep.Checks, res.checkDeleted(ctx, l, ev))
		}
		rep.Status = verdict(rep.Checks)
		if rep.Status == "invalid" {
			invalid++
		}
		reports = append(reports, rep)
	}

	if jsonMode(cmd) {
		if err := printShaped(cmd, reports, single); err != nil {
			return err
		}
	} else {
		for _, rep := range reports {
			printVerifyText(cmd, rep)
		}
	}

	if invalid > 0 {
		return common.RuntimeError(cmd, fmt.Errorf("%d of %d event(s) failed verification", invalid, len(reports)))
	}
	return missingError(cmd, res, missing)
}

func expectFromFlags(cmd *cobra.Command) (expect, error) {
	want := expect{Kind: -1, Now: time.Now()}
	want.Kind, _ = cmd.Flags().GetInt("kind")
	want.MinPow, _ = cmd.Flags().GetInt("min-pow")
	want.MaxSkew, _ = cmd.Flags().GetDuration("max-skew")
	if a, _ := cmd.Flags().GetString("author"); a != "" {
		insp, err := client.ResolveIdentifier(a)
		if err != nil {
			return want, keyresolve.ClassifyIdentifierError(cmd, a, err)
		}
		want.Author = insp.PubKeyHex
	}
	return want, nil
}

// checkCopies fails if any relay served a copy of the event that doesn't
// verify. For an addressable event it reports relays still serving an older
// version, which is normal and not a failure.
func checkCopies(l *lookup) check {
	have := len(l.foundOn())
	if have == 0 && len(l.Copies) <= 1 {
		return skip("copies", "no relay holds it")
	}
	var bad []string
	for _, c := range l.Copies {
		if c.Event.Verify(nip01.WithoutPowCheck()) != nil {
			bad = append(bad, c.Source)
		}
	}
	if len(bad) > 0 {
		return fail("copies", "invalid copy from "+strings.Join(bad, ", "))
	}
	detail := fmt.Sprintf("held by %d of %d relay(s)", have, len(l.Asked))
	if l.Ref.Addr {
		best := l.best()
		var stale []string
		for _, c := range l.Copies {
			if c.Event.ID != best.Event.ID {
				stale = append(stale, c.Source)
			}
		}
		if len(stale) > 0 {
			detail += "; older version on " + strings.Join(stale, ", ")
		}
	}
	return ok("copies", detail)
}

// checkDeleted asks the relays the event was looked up on for a NIP-09
// deletion by the event's own author.
func (r *resolver) checkDeleted(ctx context.Context, l *lookup, ev *nip01.Event) check {
	f := &nip01.SubscriptionFilter{Kinds: []int{nip09.KindDeletion}, Authors: []string{ev.PubKey}, Tags: map[string][]string{"e": {ev.ID}}}
	group := nip01.NewSubscriptionFilterGroup(f)
	if coord := coordinate(ev); coord != "" {
		group.Add(&nip01.SubscriptionFilter{Kinds: []int{nip09.KindDeletion}, Authors: []string{ev.PubKey}, Tags: map[string][]string{"a": {coord}}})
	}
	relays := append(r.firstRelays(l.Ref), r.outboxRelays(ctx, ev.PubKey, l.Ref.Hints)...)
	ts := targets(relays, nil)
	if len(ts) == 0 {
		return skip("deleted", "no relay to ask")
	}
	reached := 0
	for _, res := range readAll(ctx, ts, group, r.timeout, r.auth) {
		if res.Err != nil {
			continue
		}
		reached++
		for _, del := range res.Events {
			if del.PubKey != ev.PubKey || del.Verify() != nil {
				continue
			}
			if coord := coordinate(ev); coord != "" && deletesAddress(del, coord) && del.CreatedAt >= ev.CreatedAt {
				return fail("deleted", fmt.Sprintf("deleted by %s on %s", del.ID, res.Target))
			}
			if deletesID(del, ev.ID) {
				return fail("deleted", fmt.Sprintf("deleted by %s on %s", del.ID, res.Target))
			}
		}
	}
	if reached == 0 {
		return skip("deleted", "no relay could be reached")
	}
	return ok("deleted", fmt.Sprintf("no deletion on %d relay(s)", reached))
}

func deletesID(del *nip01.Event, id string) bool {
	for _, t := range del.Tags {
		if len(t) >= 2 && t[0] == "e" && strings.EqualFold(t[1], id) {
			return true
		}
	}
	return false
}

func deletesAddress(del *nip01.Event, coord string) bool {
	for _, t := range del.Tags {
		if len(t) >= 2 && t[0] == "a" && t[1] == coord {
			return true
		}
	}
	return false
}

// coordinate is ev's kind:pubkey:d address when it is replaceable or
// addressable, else "".
func coordinate(ev *nip01.Event) string {
	switch {
	case ev.Kind >= 30000 && ev.Kind < 40000:
		return fmt.Sprintf("%d:%s:%s", ev.Kind, ev.PubKey, dTag(ev))
	case ev.Kind == 0 || ev.Kind == 3 || (ev.Kind >= 10000 && ev.Kind < 20000):
		return fmt.Sprintf("%d:%s:", ev.Kind, ev.PubKey)
	}
	return ""
}

func printVerifyText(cmd *cobra.Command, rep verifyReport) {
	switch rep.Status {
	case "missing":
		outf(cmd, "missing  %s  (asked %d relay(s))\n", rep.Input, len(rep.Relays))
		for _, a := range rep.Relays {
			if a.Error != "" {
				outf(cmd, "  %s: %s\n", a.Relay, a.Error)
			}
		}
		return
	}
	outf(cmd, "%-7s  %s  kind %d  by %s", rep.Status, rep.ID, *rep.Kind, npub(rep.PubKey))
	if rep.Source != "" {
		outf(cmd, "  from %s", rep.Source)
	}
	outf(cmd, "\n")
	for _, c := range rep.Checks {
		if c.Status == statusSkip {
			continue
		}
		line := fmt.Sprintf("  %-4s %s", c.Status, c.Name)
		if c.Detail != "" {
			line += ": " + c.Detail
		}
		outf(cmd, "%s\n", line)
	}
}
