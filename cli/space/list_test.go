package space

import (
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip53"
)

const testPubkey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newSpaceEvent(t *testing.T, pubkey, identifier, status, service string, createdAt uint64) *nip01.Event {
	t.Helper()
	ev := nip53.NewMeetingSpace(nip53.MeetingSpaceParams{
		Pubkey:     pubkey,
		Identifier: identifier,
		Room:       identifier,
		Status:     status,
		Service:    service,
		Providers:  []nip53.Participant{{Pubkey: pubkey, Role: nip53.RoleHost}},
	})
	ev.PubKey = pubkey
	ev.CreatedAt = createdAt
	return ev
}

func newSessionEvent(t *testing.T, spacePubkey, spaceIdentifier, sessionIdentifier, status string, createdAt uint64) *nip01.Event {
	t.Helper()
	ev := nip53.NewMeetingRoomEvent(nip53.MeetingRoomEventParams{
		Pubkey:     spacePubkey,
		Identifier: sessionIdentifier,
		Space:      mustSpaceATag(t, spacePubkey, spaceIdentifier),
		Title:      "Test session",
		Starts:     createdAt,
		Status:     status,
	})
	ev.PubKey = spacePubkey
	ev.CreatedAt = createdAt
	return ev
}

func mustSpaceATag(t *testing.T, pubkey, identifier string) string {
	t.Helper()
	tag, err := nip53.SpaceATag(pubkey, identifier)
	if err != nil {
		t.Fatalf("SpaceATag() error = %v", err)
	}
	return tag
}

func TestSelectSpaces_OpenSpaceWithNoSessionIsKept(t *testing.T) {
	events := []*nip01.Event{
		newSpaceEvent(t, testPubkey, "standup", nip53.SpaceStatusOpen, "wss://relay.example", 100),
	}

	got := selectSpaces(events, time.Now(), time.Hour, false)
	if len(got) != 1 {
		t.Fatalf("selectSpaces() = %d spaces, want 1", len(got))
	}
	if got[0].Live {
		t.Fatal("got[0].Live = true, want false (no session published)")
	}
}

func TestSelectSpaces_ClosedSpaceExcludedUnlessAll(t *testing.T) {
	events := []*nip01.Event{
		newSpaceEvent(t, testPubkey, "standup", nip53.SpaceStatusClosed, "wss://relay.example", 100),
	}

	if got := selectSpaces(events, time.Now(), time.Hour, false); len(got) != 0 {
		t.Fatalf("selectSpaces(includeAll=false) = %d spaces, want 0 (closed)", len(got))
	}
	if got := selectSpaces(events, time.Now(), time.Hour, true); len(got) != 1 {
		t.Fatalf("selectSpaces(includeAll=true) = %d spaces, want 1", len(got))
	}
}

func TestSelectSpaces_LiveSessionAttachesAndMarksLive(t *testing.T) {
	now := time.Now()
	events := []*nip01.Event{
		newSpaceEvent(t, testPubkey, "standup", nip53.SpaceStatusOpen, "wss://relay.example", 100),
		newSessionEvent(t, testPubkey, "standup", "today", nip53.StatusLive, uint64(now.Unix())),
	}

	got := selectSpaces(events, now, time.Hour, false)
	if len(got) != 1 {
		t.Fatalf("selectSpaces() = %d spaces, want 1", len(got))
	}
	if !got[0].Live {
		t.Fatal("got[0].Live = false, want true")
	}
	if len(got[0].Sessions) != 1 || got[0].Sessions[0].Identifier != "today" {
		t.Fatalf("got[0].Sessions = %+v, want one session \"today\"", got[0].Sessions)
	}
}

func TestSelectSpaces_StaleLiveSessionNotCountedLive(t *testing.T) {
	now := time.Now()
	staleCreatedAt := uint64(now.Add(-2 * time.Hour).Unix())
	events := []*nip01.Event{
		newSpaceEvent(t, testPubkey, "standup", nip53.SpaceStatusOpen, "wss://relay.example", 100),
		newSessionEvent(t, testPubkey, "standup", "today", nip53.StatusLive, staleCreatedAt),
	}

	got := selectSpaces(events, now, time.Hour, false)
	if len(got) != 1 {
		t.Fatalf("selectSpaces() = %d spaces, want 1", len(got))
	}
	if got[0].Live {
		t.Fatal("got[0].Live = true, want false (session is stale)")
	}
	if len(got[0].Sessions) != 0 {
		t.Fatalf("got[0].Sessions = %+v, want none (stale session excluded)", got[0].Sessions)
	}
}

func TestSelectSpaces_NewestCopyWins(t *testing.T) {
	events := []*nip01.Event{
		newSpaceEvent(t, testPubkey, "standup", nip53.SpaceStatusOpen, "wss://old.example", 100),
		newSpaceEvent(t, testPubkey, "standup", nip53.SpaceStatusOpen, "wss://new.example", 200),
	}

	got := selectSpaces(events, time.Now(), time.Hour, false)
	if len(got) != 1 {
		t.Fatalf("selectSpaces() = %d spaces, want 1", len(got))
	}
	if got[0].Service != "wss://new.example" {
		t.Fatalf("got[0].Service = %q, want the newer copy's %q", got[0].Service, "wss://new.example")
	}
}

func TestLiveLabel(t *testing.T) {
	if got := liveLabel(true); got != "live" {
		t.Fatalf("liveLabel(true) = %q, want %q", got, "live")
	}
	if got := liveLabel(false); got != "-" {
		t.Fatalf("liveLabel(false) = %q, want %q", got, "-")
	}
}
