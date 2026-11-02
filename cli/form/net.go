package form

import (
	"context"
	"errors"
	"fmt"
	stdnet "net"
	"slices"
	"strings"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/nip59"
	"github.com/ohstr/nmilat/nipLS"
	"github.com/ohstr/nmilat/nippf"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// transport is the network: relays queried and published to. The commands
// use relays; tests swap in a fake.
type transport interface {
	Query(ctx context.Context, relays []string, filters *nip01.SubscriptionFilterGroup, timeout time.Duration, auth client.Signer) ([]*nip01.Event, error)
	Publish(ctx context.Context, relays []string, events []*nip01.Event, auth client.Signer) (*client.PublishReport, error)
}

type relayTransport struct{}

func (relayTransport) Query(ctx context.Context, relays []string, filters *nip01.SubscriptionFilterGroup, timeout time.Duration, auth client.Signer) ([]*nip01.Event, error) {
	targets, err := client.TargetsFromRelayList(relays)
	if err != nil {
		return nil, err
	}
	return client.QueryTargetsWithAuth(ctx, targets, filters, timeout, auth)
}

func (relayTransport) Publish(ctx context.Context, relays []string, events []*nip01.Event, auth client.Signer) (*client.PublishReport, error) {
	targets, err := client.TargetsFromRelayList(relays)
	if err != nil {
		return nil, err
	}
	return client.PublishToTargets(ctx, targets, events, auth)
}

// Swapped by tests.
var (
	network   transport = relayTransport{}
	signerFor           = social.Signer
	clock               = time.Now
)

// wrapBackdate: how far NIP-59 may move a gift wrap's created_at into the
// past, so a since on wraps reaches back that much further.
const wrapBackdate = 2 * 24 * time.Hour

// place is where forms are: a group on one relay (--group, --relay), or
// relays (-s) holding public forms and the gift wraps sent to you.
type place struct {
	group   string
	relay   string
	relays  []string
	inbox   bool // --inbox: also your DM relays (kind 10050) and each recipient's
	timeout time.Duration
}

func addPlaceFlags(cmd *cobra.Command) {
	cmd.Flags().String("group", "", "A NIP-29 group's id: the form is in that group (with --relay)")
	cmd.Flags().String("relay", "", "The group's relay (with --group)")
	social.AddRelaysFlag(cmd, "Comma-separated relays: public forms and the gift wraps of private ones")
	cmd.Flags().Bool("inbox", false, "Private forms: also the DM relays (kind 10050) found on -s, yours to read and each recipient's to send (default: -s only)")
	cmd.Flags().Duration("timeout", 30*time.Second, "Max time to wait per relay")
	social.AddSignerFlag(cmd)
	social.AddAttestFlags(cmd)
}

// readPlace reads --group/--relay or -s.
func readPlace(cmd *cobra.Command) (place, error) {
	f := cmd.Flags()
	var p place
	p.group, _ = f.GetString("group")
	p.relay, _ = f.GetString("relay")
	p.timeout, _ = f.GetDuration("timeout")
	p.inbox, _ = f.GetBool("inbox")
	if p.group != "" || p.relay != "" {
		if p.group == "" || p.relay == "" {
			return p, common.InvocationError(cmd, errors.New("--group and --relay go together: a group lives on one relay"))
		}
		u, _, err := client.ResolveRelayURL(p.relay)
		if err != nil {
			return p, common.InvalidInputError(cmd, p.relay, err)
		}
		p.relay = u.String()
		return p, nil
	}
	targets, err := social.Relays(cmd)
	if err != nil {
		return p, err
	}
	for _, t := range targets.Relays {
		p.relays = append(p.relays, t.Label())
	}
	return p, nil
}

// netError classifies a relay failure.
func netError(cmd *cobra.Command, err error) error {
	if errors.Is(err, client.ErrNoReachableTargets) {
		return common.NetworkError(cmd, "", err)
	}
	if errors.Is(err, client.ErrRestricted) {
		return common.AuthError(cmd, err)
	}
	if le := keyresolve.LoginError(cmd, err); le != nil {
		return le
	}
	return common.RuntimeError(cmd, err)
}

// item is one form, response or result as read: a signed event, or a rumor
// opened from a gift wrap (private).
type item struct {
	ev      *nip01.Event
	private bool
}

var formKinds = []int{nippf.KindForm, nippf.KindResponse, nippf.KindResult}

// inboxes: each pubkey's DM relays (its latest kind 10050) found on relays.
func inboxes(ctx context.Context, relays []string, pubkeys []string, timeout time.Duration, auth client.Signer) map[string][]string {
	out := map[string][]string{}
	if len(pubkeys) == 0 {
		return out
	}
	evs, err := network.Query(ctx, relays, nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{Kinds: []int{10050}, Authors: pubkeys}), timeout, auth)
	if err != nil {
		log.Warn().Err(err).Msg("--inbox: DM relay lists (kind 10050) not read; using -s only")
		return out
	}
	latest := map[string]*nip01.Event{}
	for _, ev := range evs {
		if ev.Kind != 10050 || ev.Verify(nip01.WithoutPowCheck()) != nil {
			continue
		}
		if l := latest[ev.PubKey]; l == nil || ev.CreatedAt > l.CreatedAt {
			latest[ev.PubKey] = ev
		}
	}
	for pk, ev := range latest {
		for _, t := range ev.Tags {
			if len(t) > 1 && t[0] == "relay" && t[1] != "" && !slices.Contains(out[pk], t[1]) {
				out[pk] = append(out[pk], t[1])
			}
		}
	}
	return out
}

func union(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		for _, r := range l {
			if !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	return out
}

// gather reads the forms, responses and results at p that concern me: in a
// group, all of them; on relays, the gift wraps sent to me (opened: rumors)
// and the public forms naming me or by me, with their responses and
// results. ids: forms to read whatever their p tags (an id given in full).
func gather(ctx context.Context, cmd *cobra.Command, p place, signer client.Crypto, since uint64, ids []string) ([]item, error) {
	me := strings.ToLower(signer.PubKey())
	verified := func(evs []*nip01.Event) []item {
		var out []item
		for _, ev := range evs {
			if slices.Contains(formKinds, ev.Kind) && ev.Verify(nip01.WithoutPowCheck()) == nil {
				out = append(out, item{ev: ev})
			}
		}
		return out
	}
	var items []item
	err := common.WithSpinner(cmd, "reading forms", func() error {
		if p.group != "" {
			evs, err := network.Query(ctx, []string{p.relay}, nip01.NewSubscriptionFilterGroup(
				&nip01.SubscriptionFilter{Kinds: formKinds, Tags: map[string][]string{"h": {p.group}}, Since: since}), p.timeout, signer)
			if err != nil {
				return err
			}
			items = verified(evs)
			return nil
		}
		relays := p.relays
		if p.inbox {
			relays = union(relays, inboxes(ctx, p.relays, []string{me}, p.timeout, signer)[me])
		}
		// private: the gift wraps sent to me (my own copies included)
		wf := &nip01.SubscriptionFilter{Kinds: []int{nip59.KindGiftWrap}, Tags: map[string][]string{"p": {me}}}
		if back := uint64(wrapBackdate.Seconds()); since > back {
			wf.Since = since - back
		}
		wraps, err := network.Query(ctx, relays, nip01.NewSubscriptionFilterGroup(wf), p.timeout, signer)
		if err != nil {
			return err
		}
		var skipped, invalid int
		for _, w := range wraps {
			r, err := nippf.Open(ctx, signer, w)
			switch {
			case err == nil:
			case errors.Is(err, nip46.ErrDenied):
				skipped++
				continue
			case transportFailure(err):
				return err
			default:
				invalid++
				continue
			}
			if slices.Contains(formKinds, r.Kind) && uint64(r.CreatedAt) >= since {
				items = append(items, item{ev: r, private: true})
			}
		}
		if skipped > 0 {
			log.Info().Int("skipped", skipped).Msg("gift wraps the signer would not open")
		}
		if invalid > 0 {
			log.Warn().Int("invalid", invalid).Msg("gift wraps could not be opened")
		}
		// public: forms naming me or by me (and the ids asked for)
		filters := nip01.NewSubscriptionFilterGroup(
			&nip01.SubscriptionFilter{Kinds: []int{nippf.KindForm}, Tags: map[string][]string{"p": {me}}, Since: since},
			&nip01.SubscriptionFilter{Kinds: []int{nippf.KindForm}, Authors: []string{me}, Since: since})
		if len(ids) > 0 {
			filters.Add(&nip01.SubscriptionFilter{Kinds: []int{nippf.KindForm}, IDs: ids})
		}
		evs, err := network.Query(ctx, p.relays, filters, p.timeout, signer)
		if err != nil && !errors.Is(err, client.ErrRestricted) {
			return err
		}
		forms := verified(evs)
		var formIDs []string
		for _, it := range forms {
			if !slices.Contains(formIDs, it.ev.ID) {
				formIDs = append(formIDs, it.ev.ID)
			}
		}
		items = append(items, forms...)
		if len(formIDs) == 0 {
			return nil
		}
		evs, err = network.Query(ctx, p.relays, nip01.NewSubscriptionFilterGroup(
			&nip01.SubscriptionFilter{Kinds: []int{nippf.KindResponse, nippf.KindResult}, Tags: map[string][]string{"e": formIDs}}), p.timeout, signer)
		if err != nil && !errors.Is(err, client.ErrRestricted) {
			return err
		}
		items = append(items, verified(evs)...)
		return nil
	})
	if err != nil {
		return nil, netError(cmd, err)
	}
	return items, nil
}

// transportFailure: the signer unreachable, rather than a wrap it could not
// open.
func transportFailure(err error) bool {
	var ne stdnet.Error
	return errors.Is(err, nipLS.ErrConnClosed) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne)
}

// delivery is what a send did: the event's id, its wraps (private) and the
// relays' answers.
type delivery struct {
	ID     string                `json:"id"`
	Kind   int                   `json:"kind"`
	Form   string                `json:"form,omitempty"`
	Where  string                `json:"where"` // private, group or public
	Group  string                `json:"group,omitempty"`
	To     []string              `json:"to,omitempty"`
	Wraps  []nippfWrap           `json:"wraps,omitempty"`
	Report *client.PublishReport `json:"report"`
}

type nippfWrap struct {
	To string `json:"to"`
	ID string `json:"id"`
}

// deliver sends ev, unsigned: privately (one rumor gift-wrapped to each of
// to and to me, each wrap to its recipient's DM relays and -s), in p's group,
// or signed to p's relays and hints.
func deliver(ctx context.Context, cmd *cobra.Command, p place, signer client.Crypto, ev *nip01.Event, private bool, group string, to []string, hints []string) (*delivery, error) {
	d := &delivery{Kind: ev.Kind}
	switch {
	case private:
		d.Where = "private"
		social.AddAuth(signer, ev) // --auth: the rumor carries it, inside the seal
		wraps, err := nippf.Wrap(ctx, signer, ev, to)
		if err != nil {
			return nil, social.SignerError(cmd, err)
		}
		d.ID = ev.ID
		var keys []string
		for _, w := range wraps {
			keys = append(keys, w.To)
		}
		var boxes map[string][]string
		if p.inbox {
			boxes = inboxes(ctx, p.relays, keys, p.timeout, signer)
		}
		// one publish per set of relays
		byRelays := map[string][]*nip01.Event{}
		var order []string
		for _, w := range wraps {
			d.Wraps = append(d.Wraps, nippfWrap{To: w.To, ID: w.Wrap.ID})
			if w.To != strings.ToLower(signer.PubKey()) {
				d.To = append(d.To, w.To)
			}
			k := strings.Join(union(boxes[w.To], p.relays), ",")
			if _, ok := byRelays[k]; !ok {
				order = append(order, k)
			}
			byRelays[k] = append(byRelays[k], w.Wrap)
		}
		d.Report = &client.PublishReport{}
		for _, k := range order {
			rep, err := network.Publish(ctx, strings.Split(k, ","), byRelays[k], signer)
			if err != nil {
				return nil, netError(cmd, err)
			}
			merge(d.Report, rep)
		}
		return d, nil
	case group != "":
		d.Where, d.Group = "group", group
		if err := signer.Sign(ctx, ev); err != nil {
			return nil, social.SignerError(cmd, err)
		}
		d.ID = ev.ID
		rep, err := network.Publish(ctx, []string{p.relay}, []*nip01.Event{ev}, signer)
		if err != nil {
			return nil, netError(cmd, err)
		}
		d.Report = rep
		return d, nil
	}
	d.Where = "public"
	if err := signer.Sign(ctx, ev); err != nil {
		return nil, social.SignerError(cmd, err)
	}
	d.ID = ev.ID
	rep, err := network.Publish(ctx, union(p.relays, hints), []*nip01.Event{ev}, signer)
	if err != nil {
		return nil, netError(cmd, err)
	}
	d.Report = rep
	return d, nil
}

func merge(into, rep *client.PublishReport) {
	if rep == nil {
		return
	}
	into.Attempted += rep.Attempted
	into.Succeeded += rep.Succeeded
	into.Failed += rep.Failed
	into.Results = append(into.Results, rep.Results...)
	if into.LoginError == nil {
		into.LoginError = rep.LoginError
	}
}

// printDelivery prints what a send did, then its error if a relay refused.
func printDelivery(cmd *cobra.Command, d *delivery, what string) error {
	if j, _ := cmd.Flags().GetBool("json"); j {
		printJSON(cmd, d)
	} else {
		w := cmd.OutOrStdout()
		switch d.Where {
		case "private":
			names := make([]string, 0, len(d.To))
			for _, k := range d.To {
				names = append(names, social.Npub(k))
			}
			_, _ = fmt.Fprintf(w, "%s %s, privately to %s (and a copy to you)\n", what, d.ID, strings.Join(names, ", "))
		case "group":
			_, _ = fmt.Fprintf(w, "%s %s in group %s\n", what, d.ID, d.Group)
		default:
			_, _ = fmt.Fprintf(w, "%s %s\n", what, d.ID)
		}
		social.PrintReportLines(cmd, d.Report)
	}
	return social.ReportError(cmd, d.Report)
}
