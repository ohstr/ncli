package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/huddle/room"
	"github.com/stretchr/testify/require"
)

// listURL is signed by the tests below and reconstructed by nip98.Verify from
// the request's Host and path, so both sides have to agree on it exactly.
const listURL = "http://example.com/huddle/rooms"

// secondMember is a second occupant, for asserting a peer count above one.
const secondMember = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func base64Of(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func roomsRequest(t *testing.T, privKey string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, listURL, nil)
	if privKey != "" {
		header, err := common.GenerateNIP98Header(privKey, listURL, http.MethodGet, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", header)
	}
	return req
}

func listRooms(t *testing.T, rooms *room.Manager, authorize func(context.Context, string, string) error, privKey string) (int, []liveRoom) {
	t.Helper()
	rec := httptest.NewRecorder()
	huddleRoomsHandler(rooms, authorize).ServeHTTP(rec, roomsRequest(t, privKey))

	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var payload struct {
		Rooms []liveRoom `json:"rooms"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return rec.Code, payload.Rooms
}

func TestHuddleRooms_ListsOccupiedRoomsSortedWithPinnedVersion(t *testing.T) {
	rooms := room.NewManager(0)
	_, _, _, err := rooms.Join("standup", testMember, 3, room.NewChannelSink())
	require.NoError(t, err)
	_, _, _, err = rooms.Join("standup", secondMember, 3, room.NewChannelSink())
	require.NoError(t, err)
	// Joined at version 2, so the room pins 2 and this row must say so -- a v3
	// client reading the list is being told why its join would be refused.
	_, _, _, err = rooms.Join("alpha", testMember, 2, room.NewChannelSink())
	require.NoError(t, err)

	code, live := listRooms(t, rooms, nil, "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []liveRoom{
		{ID: "alpha", Peers: 1, ProtocolVersion: 2},
		{ID: "standup", Peers: 2, ProtocolVersion: 3},
	}, live, "rooms must be sorted by id so a scripted diff is stable")
}

// The contract is an array either way: a caller doing `.rooms | length` must
// not have to special-case a null.
func TestHuddleRooms_NoLiveRoomsIsAnEmptyArray(t *testing.T) {
	rec := httptest.NewRecorder()
	huddleRoomsHandler(room.NewManager(0), nil).ServeHTTP(rec, roomsRequest(t, ""))

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"rooms":[]}`, rec.Body.String())
}

// What this endpoint promises is "live", so a call that has ended is gone from
// the list -- not reported as something a caller could still join.
func TestHuddleRooms_EndedRoomIsNotListed(t *testing.T) {
	rooms := room.NewManager(0)
	_, _, _, err := rooms.Join("standup", testMember, 3, room.NewChannelSink())
	require.NoError(t, err)

	require.True(t, rooms.End("standup"))

	code, live := listRooms(t, rooms, nil, "")
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, live)
}

// A room emptied by its last peer leaving is the same story as an ended one.
func TestHuddleRooms_EmptiedRoomIsNotListed(t *testing.T) {
	rooms := room.NewManager(0)
	_, peer, _, err := rooms.Join("standup", testMember, 3, room.NewChannelSink())
	require.NoError(t, err)

	require.True(t, rooms.Leave("standup", peer.ID))

	code, live := listRooms(t, rooms, nil, "")
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, live)
}

// With no membership requirement there is nothing to authorize: anyone who can
// reach the endpoint could join any room anyway, so the list needs no key.
func TestHuddleRooms_OpenRelayNeedsNoCredentials(t *testing.T) {
	rooms := room.NewManager(0)
	_, _, _, err := rooms.Join("standup", testMember, 3, room.NewChannelSink())
	require.NoError(t, err)

	code, live := listRooms(t, rooms, nil, "")
	require.Equal(t, http.StatusOK, code)
	require.Len(t, live, 1)
}

func TestHuddleRooms_MembersOnlyRejectsUnsignedRequest(t *testing.T) {
	authorize := func(context.Context, string, string) error { return nil }

	code, _ := listRooms(t, room.NewManager(0), authorize, "")
	require.Equal(t, http.StatusUnauthorized, code, "a members-only relay must not answer an anonymous caller")
}

func TestHuddleRooms_MembersOnlyAnswersASignedMember(t *testing.T) {
	rooms := room.NewManager(0)
	_, _, _, err := rooms.Join("standup", testMember, 3, room.NewChannelSink())
	require.NoError(t, err)

	var sawPubkey string
	authorize := func(_ context.Context, _, pubkey string) error {
		sawPubkey = pubkey
		return nil
	}

	code, live := listRooms(t, rooms, authorize, testPrivKey)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, live, 1)
	require.Equal(t, testPubKey, sawPubkey, "the verified signer is what the hook must be asked about")
}

// 403, not 401: the signature was good, the identity just is not a member.
func TestHuddleRooms_MembersOnlyRefusesANonMember(t *testing.T) {
	authorize := func(context.Context, string, string) error { return errors.New("not a relay member") }

	code, _ := listRooms(t, room.NewManager(0), authorize, testPrivKey)
	require.Equal(t, http.StatusForbidden, code)
}

// A header signing some other URL must not pass: without this check a
// NIP-98 header captured from any other endpoint would work here.
func TestHuddleRooms_RejectsHeaderSignedForAnotherURL(t *testing.T) {
	authorize := func(context.Context, string, string) error { return nil }

	header, err := common.GenerateNIP98Header(testPrivKey, "http://example.com/admin/membership/members", http.MethodGet, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, listURL, nil)
	req.Header.Set("Authorization", header)

	rec := httptest.NewRecorder()
	huddleRoomsHandler(room.NewManager(0), authorize).ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestNip98Pubkey_RejectsMalformedHeaders(t *testing.T) {
	for name, header := range map[string]string{
		"missing":      "",
		"wrong scheme": "Bearer token",
		"not base64":   "Nostr !!!!",
		"not an event": "Nostr " + base64Of(`{"foo":"bar"}`),
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, listURL, nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			_, err := nip98Pubkey(req)
			require.Error(t, err)
		})
	}
}
