package relay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip29"
	"github.com/ohstr/nmilat/nip86"
	"github.com/ohstr/nmilat/nipB7"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/wire"
	"github.com/stretchr/testify/require"
)

// Keys for the role tests: a host, a greeter, a plain member and an outsider.
const (
	hostPriv     = "000000000000000000000000000000000000000000000000000000000000000a"
	greeterPriv  = "000000000000000000000000000000000000000000000000000000000000000b"
	plainPriv    = "000000000000000000000000000000000000000000000000000000000000000c"
	outsiderPriv = "000000000000000000000000000000000000000000000000000000000000000d"
)

// rolesConfig is the design's example: hosts manage invites and members, ban
// and moderate every group; greeters may invite; uploaders may upload.
func rolesConfig() *MembershipConfig {
	return &MembershipConfig{
		Enabled: true,
		Roles: map[string]RoleTasks{
			"host":     {Can: []string{"@claims", "@members", "banpubkey", "banevent", "group:9005"}},
			"greeter":  {Can: []string{"createclaim"}},
			"uploader": {Can: []string{"blossom.upload"}},
		},
	}
}

// newAuthority builds a taskAuthority over cfg with each key in members
// (privkey -> NIP-43 roles) enrolled.
func newAuthority(t *testing.T, cfg RelayConfig, members map[string][]string) (*taskAuthority, *relayModeration) {
	t.Helper()
	wsHandler, _ := newTestWSHandler(t)
	for priv, roles := range members {
		require.NoError(t, wsHandler.Membership().Join(pubkeyOf(t, priv), roles))
	}
	moderation, err := loadModeration(filepath.Join(t.TempDir(), "moderation.json"), cfg.Nip11.PubKey)
	require.NoError(t, err)
	return newTaskAuthority(&cfg, wsHandler.Membership(), moderation), moderation
}

func ban(t *testing.T, m *relayModeration, priv string) {
	t.Helper()
	pk := pubkeyOf(t, priv)
	require.NoError(t, m.update(func(s *moderationState) { s.BannedPubkeys[pk] = "test" }))
}

func TestRoleTasks_LoadFromYAML(t *testing.T) {
	cfg := loadRelayConfigFromYAML(t, `
membership:
  enabled: true
  roles:
    Host:
      can: ["@claims", "@members", banpubkey, "group:9005"]
    greeter:
      can: [createclaim]
  members:
    can: [createclaim]
groups:
  create: [members, host]
`)
	require.Equal(t, []string{"@claims", "@members", "banpubkey", "group:9005"}, cfg.Membership.Roles["host"].Can, "config keys are lowercased")
	require.Equal(t, []string{"createclaim"}, cfg.Membership.Roles["greeter"].Can)
	require.Equal(t, []string{"createclaim"}, cfg.Membership.Members.Can)
	require.Equal(t, []string{"members", "host"}, cfg.Groups.Create)
	require.NoError(t, cfg.checkRoleTasks())

	t.Run("an empty groups.create restricts creation to the admins", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
groups:
  create: []
`)
		require.NotNil(t, cfg.Groups.Create)
		require.True(t, cfg.groupCreateRestricted())
	})

	t.Run("an omitted groups.create leaves it to everyone", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
groups:
  moderationLog: true
`)
		require.False(t, cfg.groupCreateRestricted())
	})
}

func TestRoleTasks_Validation(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name  string
		cfg   RelayConfig
		input string // "" means valid
	}{
		{"the design example", RelayConfig{Membership: rolesConfig(), Groups: &GroupsConfig{Create: []string{"members"}}}, ""},
		{"every alias and ncli name", RelayConfig{Membership: &MembershipConfig{Enabled: true, Roles: map[string]RoleTasks{"r": {Can: []string{
			"@claims", "@members", "@bans", "@roles", "@relayinfo", "@policy", "*", "group:*", "group:9000", "group:9020",
			"blossom.upload", "blossom.download", "blossom.delete", "huddle.join", "group.create",
			"createclaimwithlimits", "getclaim", "listeventsneedingmoderation", "supportedmethods",
		}}}}}, ""},
		{"an unknown task", RelayConfig{Membership: &MembershipConfig{Enabled: true, Roles: map[string]RoleTasks{"r": {Can: []string{"banhammer"}}}}}, "banhammer"},
		{"an unknown alias", RelayConfig{Membership: &MembershipConfig{Enabled: true, Roles: map[string]RoleTasks{"r": {Can: []string{"@everything"}}}}}, "@everything"},
		{"a kind outside 9000-9020", RelayConfig{Membership: &MembershipConfig{Enabled: true, Roles: map[string]RoleTasks{"r": {Can: []string{"group:9021"}}}}}, "group:9021"},
		{"create-group, which no authorizer is asked about", RelayConfig{Membership: &MembershipConfig{Enabled: true, Roles: map[string]RoleTasks{"r": {Can: []string{"group:9007"}}}}}, "group:9007"},
		{"a role named everyone", RelayConfig{Membership: &MembershipConfig{Enabled: true, Roles: map[string]RoleTasks{"everyone": {Can: []string{"createclaim"}}}}}, "everyone"},
		{"a role named Members", RelayConfig{Membership: &MembershipConfig{Enabled: true, Roles: map[string]RoleTasks{"Members": {Can: []string{"createclaim"}}}}}, "Members"},
		{"one role id in two cases", RelayConfig{Membership: &MembershipConfig{Enabled: true, Roles: map[string]RoleTasks{"Host": {Can: []string{"createclaim"}}, "host": {Can: []string{"banpubkey"}}}}}, "host"},
		{"groups.create Everyone without membership", RelayConfig{Groups: &GroupsConfig{Create: []string{"Everyone"}}}, ""},
		{"a kind that is not a number", RelayConfig{Membership: &MembershipConfig{Enabled: true, Roles: map[string]RoleTasks{"r": {Can: []string{"group:ban"}}}}}, "group:ban"},
		{"a bad members task", RelayConfig{Membership: &MembershipConfig{Enabled: true, Members: &RoleTasks{Can: []string{"blossom.uplaod"}}}}, "blossom.uplaod"},
		{"roles without membership", RelayConfig{Membership: &MembershipConfig{Roles: map[string]RoleTasks{"r": {Can: []string{"createclaim"}}}}}, "membership.roles"},
		{"members without membership", RelayConfig{Membership: &MembershipConfig{Members: &RoleTasks{}}}, "membership.members"},
		{"an empty role id", RelayConfig{Membership: &MembershipConfig{Enabled: true, Roles: map[string]RoleTasks{"": {Can: []string{"createclaim"}}}}}, "membership.roles"},
		{"memberClaims true, members.can without createclaim", RelayConfig{Membership: &MembershipConfig{Enabled: true, MemberClaims: &yes, Members: &RoleTasks{Can: []string{"blossom.upload"}}}}, "membership.memberClaims"},
		{"memberClaims false, members.can with @claims", RelayConfig{Membership: &MembershipConfig{Enabled: true, MemberClaims: &no, Members: &RoleTasks{Can: []string{"@claims"}}}}, "membership.memberClaims"},
		{"memberClaims agreeing with members.can", RelayConfig{Membership: &MembershipConfig{Enabled: true, MemberClaims: &yes, Members: &RoleTasks{Can: []string{"createclaim"}}}}, ""},
		{"groups.create everyone without membership", RelayConfig{Groups: &GroupsConfig{Create: []string{"everyone"}}}, ""},
		{"groups.create members without membership", RelayConfig{Groups: &GroupsConfig{Create: []string{"members"}}}, "members"},
		{"groups.create a role without membership", RelayConfig{Groups: &GroupsConfig{Create: []string{"host"}}}, "host"},
		{"groups.create an empty entry", RelayConfig{Membership: &MembershipConfig{Enabled: true}, Groups: &GroupsConfig{Create: []string{""}}}, "groups.create"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.checkRoleTasks()
			if c.input == "" {
				require.NoError(t, err)
				return
			}
			var ce *common.CLIError
			require.True(t, errors.As(err, &ce), "want a CLIError, got %v", err)
			require.Equal(t, common.CodeInvalidInput, ce.Code)
			require.Equal(t, c.input, ce.Input)
		})
	}
}

func TestTaskAuthority_Can(t *testing.T) {
	const nip86AdminPriv = "000000000000000000000000000000000000000000000000000000000000000e"
	nip86Admin := pubkeyOf(t, nip86AdminPriv)
	groupAdmin := pubkeyOf(t, "000000000000000000000000000000000000000000000000000000000000000f")
	cfg := RelayConfig{
		Nip11:      nip11.Metadata{PubKey: testPubKey},
		Nip86:      &Nip86Config{Enabled: true, Admins: []string{strings.ToUpper(nip86Admin)}}, // matched case-insensitively
		Groups:     &GroupsConfig{Admins: []string{groupAdmin}, Create: []string{"host"}},
		Huddle:     &HuddleConfig{Enabled: true, RequireMembership: true},
		Membership: rolesConfig(),
	}
	cfg.Membership.Members = &RoleTasks{Can: []string{"huddle.join"}}
	auth, moderation := newAuthority(t, cfg, map[string][]string{
		hostPriv:    {"Host"}, // role ids match case-insensitively
		greeterPriv: {"greeter", "uploader"},
		plainPriv:   nil,
	})
	host, greeter, plain, outsider := pubkeyOf(t, hostPriv), pubkeyOf(t, greeterPriv), pubkeyOf(t, plainPriv), pubkeyOf(t, outsiderPriv)

	cases := []struct {
		who  string
		task string
		want bool
	}{
		{testPubKey, "changerelayname", true},
		// The config's admins hold * but no group:<kind>: groups.admins (and
		// the relay key) moderate groups through nmilat, not through here.
		{testPubKey, "group:9000", false},
		{nip86Admin, "blossom.delete", true},
		{nip86Admin, "group:9000", false},
		{groupAdmin, "group:9001", false},
		{groupAdmin, "createclaim", false},
		{host, "createclaim", true},        // @claims
		{host, "getclaim", true},           // @claims, an ncli extra
		{host, "listallowedpubkeys", true}, // @members
		{host, "banpubkey", true},
		{host, "unbanpubkey", false},
		{host, "group:9005", true},
		{host, "group:9000", false},
		{host, "group.create", true}, // groups.create: [host]
		{host, "huddle.join", true},  // members grant
		{greeter, "createclaim", true},
		{greeter, "listclaims", false},
		{greeter, "blossom.upload", true}, // second role
		{greeter, "group.create", false},
		{plain, "createclaim", false},
		{plain, "huddle.join", true},
		{outsider, "huddle.join", false},
		{outsider, "createclaim", false},
		{outsider, "blossom.download", true}, // not restricted: everyone
	}
	for _, c := range cases {
		require.Equal(t, c.want, auth.can(c.who, c.task), "can(%s, %s)", c.who[:8], c.task)
	}

	require.True(t, auth.mayUseNip86(host))
	require.True(t, auth.mayUseNip86(greeter))
	require.False(t, auth.mayUseNip86(plain), "huddle.join is not a NIP-86 method")
	require.False(t, auth.mayUseNip86(outsider))

	t.Run("a ban takes away what roles and membership give", func(t *testing.T) {
		ban(t, moderation, hostPriv)
		require.False(t, auth.can(host, "createclaim"))
		require.False(t, auth.can(host, "huddle.join"))
		require.False(t, auth.can(host, "blossom.download"), "not even what everyone may do")
		require.False(t, auth.mayUseNip86(host))
	})

	t.Run("but not what the config file gives", func(t *testing.T) {
		ban(t, moderation, nip86AdminPriv)
		require.True(t, auth.can(nip86Admin, "unbanpubkey"))
		require.True(t, auth.mayUseNip86(nip86Admin))
	})

	t.Run("star covers every task but group kinds", func(t *testing.T) {
		var s taskSet
		s.add("*")
		for _, task := range []string{"createclaim", "blossom.delete", "group.create"} {
			require.True(t, s.has(task), task)
		}
		require.False(t, s.has("group:9009"), "group kinds must be listed")
		s.add("group:*")
		require.True(t, s.has("group:9009"))
	})

	t.Run("a lost membership takes the roles with it", func(t *testing.T) {
		require.NoError(t, auth.membership.Leave(greeter))
		require.False(t, auth.can(greeter, "createclaim"))
	})
}

// With only the old switches set, the grants reproduce them exactly.
func TestTaskAuthority_OldSwitchesMapOn(t *testing.T) {
	yes, no := true, false
	type want struct{ member, outsider bool }
	cases := []struct {
		name string
		cfg  func(*RelayConfig)
		task string
		want want
	}{
		{"memberClaims unset, open relay", func(*RelayConfig) {}, "createclaim", want{true, false}},
		{"memberClaims unset, members-only relay", func(c *RelayConfig) { c.Nip11.Limitation.MembershipRequired = true }, "createclaim", want{false, false}},
		{"memberClaims true", func(c *RelayConfig) { c.Membership.MemberClaims = &yes }, "createclaim", want{true, false}},
		{"memberClaims false", func(c *RelayConfig) { c.Membership.MemberClaims = &no }, "createclaim", want{false, false}},
		{"memberClaims never reaches listclaims", func(c *RelayConfig) { c.Membership.MemberClaims = &yes }, "listclaims", want{false, false}},
		{"blossom open", func(c *RelayConfig) { c.Blossom = &BlossomConfig{Enabled: true} }, "blossom.upload", want{true, true}},
		{"blossom upload members-only", func(c *RelayConfig) {
			c.Blossom = &BlossomConfig{Enabled: true, MembersOnly: BlossomMembersOnly{Upload: &yes}}
		}, "blossom.upload", want{true, false}},
		{"blossom upload unset on a members-only relay", func(c *RelayConfig) {
			c.Nip11.Limitation.MembershipRequired = true
			c.Blossom = &BlossomConfig{Enabled: true}
		}, "blossom.upload", want{true, false}},
		{"blossom download open", func(c *RelayConfig) { c.Blossom = &BlossomConfig{Enabled: true} }, "blossom.download", want{true, true}},
		{"blossom download members-only", func(c *RelayConfig) {
			c.Blossom = &BlossomConfig{Enabled: true, MembersOnly: BlossomMembersOnly{Download: true}}
		}, "blossom.download", want{true, false}},
		{"blossom delete", func(c *RelayConfig) { c.Blossom = &BlossomConfig{Enabled: true} }, "blossom.delete", want{false, false}},
		{"huddle open", func(c *RelayConfig) { c.Huddle = &HuddleConfig{Enabled: true} }, "huddle.join", want{true, true}},
		{"huddle requireMembership", func(c *RelayConfig) { c.Huddle = &HuddleConfig{Enabled: true, RequireMembership: true} }, "huddle.join", want{true, false}},
		{"groups.create unset", func(*RelayConfig) {}, "group.create", want{true, true}},
		{"groups.create everyone", func(c *RelayConfig) { c.Groups = &GroupsConfig{Create: []string{"everyone"}} }, "group.create", want{true, true}},
		{"groups.create members", func(c *RelayConfig) { c.Groups = &GroupsConfig{Create: []string{"members"}} }, "group.create", want{true, false}},
		{"no member holds group moderation", func(*RelayConfig) {}, "group:9000", want{false, false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := RelayConfig{Nip11: nip11.Metadata{PubKey: testPubKey}, Membership: &MembershipConfig{Enabled: true}}
			c.cfg(&cfg)
			auth, _ := newAuthority(t, cfg, map[string][]string{plainPriv: nil})
			require.Equal(t, c.want.member, auth.can(pubkeyOf(t, plainPriv), c.task), "member")
			require.Equal(t, c.want.outsider, auth.can(pubkeyOf(t, outsiderPriv), c.task), "outsider")
			// The relay key holds * : every task but group:<kind>, which
			// nmilat gives it as a relay admin instead.
			require.Equal(t, !isGroupKindTask(c.task), auth.can(testPubKey, c.task), "the relay key holds *")
		})
	}
}

// membership.members.can, when set, is the members grant on its own: the old
// switches then only say whether a task is restricted.
func TestTaskAuthority_MembersCanWins(t *testing.T) {
	yes := true
	cfg := RelayConfig{
		Nip11: nip11.Metadata{PubKey: testPubKey},
		Membership: &MembershipConfig{
			Enabled: true,
			Members: &RoleTasks{Can: []string{"createclaim"}},
			Roles:   map[string]RoleTasks{"uploader": {Can: []string{"blossom.upload"}}},
		},
		Blossom: &BlossomConfig{Enabled: true, MembersOnly: BlossomMembersOnly{Upload: &yes}},
		Huddle:  &HuddleConfig{Enabled: true, RequireMembership: true},
	}
	auth, _ := newAuthority(t, cfg, map[string][]string{plainPriv: nil, greeterPriv: {"uploader"}})
	require.True(t, auth.can(pubkeyOf(t, plainPriv), "createclaim"))
	require.False(t, auth.can(pubkeyOf(t, plainPriv), "blossom.upload"), "members.can leaves it out")
	require.False(t, auth.can(pubkeyOf(t, plainPriv), "huddle.join"), "members.can leaves it out")
	require.True(t, auth.can(pubkeyOf(t, greeterPriv), "blossom.upload"), "the uploader role carries it")
	require.False(t, auth.can(pubkeyOf(t, outsiderPriv), "blossom.upload"))
}

/////////////////////////////////////////////////////////////////////
// NIP-86: Authorize and Visible per role, supportedmethods per caller
/////////////////////////////////////////////////////////////////////

func newRoleNip86Handler(t *testing.T, membership *MembershipConfig, members map[string][]string) (*nip86.Handler, *relayModeration) {
	t.Helper()
	h, moderation, _ := newRoleNip86HandlerWS(t, membership, members)
	return h, moderation
}

// newRoleNip86HandlerWS is newRoleNip86Handler, also returning the session
// handler so a test can read the membership records.
func newRoleNip86HandlerWS(t *testing.T, membership *MembershipConfig, members map[string][]string) (*nip86.Handler, *relayModeration, *relay.SessionHandler) {
	t.Helper()
	wsHandler, store := newTestWSHandler(t)
	for priv, roles := range members {
		require.NoError(t, wsHandler.Membership().Join(pubkeyOf(t, priv), roles))
	}
	prev := config
	t.Cleanup(func() { config = prev })
	adminPub := pubkeyOf(t, testPrivKey)
	config = RelayConfig{
		Nip11:      nip11.Metadata{PubKey: adminPub, PrivKey: testPrivKey, Self: adminPub},
		Nip86:      &Nip86Config{Enabled: true},
		Membership: membership,
	}
	moderation, err := loadModeration(filepath.Join(t.TempDir(), "moderation.json"), adminPub)
	require.NoError(t, err)
	h, err := newNip86Handler(wsHandler, store, nil, moderation)
	require.NoError(t, err)
	return h, moderation, wsHandler
}

func supportedFor(t *testing.T, h *nip86.Handler, priv string) []string {
	t.Helper()
	got := callHandler(t, h, priv, nip86.MethodSupportedMethods)
	require.Equal(t, http.StatusOK, got.status)
	require.Empty(t, got.resp.Error)
	return resultStrings(t, got.resp.Result)
}

func TestNip86_RolesDecideWhatEachCallerMayDo(t *testing.T) {
	no := false
	m := rolesConfig()
	m.MemberClaims = &no
	h, moderation := newRoleNip86Handler(t, m, map[string][]string{
		hostPriv:    {"host"},
		greeterPriv: {"greeter"},
		plainPriv:   nil,
	})

	t.Run("supportedmethods answers each caller with its own set", func(t *testing.T) {
		require.ElementsMatch(t, []string{
			"supportedmethods", "createclaim", "createclaimwithlimits", "getclaim", "listclaims", "deleteclaim",
			"allowpubkey", "unallowpubkey", "listallowedpubkeys", "banpubkey", "banevent",
		}, supportedFor(t, h, hostPriv))
		require.ElementsMatch(t, []string{"supportedmethods", "createclaim"}, supportedFor(t, h, greeterPriv))
		require.Contains(t, supportedFor(t, h, testPrivKey), "changerelayname", "the admin sees everything")
	})

	t.Run("a role's method works", func(t *testing.T) {
		got := callHandler(t, h, greeterPriv, nip86.MethodCreateClaim, `"greeter-code-000001"`)
		require.Equal(t, http.StatusOK, got.status)
		require.Empty(t, got.resp.Error)

		got = callHandler(t, h, hostPriv, nip86.MethodBanPubkey, `"`+pubkeyOf(t, outsiderPriv)+`"`)
		require.Equal(t, http.StatusOK, got.status)
		require.Empty(t, got.resp.Error)
	})

	t.Run("a method the role lacks is unknown to it", func(t *testing.T) {
		got := callHandler(t, h, greeterPriv, nip86.MethodListClaims)
		require.Equal(t, http.StatusOK, got.status)
		require.Contains(t, got.resp.Error, "unknown method")

		got = callHandler(t, h, hostPriv, nip86.MethodUnbanPubkey, `"`+pubkeyOf(t, outsiderPriv)+`"`)
		require.Contains(t, got.resp.Error, "unknown method")
	})

	t.Run("a member with no method, and an outsider, are a 401", func(t *testing.T) {
		require.Equal(t, http.StatusUnauthorized, callHandler(t, h, plainPriv, nip86.MethodSupportedMethods).status)
		require.Equal(t, http.StatusUnauthorized, callHandler(t, h, outsiderPriv, nip86.MethodSupportedMethods).status)
	})

	t.Run("a banned role holder is a 401", func(t *testing.T) {
		ban(t, moderation, greeterPriv)
		require.Equal(t, http.StatusUnauthorized, callHandler(t, h, greeterPriv, nip86.MethodCreateClaim, `"greeter-code-000002"`).status)
	})
}

/////////////////////////////////////////////////////////////////////
// Huddle: a role carries huddle.join
/////////////////////////////////////////////////////////////////////

func TestHuddle_RoleCarriesJoin(t *testing.T) {
	store := newHuddleStore(t)
	t.Cleanup(store.Close)
	metadata := &nip11.Metadata{PubKey: huddleRelayPub, PrivKey: huddleRelayPriv, URL: huddleRelayURL}
	wsHandler := relay.NewSessionHandler(store, metadata, nil)

	alice, bob := pubkeyOf(t, alicePriv), pubkeyOf(t, bobPriv)
	require.NoError(t, wsHandler.Membership().Join(alice, []string{"caller"}))
	require.NoError(t, wsHandler.Membership().Join(bob, nil))

	huddle := &HuddleConfig{Enabled: true, RequireMembership: true}
	cfg := RelayConfig{
		Nip11:  *metadata,
		Huddle: huddle,
		Membership: &MembershipConfig{
			Enabled: true,
			Members: &RoleTasks{Can: []string{}},
			Roles:   map[string]RoleTasks{"caller": {Can: []string{"huddle.join"}}},
		},
	}
	moderation, err := loadModeration(filepath.Join(t.TempDir(), "moderation.json"), huddleRelayPub)
	require.NoError(t, err)
	auth := newTaskAuthority(&cfg, wsHandler.Membership(), moderation)

	mux := http.NewServeMux()
	rooms := registerHuddleRoutes(mux, wsHandler, huddle, huddleRelayURL, auth)
	t.Cleanup(func() { endHuddleRooms(rooms) })
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	_, msg := handshake(t, ts, "room-1", alicePriv)
	require.Equal(t, "joined", msg.Type, "the caller role joins: %+v", msg)

	_, msg = handshake(t, ts, "room-1", bobPriv)
	require.Equal(t, "join_rejected", msg.Code, "a member without the role does not: %+v", msg)

	_, msg = handshake(t, ts, "room-1", huddleRelayPriv)
	require.Equal(t, "joined", msg.Type, "the relay key holds every task: %+v", msg)
}

/////////////////////////////////////////////////////////////////////
// In-process relay: Blossom role grants and the kind 9007 gate
/////////////////////////////////////////////////////////////////////

// enrollWithRoles enrolls pubkey with roles through the admin API.
func enrollWithRoles(t *testing.T, baseURL, pubkey string, roles ...string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"pubkey": pubkey, "roles": roles})
	require.NoError(t, err)
	target := baseURL + "/admin/membership/members"
	header, err := common.GenerateNIP98Header(testPrivKey, target, http.MethodPost, body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", header)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestScenario_RolesOnACommunityRelay(t *testing.T) {
	cfg := communityRelayConfig(t)
	cfg.Membership = rolesConfig()
	cfg.Membership.Members = &RoleTasks{Can: []string{"createclaim"}}
	cfg.Groups.Create = []string{"host"}
	ts := bootScenario(t, cfg)

	host, uploader, plain := pubkeyOf(t, hostPriv), pubkeyOf(t, greeterPriv), pubkeyOf(t, plainPriv)
	enrollWithRoles(t, ts.URL, host, "host")
	enrollWithRoles(t, ts.URL, uploader, "uploader")
	enrollWithRoles(t, ts.URL, plain)

	t.Run("blossom upload follows the uploader role", func(t *testing.T) {
		blob := []byte("a file for the role test")
		sum := sha256.Sum256(blob)
		sha := hex.EncodeToString(sum[:])
		upload := func(priv string) int {
			req, err := http.NewRequest(http.MethodPut, ts.URL+"/upload", bytes.NewReader(blob))
			require.NoError(t, err)
			req.Header.Set("Authorization", blossomToken(t, priv, nipB7.VerbUpload, sha))
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			_ = resp.Body.Close()
			return resp.StatusCode
		}
		require.Equal(t, http.StatusForbidden, upload(plainPriv), "members.can leaves blossom.upload out")
		require.Equal(t, http.StatusForbidden, upload(outsiderPriv))
		require.Equal(t, http.StatusOK, upload(greeterPriv), "the uploader role uploads")
		require.Equal(t, http.StatusOK, upload(testPrivKey), "the relay key uploads")
	})

	t.Run("only a host creates a group", func(t *testing.T) {
		member := connectAs(t, ts, plainPriv)
		ok := publishAs(t, member, nip29.NewCreateGroup(plain, "by-a-member"), plainPriv)
		require.False(t, ok.Accepted)
		require.True(t, strings.HasPrefix(ok.Message, "restricted:"), ok.Message)

		outsider := connectAs(t, ts, outsiderPriv)
		require.False(t, publishAs(t, outsider, nip29.NewCreateGroup(pubkeyOf(t, outsiderPriv), "by-an-outsider"), outsiderPriv).Accepted)

		hostConn := connectAs(t, ts, hostPriv)
		require.True(t, publishAs(t, hostConn, nip29.NewCreateGroup(host, "by-a-host"), hostPriv).Accepted)
	})
}

// With groups.create left out, anyone may still create a group: the
// community scenario's member does (TestScenario_CommunityRelay), and so does
// an outsider here.
func TestScenario_GroupCreateOpenByDefault(t *testing.T) {
	ts := bootScenario(t, communityRelayConfig(t))
	outsider := connectAs(t, ts, outsiderPriv)
	require.True(t, publishAs(t, outsider, nip29.NewCreateGroup(pubkeyOf(t, outsiderPriv), "anyones"), outsiderPriv).Accepted)
}

/////////////////////////////////////////////////////////////////////
// Invite requests and NIP-29 group tasks
/////////////////////////////////////////////////////////////////////

func TestInviteRequestAllowed(t *testing.T) {
	no := false
	m := rolesConfig()
	m.MemberClaims = &no
	auth, _ := newAuthority(t, RelayConfig{Nip11: nip11.Metadata{PubKey: testPubKey}, Membership: m}, map[string][]string{
		greeterPriv: {"greeter"},
		plainPriv:   nil,
	})
	require.NoError(t, auth.inviteRequestAllowed([]relay.AuthedIdentity{{Pubkey: pubkeyOf(t, greeterPriv)}}))
	require.Error(t, auth.inviteRequestAllowed([]relay.AuthedIdentity{{Pubkey: pubkeyOf(t, plainPriv)}}))
	require.Error(t, auth.inviteRequestAllowed(nil))
	// Any of the session's identities will do; a NIP-AA virtual identity
	// counts only for its own key, not its owner's.
	require.NoError(t, auth.inviteRequestAllowed([]relay.AuthedIdentity{{Pubkey: pubkeyOf(t, plainPriv)}, {Pubkey: pubkeyOf(t, greeterPriv)}}))
	require.Error(t, auth.inviteRequestAllowed([]relay.AuthedIdentity{{Pubkey: pubkeyOf(t, outsiderPriv), Membership: relay.MembershipVirtual, Owner: pubkeyOf(t, greeterPriv)}}))
}

func TestScenario_InviteRequestNeedsCreateclaim(t *testing.T) {
	no := false
	cfg := communityRelayConfig(t)
	cfg.Membership = rolesConfig()
	cfg.Membership.MemberClaims = &no
	ts := bootScenario(t, cfg)
	enrollWithRoles(t, ts.URL, pubkeyOf(t, greeterPriv), "greeter")
	enrollWithRoles(t, ts.URL, pubkeyOf(t, plainPriv))

	request := func(priv string) (got bool, closed string) {
		conn := connectAs(t, ts, priv)
		conn.Outgoing() <- wire.NewRequestPacket("inv", nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{Kinds: []int{28935}}))
		deadline := time.After(5 * time.Second)
		for {
			select {
			case res := <-conn.Read():
				switch r := res.(type) {
				case *wire.EventSubscriptionResponse:
					got = true
				case *wire.EOSESubscriptionResponse:
					return got, ""
				case *wire.ClosedSubscriptionResponse:
					return got, r.Message
				}
			case <-deadline:
				t.Fatal("timed out waiting for the invite answer")
			}
		}
	}

	got, closed := request(greeterPriv)
	require.True(t, got, "a greeter gets a code (closed: %q)", closed)
	got, closed = request(plainPriv)
	require.False(t, got)
	require.True(t, strings.HasPrefix(closed, "restricted:"), closed)
}

func TestGroupAuthorizer(t *testing.T) {
	cfg := RelayConfig{Nip11: nip11.Metadata{PubKey: testPubKey}, Membership: rolesConfig()}
	auth, _ := newAuthority(t, cfg, map[string][]string{hostPriv: {"host"}, plainPriv: nil})
	require.True(t, auth.groupAuthorizer("any", pubkeyOf(t, hostPriv), 9005))
	require.False(t, auth.groupAuthorizer("any", pubkeyOf(t, hostPriv), 9000))
	require.False(t, auth.groupAuthorizer("any", pubkeyOf(t, plainPriv), 9005))
	require.False(t, auth.groupAuthorizer("any", testPubKey, 9009), "the config's admins get no group power from here")

	cfg.Membership.Roles["warden"] = RoleTasks{Can: []string{"group:*"}}
	auth, _ = newAuthority(t, cfg, map[string][]string{hostPriv: {"warden"}})
	require.True(t, auth.groupAuthorizer("any", pubkeyOf(t, hostPriv), 9009))
	require.False(t, auth.groupAuthorizer("any", pubkeyOf(t, hostPriv), 1), "only moderation kinds")
}

func TestScenario_GroupKindTasks(t *testing.T) {
	cfg := communityRelayConfig(t)
	cfg.Membership = &MembershipConfig{Enabled: true, Roles: map[string]RoleTasks{"steward": {Can: []string{"group:9000"}}}}
	ts := bootScenario(t, cfg)
	steward, plain := pubkeyOf(t, hostPriv), pubkeyOf(t, plainPriv)
	enrollWithRoles(t, ts.URL, steward, "steward")
	enrollWithRoles(t, ts.URL, plain)

	creator := connectAs(t, ts, plainPriv)
	require.True(t, publishAs(t, creator, nip29.NewCreateGroup(plain, "stewarded"), plainPriv).Accepted)

	// The steward is no member of the group, yet may add one; not so delete.
	stewardConn := connectAs(t, ts, hostPriv)
	require.True(t, publishAs(t, stewardConn, nip29.NewPutUser(steward, "stewarded", pubkeyOf(t, outsiderPriv)), hostPriv).Accepted)
	require.False(t, publishAs(t, stewardConn, nip29.NewDeleteEvent(steward, "stewarded", strings.Repeat("ab", 32)), hostPriv).Accepted)
}

func TestScenario_GroupGrantCeiling(t *testing.T) {
	for _, ceiling := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[ceiling], func(t *testing.T) {
			cfg := communityRelayConfig(t)
			cfg.Groups.Roles = map[string][]int{"admin": {9000, 9001, 9002, 9005, 9008, 9009}, "inviter": {9000}}
			cfg.Groups.GrantCeiling = &ceiling
			ts := bootScenario(t, cfg)
			creatorPub, inviterPub := pubkeyOf(t, plainPriv), pubkeyOf(t, hostPriv)

			creator := connectAs(t, ts, plainPriv)
			require.True(t, publishAs(t, creator, nip29.NewCreateGroup(creatorPub, "ceiling"), plainPriv).Accepted)
			require.True(t, publishAs(t, creator, nip29.NewPutUser(creatorPub, "ceiling", inviterPub, "inviter"), plainPriv).Accepted)

			// An inviter (9000 only) hands out admin: more than it holds.
			inviter := connectAs(t, ts, hostPriv)
			ok := publishAs(t, inviter, nip29.NewPutUser(inviterPub, "ceiling", pubkeyOf(t, outsiderPriv), "admin"), hostPriv)
			require.Equal(t, !ceiling, ok.Accepted, ok.Message)
		})
	}
}

/////////////////////////////////////////////////////////////////////
// Review fixes: group power, role id case, the grant ceiling default
/////////////////////////////////////////////////////////////////////

// A nip86.admins key is no group admin: it may not moderate a group it
// does not belong to. groups.admins still may, and so may a relay role
// that lists the kind.
func TestScenario_Nip86AdminIsNoGroupAdmin(t *testing.T) {
	const nip86AdminPriv = "000000000000000000000000000000000000000000000000000000000000000e"
	const groupAdminPriv = "000000000000000000000000000000000000000000000000000000000000000f"
	cfg := communityRelayConfig(t)
	cfg.Nip86 = &Nip86Config{Enabled: true, Admins: []string{pubkeyOf(t, nip86AdminPriv)}}
	cfg.Groups.Admins = []string{pubkeyOf(t, groupAdminPriv)}
	cfg.Membership = &MembershipConfig{Enabled: true, Roles: map[string]RoleTasks{
		"janitor": {Can: []string{"group:9005"}},
		"boss":    {Can: []string{"*"}},
	}}
	ts := bootScenario(t, cfg)
	owner, janitor, boss := pubkeyOf(t, plainPriv), pubkeyOf(t, hostPriv), pubkeyOf(t, greeterPriv)
	enrollWithRoles(t, ts.URL, owner)
	enrollWithRoles(t, ts.URL, janitor, "janitor")
	enrollWithRoles(t, ts.URL, boss, "boss")

	creator := connectAs(t, ts, plainPriv)
	require.True(t, publishAs(t, creator, nip29.NewCreateGroup(owner, "owned"), plainPriv).Accepted)

	nip86Admin := pubkeyOf(t, nip86AdminPriv)
	adminConn := connectAs(t, ts, nip86AdminPriv)
	ok := publishAs(t, adminConn, nip29.NewPutUser(nip86Admin, "owned", nip86Admin, "admin"), nip86AdminPriv)
	require.False(t, ok.Accepted, "a nip86 admin may not seat itself in someone's group: %s", ok.Message)

	bossConn := connectAs(t, ts, greeterPriv)
	require.False(t, publishAs(t, bossConn, nip29.NewPutUser(boss, "owned", boss), greeterPriv).Accepted,
		"* in a relay role does not cover group kinds")

	groupAdmin := pubkeyOf(t, groupAdminPriv)
	gaConn := connectAs(t, ts, groupAdminPriv)
	require.True(t, publishAs(t, gaConn, nip29.NewPutUser(groupAdmin, "owned", pubkeyOf(t, outsiderPriv)), groupAdminPriv).Accepted,
		"groups.admins moderate every group")

	msg := nip01.NewUnsignedEvent(9, owner, "hello", []string{"h", "owned"})
	require.True(t, publishAs(t, creator, msg, plainPriv).Accepted)
	janitorConn := connectAs(t, ts, hostPriv)
	ok = publishAs(t, janitorConn, nip29.NewDeleteEvent(janitor, "owned", msg.ID), hostPriv)
	require.True(t, ok.Accepted, "a role listing group:9005 deletes events: %s", ok.Message)
}

// With relay roles carrying a group kind and grantCeiling unset, the ceiling
// is on: a group:9000 role may add members but may not make anyone (itself
// included) a group admin, which would outlive the role. An explicit false
// still turns it off.
func TestScenario_GrantCeilingDefaultsOnWithGroupTasks(t *testing.T) {
	for _, c := range []struct {
		name     string
		ceiling  *bool
		admitted bool
	}{
		{"unset", nil, false},
		{"explicit false", new(bool), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := communityRelayConfig(t)
			cfg.Groups.GrantCeiling = c.ceiling
			cfg.Membership = &MembershipConfig{Enabled: true, Roles: map[string]RoleTasks{"steward": {Can: []string{"group:9000"}}}}
			ts := bootScenario(t, cfg)
			owner, steward := pubkeyOf(t, plainPriv), pubkeyOf(t, hostPriv)
			enrollWithRoles(t, ts.URL, owner)
			enrollWithRoles(t, ts.URL, steward, "steward")

			creator := connectAs(t, ts, plainPriv)
			require.True(t, publishAs(t, creator, nip29.NewCreateGroup(owner, "stewarded"), plainPriv).Accepted)

			stewardConn := connectAs(t, ts, hostPriv)
			require.True(t, publishAs(t, stewardConn, nip29.NewPutUser(steward, "stewarded", pubkeyOf(t, outsiderPriv)), hostPriv).Accepted,
				"a plain seat is within group:9000")
			ok := publishAs(t, stewardConn, nip29.NewPutUser(steward, "stewarded", steward, "admin"), hostPriv)
			require.Equal(t, c.admitted, ok.Accepted, ok.Message)
		})
	}
}

func TestGrantCeilingDefault(t *testing.T) {
	yes, no := true, false
	withRoles := func(tasks ...string) *MembershipConfig {
		return &MembershipConfig{Enabled: true, Roles: map[string]RoleTasks{"r": {Can: tasks}}}
	}
	cases := []struct {
		name string
		cfg  RelayConfig
		want bool
	}{
		{"no membership", RelayConfig{}, false},
		{"roles without group tasks", RelayConfig{Membership: withRoles("createclaim", "*")}, false},
		{"a role with group:9000", RelayConfig{Membership: withRoles("group:9000")}, true},
		{"a role with group:*", RelayConfig{Membership: withRoles("group:*")}, true},
		{"members with a group kind", RelayConfig{Membership: &MembershipConfig{Enabled: true, Members: &RoleTasks{Can: []string{"group:9005"}}}}, true},
		{"explicit false wins", RelayConfig{Membership: withRoles("group:9000"), Groups: &GroupsConfig{GrantCeiling: &no}}, false},
		{"explicit true", RelayConfig{Groups: &GroupsConfig{GrantCeiling: &yes}}, true},
	}
	for _, c := range cases {
		require.Equal(t, c.want, c.cfg.grantCeiling(), c.name)
	}
}

// Role ids are canonical lowercase: assigning "HOST" stores "host", and
// unassigning "host" removes a "Host" written before ids were canonical.
func TestNip86_RoleIDsAreCaseInsensitive(t *testing.T) {
	h, _, ws := newRoleNip86HandlerWS(t, rolesConfig(), map[string][]string{
		hostPriv:  {"Host"},
		plainPriv: nil,
	})
	host, plain := pubkeyOf(t, hostPriv), pubkeyOf(t, plainPriv)
	roles := func(pk string) []string {
		rec, err := ws.Membership().Get(pk)
		require.NoError(t, err)
		require.NotNil(t, rec)
		return rec.Roles
	}

	got := callHandler(t, h, testPrivKey, nip86.MethodUnassignRole, `"`+host+`"`, `"host"`)
	require.Empty(t, got.resp.Error)
	require.Empty(t, roles(host), "unassign host removes Host")

	got = callHandler(t, h, testPrivKey, nip86.MethodAssignRole, `"`+plain+`"`, `"HOST"`)
	require.Empty(t, got.resp.Error)
	require.Equal(t, []string{"host"}, roles(plain))
	got = callHandler(t, h, testPrivKey, nip86.MethodAssignRole, `"`+plain+`"`, `"Host"`)
	require.Empty(t, got.resp.Error)
	require.Equal(t, []string{"host"}, roles(plain), "assigning it again in another case is no second role")
	require.Contains(t, supportedFor(t, h, plainPriv), "banpubkey", "the canonical id carries the host tasks")
}

// allowpubkey on a member already enrolled keeps its roles.
func TestNip86_AllowpubkeyKeepsRoles(t *testing.T) {
	h, _, ws := newRoleNip86HandlerWS(t, rolesConfig(), map[string][]string{hostPriv: {"host"}})
	host := pubkeyOf(t, hostPriv)
	got := callHandler(t, h, testPrivKey, nip86.MethodAllowPubkey, `"`+host+`"`)
	require.Empty(t, got.resp.Error)
	rec, err := ws.Membership().Get(host)
	require.NoError(t, err)
	require.Equal(t, []string{"host"}, rec.Roles)
}

// The config file is read as written: Host and host are one role written
// twice, and a dotted id cannot be a config key.
func TestCheckRoleKeysInFile(t *testing.T) {
	write := func(body string) string {
		path := filepath.Join(t.TempDir(), "relay.yaml")
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		return path
	}
	require.NoError(t, checkRoleKeysInFile(""))
	require.NoError(t, checkRoleKeysInFile(write("membership:\n  roles:\n    host: {can: [createclaim]}\n    greeter: {can: [createclaim]}\n")))

	var ce *common.CLIError
	err := checkRoleKeysInFile(write("membership:\n  roles:\n    Host: {can: [createclaim]}\n    host: {can: [banpubkey]}\n"))
	require.True(t, errors.As(err, &ce), "%v", err)
	require.Equal(t, common.CodeInvalidInput, ce.Code)
	require.Equal(t, "host", ce.Input)

	err = checkRoleKeysInFile(write("membership:\n  roles:\n    team.lead: {can: [createclaim]}\n"))
	require.True(t, errors.As(err, &ce), "%v", err)
	require.Equal(t, "team.lead", ce.Input)
}

// groups.create keywords match in any case, so Everyone is the keyword,
// not a role id, and Members is every member.
func TestGroupCreateKeywordsAnyCase(t *testing.T) {
	cfg := RelayConfig{Groups: &GroupsConfig{Create: []string{"Everyone"}}}
	require.False(t, cfg.groupCreateRestricted())

	cfg = RelayConfig{Nip11: nip11.Metadata{PubKey: testPubKey}, Membership: &MembershipConfig{Enabled: true}, Groups: &GroupsConfig{Create: []string{"Members"}}}
	require.NoError(t, cfg.checkRoleTasks())
	require.Equal(t, []string{"members"}, cfg.Groups.Create)
	auth, _ := newAuthority(t, cfg, map[string][]string{plainPriv: nil})
	require.True(t, auth.can(pubkeyOf(t, plainPriv), "group.create"))
	require.False(t, auth.can(pubkeyOf(t, outsiderPriv), "group.create"))
}
