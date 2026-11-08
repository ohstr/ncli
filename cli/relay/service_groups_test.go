package relay

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip29"
	"github.com/ohstr/nmilat/relay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupsRolesConfig(t *testing.T) {
	const pub = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"
	base := "nip11:\n  pubkey: \"" + pub + "\"\nstore: \"test.db\"\n"

	t.Run("omitted keeps the default", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, base)
		p, err := groupModerationPolicy(&cfg)
		require.NoError(t, err)
		assert.Nil(t, p)
	})

	t.Run("roles become the policy", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, base+`groups:
  roles:
    admin: [9000, 9001, 9002, 9005, 9008, 9009, 9010]
    moderator: [9005, 9010]
`)
		p, err := groupModerationPolicy(&cfg)
		require.NoError(t, err)
		assert.True(t, p.Allows("moderator", nip29.KindDeleteEvent))
		assert.False(t, p.Allows("moderator", nip29.KindRemoveUser))
		assert.True(t, p.Allows("admin", nip29.KindDeleteGroup))
	})

	t.Run("admin is required", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, base+"groups:\n  roles:\n    moderator: [9005]\n")
		_, err := groupModerationPolicy(&cfg)
		assert.ErrorContains(t, err, `must define "admin"`)
	})

	t.Run("only moderation kinds", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, base+"groups:\n  roles:\n    admin: [9000, 1]\n")
		_, err := groupModerationPolicy(&cfg)
		assert.ErrorContains(t, err, "groups.roles.admin: 1 is not a NIP-29 moderation kind")
	})
}

// The relay starts with groups.roles and groups.roleDescriptions in place:
// its startup republish of each group's kind:39001 and kind:39003 runs
// under the configured policy, so a group whose stored ones already follow
// it gets nothing new. Set after the handler is built, startup would first
// publish both under the default policy, dropping the moderator.
func TestRelayStartsWithGroupsPolicy(t *testing.T) {
	const moderator = scenarioMemberPub
	cfg := communityRelayConfig(t)
	cfg.Groups.Roles = map[string][]int{
		"admin": {nip29.KindPutUser, nip29.KindRemoveUser, nip29.KindEditMetadata, nip29.KindDeleteEvent,
			nip29.KindDeleteGroup, nip29.KindCreateInvite, nip29.KindUpdatePinList},
		"moderator": {nip29.KindDeleteEvent},
	}
	cfg.Groups.RoleDescriptions = map[string]string{"moderator": "keeps the peace"}
	prevConfig := config
	t.Cleanup(func() { config = prevConfig })
	config = cfg

	storeCfg := cfg
	storeCfg.RelayNotesDb = filepath.Join(t.TempDir(), "test.db")
	storeCfg.Nip11.Limitation = nip11.Limitation{MaxLimit: 1000}
	open := func() *relay.EventStore {
		store, err := openRelayStore(&storeCfg)
		require.NoError(t, err)
		return store
	}
	stored := func(store *relay.EventStore, kind int) *nip01.Event {
		t.Helper()
		evs, err := store.QueryEvents(context.Background(), &nip01.SubscriptionFilter{
			Kinds: []int{kind}, Tags: map[string][]string{"d": {"g1"}}, Limit: 5,
		})
		require.NoError(t, err)
		require.Len(t, evs, 1, "kind:%d", kind)
		return evs[0]
	}
	meta := &nip11.Metadata{PubKey: testPubKey, Self: testPubKey}

	// A group with a moderator, its 39001 and 39003 published under the
	// configured policy.
	store := open()
	h := relay.NewSessionHandler(store, meta, nil,
		append([]relay.SessionOption{relay.WithSessionPrivKey(testPrivKey)}, groupsSessionOptions(&cfg)...)...)
	require.NoError(t, h.Groups().Create(&relay.GroupRecord{
		ID:        "g1",
		Members:   []relay.GroupMember{{Pubkey: testPubKey, Roles: []string{"admin"}}, {Pubkey: moderator, Roles: []string{"moderator"}}},
		CreatedAt: time.Now().Unix(),
	}))
	policy, err := groupModerationPolicy(&cfg)
	require.NoError(t, err)
	h.Groups().SetModerationPolicy(policy)
	admins, roles := stored(store, nip29.KindGroupAdmins), stored(store, nip29.KindGroupRoles)
	require.Contains(t, fmt.Sprint(admins.Tags), moderator)
	require.Contains(t, fmt.Sprint(roles.Tags), "keeps the peace")
	store.Close()
	// A republish a second later would carry a new created_at, so a new id.
	time.Sleep(1100 * time.Millisecond)

	s := NewServer(open(), nil)
	assert.Equal(t, admins.ID, stored(s.store, nip29.KindGroupAdmins).ID, "startup republished kind:39001")
	assert.Equal(t, roles.ID, stored(s.store, nip29.KindGroupRoles).ID, "startup republished kind:39003")
	s.Stop()

	// The test can see the difference: started under the default policy,
	// the 39001 is republished without the moderator.
	store = open()
	defer store.Close()
	relay.NewSessionHandler(store, meta, nil, relay.WithSessionPrivKey(testPrivKey))
	after := stored(store, nip29.KindGroupAdmins)
	assert.NotEqual(t, admins.ID, after.ID)
	assert.NotContains(t, fmt.Sprint(after.Tags), moderator)
}
