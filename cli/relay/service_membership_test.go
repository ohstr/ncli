package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip43"
	"github.com/ohstr/nmilat/relay"
	"github.com/stretchr/testify/require"
)

const (
	testPubKey  = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"
	testPrivKey = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	testMember  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// newTestWSHandler builds a real *relay.SessionHandler (and its backing
// *relay.EventStore) against a fresh temp bbolt file -- the membership admin
// handlers under test call real Membership()/store methods, not mocks, so
// a bug in the persisted-record shape or the store<->cache interaction
// would actually be caught here.
func newTestWSHandler(t *testing.T) (*relay.SessionHandler, *relay.EventStore) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := relay.NewEventStore(dbPath, &nip11.Limitation{MaxLimit: 1000})
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	meta := &nip11.Metadata{PubKey: testPubKey, PrivKey: testPrivKey, Self: testPubKey}
	wsHandler := relay.NewSessionHandler(store, meta, nil)
	return wsHandler, store
}

// withTestConfig points the package-level config at a minimal
// membership-enabled RelayConfig for the duration of the calling test,
// restoring the zero value afterward -- every membership admin handler
// reads config.Membership/config.Nip11 directly (mirroring how
// initConfig/RunRelay populate it for real), so tests need the same global
// populated the same way.
func withTestConfig(t *testing.T, publishAddRemove bool) {
	t.Helper()
	prev := config
	config = RelayConfig{
		Nip11:      nip11.Metadata{PubKey: testPubKey, PrivKey: testPrivKey, Self: testPubKey},
		Membership: &MembershipConfig{Enabled: true, PublishAddRemoveEvents: publishAddRemove},
	}
	t.Cleanup(func() { config = prev })
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&v))
	return v
}

/////////////////////////////////////////////////////////////////////
// requireMembership
/////////////////////////////////////////////////////////////////////

func TestRequireMembership(t *testing.T) {
	t.Cleanup(func() { config = RelayConfig{} })
	var called bool
	next := func(w http.ResponseWriter, r *http.Request) { called = true }

	t.Run("nil membership config", func(t *testing.T) {
		called = false
		config = RelayConfig{}
		w := httptest.NewRecorder()
		requireMembership(next)(w, httptest.NewRequest("GET", "/x", nil))
		require.Equal(t, http.StatusNotImplemented, w.Code)
		require.False(t, called)
	})

	t.Run("membership disabled", func(t *testing.T) {
		called = false
		config = RelayConfig{Membership: &MembershipConfig{Enabled: false}}
		w := httptest.NewRecorder()
		requireMembership(next)(w, httptest.NewRequest("GET", "/x", nil))
		require.Equal(t, http.StatusNotImplemented, w.Code)
		require.False(t, called)
	})

	t.Run("membership enabled", func(t *testing.T) {
		called = false
		config = RelayConfig{Membership: &MembershipConfig{Enabled: true}}
		w := httptest.NewRecorder()
		requireMembership(next)(w, httptest.NewRequest("GET", "/x", nil))
		require.True(t, called)
	})
}

/////////////////////////////////////////////////////////////////////
// Members
/////////////////////////////////////////////////////////////////////

func TestHandleMembersList(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, _ := newTestWSHandler(t)

	w := httptest.NewRecorder()
	handleMembersList(wsHandler)(w, httptest.NewRequest("GET", "/admin/membership/members", nil))
	require.Equal(t, http.StatusOK, w.Code)
	got := decodeJSON[struct {
		Members []relay.MemberRecord `json:"members"`
	}](t, w)
	require.Empty(t, got.Members)

	require.NoError(t, wsHandler.Membership().Join(testMember, []string{"r1"}))

	w = httptest.NewRecorder()
	handleMembersList(wsHandler)(w, httptest.NewRequest("GET", "/admin/membership/members", nil))
	require.Equal(t, http.StatusOK, w.Code)
	got = decodeJSON[struct {
		Members []relay.MemberRecord `json:"members"`
	}](t, w)
	require.Len(t, got.Members, 1)
	require.Equal(t, testMember, got.Members[0].Pubkey)
	require.Equal(t, []string{"r1"}, got.Members[0].Roles)
}

func TestHandleMemberShow(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, _ := newTestWSHandler(t)

	t.Run("malformed pubkey", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/admin/membership/members/nope", nil)
		r.SetPathValue("pubkey", "nope")
		w := httptest.NewRecorder()
		handleMemberShow(wsHandler)(w, r)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("not a member", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/admin/membership/members/"+testMember, nil)
		r.SetPathValue("pubkey", testMember)
		w := httptest.NewRecorder()
		handleMemberShow(wsHandler)(w, r)
		require.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("existing member", func(t *testing.T) {
		require.NoError(t, wsHandler.Membership().Join(testMember, []string{"vip"}))
		r := httptest.NewRequest("GET", "/admin/membership/members/"+testMember, nil)
		r.SetPathValue("pubkey", testMember)
		w := httptest.NewRecorder()
		handleMemberShow(wsHandler)(w, r)
		require.Equal(t, http.StatusOK, w.Code)
		got := decodeJSON[relay.MemberRecord](t, w)
		require.Equal(t, testMember, got.Pubkey)
		require.Equal(t, []string{"vip"}, got.Roles)
	})
}

func TestHandleMemberAdd(t *testing.T) {
	t.Run("invalid body", func(t *testing.T) {
		withTestConfig(t, false)
		wsHandler, store := newTestWSHandler(t)
		r := httptest.NewRequest("POST", "/admin/membership/members", strings.NewReader("not-json"))
		w := httptest.NewRecorder()
		handleMemberAdd(wsHandler, store)(w, r)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("invalid pubkey", func(t *testing.T) {
		withTestConfig(t, false)
		wsHandler, store := newTestWSHandler(t)
		r := httptest.NewRequest("POST", "/admin/membership/members", strings.NewReader(`{"pubkey":"short"}`))
		w := httptest.NewRecorder()
		handleMemberAdd(wsHandler, store)(w, r)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("valid, no publish", func(t *testing.T) {
		withTestConfig(t, false)
		wsHandler, store := newTestWSHandler(t)
		r := httptest.NewRequest("POST", "/admin/membership/members", strings.NewReader(`{"pubkey":"`+testMember+`","roles":["admin"]}`))
		w := httptest.NewRecorder()
		handleMemberAdd(wsHandler, store)(w, r)
		require.Equal(t, http.StatusOK, w.Code)

		require.True(t, wsHandler.Membership().IsMember(testMember))

		events, err := store.QueryEvents(t.Context(), &nip01.SubscriptionFilter{Kinds: []int{nip43.KindAddUser}})
		require.NoError(t, err)
		require.Empty(t, events, "PublishAddRemoveEvents is off, no kind:8000 should be published")
	})

	t.Run("valid, publishes add-user event", func(t *testing.T) {
		withTestConfig(t, true)
		wsHandler, store := newTestWSHandler(t)
		r := httptest.NewRequest("POST", "/admin/membership/members", strings.NewReader(`{"pubkey":"`+testMember+`"}`))
		w := httptest.NewRecorder()
		handleMemberAdd(wsHandler, store)(w, r)
		require.Equal(t, http.StatusOK, w.Code)

		events, err := store.QueryEvents(t.Context(), &nip01.SubscriptionFilter{Kinds: []int{nip43.KindAddUser}})
		require.NoError(t, err)
		require.Len(t, events, 1)
		require.NoError(t, events[0].Verify())
		addUser, err := nip43.ParseAddUser(events[0])
		require.NoError(t, err)
		require.Equal(t, testMember, addUser.Pubkey)
	})
}

func TestHandleMemberRemove(t *testing.T) {
	t.Run("idempotent on non-member", func(t *testing.T) {
		withTestConfig(t, false)
		wsHandler, store := newTestWSHandler(t)
		r := httptest.NewRequest("DELETE", "/admin/membership/members/"+testMember, nil)
		r.SetPathValue("pubkey", testMember)
		w := httptest.NewRecorder()
		handleMemberRemove(wsHandler, store)(w, r)
		require.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("removes an existing member and publishes remove-user event", func(t *testing.T) {
		withTestConfig(t, true)
		wsHandler, store := newTestWSHandler(t)
		require.NoError(t, wsHandler.Membership().Join(testMember, nil))

		r := httptest.NewRequest("DELETE", "/admin/membership/members/"+testMember, nil)
		r.SetPathValue("pubkey", testMember)
		w := httptest.NewRecorder()
		handleMemberRemove(wsHandler, store)(w, r)
		require.Equal(t, http.StatusOK, w.Code)
		require.False(t, wsHandler.Membership().IsMember(testMember))

		events, err := store.QueryEvents(t.Context(), &nip01.SubscriptionFilter{Kinds: []int{nip43.KindRemoveUser}})
		require.NoError(t, err)
		require.Len(t, events, 1)
	})
}

/////////////////////////////////////////////////////////////////////
// Invites
/////////////////////////////////////////////////////////////////////

func TestHandleInviteCreate(t *testing.T) {
	withTestConfig(t, false)

	t.Run("invalid ttl", func(t *testing.T) {
		wsHandler, _ := newTestWSHandler(t)
		r := httptest.NewRequest("POST", "/admin/membership/invites", strings.NewReader(`{"ttl":"not-a-duration"}`))
		w := httptest.NewRecorder()
		handleInviteCreate(wsHandler)(w, r)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("negative max_uses", func(t *testing.T) {
		wsHandler, _ := newTestWSHandler(t)
		r := httptest.NewRequest("POST", "/admin/membership/invites", strings.NewReader(`{"max_uses":-1}`))
		w := httptest.NewRecorder()
		handleInviteCreate(wsHandler)(w, r)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("valid", func(t *testing.T) {
		wsHandler, _ := newTestWSHandler(t)
		r := httptest.NewRequest("POST", "/admin/membership/invites", strings.NewReader(`{"ttl":"1h","max_uses":2,"roles":["r1"]}`))
		w := httptest.NewRecorder()
		handleInviteCreate(wsHandler)(w, r)
		require.Equal(t, http.StatusOK, w.Code)
		got := decodeJSON[relay.InviteClaim](t, w)
		require.NotEmpty(t, got.Code)
		require.Equal(t, 2, got.MaxUses)
		require.Equal(t, []string{"r1"}, got.Roles)
	})
}

func TestHandleInviteListAndRevoke(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)

	w := httptest.NewRecorder()
	handleInviteList(store)(w, httptest.NewRequest("GET", "/admin/membership/invites", nil))
	require.Equal(t, http.StatusOK, w.Code)
	got := decodeJSON[struct {
		Invites []relay.InviteClaim `json:"invites"`
	}](t, w)
	require.Empty(t, got.Invites)

	claim, err := wsHandler.Membership().IssueInvite(0, 0, nil)
	require.NoError(t, err)

	w = httptest.NewRecorder()
	handleInviteList(store)(w, httptest.NewRequest("GET", "/admin/membership/invites", nil))
	got = decodeJSON[struct {
		Invites []relay.InviteClaim `json:"invites"`
	}](t, w)
	require.Len(t, got.Invites, 1)
	require.Equal(t, claim.Code, got.Invites[0].Code)

	r := httptest.NewRequest("DELETE", "/admin/membership/invites/"+claim.Code, nil)
	r.SetPathValue("code", claim.Code)
	w = httptest.NewRecorder()
	handleInviteRevoke(store)(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	stored, err := store.GetInviteClaim(claim.Code)
	require.NoError(t, err)
	require.Nil(t, stored)
}

/////////////////////////////////////////////////////////////////////
// Roles
/////////////////////////////////////////////////////////////////////

func TestHandleRoleCreate(t *testing.T) {
	withTestConfig(t, false)

	t.Run("missing id", func(t *testing.T) {
		_, store := newTestWSHandler(t)
		r := httptest.NewRequest("POST", "/admin/membership/roles", strings.NewReader(`{"label":"x"}`))
		w := httptest.NewRecorder()
		handleRoleCreate(store)(w, r)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("color out of range", func(t *testing.T) {
		_, store := newTestWSHandler(t)
		r := httptest.NewRequest("POST", "/admin/membership/roles", strings.NewReader(`{"id":"vip","color":361}`))
		w := httptest.NewRecorder()
		handleRoleCreate(store)(w, r)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("valid, round-trips through the store", func(t *testing.T) {
		_, store := newTestWSHandler(t)
		r := httptest.NewRequest("POST", "/admin/membership/roles", strings.NewReader(`{"id":"vip","label":"VIP","description":"desc","color":180,"order":2}`))
		w := httptest.NewRecorder()
		handleRoleCreate(store)(w, r)
		require.Equal(t, http.StatusOK, w.Code)

		events, err := store.QueryEvents(t.Context(), &nip01.SubscriptionFilter{Kinds: []int{nip43.KindRoleDefinition}, Authors: []string{testPubKey}})
		require.NoError(t, err)
		require.Len(t, events, 1)
		require.NoError(t, events[0].Verify())

		role, err := nip43.ParseRole(events[0])
		require.NoError(t, err)
		require.Equal(t, "vip", role.ID)
		require.Equal(t, "VIP", role.Label)
		require.NotNil(t, role.Color)
		require.Equal(t, 180, *role.Color)
		require.NotNil(t, role.Order)
		require.Equal(t, 2, *role.Order)
	})
}

func TestHandleRolesList(t *testing.T) {
	withTestConfig(t, false)
	_, store := newTestWSHandler(t)

	w := httptest.NewRecorder()
	handleRolesList(store)(w, httptest.NewRequest("GET", "/admin/membership/roles", nil))
	require.Equal(t, http.StatusOK, w.Code)
	got := decodeJSON[struct {
		Roles []roleJSON `json:"roles"`
	}](t, w)
	require.Empty(t, got.Roles)

	createReq := httptest.NewRequest("POST", "/admin/membership/roles", strings.NewReader(`{"id":"admin","label":"Admin"}`))
	handleRoleCreate(store)(httptest.NewRecorder(), createReq)

	w = httptest.NewRecorder()
	handleRolesList(store)(w, httptest.NewRequest("GET", "/admin/membership/roles", nil))
	got = decodeJSON[struct {
		Roles []roleJSON `json:"roles"`
	}](t, w)
	require.Len(t, got.Roles, 1)
	require.Equal(t, "admin", got.Roles[0].ID)
	require.Equal(t, "Admin", got.Roles[0].Label)
}
