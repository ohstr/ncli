package relay

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupsHostingConfig(t *testing.T) {
	const pub = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"
	base := "nip11:\n  pubkey: \"" + pub + "\"\nstore: \"test.db\"\n"

	t.Run("round-trips", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, base+`groups:
  roles:
    admin: [9000, 9001]
    moderator: [9005]
  roleDescriptions:
    moderator: keeps the peace
  admins: ["`+pub+`"]
  moderationLog: true
  pendingJoins: true
`)
		require.NoError(t, checkGroupsConfig(&cfg))
		assert.Equal(t, "keeps the peace", cfg.Groups.RoleDescriptions["moderator"])
		assert.True(t, cfg.Groups.ModerationLog)
		assert.True(t, cfg.Groups.PendingJoins)
		admins, err := groupAdmins(&cfg)
		require.NoError(t, err)
		assert.Equal(t, []string{pub}, admins)
	})

	t.Run("a bad admin key fails", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, base+"groups:\n  admins: [nope]\n")
		assert.ErrorContains(t, checkGroupsConfig(&cfg), "groups.admins")
	})

	t.Run("describing an unknown role fails", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, base+"groups:\n  roleDescriptions:\n    moderator: x\n")
		assert.ErrorContains(t, checkGroupsConfig(&cfg), "no such role")
		cfg = loadRelayConfigFromYAML(t, base+"groups:\n  roleDescriptions:\n    admin: runs it\n")
		assert.NoError(t, checkGroupsConfig(&cfg))
	})
}
