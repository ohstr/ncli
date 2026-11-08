package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip86"
	"github.com/ohstr/nmilat/nip98"
	"github.com/ohstr/nmilat/relay"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// strongCode is a client-chosen invite code that clears the min-strength bar
// (>=16 chars, [A-Za-z0-9_-]). Reused across the method tests below.
const strongCode = "guests-2026-abcdef01"

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

// nip86 alone enables the API: its moderation methods work on any relay.
// The membership methods additionally need membership.
func TestNip86Enabled_WithoutMembership(t *testing.T) {
	prev := config
	t.Cleanup(func() { config = prev })

	config = RelayConfig{Nip86: &Nip86Config{Enabled: true}}
	require.True(t, nip86Enabled())
	require.False(t, membershipOn())

	config.Membership = &MembershipConfig{Enabled: true}
	require.True(t, membershipOn())
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

	resp := call(t, router, nip86.MethodCreateClaim, `"party-2026-guests1"`)
	require.Empty(t, resp.Error)
	require.Equal(t, true, resp.Result) // spec form MUST answer boolean true, not the record

	stored, err := store.GetInviteClaim("party-2026-guests1")
	require.NoError(t, err)
	require.NotNil(t, stored)

	// Reusing a live code would reset the uses and expiry of an invite already
	// handed out.
	again := call(t, router, nip86.MethodCreateClaim, `"party-2026-guests1"`)
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

	require.Empty(t, call(t, router, nip86.MethodCreateClaim, `"revoke-me-abcdef01"`).Error)
	require.Empty(t, call(t, router, nip86.MethodDeleteClaim, `"revoke-me-abcdef01"`).Error)

	stored, err := store.GetInviteClaim("revoke-me-abcdef01")
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
		// The two PROPOSED NIP-86 additions must be detectable here, since a
		// client decides whether it may set per-invite limits from this list.
		nip86MethodCreateClaimWithLimits,
		nip86MethodGetClaim,
	} {
		require.Contains(t, methods, want)
	}
}

/////////////////////////////////////////////////////////////////////
// A: createclaim min-strength + legacy generate
/////////////////////////////////////////////////////////////////////

func TestNip86_CreateClaim_RejectsWeakClientCode(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	// Too short (< 16 chars).
	short := call(t, router, nip86.MethodCreateClaim, `"short-code"`)
	require.NotEmpty(t, short.Error)
	require.Contains(t, short.Error, "at least 16")

	// Illegal character (space) even though length is fine.
	bad := call(t, router, nip86.MethodCreateClaim, `"has a space in it here"`)
	require.NotEmpty(t, bad.Error)

	// Nothing got stored for either rejected code.
	claims, err := store.ListInviteClaims()
	require.NoError(t, err)
	require.Empty(t, claims)
}

// The legacy forms [] and [""] still make the relay generate a code and return
// the full record -- released bzzapp depends on this.
func TestNip86_CreateClaim_LegacyEmptyStringGenerates(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	resp := call(t, router, nip86.MethodCreateClaim, `""`)
	require.Empty(t, resp.Error)
	// The result is the full InviteClaim record, not a bare `true`.
	require.NotEqual(t, true, resp.Result)

	claims, err := store.ListInviteClaims()
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NotEmpty(t, claims[0].Code)
}

/////////////////////////////////////////////////////////////////////
// A: listclaims -> live code strings only
/////////////////////////////////////////////////////////////////////

func TestNip86_ListClaims_LiveCodeStringsOnly(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	now := time.Now().Unix()
	live := &relay.InviteClaim{Code: "live-code-0000001", CreatedAt: now, ExpiresAt: now + 3600, MaxUses: 0}
	expired := &relay.InviteClaim{Code: "expired-code-00001", CreatedAt: now - 7200, ExpiresAt: now - 3600}
	usedUp := &relay.InviteClaim{Code: "usedup-code-000001", CreatedAt: now, ExpiresAt: now + 3600, MaxUses: 1, Uses: 1}
	for _, c := range []*relay.InviteClaim{live, expired, usedUp} {
		require.NoError(t, store.PutInviteClaim(c))
	}

	resp := call(t, router, nip86.MethodListClaims)
	require.Empty(t, resp.Error)
	codes, ok := resp.Result.([]string)
	require.True(t, ok, "listclaims must return a plain []string, got %T", resp.Result)
	require.Equal(t, []string{"live-code-0000001"}, codes)
}

/////////////////////////////////////////////////////////////////////
// C: createclaimwithlimits
/////////////////////////////////////////////////////////////////////

func TestNip86_CreateClaimWithLimits_DefaultsWhenFieldsAbsent(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	config.Membership.InviteMaxUses = 5
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	// No limits object at all -> relay defaults; result is bare true.
	resp := call(t, router, nip86MethodCreateClaimWithLimits, `"`+strongCode+`"`)
	require.Empty(t, resp.Error)
	require.Equal(t, true, resp.Result)

	stored, err := store.GetInviteClaim(strongCode)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, 5, stored.MaxUses, "absent max_uses -> config default")
	require.Greater(t, stored.ExpiresAt, time.Now().Unix(), "absent expires_at -> now + default TTL")
}

func TestNip86_CreateClaimWithLimits_HonorsExplicitFields(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	exp := time.Now().Add(time.Hour).Unix()
	resp := call(t, router, nip86MethodCreateClaimWithLimits, `"`+strongCode+`"`, `{"expires_at":`+strconv.FormatInt(exp, 10)+`,"max_uses":3}`)
	require.Empty(t, resp.Error)

	stored, err := store.GetInviteClaim(strongCode)
	require.NoError(t, err)
	require.Equal(t, exp, stored.ExpiresAt, "explicit expires_at must be stored verbatim, never clamped")
	require.Equal(t, 3, stored.MaxUses)
}

func TestNip86_CreateClaimWithLimits_NoExpiryGatedByConfig(t *testing.T) {
	t.Run("rejected when not allowed", func(t *testing.T) {
		withTestConfig(t, false)
		wsHandler, store := newTestWSHandler(t)
		router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

		resp := call(t, router, nip86MethodCreateClaimWithLimits, `"`+strongCode+`"`, `{"expires_at":0}`)
		require.NotEmpty(t, resp.Error)
		stored, err := store.GetInviteClaim(strongCode)
		require.NoError(t, err)
		require.Nil(t, stored, "a rejected request must not create the code")
	})

	t.Run("allowed when inviteAllowNoExpiry is set", func(t *testing.T) {
		withTestConfig(t, false)
		wsHandler, store := newTestWSHandler(t)
		config.Membership.InviteAllowNoExpiry = true
		router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

		resp := call(t, router, nip86MethodCreateClaimWithLimits, `"`+strongCode+`"`, `{"expires_at":0}`)
		require.Empty(t, resp.Error)
		stored, err := store.GetInviteClaim(strongCode)
		require.NoError(t, err)
		require.NotNil(t, stored)
		require.EqualValues(t, 0, stored.ExpiresAt, "expires_at:0 -> never expires")
	})
}

func TestNip86_CreateClaimWithLimits_RejectsBadExpiry(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	config.Membership.InviteMaxTTL = "1h"
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	t.Run("past expires_at", func(t *testing.T) {
		past := time.Now().Add(-time.Hour).Unix()
		resp := call(t, router, nip86MethodCreateClaimWithLimits, `"`+strongCode+`"`, `{"expires_at":`+strconv.FormatInt(past, 10)+`}`)
		require.NotEmpty(t, resp.Error)
		require.Contains(t, resp.Error, "future")
	})

	t.Run("expires_at over inviteMaxTTL", func(t *testing.T) {
		over := time.Now().Add(2 * time.Hour).Unix() // maxTTL is 1h
		resp := call(t, router, nip86MethodCreateClaimWithLimits, `"`+strongCode+`"`, `{"expires_at":`+strconv.FormatInt(over, 10)+`}`)
		require.NotEmpty(t, resp.Error)
	})

	// Neither rejected request created a code.
	stored, err := store.GetInviteClaim(strongCode)
	require.NoError(t, err)
	require.Nil(t, stored)
}

func TestNip86_CreateClaimWithLimits_RejectsBadMaxUses(t *testing.T) {
	cases := map[string]string{
		"zero":        `{"max_uses":0}`,
		"negative":    `{"max_uses":-3}`,
		"float":       `{"max_uses":1.5}`,
		"non-integer": `{"max_uses":"3"}`,
	}
	for name, limits := range cases {
		t.Run(name, func(t *testing.T) {
			withTestConfig(t, false)
			wsHandler, store := newTestWSHandler(t)
			router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

			resp := call(t, router, nip86MethodCreateClaimWithLimits, `"`+strongCode+`"`, limits)
			require.NotEmpty(t, resp.Error)
			stored, err := store.GetInviteClaim(strongCode)
			require.NoError(t, err)
			require.Nil(t, stored)
		})
	}
}

func TestNip86_CreateClaimWithLimits_RejectsUnknownKey(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	resp := call(t, router, nip86MethodCreateClaimWithLimits, `"`+strongCode+`"`, `{"expires_at":0,"surprise":true}`)
	require.NotEmpty(t, resp.Error, "an unknown field must be an error, never silently ignored")
	stored, err := store.GetInviteClaim(strongCode)
	require.NoError(t, err)
	require.Nil(t, stored)
}

func TestNip86_CreateClaimWithLimits_RejectsEmptyWeakDuplicate(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	t.Run("empty claim never generates", func(t *testing.T) {
		resp := call(t, router, nip86MethodCreateClaimWithLimits, `""`, `{}`)
		require.NotEmpty(t, resp.Error)
		claims, err := store.ListInviteClaims()
		require.NoError(t, err)
		require.Empty(t, claims, "createclaimwithlimits must never generate a code")
	})

	t.Run("weak claim", func(t *testing.T) {
		resp := call(t, router, nip86MethodCreateClaimWithLimits, `"too-short"`, `{}`)
		require.NotEmpty(t, resp.Error)
	})

	t.Run("duplicate", func(t *testing.T) {
		require.Empty(t, call(t, router, nip86MethodCreateClaimWithLimits, `"`+strongCode+`"`, `{}`).Error)
		dup := call(t, router, nip86MethodCreateClaimWithLimits, `"`+strongCode+`"`, `{}`)
		require.NotEmpty(t, dup.Error)
	})
}

/////////////////////////////////////////////////////////////////////
// D: getclaim
/////////////////////////////////////////////////////////////////////

func TestNip86_GetClaim_ReturnsStateIncludingDeadCodes(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	now := time.Now().Unix()
	cases := []*relay.InviteClaim{
		{Code: "live-code-0000001", CreatedAt: now, ExpiresAt: now + 3600, MaxUses: 5, Uses: 1},
		{Code: "expired-code-00001", CreatedAt: now - 7200, ExpiresAt: now - 3600, MaxUses: 0, Uses: 0},
		{Code: "usedup-code-000001", CreatedAt: now, ExpiresAt: now + 3600, MaxUses: 1, Uses: 1},
	}
	for _, c := range cases {
		require.NoError(t, store.PutInviteClaim(c))
	}

	for _, c := range cases {
		resp := call(t, router, nip86MethodGetClaim, `"`+c.Code+`"`)
		require.Empty(t, resp.Error, "getclaim must return state even for expired/used-up codes")
		m, ok := resp.Result.(map[string]any)
		require.True(t, ok)
		// The output field is `claim`, not the record's `code`.
		require.Equal(t, c.Code, m["claim"])
		require.NotContains(t, m, "code")
		require.EqualValues(t, c.ExpiresAt, m["expires_at"])
		require.EqualValues(t, c.MaxUses, m["max_uses"])
		require.EqualValues(t, c.Uses, m["uses"])
	}

	unknown := call(t, router, nip86MethodGetClaim, `"nope-unknown-00001"`)
	require.NotEmpty(t, unknown.Error)
}

/////////////////////////////////////////////////////////////////////
// B: member claims -- the Authorize (handler) + Visible (router) wiring
/////////////////////////////////////////////////////////////////////

// memberPrivKey and nonMemberPrivKey are fixed test secrets distinct from the
// admin's testPrivKey; their pubkeys are derived with pubkeyOf so a test never
// hard-codes a pubkey that must match a privkey.
const (
	memberPrivKey    = "0000000000000000000000000000000000000000000000000000000000000002"
	nonMemberPrivKey = "0000000000000000000000000000000000000000000000000000000000000003"
)

// pubkeyOf derives the x-only pubkey of a hex private key by signing an empty
// event -- the same trick nmilat's own nip86 handler tests use, so the test
// never has to hard-code a pubkey alongside its secret.
func pubkeyOf(t *testing.T, privKey string) string {
	t.Helper()
	ev := &nip01.Event{}
	require.NoError(t, ev.Sign(privKey))
	return ev.PubKey
}

// nip86HandlerResult is a reply plus the HTTP status, so a test can tell a 401
// (authorization refused -- the Router was never reached) from a 200 carrying
// an error field (reached the Router, but the method was refused or failed).
type nip86HandlerResult struct {
	status int
	resp   nip86.Response
}

// callHandler signs a NIP-98 request as privKey and runs it through the full
// nip86 HTTP handler (exercising Authorize + Visible together, as production
// does), returning the status and -- on 200 -- the decoded response. The URL is
// the bare relay root both sides agree on; the body is bound by NewAuthHeader's
// payload hash, so a signature really does cover this exact request.
func callHandler(t *testing.T, h *nip86.Handler, privKey, method string, params ...string) nip86HandlerResult {
	t.Helper()
	reqBody, err := json.Marshal(nip86.Request{Method: method, Params: nip86Params(t, params...)})
	require.NoError(t, err)

	const relayURL = "http://example.com/"
	auth, err := nip98.NewAuthHeader(privKey, relayURL, http.MethodPost, reqBody)
	require.NoError(t, err)

	httpReq := httptest.NewRequest(http.MethodPost, relayURL, bytes.NewReader(reqBody))
	httpReq.Header.Set("Content-Type", nip86.ContentType)
	httpReq.Header.Set("Authorization", auth)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httpReq)

	res := nip86HandlerResult{status: rec.Code}
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res.resp))
	}
	return res
}

// newMemberClaimHandler builds the real nip86 handler against a
// membership+nip86-enabled relay whose sole admin is testPrivKey's pubkey, with
// memberClaims set as given. memberPrivKey's pubkey is joined as a member;
// nonMemberPrivKey's is not. It returns the handler and the backing store.
func newMemberClaimHandler(t *testing.T, memberClaims bool) (*nip86.Handler, *relay.EventStore) {
	t.Helper()
	mc := memberClaims
	return newMemberClaimHandlerOn(t, &mc, false)
}

// newMemberClaimHandlerOn is newMemberClaimHandler with memberClaims possibly
// unset (nil) and the relay members-only (nip11.limitation.membership_required)
// or open. The config is not run through checkAndDefaultMembership, so an unset
// memberClaims exercises membershipMemberClaims' own default.
func newMemberClaimHandlerOn(t *testing.T, memberClaims *bool, membershipRequired bool) (*nip86.Handler, *relay.EventStore) {
	t.Helper()
	wsHandler, store := newTestWSHandler(t)
	adminPub := pubkeyOf(t, testPrivKey)
	require.NoError(t, wsHandler.Membership().Join(pubkeyOf(t, memberPrivKey), nil))

	prev := config
	t.Cleanup(func() { config = prev })
	md := nip11.Metadata{PubKey: adminPub, PrivKey: testPrivKey, Self: adminPub}
	md.Limitation.MembershipRequired = membershipRequired
	md.Limitation.AuthRequired = membershipRequired
	config = RelayConfig{
		Nip11:      md,
		Nip86:      &Nip86Config{Enabled: true},
		Membership: &MembershipConfig{Enabled: true, MemberClaims: memberClaims},
	}

	moderation, err := loadModeration(filepath.Join(t.TempDir(), "moderation.json"), adminPub)
	require.NoError(t, err)
	h, err := newNip86Handler(wsHandler, store, nil, moderation)
	require.NoError(t, err)
	require.NotNil(t, h)
	return h, store
}

// resultStrings coerces a decoded supportedmethods result ([]any of strings)
// into a []string for comparison.
func resultStrings(t *testing.T, v any) []string {
	t.Helper()
	raw, ok := v.([]any)
	require.True(t, ok, "supportedmethods result should be a JSON array")
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		s, ok := e.(string)
		require.True(t, ok)
		out = append(out, s)
	}
	return out
}

func TestNip86_MemberClaims_SupportedMethodsIsCreateClaimAndSupportedOnly(t *testing.T) {
	h, _ := newMemberClaimHandler(t, true)

	got := callHandler(t, h, memberPrivKey, nip86.MethodSupportedMethods)
	require.Equal(t, http.StatusOK, got.status)
	require.Empty(t, got.resp.Error)
	// Sorted, and nothing else: a member must not even see the admin methods.
	require.Equal(t, []string{"createclaim", "supportedmethods"}, resultStrings(t, got.resp.Result))
}

func TestNip86_MemberClaims_MemberCanCreateClaimAndItRedeems(t *testing.T) {
	h, store := newMemberClaimHandler(t, true)

	const code = "member-code-0000001"
	got := callHandler(t, h, memberPrivKey, nip86.MethodCreateClaim, `"`+code+`"`)
	require.Equal(t, http.StatusOK, got.status)
	require.Empty(t, got.resp.Error, "a member may mint an invite via createclaim")

	// A member's claim carries config defaults and no roles.
	stored, err := store.GetInviteClaim(code)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Empty(t, stored.Roles, "a member-minted claim carries no roles")

	// It redeems: ConsumeInviteClaim is exactly what a kind-28934 join calls,
	// so a successful consume (uses incremented) proves the code is live and
	// joinable -- asserted deterministically with an explicit `now`, no sleep.
	consumed, err := store.ConsumeInviteClaim(code, time.Now().Unix())
	require.NoError(t, err)
	require.Equal(t, 1, consumed.Uses)
}

func TestNip86_MemberClaims_MemberIsRefusedEveryOtherMethod(t *testing.T) {
	h, _ := newMemberClaimHandler(t, true)

	// Each is registered on the router but hidden from a member by Visible, so
	// Dispatch refuses it as unknown -- a 200 carrying an unknown-method error,
	// NOT a 401 (the member is authorized; it is the method that is off-limits).
	hidden := []struct {
		name   string
		params []string
	}{
		{nip86.MethodListClaims, nil},
		{nip86MethodGetClaim, []string{`"` + strongCode + `"`}},
		{nip86MethodCreateClaimWithLimits, []string{`"` + strongCode + `"`}},
		{nip86.MethodDeleteClaim, []string{`"` + strongCode + `"`}},
		{nip86.MethodAllowPubkey, []string{`"` + testMember + `"`}},
		{nip86.MethodBanPubkey, []string{`"` + testMember + `"`}}, // a moderation method
	}
	for _, c := range hidden {
		t.Run(c.name, func(t *testing.T) {
			got := callHandler(t, h, memberPrivKey, c.name, c.params...)
			require.Equal(t, http.StatusOK, got.status, "an authorized member reaches the router, not a 401")
			require.Contains(t, got.resp.Error, "unknown method", "a method hidden from a member is refused as unknown")
		})
	}
}

func TestNip86_MemberClaims_DisabledMakesMemberA401(t *testing.T) {
	h, _ := newMemberClaimHandler(t, false)

	// memberClaims:false leaves Authorize nil, so only AllowedPubkeys (the
	// admins) pass NIP-98's gate; a member is refused before any method -- a 401,
	// exactly as before the feature existed.
	got := callHandler(t, h, memberPrivKey, nip86.MethodSupportedMethods)
	require.Equal(t, http.StatusUnauthorized, got.status)
}

func TestNip86_MemberClaims_NonMemberIsA401(t *testing.T) {
	h, _ := newMemberClaimHandler(t, true)

	// A verified key that is neither an admin nor a member is still refused,
	// even with memberClaims on: Authorize returns false, so VerifyAnyPubkey's
	// widened gate closes again on ErrWrongPubkey -> 401.
	got := callHandler(t, h, nonMemberPrivKey, nip86.MethodSupportedMethods)
	require.Equal(t, http.StatusUnauthorized, got.status)
}

func TestNip86_MemberClaims_AdminReachesEveryMethod(t *testing.T) {
	h, _ := newMemberClaimHandler(t, true)

	// The admin's supportedmethods lists everything -- member-visible and
	// admin-only and moderation alike.
	got := callHandler(t, h, testPrivKey, nip86.MethodSupportedMethods)
	require.Equal(t, http.StatusOK, got.status)
	require.Empty(t, got.resp.Error)
	methods := resultStrings(t, got.resp.Result)
	for _, want := range []string{
		nip86.MethodCreateClaim,
		nip86.MethodListClaims,
		nip86MethodGetClaim,
		nip86MethodCreateClaimWithLimits,
		nip86.MethodDeleteClaim,
		nip86.MethodAllowPubkey,
		nip86.MethodBanPubkey,
		nip86.MethodSupportedMethods,
	} {
		require.Contains(t, methods, want)
	}

	// And an admin-only method actually runs for the admin (not refused).
	listed := callHandler(t, h, testPrivKey, nip86.MethodListClaims)
	require.Equal(t, http.StatusOK, listed.status)
	require.Empty(t, listed.resp.Error)
}

// memberClaims unset follows nip11.limitation.membership_required: a
// members-only relay keeps createclaim admin-only (a member is a 401), an open
// relay lets a member mint. An explicit value wins either way.
func TestNip86_MemberClaims_DefaultFollowsMembershipRequired(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name               string
		memberClaims       *bool
		membershipRequired bool
		memberMints        bool
	}{
		{"members-only relay, unset: member refused", nil, true, false},
		{"open relay, unset: member mints", nil, false, true},
		{"members-only relay, explicit true: member mints", &yes, true, true},
		{"open relay, explicit false: member refused", &no, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, store := newMemberClaimHandlerOn(t, c.memberClaims, c.membershipRequired)

			const code = "member-code-default01"
			got := callHandler(t, h, memberPrivKey, nip86.MethodCreateClaim, `"`+code+`"`)
			stored, err := store.GetInviteClaim(code)
			require.NoError(t, err)
			if c.memberMints {
				require.Equal(t, http.StatusOK, got.status)
				require.Empty(t, got.resp.Error)
				require.NotNil(t, stored, "the member's invite was minted")
			} else {
				require.Equal(t, http.StatusUnauthorized, got.status)
				require.Nil(t, stored, "no invite was minted")
			}

			// The admin is unaffected: createclaim works for it either way.
			const adminCode = "admin-code-default001"
			admin := callHandler(t, h, testPrivKey, nip86.MethodCreateClaim, `"`+adminCode+`"`)
			require.Equal(t, http.StatusOK, admin.status)
			require.Empty(t, admin.resp.Error)
		})
	}
}

/////////////////////////////////////////////////////////////////////
// F: kind 28934 enforcement for a createclaimwithlimits-created claim
/////////////////////////////////////////////////////////////////////

// inviteJoinMessage mirrors nmilat relay.handleJoin's mapping of a
// ConsumeInviteClaim error to the "restricted: ..." OK message a joining client
// sees. Asserting it here ties a code minted by createclaimwithlimits to the
// exact join-time wording, without needing nmilat's unexported Session harness.
func inviteJoinMessage(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, relay.ErrInviteClaimExpired):
		return "restricted: that invite code is expired."
	default: // NotFound and Exhausted both read as invalid from outside.
		return "restricted: that is an invalid invite code."
	}
}

func TestNip86_Enforcement_ExpiredAndUsedUp(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	t.Run("expired", func(t *testing.T) {
		code := "expiry-code-000001"
		exp := time.Now().Add(time.Hour).Unix()
		require.Empty(t, call(t, router, nip86MethodCreateClaimWithLimits, `"`+code+`"`, `{"expires_at":`+strconv.FormatInt(exp, 10)+`}`).Error)

		// Redeem as if the clock is past the expiry (ConsumeInviteClaim takes
		// `now`, so this is deterministic -- no sleep).
		_, err := store.ConsumeInviteClaim(code, exp+1)
		require.ErrorIs(t, err, relay.ErrInviteClaimExpired)
		require.Equal(t, "restricted: that invite code is expired.", inviteJoinMessage(err))

		// A failed redemption must not have incremented uses.
		stored, gerr := store.GetInviteClaim(code)
		require.NoError(t, gerr)
		require.Equal(t, 0, stored.Uses)
	})

	t.Run("used up", func(t *testing.T) {
		code := "single-code-000001"
		require.Empty(t, call(t, router, nip86MethodCreateClaimWithLimits, `"`+code+`"`, `{"max_uses":1}`).Error)

		now := time.Now().Unix()
		consumed, err := store.ConsumeInviteClaim(code, now)
		require.NoError(t, err)
		require.Equal(t, 1, consumed.Uses, "a successful redemption increments uses")

		_, err = store.ConsumeInviteClaim(code, now)
		require.ErrorIs(t, err, relay.ErrInviteClaimExhausted)
		require.Equal(t, "restricted: that is an invalid invite code.", inviteJoinMessage(err))
	})
}

func TestNip86_Enforcement_SingleUseIsAtomicUnderConcurrency(t *testing.T) {
	withTestConfig(t, false)
	wsHandler, store := newTestWSHandler(t)
	router := newNip86Router(membershipAdmin{ws: wsHandler, store: store})

	code := "race-code-00000001"
	require.Empty(t, call(t, router, nip86MethodCreateClaimWithLimits, `"`+code+`"`, `{"max_uses":1}`).Error)

	now := time.Now().Unix()
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := store.ConsumeInviteClaim(code, now); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	require.Equal(t, 1, successes, "a single-use code must admit exactly one of two racing joins")
}

/////////////////////////////////////////////////////////////////////
// G: config validation at startup
/////////////////////////////////////////////////////////////////////

func TestMembershipConfig_InviteFieldsAndValidation(t *testing.T) {
	t.Run("new fields parse from yaml", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  privkey: "`+testPrivKey+`"
membership:
  enabled: true
  inviteAllowNoExpiry: true
  inviteMaxTTL: "168h"
  memberClaims: false
`)
		require.NotNil(t, cfg.Membership)
		require.True(t, cfg.Membership.InviteAllowNoExpiry)
		require.Equal(t, "168h", cfg.Membership.InviteMaxTTL)
		require.NotNil(t, cfg.Membership.MemberClaims)
		require.False(t, *cfg.Membership.MemberClaims)
	})

	t.Run("bad inviteMaxTTL refused at startup", func(t *testing.T) {
		cfg := RelayConfig{
			Nip11:      nip11.Metadata{PrivKey: testPrivKey},
			Membership: &MembershipConfig{Enabled: true, InviteMaxTTL: "not-a-duration"},
		}
		require.Error(t, cfg.checkAndDefaultMembership())
	})

	t.Run("non-positive inviteMaxTTL refused", func(t *testing.T) {
		cfg := RelayConfig{
			Nip11:      nip11.Metadata{PrivKey: testPrivKey},
			Membership: &MembershipConfig{Enabled: true, InviteMaxTTL: "0h"},
		}
		require.Error(t, cfg.checkAndDefaultMembership())
	})

	t.Run("memberClaims defaults to true when unset on an open relay", func(t *testing.T) {
		cfg := RelayConfig{
			Nip11:      nip11.Metadata{PrivKey: testPrivKey},
			Membership: &MembershipConfig{Enabled: true},
		}
		require.NoError(t, cfg.checkAndDefaultMembership())
		require.NotNil(t, cfg.Membership.MemberClaims)
		require.True(t, *cfg.Membership.MemberClaims)
	})

	t.Run("memberClaims defaults to false when unset on a members-only relay", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  privkey: "`+testPrivKey+`"
  limitation:
    auth_required: true
    membership_required: true
membership:
  enabled: true
`)
		require.True(t, cfg.Nip11.Limitation.MembershipRequired)
		require.NoError(t, cfg.checkAndDefaultMembership())
		require.NotNil(t, cfg.Membership.MemberClaims)
		require.False(t, *cfg.Membership.MemberClaims)
	})

	t.Run("an explicit memberClaims wins over the default", func(t *testing.T) {
		yes, no := true, false
		membersOnly := RelayConfig{
			Nip11:      nip11.Metadata{PrivKey: testPrivKey},
			Membership: &MembershipConfig{Enabled: true, MemberClaims: &yes},
		}
		membersOnly.Nip11.Limitation.MembershipRequired = true
		require.NoError(t, membersOnly.checkAndDefaultMembership())
		require.True(t, *membersOnly.Membership.MemberClaims)

		open := RelayConfig{
			Nip11:      nip11.Metadata{PrivKey: testPrivKey},
			Membership: &MembershipConfig{Enabled: true, MemberClaims: &no},
		}
		require.NoError(t, open.checkAndDefaultMembership())
		require.False(t, *open.Membership.MemberClaims)
	})

	t.Run("membershipMemberClaims follows membership_required when unset", func(t *testing.T) {
		prev := config
		t.Cleanup(func() { config = prev })

		config = RelayConfig{Membership: &MembershipConfig{Enabled: true}}
		require.True(t, membershipMemberClaims(), "open relay")

		config.Nip11.Limitation.MembershipRequired = true
		require.False(t, membershipMemberClaims(), "members-only relay")

		yes := true
		config.Membership.MemberClaims = &yes
		require.True(t, membershipMemberClaims(), "explicit true wins")
	})
}

/////////////////////////////////////////////////////////////////////
// Option-2 CLI: invites create flag resolution + invites get
/////////////////////////////////////////////////////////////////////

func TestResolveInviteCreateExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	policy := invitePolicy{
		defaultTTL:     24 * time.Hour,
		maxTTL:         720 * time.Hour,
		allowNoExpiry:  false,
		defaultMaxUses: 0,
	}

	t.Run("mutually exclusive flags", func(t *testing.T) {
		_, err := resolveInviteCreateExpiry(time.Hour, true, now.Add(time.Hour).Unix(), true, false, policy, now)
		require.ErrorContains(t, err, "mutually exclusive")
		_, err = resolveInviteCreateExpiry(0, false, now.Add(time.Hour).Unix(), true, true, policy, now)
		require.ErrorContains(t, err, "mutually exclusive")
	})

	t.Run("no-expiry gated by policy", func(t *testing.T) {
		_, err := resolveInviteCreateExpiry(0, false, 0, false, true, policy, now)
		require.Error(t, err, "--no-expiry must be refused when inviteAllowNoExpiry is false")

		allow := policy
		allow.allowNoExpiry = true
		choice, err := resolveInviteCreateExpiry(0, false, 0, false, true, allow, now)
		require.NoError(t, err)
		require.NotNil(t, choice.expiresAt)
		require.EqualValues(t, 0, *choice.expiresAt)
	})

	t.Run("expires-at future vs over-max", func(t *testing.T) {
		future := now.Add(48 * time.Hour).Unix()
		choice, err := resolveInviteCreateExpiry(0, false, future, true, false, policy, now)
		require.NoError(t, err)
		require.NotNil(t, choice.expiresAt)
		require.Equal(t, future, *choice.expiresAt)

		_, err = resolveInviteCreateExpiry(0, false, now.Add(-time.Hour).Unix(), true, false, policy, now)
		require.ErrorContains(t, err, "future")

		over := now.Add(1000 * time.Hour).Unix() // beyond the 720h maxTTL
		_, err = resolveInviteCreateExpiry(0, false, over, true, false, policy, now)
		require.Error(t, err)
	})

	t.Run("ttl passes through", func(t *testing.T) {
		choice, err := resolveInviteCreateExpiry(2*time.Hour, true, 0, false, false, policy, now)
		require.NoError(t, err)
		require.Equal(t, "2h0m0s", choice.ttl)
		require.Nil(t, choice.expiresAt)
	})
}

// membershipTestCmd builds a cobra command carrying the flags the admin
// membership commands read.
func membershipTestCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "x"}
	cmd.Flags().Bool("json", true, "")
	cmd.Flags().Bool("quiet", true, "")
	return cmd
}

func TestRunInvitesGet(t *testing.T) {
	now := time.Now().Unix()
	// The admin list route returns every stored claim, incl. expired/used-up.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"invites":[` +
			`{"code":"live-code-0000001","expires_at":` + strconv.FormatInt(now+3600, 10) + `,"max_uses":5,"uses":1},` +
			`{"code":"expired-code-00001","expires_at":` + strconv.FormatInt(now-3600, 10) + `,"max_uses":0,"uses":0}` +
			`]}`))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	withViper(t, map[string]any{"port": port, "nip11.privkey": testPrivKey})

	t.Run("live code", func(t *testing.T) {
		require.NoError(t, runInvitesGet(membershipTestCmd(t), []string{"live-code-0000001"}))
	})
	t.Run("expired code still returns state", func(t *testing.T) {
		require.NoError(t, runInvitesGet(membershipTestCmd(t), []string{"expired-code-00001"}))
	})
	t.Run("unknown code is not_found", func(t *testing.T) {
		err := runInvitesGet(membershipTestCmd(t), []string{"no-such-code-00001"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "no such invite code")
	})
}
