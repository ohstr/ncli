package groups

import (
	"context"
	"net/url"
	"sort"
	"time"

	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip29"
)

// previousQueryTimeout bounds how long attachPreviousTags waits for the
// group's recent timeline before giving up -- a slow relay here shouldn't
// hang every write command indefinitely.
const previousQueryTimeout = 10 * time.Second

// attachPreviousTags queries groupID's most recent events on relayURL and
// adds up to nip29.RecommendedTimelineReferences `previous` references to
// ev -- the tag NIP-29 uses so a group write can't be replayed out of
// context. None of nmilat's nip29.NewXxx constructors add it themselves
// (the spec makes it "advice a relay may apply, not a rule this package
// enforces"), so every ncli write command funnels through here before
// signing.
//
// A group with no prior history (e.g. right after "groups create") simply
// has nothing to reference -- that's normal, not an error, so this only
// returns an error for an actual query failure, never for an empty
// timeline.
func attachPreviousTags(ctx context.Context, relayURL *url.URL, groupID string, ev *nip01.Event) error {
	targets, err := client.TargetsFromRelayList([]string{relayURL.String()})
	if err != nil {
		return err
	}

	filters := nip01.NewSubscriptionFilterGroup(
		nip01.NewFilter().WithTag(nip29.TagGroupID, groupID).WithLimit(nip29.RecommendedTimelineReferences),
	)

	events, err := client.QueryTargets(ctx, targets, filters, previousQueryTimeout)
	if err != nil {
		return err
	}

	ids := recentEventIDs(events, nip29.RecommendedTimelineReferences)
	if len(ids) == 0 {
		return nil
	}
	nip29.AddTimelineReferences(ev, ids...)
	return nil
}

// recentEventIDs returns up to max event ids from events, newest first --
// the pure selection logic behind attachPreviousTags, split out so it's
// testable without a live relay. Input order is not assumed to already be
// newest-first (a relay is free to answer in any order before EOSE).
func recentEventIDs(events []*nip01.Event, max int) []string {
	if len(events) == 0 {
		return nil
	}
	sort.Slice(events, func(i, j int) bool { return events[i].CreatedAt > events[j].CreatedAt })
	if len(events) > max {
		events = events[:max]
	}
	ids := make([]string, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}
	return ids
}
