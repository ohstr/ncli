package event

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip65"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// maxOutboxRelays caps how many of an author's NIP-65 write relays are
// tried, so one huge relay list can't turn a lookup into a crawl.
const maxOutboxRelays = 8

// readConcurrency bounds how many relays are read at once when every
// relay is asked (seen, --cross-relays).
const readConcurrency = 8

// resolver finds events on relays. Relays are tried in this order: the
// reference's own hints, then --relays (or, without it, "ncli prefs
// relays"), then the author's NIP-65 write relays.
type resolver struct {
	cmd       *cobra.Command
	relays    []string
	onlyHints bool
	noHints   bool
	noOutbox  bool
	offline   bool
	timeout   time.Duration
	auth      client.Signer

	outbox map[string][]string
}

func newResolver(cmd *cobra.Command) (*resolver, func(), error) {
	r := &resolver{cmd: cmd, outbox: map[string][]string{}}
	relaysVal, _ := cmd.Flags().GetString("relays")
	r.relays = splitComma(relaysVal)
	r.onlyHints, _ = cmd.Flags().GetBool("only-hints")
	r.noHints, _ = cmd.Flags().GetBool("no-hints")
	r.noOutbox, _ = cmd.Flags().GetBool("no-outbox")
	r.offline, _ = cmd.Flags().GetBool("offline")
	r.timeout, _ = cmd.Flags().GetDuration("timeout")
	if r.onlyHints && r.noHints {
		return nil, nil, common.InvocationError(cmd, errors.New("--only-hints and --no-hints contradict each other"))
	}
	if r.offline && len(r.relays) > 0 {
		return nil, nil, common.InvocationError(cmd, errors.New("--offline cannot be combined with --relays"))
	}

	closeFn := func() {}
	if v, _ := cmd.Flags().GetString("auth-identity"); v != "" && !r.offline {
		jsonMode, _ := cmd.Flags().GetBool("json")
		sgn, c, err := keyresolve.ResolveSigner(cmd, jsonMode, v)
		if err != nil {
			return nil, nil, err
		}
		r.auth, closeFn = sgn, c
	}
	return r, closeFn, nil
}

func splitComma(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// baseRelays is --relays, or the prefs relays when it isn't given. A
// missing prefs list is not an error here: hints alone may be enough.
func (r *resolver) baseRelays() []string {
	if len(r.relays) > 0 {
		return r.relays
	}
	if r.onlyHints {
		return nil
	}
	targets, err := client.TargetsFromPrefs()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(targets.Relays))
	for _, t := range targets.Relays {
		out = append(out, t.Relay)
	}
	return out
}

// firstRelays are the relays tried before the author's outbox: the
// reference's hints, then the base relays.
func (r *resolver) firstRelays(rf *ref) []string {
	var out []string
	if !r.noHints {
		out = append(out, rf.Hints...)
	}
	if !r.onlyHints {
		out = append(out, r.baseRelays()...)
	}
	return out
}

// targets turns relay strings into deduplicated targets, skipping (with a
// warning) any that don't parse -- a bad hint inside an nevent shouldn't
// sink the lookup.
func targets(relays []string, skip map[string]bool) []*client.FlowSpec {
	var out []*client.FlowSpec
	seen := map[string]bool{}
	for _, raw := range relays {
		spec, err := client.TargetsFromRelayList([]string{raw})
		if err != nil {
			log.Warn().Msgf("skipping relay %q: %v", raw, err)
			continue
		}
		t := spec.Relays[0]
		key := relayKey(t.Label())
		if seen[key] || skip[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}

func relayKey(label string) string {
	return strings.TrimRight(strings.ToLower(label), "/")
}

// outboxRelays returns author's NIP-65 write relays, looked up once per
// author on the base relays and the given hints.
func (r *resolver) outboxRelays(ctx context.Context, author string, hints []string) []string {
	if r.noOutbox || r.onlyHints || r.offline || author == "" {
		return nil
	}
	if got, ok := r.outbox[author]; ok {
		return got
	}
	r.outbox[author] = nil
	where := targets(append(append([]string{}, hints...), r.baseRelays()...), nil)
	if len(where) == 0 {
		return nil
	}
	filters := nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds:   []int{nip65.KindRelayListMetadata},
		Authors: []string{author},
	})
	var newest *nip01.Event
	for _, res := range readAll(ctx, where, filters, r.timeout, r.auth) {
		for _, ev := range res.Events {
			if ev.PubKey != author || ev.Verify() != nil {
				continue
			}
			if newest == nil || ev.CreatedAt > newest.CreatedAt {
				newest = ev
			}
		}
	}
	if newest == nil {
		return nil
	}
	list, err := nip65.ParseRelayList(newest)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range list.Relays {
		if e.Write {
			out = append(out, e.URL)
		}
		if len(out) == maxOutboxRelays {
			break
		}
	}
	r.outbox[author] = out
	return out
}

// readAll reads filters from every target at once (bounded), keeping
// results in target order.
func readAll(ctx context.Context, ts []*client.FlowSpec, filters *nip01.SubscriptionFilterGroup, timeout time.Duration, auth client.Signer) []client.TargetRead {
	out := make([]client.TargetRead, len(ts))
	sem := make(chan struct{}, readConcurrency)
	var wg sync.WaitGroup
	for i, t := range ts {
		wg.Add(1)
		go func(i int, t *client.FlowSpec) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = client.ReadTarget(ctx, t, filters, timeout, auth)
		}(i, t)
	}
	wg.Wait()
	return out
}

// relayAnswer is what one relay said about one reference.
type relayAnswer struct {
	Relay      string `json:"relay"`
	Found      bool   `json:"found"`
	Restricted bool   `json:"restricted,omitempty"`
	Error      string `json:"error,omitempty"`
	ElapsedMs  int64  `json:"elapsed_ms"`
}

// eventCopy is one copy of an event and where it came from.
type eventCopy struct {
	Source string
	Event  *nip01.Event
}

// lookup is everything learned about one reference.
type lookup struct {
	Ref    *ref
	Copies []eventCopy
	Asked  []relayAnswer
}

// best picks the copy a command should act on: for an id, the first copy
// that verifies (else the first); for an address, the newest copy that
// verifies (else the newest).
func (l *lookup) best() *eventCopy {
	if len(l.Copies) == 0 {
		return nil
	}
	var pick, pickValid *eventCopy
	for i := range l.Copies {
		c := &l.Copies[i]
		valid := c.Event.Verify(nip01.WithoutPowCheck()) == nil
		if l.Ref.Addr {
			if pick == nil || newer(c.Event, pick.Event) {
				pick = c
			}
			if valid && (pickValid == nil || newer(c.Event, pickValid.Event)) {
				pickValid = c
			}
			continue
		}
		if pick == nil {
			pick = c
		}
		if valid && pickValid == nil {
			pickValid = c
		}
	}
	if pickValid != nil {
		return pickValid
	}
	return pick
}

// newer orders versions of a replaceable event the NIP-01 way: later
// created_at wins, ties go to the lowest id.
func newer(a, b *nip01.Event) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt > b.CreatedAt
	}
	return a.ID < b.ID
}

// satisfied reports whether l needs no more relays in first-hit mode: an id
// is settled by one copy that verifies. An address never is, since another
// relay may hold a newer version.
func (l *lookup) satisfied() bool {
	if l.Ref.inline() {
		return true
	}
	if l.Ref.Addr {
		return false
	}
	for _, c := range l.Copies {
		if c.Event.Verify(nip01.WithoutPowCheck()) == nil {
			return true
		}
	}
	return false
}

// resolve finds every reference. With everywhere false it stops asking
// relays about an id as soon as one returns a copy that verifies; with it
// true it asks every relay (and, for an event given in full, asks relays
// too, so "seen" and --cross-relays can report who holds it).
func (r *resolver) resolve(ctx context.Context, refs []*ref, everywhere bool) []*lookup {
	lookups := make([]*lookup, len(refs))
	for i, rf := range refs {
		lookups[i] = &lookup{Ref: rf}
		if rf.inline() {
			lookups[i].Copies = []eventCopy{{Source: rf.Source, Event: rf.Event}}
		}
	}
	if r.offline {
		return lookups
	}

	pending := func(l *lookup) bool {
		if l.Ref.inline() {
			return everywhere
		}
		return everywhere || !l.satisfied()
	}

	// Stage one: hints and base relays.
	r.askEach(ctx, lookups, pending, everywhere, func(l *lookup) []string { return r.firstRelays(l.Ref) })

	// Stage two: the author's outbox relays, for whatever is still open.
	r.askEach(ctx, lookups, pending, everywhere, func(l *lookup) []string {
		author := l.Ref.Author
		if l.Ref.inline() {
			author = l.Ref.Event.PubKey
		}
		return r.outboxRelays(ctx, author, l.Ref.Hints)
	})
	return lookups
}

// askEach groups open lookups by the relays they should ask, so references
// that share relays share one REQ per relay.
func (r *resolver) askEach(ctx context.Context, lookups []*lookup, pending func(*lookup) bool, everywhere bool, relaysFor func(*lookup) []string) {
	type group struct {
		targets []*client.FlowSpec
		members []*lookup
	}
	groups := map[string]*group{}
	var order []string
	for _, l := range lookups {
		if !pending(l) {
			continue
		}
		skip := map[string]bool{}
		for _, a := range l.Asked {
			skip[relayKey(a.Relay)] = true
		}
		ts := targets(relaysFor(l), skip)
		if len(ts) == 0 {
			continue
		}
		keys := make([]string, len(ts))
		for i, t := range ts {
			keys[i] = relayKey(t.Label())
		}
		key := strings.Join(keys, "|")
		g, ok := groups[key]
		if !ok {
			g = &group{targets: ts}
			groups[key] = g
			order = append(order, key)
		}
		g.members = append(g.members, l)
	}

	for _, key := range order {
		g := groups[key]
		if everywhere {
			results := readAll(ctx, g.targets, filtersFor(g.members), r.timeout, r.auth)
			for _, res := range results {
				record(g.members, res)
			}
			continue
		}
		for _, t := range g.targets {
			var open []*lookup
			for _, l := range g.members {
				if pending(l) {
					open = append(open, l)
				}
			}
			if len(open) == 0 {
				break
			}
			log.Info().Msgf("asking %s", t.Label())
			record(open, client.ReadTarget(ctx, t, filtersFor(open), r.timeout, r.auth))
		}
	}
}

func filtersFor(ls []*lookup) *nip01.SubscriptionFilterGroup {
	g := nip01.NewSubscriptionFilterGroup()
	var ids []string
	for _, l := range ls {
		switch {
		case l.Ref.inline():
			ids = append(ids, l.Ref.Event.ID)
		case l.Ref.Addr:
			g.Add(l.Ref.filter())
		default:
			ids = append(ids, l.Ref.ID)
		}
	}
	if len(ids) > 0 {
		g.Add(&nip01.SubscriptionFilter{IDs: ids})
	}
	return g
}

// record files one relay's answer under every lookup it was asked about.
func record(ls []*lookup, res client.TargetRead) {
	for _, l := range ls {
		a := relayAnswer{Relay: res.Target, Restricted: res.Restricted, ElapsedMs: res.Elapsed.Milliseconds()}
		if res.Err != nil {
			a.Error = res.Err.Error()
		}
		for _, ev := range res.Events {
			match := false
			if l.Ref.inline() {
				match = strings.EqualFold(ev.ID, l.Ref.Event.ID)
			} else {
				match = l.Ref.matches(ev)
			}
			if !match {
				continue
			}
			a.Found = true
			if l.Ref.inline() {
				continue
			}
			l.Copies = append(l.Copies, eventCopy{Source: res.Target, Event: ev})
		}
		l.Asked = append(l.Asked, a)
	}
}

// missingError classifies references no relay could produce: every relay
// unreachable is a network failure, a relay refusing the read is auth, and
// anything else is not found.
func missingError(cmd *cobra.Command, r *resolver, missing []*lookup) error {
	if len(missing) == 0 {
		return nil
	}
	inputs := make([]string, len(missing))
	unreachable, restricted, asked := true, false, false
	for i, l := range missing {
		inputs[i] = l.Ref.Input
		for _, a := range l.Asked {
			asked = true
			if a.Restricted {
				restricted = true
			}
			if a.Error == "" {
				unreachable = false
			}
		}
	}
	msg := fmt.Sprintf("%d of the given events could not be found: %s", len(missing), strings.Join(inputs, ", "))
	switch {
	case r.offline:
		return common.InvalidInputError(cmd, strings.Join(inputs, ","), fmt.Errorf("%s (--offline only reads events given in full)", msg))
	case !asked:
		return common.NotFoundError(cmd, strings.Join(inputs, ","), fmt.Errorf("%s: no relay to ask; pass --relays, use an nevent with relay hints, or run \"ncli prefs relays add\"", msg))
	case restricted:
		return common.AuthError(cmd, fmt.Errorf("%s: a relay restricted the read (try --auth-identity)", msg))
	case unreachable:
		return common.NetworkError(cmd, "", fmt.Errorf("%s: no relay could be reached", msg))
	}
	return common.NotFoundError(cmd, strings.Join(inputs, ","), errors.New(msg))
}

// found splits lookups into the events to act on and the references
// nothing was found for.
func found(lookups []*lookup) (hits []*lookup, missing []*lookup) {
	for _, l := range lookups {
		if l.best() == nil {
			missing = append(missing, l)
			continue
		}
		hits = append(hits, l)
	}
	return hits, missing
}

// foundOn lists the relays an event was found on, in the order asked.
func (l *lookup) foundOn() []string {
	var out []string
	for _, a := range l.Asked {
		if a.Found {
			out = append(out, a.Relay)
		}
	}
	return out
}
