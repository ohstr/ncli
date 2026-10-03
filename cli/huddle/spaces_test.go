package huddle

import (
	"fmt"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip53"
	"github.com/stretchr/testify/require"
)

const (
	spaceAuthor = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	otherAuthor = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// now is the clock every case below is judged against, so a "stale" case is
// stale by construction rather than by how long the test took to run.
var now = time.Unix(1_700_000_000, 0)

func spaceEvent(t *testing.T, pubkey, identifier, status string, createdAt int64) *nip01.Event {
	t.Helper()
	ev := nip53.NewMeetingSpace(nip53.MeetingSpaceParams{
		Pubkey:     pubkey,
		Identifier: identifier,
		Room:       "Standup",
		Status:     status,
		Service:    "wss://relay.example",
		Providers:  []nip53.Participant{{Pubkey: pubkey, Role: nip53.RoleHost}},
	})
	ev.PubKey = pubkey
	ev.CreatedAt = uint64(createdAt)

	// Parsing is what the command does, so a fixture that cannot be parsed is
	// a broken test rather than a real exclusion.
	_, err := nip53.ParseMeetingSpace(ev)
	require.NoError(t, err, "fixture must be a valid kind:30312")
	return ev
}

func sessionEvent(t *testing.T, pubkey, identifier, status string, spacePubkey, spaceIdentifier string, createdAt int64) *nip01.Event {
	t.Helper()
	peers := 3
	ev := nip53.NewMeetingRoomEvent(nip53.MeetingRoomEventParams{
		Pubkey:              pubkey,
		Identifier:          identifier,
		Space:               fmt.Sprintf("%d:%s:%s", nip53.KindMeetingSpace, spacePubkey, spaceIdentifier),
		Title:               "Daily standup",
		Starts:              uint64(createdAt),
		Status:              status,
		CurrentParticipants: &peers,
	})
	ev.PubKey = pubkey
	ev.CreatedAt = uint64(createdAt)

	_, err := nip53.ParseMeetingRoomEvent(ev)
	require.NoError(t, err, "fixture must be a valid kind:30313")
	return ev
}

func fresh() int64 { return now.Unix() - 60 }

func TestSelectLiveSpaces_OpenSpaceWithALiveSession(t *testing.T) {
	events := []*nip01.Event{
		spaceEvent(t, spaceAuthor, "standup", nip53.SpaceStatusOpen, fresh()),
		sessionEvent(t, spaceAuthor, "today", nip53.StatusLive, spaceAuthor, "standup", fresh()),
	}

	live := selectLiveSpaces(events, now, nip53.DefaultStaleWindow)
	require.Len(t, live, 1)
	require.Equal(t, fmt.Sprintf("30312:%s:standup", spaceAuthor), live[0].Address)
	require.Equal(t, "Standup", live[0].Room)
	require.Equal(t, nip53.SpaceStatusOpen, live[0].Status)
	require.Len(t, live[0].Sessions, 1)
	require.Equal(t, "today", live[0].Sessions[0].Identifier)
	require.NotNil(t, live[0].Sessions[0].Participants)
	require.Equal(t, 3, *live[0].Sessions[0].Participants)
}

// "Open" is the whole filter on the space side: a private or closed space is
// not somewhere to send a caller, however live its session is.
func TestSelectLiveSpaces_ExcludesSpacesThatAreNotOpen(t *testing.T) {
	for _, status := range []string{nip53.SpaceStatusPrivate, nip53.SpaceStatusClosed} {
		t.Run(status, func(t *testing.T) {
			events := []*nip01.Event{
				spaceEvent(t, spaceAuthor, "standup", status, fresh()),
				sessionEvent(t, spaceAuthor, "today", nip53.StatusLive, spaceAuthor, "standup", fresh()),
			}
			require.Empty(t, selectLiveSpaces(events, now, nip53.DefaultStaleWindow))
		})
	}
}

// An open space whose only session has finished (or has not started) is not
// reported at all -- the ask was live, not merely existing.
func TestSelectLiveSpaces_ExcludesSessionsThatAreNotLive(t *testing.T) {
	for _, status := range []string{nip53.StatusEnded, nip53.StatusPlanned} {
		t.Run(status, func(t *testing.T) {
			events := []*nip01.Event{
				spaceEvent(t, spaceAuthor, "standup", nip53.SpaceStatusOpen, fresh()),
				sessionEvent(t, spaceAuthor, "today", status, spaceAuthor, "standup", fresh()),
			}
			require.Empty(t, selectLiveSpaces(events, now, nip53.DefaultStaleWindow))
		})
	}
}

// A host whose process died leaves a session claiming live forever. The spec
// lets a client read an un-refreshed live event as ended, which is the only
// thing keeping an abandoned meeting out of this list.
func TestSelectLiveSpaces_ExcludesAStaleLiveSession(t *testing.T) {
	stale := now.Add(-2 * time.Hour).Unix()
	events := []*nip01.Event{
		spaceEvent(t, spaceAuthor, "standup", nip53.SpaceStatusOpen, fresh()),
		sessionEvent(t, spaceAuthor, "today", nip53.StatusLive, spaceAuthor, "standup", stale),
	}

	require.Empty(t, selectLiveSpaces(events, now, nip53.DefaultStaleWindow),
		"a live session older than the window must read as ended")

	// The same event is live again under a window wide enough to cover it,
	// proving the exclusion came from staleness and not something else.
	require.Len(t, selectLiveSpaces(events, now, 3*time.Hour), 1)
}

// A stale copy from one relay beside a fresh one from another is normal, so
// the newest (kind, pubkey, d) has to win rather than whichever arrived last.
func TestSelectLiveSpaces_NewestReplaceableCopyWins(t *testing.T) {
	events := []*nip01.Event{
		spaceEvent(t, spaceAuthor, "standup", nip53.SpaceStatusOpen, fresh()-600),
		spaceEvent(t, spaceAuthor, "standup", nip53.SpaceStatusClosed, fresh()),
		sessionEvent(t, spaceAuthor, "today", nip53.StatusLive, spaceAuthor, "standup", fresh()),
	}

	require.Empty(t, selectLiveSpaces(events, now, nip53.DefaultStaleWindow),
		"the newer copy closed the space, so the older open one must not win")
}

func TestSelectLiveSpaces_NewestSessionCopyWins(t *testing.T) {
	events := []*nip01.Event{
		spaceEvent(t, spaceAuthor, "standup", nip53.SpaceStatusOpen, fresh()),
		sessionEvent(t, spaceAuthor, "today", nip53.StatusLive, spaceAuthor, "standup", fresh()-600),
		sessionEvent(t, spaceAuthor, "today", nip53.StatusEnded, spaceAuthor, "standup", fresh()),
	}

	require.Empty(t, selectLiveSpaces(events, now, nip53.DefaultStaleWindow),
		"the session ended in its newest copy")
}

// The same d tag from a different pubkey is a different space, since the
// coordinate is (kind, pubkey, d).
func TestSelectLiveSpaces_SameIdentifierFromAnotherAuthorIsAnotherSpace(t *testing.T) {
	events := []*nip01.Event{
		spaceEvent(t, spaceAuthor, "standup", nip53.SpaceStatusOpen, fresh()),
		sessionEvent(t, spaceAuthor, "today", nip53.StatusLive, spaceAuthor, "standup", fresh()),
		spaceEvent(t, otherAuthor, "standup", nip53.SpaceStatusOpen, fresh()),
		sessionEvent(t, otherAuthor, "today", nip53.StatusLive, otherAuthor, "standup", fresh()),
	}

	live := selectLiveSpaces(events, now, nip53.DefaultStaleWindow)
	require.Len(t, live, 2)
	require.True(t, live[0].Address < live[1].Address, "spaces must be sorted by address")
}

// A session pointing at a space these relays do not carry has nothing to
// attach to, and must not invent one or panic.
func TestSelectLiveSpaces_SessionWithNoParentSpaceIsDropped(t *testing.T) {
	events := []*nip01.Event{
		sessionEvent(t, spaceAuthor, "today", nip53.StatusLive, spaceAuthor, "missing", fresh()),
	}
	require.Empty(t, selectLiveSpaces(events, now, nip53.DefaultStaleWindow))
}

// An open space with nothing running is not an error, just not a hit.
func TestSelectLiveSpaces_OpenSpaceWithNoSessionIsNotListed(t *testing.T) {
	events := []*nip01.Event{
		spaceEvent(t, spaceAuthor, "standup", nip53.SpaceStatusOpen, fresh()),
	}
	require.Empty(t, selectLiveSpaces(events, now, nip53.DefaultStaleWindow))
}

// Malformed and foreign events share the relay with real ones; one bad
// publisher must not hide every valid space.
func TestSelectLiveSpaces_SkipsUnparseableAndUnrelatedEvents(t *testing.T) {
	events := []*nip01.Event{
		nil,
		{Kind: nip53.KindMeetingSpace, PubKey: spaceAuthor, CreatedAt: uint64(fresh())}, // no d/room/status/service
		{Kind: 1, PubKey: spaceAuthor, CreatedAt: uint64(fresh())},
		spaceEvent(t, spaceAuthor, "standup", nip53.SpaceStatusOpen, fresh()),
		sessionEvent(t, spaceAuthor, "today", nip53.StatusLive, spaceAuthor, "standup", fresh()),
	}

	live := selectLiveSpaces(events, now, nip53.DefaultStaleWindow)
	require.Len(t, live, 1)
	require.Equal(t, "standup", live[0].Identifier)
}

func TestSelectLiveSpaces_MultipleSessionsAreSortedWithinASpace(t *testing.T) {
	events := []*nip01.Event{
		spaceEvent(t, spaceAuthor, "standup", nip53.SpaceStatusOpen, fresh()),
		sessionEvent(t, spaceAuthor, "zulu", nip53.StatusLive, spaceAuthor, "standup", fresh()),
		sessionEvent(t, spaceAuthor, "alpha", nip53.StatusLive, spaceAuthor, "standup", fresh()),
	}

	live := selectLiveSpaces(events, now, nip53.DefaultStaleWindow)
	require.Len(t, live, 1)
	require.Equal(t, []string{"alpha", "zulu"},
		[]string{live[0].Sessions[0].Identifier, live[0].Sessions[1].Identifier})
}
