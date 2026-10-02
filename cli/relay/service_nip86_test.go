package relay

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip86"
	"github.com/stretchr/testify/require"
)

func nip86Params(t *testing.T, raw ...string) []json.RawMessage {
	t.Helper()
	params := make([]json.RawMessage, 0, len(raw))
	for _, r := range raw {
		params = append(params, json.RawMessage(r))
	}
	return params
}

func call(t *testing.T, router *nip86.Router, method string, params ...string) nip86.Response {
	t.Helper()
	return router.Dispatch(context.Background(), testPubKey, nip86.Request{
		Method: method,
		Params: nip86Params(t, params...),
	})
}

func TestNip86Admins_IncludesRelayKeyAndConfiguredAdmins(t *testing.T) {
	prev := config
	t.Cleanup(func() { config = prev })

	config = RelayConfig{
		Nip11: nip11.Metadata{PubKey: testPubKey},
		Nip86: &Nip86Config{Enabled: true, Admins: []string{testMember}},
	}

	admins := nip86Admins()
	require.Contains(t, admins, testPubKey, "the relay's own key must keep working")
	require.Contains(t, admins, testMember, "a configured admin must be allowed")
}

// Enabling the API without membership would expose methods with nothing to
// administer, so both have to be on.
func TestNip86Enabled_RequiresMembership(t *testing.T) {
	prev := config
	t.Cleanup(func() { config = prev })

	config = RelayConfig{Nip86: &Nip86Config{Enabled: true}}
	require.False(t, nip86Enabled(), "nip86 alone must not enable the API")

	config.Membership = &MembershipConfig{Enabled: true}
	require.True(t, nip86Enabled())
}

func TestNip86_AllowAndListPubkeys(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	resp := call(t, router, nip86.MethodAllowPubkey, `"`+testMember+`"`)
	require.Empty(t, resp.Error)
	require.True(t, wsHandler.Membership().IsMember(testMember))

	listed := call(t, router, nip86.MethodListAllowedPubkeys)
	require.Empty(t, listed.Error)
	require.Equal(t, []string{testMember}, listed.Result)
}

func TestNip86_AllowRejectsAMalformedPubkey(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	resp := call(t, router, nip86.MethodAllowPubkey, `"not-a-pubkey"`)
	require.NotEmpty(t, resp.Error, "a typo must not become a member that can never authenticate")
}

func TestNip86_CreateClaimWithAChosenCode(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	resp := call(t, router, nip86.MethodCreateClaim, `"party-2026"`)
	require.Empty(t, resp.Error)

	stored, err := store.GetInviteClaim("party-2026")
	require.NoError(t, err)
	require.NotNil(t, stored)

	// Reusing a live code would reset the uses and expiry of an invite already
	// handed out.
	again := call(t, router, nip86.MethodCreateClaim, `"party-2026"`)
	require.NotEmpty(t, again.Error)
}

func TestNip86_CreateClaimGeneratesACodeWhenNoneGiven(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	resp := call(t, router, nip86.MethodCreateClaim)
	require.Empty(t, resp.Error)

	claims, err := store.ListInviteClaims()
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NotEmpty(t, claims[0].Code)
}

func TestNip86_DeleteClaim(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	require.Empty(t, call(t, router, nip86.MethodCreateClaim, `"revoke-me"`).Error)
	require.Empty(t, call(t, router, nip86.MethodDeleteClaim, `"revoke-me"`).Error)

	stored, err := store.GetInviteClaim("revoke-me")
	require.NoError(t, err)
	require.Nil(t, stored)
}

// The decision recorded for private Spaces: removal ends a live call rather
// than only blocking the next join.
func TestNip86_UnallowPubkeyEvictsFromALiveHuddle(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)

	rooms := room.NewManager(0)
	_, _, _, err := rooms.Join("space-1", testMember, 3, room.NewChannelSink())
	require.NoError(t, err)

	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store, rooms: rooms})
	require.Empty(t, call(t, router, nip86.MethodAllowPubkey, `"`+testMember+`"`).Error)
	require.Empty(t, call(t, router, nip86.MethodUnallowPubkey, `"`+testMember+`"`).Error)

	require.False(t, wsHandler.Membership().IsMember(testMember))
	_, stillThere := rooms.Get("space-1")
	require.False(t, stillThere, "the removed member's room held only them, so it should be gone")
}

func TestNip86_UnallowPubkeyLeavesOtherPeersInTheCall(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)

	const otherMember = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	rooms := room.NewManager(0)
	_, _, _, err := rooms.Join("space-1", testMember, 3, room.NewChannelSink())
	require.NoError(t, err)
	_, _, _, err = rooms.Join("space-1", otherMember, 3, room.NewChannelSink())
	require.NoError(t, err)

	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store, rooms: rooms})
	require.Empty(t, call(t, router, nip86.MethodUnallowPubkey, `"`+testMember+`"`).Error)

	live, ok := rooms.Get("space-1")
	require.True(t, ok, "the call must survive one member being removed")
	require.Equal(t, 1, live.Len())
}

// Removal must not require huddle audio to be configured at all.
func TestNip86_UnallowPubkeyWithoutHuddleRooms(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	require.Empty(t, call(t, router, nip86.MethodAllowPubkey, `"`+testMember+`"`).Error)
	require.Empty(t, call(t, router, nip86.MethodUnallowPubkey, `"`+testMember+`"`).Error)
	require.False(t, wsHandler.Membership().IsMember(testMember))
}

func TestNip86_RolesCreateListAndAssign(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	require.Empty(t, call(t, router, nip86.MethodCreateRole, `{"id":"vip","label":"VIP"}`).Error)

	require.Empty(t, call(t, router, nip86.MethodAllowPubkey, `"`+testMember+`"`).Error)
	require.Empty(t, call(t, router, nip86.MethodAssignRole, `"`+testMember+`"`, `"vip"`).Error)

	rec, err := wsHandler.Membership().Get(testMember)
	require.NoError(t, err)
	require.Equal(t, []string{"vip"}, rec.Roles)

	require.Empty(t, call(t, router, nip86.MethodUnassignRole, `"`+testMember+`"`, `"vip"`).Error)
	rec, err = wsHandler.Membership().Get(testMember)
	require.NoError(t, err)
	require.Empty(t, rec.Roles)
}

func TestNip86_SupportedMethodsNamesTheRoleMethods(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	resp := call(t, router, nip86.MethodSupportedMethods)
	methods, ok := resp.Result.([]string)
	require.True(t, ok)
	for _, want := range []string{
		nip86.MethodAllowPubkey,
		nip86.MethodUnallowPubkey,
		nip86.MethodListAllowedPubkeys,
		nip86.MethodCreateClaim,
		nip86.MethodListClaims,
		nip86.MethodDeleteClaim,
		nip86.MethodCreateRole,
		nip86.MethodEditRole,
		nip86.MethodDeleteRole,
		nip86.MethodAssignRole,
		nip86.MethodUnassignRole,
	} {
		require.Contains(t, methods, want)
	}
}
