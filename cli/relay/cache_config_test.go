package relay

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/relay"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	cacheTestPriv = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	cacheTestPub  = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"
)

func requireCode(t *testing.T, err error, code common.ErrorCode, msg string) {
	t.Helper()
	var ce *common.CLIError
	require.True(t, errors.As(err, &ce), "%v is not a CLIError", err)
	assert.Equal(t, code, ce.Code)
	assert.Contains(t, err.Error(), msg)
}

func TestRenamedKeysFailNamingTheNewOne(t *testing.T) {
	for _, k := range renamedKeys {
		t.Run(k.old, func(t *testing.T) {
			path := strings.Split(k.old, ".")
			yaml := ""
			for i, p := range path {
				yaml += strings.Repeat("  ", i) + p + ":"
				if i < len(path)-1 {
					yaml += "\n"
				}
			}
			loadRelayConfigFromYAML(t, "store: x.db\n"+yaml+" true\n")
			err := checkRenamedKeys(viper.GetViper())
			requireCode(t, err, common.CodeUsage, k.old+" was renamed to "+k.new)
		})
	}

	t.Run("a whole old block", func(t *testing.T) {
		loadRelayConfigFromYAML(t, "store: x.db\ncache:\n  topZapped:\n    enabled: true\n    window: 7d\n")
		requireCode(t, checkRenamedKeys(viper.GetViper()), common.CodeUsage, "cache.topZapped was renamed to cache.zaps")
	})

	t.Run("from the environment", func(t *testing.T) {
		v := viper.New()
		v.SetEnvPrefix("NCLI")
		v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
		v.AutomaticEnv()
		t.Setenv("NCLI_CACHE_TOPZAPPED", "true")
		requireCode(t, checkRenamedKeys(v), common.CodeUsage, "cache.topZapped was renamed to cache.zaps")
	})

	t.Run("removed search queue keys say why", func(t *testing.T) {
		for key, why := range map[string]string{
			"queueSize":   "nothing is queued in memory",
			"max_ch_size": "nothing is queued in memory",
		} {
			loadRelayConfigFromYAML(t, "store: x.db\ncache:\n  profiles:\n    search: {enabled: true, "+key+": 5}\n")
			requireCode(t, checkRenamedKeys(viper.GetViper()), common.CodeUsage, "cache.profiles.search."+key+" was removed: ")
			requireCode(t, checkRenamedKeys(viper.GetViper()), common.CodeUsage, why)
		}
	})

	t.Run("search batching keys pass", func(t *testing.T) {
		loadRelayConfigFromYAML(t, "store: x.db\ncache:\n  profiles:\n    search: {enabled: true, batchSize: 5000, maxInFlight: 2, searchAbout: false}\n")
		require.NoError(t, checkRenamedKeys(viper.GetViper()))
	})

	t.Run("the new layout passes", func(t *testing.T) {
		loadRelayConfigFromYAML(t, `
store: x.db
cache:
  profiles:
    search: {enabled: true, meilisearch: {host: h, key: k, index: i}}
    stats: {enabled: true}
    checks: {ttl: 7d, workers: 3}
    scoring: {social: {followers_weight: 2}}
  zaps: {enabled: true, windows: [24h], coins: [sats]}
  relay: {stats: {enabled: true}}
`)
		assert.NoError(t, checkRenamedKeys(viper.GetViper()))
	})
}

func TestCacheConfigValidation(t *testing.T) {
	base := func(nip11, cache string) RelayConfig {
		return loadRelayConfigFromYAML(t, "store: x.db\nnip11:\n"+nip11+"\ncache:\n"+cache)
	}
	keyed := "  privkey: " + cacheTestPriv + "\n  url: wss://relay.example.com"

	for _, c := range []struct {
		name, nip11, cache string
		code               common.ErrorCode
		msg                string
	}{
		{"zaps need a key", "  pubkey: " + cacheTestPub, "  zaps: {enabled: true}", common.CodeUsage, "cache.zaps.enabled requires nip11.privkey"},
		{"stats need a key", "  pubkey: " + cacheTestPub, "  profiles: {stats: {enabled: true}}", common.CodeUsage, "cache.profiles.stats.enabled requires nip11.privkey"},
		{"relay stats need a key", "  pubkey: " + cacheTestPub, "  relay: {stats: {enabled: true}}", common.CodeUsage, "cache.relay.stats.enabled requires nip11.privkey"},
		{"relay stats need a url", "  privkey: " + cacheTestPriv, "  relay: {stats: {enabled: true}}", common.CodeUsage, "cache.relay.stats.enabled requires nip11.url"},
		{"relay stats need a ws url", "  privkey: " + cacheTestPriv + "\n  url: https://relay.example.com", "  relay: {stats: {enabled: true}}", common.CodeInvalidInput, "nip11.url"},
		{"bad window", keyed, "  zaps: {enabled: true, windows: [24h, 3x]}", common.CodeInvalidInput, "cache.zaps.windows"},
		{"duplicate window", keyed, "  zaps: {windows: [7d, 1w]}", common.CodeInvalidInput, "duplicate window"},
		{"empty windows", keyed, "  zaps: {windows: []}", common.CodeInvalidInput, "no windows"},
		{"unknown coin", keyed, "  zaps: {coins: [sats, btc]}", common.CodeInvalidInput, "cache.zaps.coins"},
		{"limit over maxLimit", keyed, "  zaps: {limit: 300, maxLimit: 100}", common.CodeInvalidInput, "cache.zaps.limit: 300 exceeds maxLimit 100"},
		{"limit over the default maxLimit", keyed, "  zaps: {limit: 201}", common.CodeInvalidInput, "exceeds maxLimit 200"},
		{"negative limit", keyed, "  zaps: {limit: -1}", common.CodeInvalidInput, ">= 0"},
		{"bad refresh", keyed, "  zaps: {refresh: soon}", common.CodeInvalidInput, "cache.zaps.refresh"},
		{"zero refresh", keyed, "  zaps: {refresh: 0s}", common.CodeInvalidInput, "must be positive"},
		{"bad interval", keyed, "  relay: {stats: {enabled: true, interval: x}}", common.CodeInvalidInput, "cache.relay.stats.interval"},
		{"bad check ttl", keyed, "  profiles: {checks: {ttl: soon}}", common.CodeInvalidInput, "cache.profiles.checks.ttl"},
		{"bad scoring", keyed, "  profiles: {scoring: {engine: {x: 1}}}", common.CodeInvalidInput, "cache.profiles.scoring"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := base(c.nip11, c.cache)
			requireCode(t, checkCacheConfig(&cfg), c.code, c.msg)
		})
	}

	t.Run("a full valid block", func(t *testing.T) {
		cfg := base(keyed, `
  profiles:
    stats: {enabled: true, maxTargets: 10}
    checks: {ttl: 14d, perHostInterval: 250ms}
  zaps: {enabled: true, windows: [24h, 7d, 30d, all], coins: [loki, sats], limit: 20, maxLimit: 100, refresh: 2m}
  relay: {stats: {enabled: true, interval: 30s}}
`)
		require.NoError(t, checkCacheConfig(&cfg))
		z, err := zapRanking(&cfg)
		require.NoError(t, err)
		assert.True(t, z.Enabled)
		assert.Equal(t, []string{"24h", "7d", "30d", "all"}, labels(z.Windows))
		assert.Equal(t, []relay.Coin{relay.CoinLoki, relay.CoinSats}, z.Coins)
		assert.Equal(t, 20, z.Limit)
		assert.Equal(t, 100, z.MaxLimit)
		assert.Equal(t, 2*time.Minute, z.Refresh)
		d, err := relayStatsInterval(&cfg)
		require.NoError(t, err)
		assert.Equal(t, 30*time.Second, d)
	})

	t.Run("unset values leave nmilat's defaults", func(t *testing.T) {
		cfg := base(keyed, "  zaps: {enabled: true}")
		z, err := zapRanking(&cfg)
		require.NoError(t, err)
		assert.Equal(t, relay.ZapRankingConfig{Enabled: true}, z)
		d, err := relayStatsInterval(&cfg)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, d)
	})

	t.Run("no cache block at all", func(t *testing.T) {
		cfg := RelayConfig{}
		require.NoError(t, checkCacheConfig(&cfg))
		assert.False(t, cfg.zapsEnabled() || cfg.searchEnabled() || cfg.profileStatsEnabled() || cfg.relayStatsEnabled())
	})
}

func labels(ws []relay.ZapWindow) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Label
	}
	return out
}

func TestCheckWorkersComeFromTheChecksBlock(t *testing.T) {
	cfg := loadRelayConfigFromYAML(t, "store: x.db\ncache:\n  profiles:\n    checks: {workers: 7}\n")
	require.NotNil(t, cfg.profiles().Checks)
	assert.Equal(t, 7, cfg.profiles().Checks.Workers)
}
