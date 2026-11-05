package client

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/ohstr/nmilat/nip01"
)

// A follow's stored events, paged: a relay serves
// a REQ's stored events newest first, at most its own cap (strfry 500,
// others 1000), then EOSE. A subscription resumed after a gap that holds
// more than that got only the newest: an event dated back (NIP-59 dates a
// wrap up to two days in the past) under a flood dated now was never
// delivered. So once the stored events end, the follow asks again for
// those older than the oldest it got (until), page by page, while a page
// brings an event it hadn't got yet, down to the filters' since, within a
// budget of pages and bytes. Filters with a limit or an until of their own
// are taken as they are, never paged. A phase that brought fewer than
// followPageMin events wasn't cut by a relay's cap (none serves fewer): the
// paging ends there, so a quiet resubscription asks nothing more.

// followPageMin: the fewest events a phase cut by a cap brings;
// followPages and followPageBytes bound one subscription's paging.
var (
	followPageMin   = 50
	followPages     = 100
	followPageBytes = 32 << 20
)

// followPaged, when set (tests only), is told each time a subscription's
// stored events, every page of them, were handled: what a test waits on
// before it stops a follow, instead of a wall-clock bound.
var followPaged func(relay string)

type followPager struct {
	base  *nip01.SubscriptionFilterGroup
	ok    bool   // pageable (no filter with its own limit or until), and not stopped
	since uint64 // the lowest since of the filters: paging ends there

	stored bool   // the subscription is still in its stored events
	sub    string // the page in flight ("" none)
	until  uint64 // its until (0: the subscription's own stored events)
	n      int    // events the phase brought
	fresh  int    // of them, events no earlier phase brought
	minAt  uint64 // the oldest of them
	ids    map[string]bool
	pages  int
	bytes  int
	cut    string // why paging stopped short ("" none)
}

func newFollowPager(filters *nip01.SubscriptionFilterGroup) *followPager {
	p := &followPager{base: filters, ok: true}
	first := true
	for _, f := range filters.GetAll() {
		if f.Limit != 0 || f.Until != 0 {
			p.ok = false
		}
		if first || f.Since < p.since {
			p.since, first = f.Since, false
		}
	}
	return p
}

// restart: the subscription is (re)sent; its stored events come first.
func (p *followPager) restart() {
	p.stored, p.sub, p.until, p.n, p.fresh, p.minAt = true, "", 0, 0, 0, 0
	p.ids, p.pages, p.bytes, p.cut = map[string]bool{}, 0, 0, ""
}

// stop: no more pages on this subscription.
func (p *followPager) stop() { p.ok, p.sub = false, "" }

// saw counts a stored event of the phase in flight.
func (p *followPager) saw(ev *nip01.Event) {
	p.n++
	if !p.ids[ev.ID] {
		p.ids[ev.ID] = true
		p.fresh++
	}
	if p.minAt == 0 || ev.CreatedAt < p.minAt {
		p.minAt = ev.CreatedAt
	}
	p.bytes += 256 + len(ev.Content)
	for _, t := range ev.Tags {
		for _, v := range t {
			p.bytes += len(v) + 4
		}
	}
}

// next: the until of the next page, once the phase in flight ended; false
// when paging is done (or cut: p.cut says why).
func (p *followPager) next() (uint64, bool) {
	p.sub = ""
	if !p.ok || p.n < followPageMin || p.fresh == 0 {
		return 0, false
	}
	until := p.minAt
	if p.until != 0 && until >= p.until {
		until = p.until - 1 // one second holds a whole page: on past it
	}
	if until == 0 || until < p.since {
		return 0, false
	}
	if p.pages >= followPages || p.bytes >= followPageBytes {
		p.cut = fmt.Sprintf("stored events past %d pages or %d MiB: the older ones before %d weren't read", followPages, followPageBytes>>20, until)
		p.ok = false
		return 0, false
	}
	p.pages++
	p.sub, p.until, p.n, p.fresh, p.minAt = "page-"+uuid.NewString(), until, 0, 0, 0
	return until, true
}

// filters: the subscription's filters, up to until.
func (p *followPager) filters(until uint64) *nip01.SubscriptionFilterGroup {
	out := nip01.NewSubscriptionFilterGroup()
	for _, f := range p.base.GetAll() {
		c := *f
		c.Until = until
		out.Add(&c)
	}
	return out
}
