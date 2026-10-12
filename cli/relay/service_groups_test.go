package relay

import (
	"testing"

	"github.com/ohstr/nmilat/nip29"
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
