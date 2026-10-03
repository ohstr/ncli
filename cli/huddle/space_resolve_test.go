package huddle

import (
	"testing"

	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip53"
	"github.com/stretchr/testify/require"
)

const refPubkey = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"

func TestParseActivityRef_BareCoordinate(t *testing.T) {
	ref, err := parseActivityRef("30312:" + refPubkey + ":standup")
	require.NoError(t, err)
	require.Equal(t, nip53.KindMeetingSpace, ref.Kind)
	require.Equal(t, refPubkey, ref.Pubkey)
	require.Equal(t, "standup", ref.Identifier)
	require.Equal(t, "30312:"+refPubkey+":standup", ref.Address())
}

func TestParseActivityRef_SessionCoordinate(t *testing.T) {
	ref, err := parseActivityRef("30313:" + refPubkey + ":today")
	require.NoError(t, err)
	require.Equal(t, nip53.KindMeetingRoomEvent, ref.Kind)
	require.Equal(t, "today", ref.Identifier)
}

func TestParseActivityRef_Naddr(t *testing.T) {
	naddr, err := nip19.EncodeAddr(nip19.EntityPointer{
		Identifier: "standup",
		PublicKey:  refPubkey,
		Kind:       nip53.KindMeetingSpace,
		Relays:     []string{"wss://relay.example"},
	})
	require.NoError(t, err)

	ref, err := parseActivityRef(naddr)
	require.NoError(t, err)
	require.Equal(t, nip53.KindMeetingSpace, ref.Kind)
	require.Equal(t, refPubkey, ref.Pubkey)
	require.Equal(t, "standup", ref.Identifier)
	require.Equal(t, []string{"wss://relay.example"}, ref.Relays)
}

// A plain room id must stay a plain room id: joining by room id is the
// original behavior and must not be swallowed by the space parser.
func TestParseActivityRef_PlainRoomIdIsNotAReference(t *testing.T) {
	for _, arg := range []string{"standup", "my-room", "a:b", "550e8400-e29b-41d4-a716-446655440000", ""} {
		_, err := parseActivityRef(arg)
		require.ErrorIs(t, err, errNotActivityRef, "%q must be treated as a room id", arg)
	}
}

// A coordinate whose kind is neither 30312 nor 30313 is a mistake worth
// reporting, not something to silently dial as a room named "30023:...".
func TestParseActivityRef_RejectsAnUnrelatedKind(t *testing.T) {
	_, err := parseActivityRef("30023:" + refPubkey + ":an-article")
	require.Error(t, err)
	require.NotErrorIs(t, err, errNotActivityRef)
}

func TestParseActivityRef_RejectsAMalformedNaddr(t *testing.T) {
	_, err := parseActivityRef("naddr1notrealbech32")
	require.Error(t, err)
	require.NotErrorIs(t, err, errNotActivityRef)
}

// A published endpoint names both the relay and the room outright, so it is
// taken at its word rather than guessed from the d tag.
func TestTransportFromSpace_PrefersAPublishedEndpoint(t *testing.T) {
	base, room, err := transportFromSpace(&nip53.MeetingSpace{
		Identifier: "ignored-when-endpoint-is-explicit",
		Service:    "wss://relay.example",
		Endpoint:   "wss://audio.example/huddle/room-42/audio",
	})
	require.NoError(t, err)
	require.Equal(t, "wss://audio.example", base.String())
	require.Equal(t, "room-42", room)
}

// Without a usable endpoint, the service is the relay and the space's own d
// tag is the room id.
func TestTransportFromSpace_FallsBackToServiceAndIdentifier(t *testing.T) {
	base, room, err := transportFromSpace(&nip53.MeetingSpace{
		Identifier: "standup",
		Service:    "wss://relay.example",
	})
	require.NoError(t, err)
	require.Equal(t, "wss://relay.example", base.String())
	require.Equal(t, "standup", room)
}

// An endpoint that is not a huddle audio URL is not a room id in disguise, so
// the service path is used instead of dialing something arbitrary.
func TestTransportFromSpace_IgnoresAnUnrecognizedEndpoint(t *testing.T) {
	base, room, err := transportFromSpace(&nip53.MeetingSpace{
		Identifier: "standup",
		Service:    "wss://relay.example",
		Endpoint:   "https://meet.example/some/other/thing",
	})
	require.NoError(t, err)
	require.Equal(t, "wss://relay.example", base.String())
	require.Equal(t, "standup", room)
}

// A space may publish http(s) for the same host; the audio socket is still ws.
func TestTransportFromSpace_MapsHTTPSchemesToWebSocket(t *testing.T) {
	for raw, want := range map[string]string{
		"https://relay.example": "wss://relay.example",
		"http://localhost:7777": "ws://localhost:7777",
		"wss://relay.example/":  "wss://relay.example",
	} {
		t.Run(raw, func(t *testing.T) {
			base, _, err := transportFromSpace(&nip53.MeetingSpace{Identifier: "standup", Service: raw})
			require.NoError(t, err)
			require.Equal(t, want, base.String())
		})
	}
}

func TestTransportFromSpace_RejectsAnUndialableSpace(t *testing.T) {
	_, _, err := transportFromSpace(&nip53.MeetingSpace{Identifier: "standup"})
	require.Error(t, err, "a space with no service URL cannot be dialed")

	_, _, err = transportFromSpace(&nip53.MeetingSpace{Identifier: "standup", Service: "mailto:someone@example"})
	require.Error(t, err, "a non-websocket scheme is not a relay")

	_, _, err = transportFromSpace(&nip53.MeetingSpace{Service: "wss://relay.example"})
	require.Error(t, err, "no identifier and no endpoint leaves no room id")
}

// A path prefix on the relay has to survive, or a relay served under a
// subpath is dialed at the wrong URL.
func TestTransportFromSpace_KeepsAPathPrefix(t *testing.T) {
	base, room, err := transportFromSpace(&nip53.MeetingSpace{
		Identifier: "standup",
		Service:    "wss://relay.example/nostr",
	})
	require.NoError(t, err)
	require.Equal(t, "wss://relay.example/nostr", base.String())

	endpoint, err := Endpoint(base, room)
	require.NoError(t, err)
	require.Equal(t, "wss://relay.example/nostr/huddle/standup/audio", endpoint)
}
