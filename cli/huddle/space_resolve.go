package huddle

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip53"
)

// activityRef is a NIP-53 activity named on the command line: either a
// kind:30312 meeting space or a kind:30313 session inside one, given as an
// naddr or a bare <kind>:<pubkey>:<d> coordinate.
type activityRef struct {
	Kind       int
	Pubkey     string
	Identifier string
	// Relays are an naddr's relay hints, which name where the event was
	// expected to be found.
	Relays []string
}

// Address is the replaceable-event coordinate, which is also what a kind:1311
// chat message puts in its "a" tag.
func (a activityRef) Address() string {
	return fmt.Sprintf("%d:%s:%s", a.Kind, a.Pubkey, a.Identifier)
}

// errNotActivityRef means the argument is a plain room id, not a space. It is
// not a user-facing failure: joining by room id is the original behavior.
var errNotActivityRef = errors.New("not an activity reference")

// coordinatePattern matches a bare <kind>:<pubkey>:<d> address. The pubkey is
// pinned to 64 hex so a room id containing colons is not mistaken for one.
var coordinatePattern = regexp.MustCompile(`^([0-9]{1,5}):([0-9a-fA-F]{64}):(.*)$`)

// parseActivityRef reads an naddr or a <kind>:<pubkey>:<d> coordinate.
// It returns errNotActivityRef for anything else, which the caller treats as a
// room id.
func parseActivityRef(arg string) (activityRef, error) {
	trimmed := strings.TrimSpace(arg)

	if strings.HasPrefix(trimmed, "naddr1") {
		pointer, err := nip19.DecodeAddr(trimmed)
		if err != nil {
			return activityRef{}, fmt.Errorf("invalid naddr: %w", err)
		}
		ref := activityRef{
			Kind:       pointer.Kind,
			Pubkey:     pointer.PublicKey,
			Identifier: pointer.Identifier,
			Relays:     pointer.Relays,
		}
		return ref, validateActivityKind(ref.Kind)
	}

	if match := coordinatePattern.FindStringSubmatch(trimmed); match != nil {
		kind, err := strconv.Atoi(match[1])
		if err != nil {
			return activityRef{}, errNotActivityRef
		}
		ref := activityRef{
			Kind:       kind,
			Pubkey:     strings.ToLower(match[2]),
			Identifier: match[3],
		}
		return ref, validateActivityKind(ref.Kind)
	}

	return activityRef{}, errNotActivityRef
}

func validateActivityKind(kind int) error {
	switch kind {
	case nip53.KindMeetingSpace, nip53.KindMeetingRoomEvent:
		return nil
	default:
		return fmt.Errorf("kind %d is not a meeting space (%d) or session (%d)",
			kind, nip53.KindMeetingSpace, nip53.KindMeetingRoomEvent)
	}
}

// huddleEndpointPattern splits a full huddle audio URL into the relay base and
// the room id, so a space that publishes its endpoint outright is taken at its
// word rather than guessed at.
var huddleEndpointPattern = regexp.MustCompile(`^(.*?)/huddle/([^/]+)/audio/?$`)

// transportFromSpace works out which relay to dial and which room id to ask
// for, from a space's own tags.
//
// NIP-53 does not say how a meeting space maps onto a huddle room id -- it
// only carries a service URL and an optional endpoint -- so this is ncli's
// reading of it, in order:
//
//  1. an `endpoint` that is a full /huddle/<id>/audio URL names both outright;
//  2. otherwise `service` is the relay and the space's own `d` tag is the room
//     id, which is the convention a relay serving its own spaces falls into.
//
// A publisher doing something else needs the room id passed directly.
func transportFromSpace(space *nip53.MeetingSpace) (*url.URL, string, error) {
	if space.Endpoint != "" {
		if match := huddleEndpointPattern.FindStringSubmatch(strings.TrimSpace(space.Endpoint)); match != nil {
			base, err := transportURL(match[1])
			if err != nil {
				return nil, "", fmt.Errorf("space endpoint %q: %w", space.Endpoint, err)
			}
			return base, match[2], nil
		}
	}

	if space.Service == "" {
		return nil, "", errors.New("space names no service URL to dial")
	}
	base, err := transportURL(space.Service)
	if err != nil {
		return nil, "", fmt.Errorf("space service %q: %w", space.Service, err)
	}
	if space.Identifier == "" {
		return nil, "", errors.New("space has no identifier to use as a room id")
	}
	return base, space.Identifier, nil
}

// transportURL normalizes a published URL to the ws/wss the huddle socket is
// dialed over. An http(s) URL is accepted and mapped, since a space may
// publish either for the same host.
func transportURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "ws", "wss":
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "":
		return nil, errors.New("no URL scheme")
	default:
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("no host")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u, nil
}
