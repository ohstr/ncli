package huddle

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip53"
	"github.com/rs/zerolog/log"
)

// spaceLookupTimeout bounds the per-relay query for the space event.
const spaceLookupTimeout = 10 * time.Second

// joinTarget is everything resolving a space yields: where to dial, which
// room to ask for, and what chat messages are scoped to.
type joinTarget struct {
	// Activity is the coordinate kind:1311 messages carry in their "a" tag.
	Activity string
	// ChatRelay is the Nostr relay chat is published to and read from. It is
	// the space's service URL -- the endpoint, when present, names the media
	// socket, which need not be the same host.
	ChatRelay *url.URL
	// Transport is the base URL the huddle audio socket is dialed on.
	Transport *url.URL
	Room      string
	Space     *nip53.MeetingSpace
}

// resolveJoinTarget looks up ref on the given targets and works out how to
// join it. A 30313 session is resolved through to its parent space, since the
// session says when a meeting is but the space says where it is.
func resolveJoinTarget(ctx context.Context, ref activityRef, targets *client.TargetsSpec) (*joinTarget, error) {
	space := ref
	if ref.Kind == nip53.KindMeetingRoomEvent {
		event, err := fetchAddressable(ctx, ref, targets)
		if err != nil {
			return nil, err
		}
		session, err := nip53.ParseMeetingRoomEvent(event)
		if err != nil {
			return nil, fmt.Errorf("session %s is malformed: %w", ref.Address(), err)
		}
		if session.Status == nip53.StatusEnded {
			return nil, fmt.Errorf("session %q has ended", session.Title)
		}
		space = activityRef{
			Kind:       nip53.KindMeetingSpace,
			Pubkey:     session.SpacePubkey,
			Identifier: session.SpaceIdentifier,
		}
		if session.SpaceRelay != "" {
			space.Relays = []string{session.SpaceRelay}
		}
	}

	event, err := fetchAddressable(ctx, space, targets)
	if err != nil {
		return nil, err
	}
	parsed, err := nip53.ParseMeetingSpace(event)
	if err != nil {
		return nil, fmt.Errorf("space %s is malformed: %w", space.Address(), err)
	}

	// A closed space is a refusal to host; private is not, it just means the
	// space is not advertised, and someone holding its coordinate was told
	// about it deliberately.
	if parsed.Status == nip53.SpaceStatusClosed {
		return nil, fmt.Errorf("space %q is closed", parsed.Room)
	}

	transport, room, err := transportFromSpace(parsed)
	if err != nil {
		return nil, err
	}

	chatRelay := transport
	if parsed.Service != "" {
		if u, err := transportURL(parsed.Service); err == nil {
			chatRelay = u
		}
	}

	return &joinTarget{
		// Chat is scoped to whatever was named: joining a session puts the
		// conversation on that session, not on every meeting the space hosts.
		Activity:  ref.Address(),
		ChatRelay: chatRelay,
		Transport: transport,
		Room:      room,
		Space:     parsed,
	}, nil
}

// fetchAddressable fetches one replaceable event by coordinate, keeping the
// newest copy. `d` and the author are both relay-filterable, so this asks the
// relay a precise question rather than scanning.
func fetchAddressable(ctx context.Context, ref activityRef, targets *client.TargetsSpec) (*nip01.Event, error) {
	filters := nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds:   []int{ref.Kind},
		Authors: []string{ref.Pubkey},
		Tags:    map[string][]string{"d": {ref.Identifier}},
	})

	events, err := client.QueryTargets(ctx, targets, filters, spaceLookupTimeout)
	if err != nil {
		return nil, err
	}

	var newest *nip01.Event
	for _, event := range events {
		if event == nil || event.Kind != ref.Kind || event.PubKey != ref.Pubkey {
			continue
		}
		if newest == nil || event.CreatedAt > newest.CreatedAt {
			newest = event
		}
	}
	if newest == nil {
		return nil, fmt.Errorf("%s was not found on any queried relay", ref.Address())
	}
	return newest, nil
}

// joinTargets builds the relay list the space lookup runs against: an explicit
// --relay wins, then an naddr's own relay hints, then the configured prefs
// relays.
func joinTargets(relayFlag string, ref activityRef) (*client.TargetsSpec, error) {
	if relayFlag != "" {
		return client.TargetsFromRelayList([]string{relayFlag})
	}
	if len(ref.Relays) > 0 {
		targets, err := client.TargetsFromRelayList(ref.Relays)
		if err == nil {
			return targets, nil
		}
		log.Warn().Err(err).Msg("ignoring unusable relay hints from the naddr")
	}
	targets, err := client.TargetsFromPrefs()
	if err != nil {
		return nil, errors.New("--relay is required: no relays are configured and the reference carries no usable hints")
	}
	return targets, nil
}
