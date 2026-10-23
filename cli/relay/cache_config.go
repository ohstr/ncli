package relay

import (
	"errors"
	"fmt"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/utils"
	"github.com/spf13/viper"
)

// CacheConfig is the relay's optional serving layer: profiles (search,
// NIP-85 stats and the checks and scores both use), zaps (the zap ranking)
// and relay (the relay's own published stats).
type CacheConfig struct {
	Profiles *ProfilesConfig   `mapstructure:"profiles"`
	Zaps     *ZapsConfig       `mapstructure:"zaps"`
	Relay    *RelayCacheConfig `mapstructure:"relay"`
}

// ProfilesConfig groups what's built from kind 0 profiles. Checks and
// scoring run when search or stats is on.
type ProfilesConfig struct {
	Search *SearchConfig       `mapstructure:"search"`
	Stats  *ProfileStatsConfig `mapstructure:"stats"`
	Checks *VerificationConfig `mapstructure:"checks"`
	// Scoring overrides the built-in profile score weights, by section:
	// scoring (completeness and penalties), verification (NIP-05/LUD-16
	// points) and social (followers and zaps); keys as in nmilat's
	// config.yaml, e.g. {social: {followers_weight: 25}}.
	Scoring map[string]any `mapstructure:"scoring"`
}

// SearchConfig is Meilisearch-backed profile search (NIP-50).
type SearchConfig struct {
	Enabled     bool              `mapstructure:"enabled"`
	Readonly    bool              `mapstructure:"readonly"`
	Meilisearch MeilisearchConfig `mapstructure:"meilisearch"`
}

type MeilisearchConfig struct {
	Host  string `mapstructure:"host"`
	Key   string `mapstructure:"key"`
	Index string `mapstructure:"index"`
}

// ProfileStatsConfig configures NIP-85 user assertions.
type ProfileStatsConfig struct {
	// Enabled requires nip11.privkey, which signs the assertions.
	Enabled bool `mapstructure:"enabled"`
	// MaxTargets caps the #d pubkeys per request (default 500).
	MaxTargets int `mapstructure:"maxTargets"`
}

// VerificationConfig tunes profile checks. A check is fetched only for a
// new or changed identifier, when its result expires (TTL), on a retry,
// or by `ncli relay verify --refresh`; never by an index rebuild.
type VerificationConfig struct {
	// TTL keeps a result before it's checked again (default 14d); "0"
	// never re-checks an unchanged identifier.
	TTL string `mapstructure:"ttl"`
	// Retry backs off after a timeout, block or 5xx, keeping the previous
	// result meanwhile.
	Retry *VerificationRetryConfig `mapstructure:"retry"`
	// PerHostInterval spaces requests to one host (default 250ms).
	PerHostInterval string `mapstructure:"perHostInterval"`
	// Workers is how many checks run at once (default 50).
	Workers int `mapstructure:"workers"`
}

type VerificationRetryConfig struct {
	// Initial wait (default 1h), doubling up to Max (default 24h); a
	// longer Retry-After from the server wins.
	Initial string `mapstructure:"initial"`
	Max     string `mapstructure:"max"`
	// MaxAttempts failures in a row end the retries until the next TTL
	// (default 5).
	MaxAttempts int `mapstructure:"maxAttempts"`
}

// ZapsConfig is the signed zap-ranking cache action.
type ZapsConfig struct {
	// Enabled requires nip11.privkey, which signs the ranking.
	Enabled bool `mapstructure:"enabled"`
	// Windows a client may ask for; the first is the default (24h).
	Windows []string `mapstructure:"windows"`
	// Coins a client may ask for; the first is the default (sats, loki).
	Coins    []string `mapstructure:"coins"`
	Limit    int      `mapstructure:"limit"`    // default list length (50)
	MaxLimit int      `mapstructure:"maxLimit"` // cap on a request (200)
	Refresh  string   `mapstructure:"refresh"`  // how long a list is reused (1m)
}

type RelayCacheConfig struct {
	Stats *RelayStatsConfig `mapstructure:"stats"`
}

// RelayStatsConfig publishes the relay's NIP-66 kind 30166 about itself.
type RelayStatsConfig struct {
	// Enabled requires nip11.privkey and nip11.url.
	Enabled bool `mapstructure:"enabled"`
	// Interval between checks for changed numbers (default 1m).
	Interval string `mapstructure:"interval"`
}

func (c *RelayConfig) profiles() *ProfilesConfig {
	if c.Cache == nil || c.Cache.Profiles == nil {
		return &ProfilesConfig{}
	}
	return c.Cache.Profiles
}

// searchConfig is cache.profiles.search, nil when absent.
func (c *RelayConfig) searchConfig() *SearchConfig { return c.profiles().Search }

func (c *RelayConfig) searchEnabled() bool {
	s := c.searchConfig()
	return s != nil && s.Enabled
}

// searchWritable reports search on and not read-only: only then are
// profile scores sent to it.
func (c *RelayConfig) searchWritable() bool {
	s := c.searchConfig()
	return s != nil && s.Enabled && !s.Readonly
}

func (c *RelayConfig) profileStatsEnabled() bool {
	s := c.profiles().Stats
	return s != nil && s.Enabled
}

// statsInUse reports whether the stats (social) index is read: by search
// scores or by NIP-85 stats.
func (c *RelayConfig) statsInUse() bool {
	return c.searchWritable() || c.profileStatsEnabled()
}

// derivedIndexes are the store indexes the cache features read, so the
// store keeps no other.
func (c *RelayConfig) derivedIndexes() relay.DerivedIndexes {
	return relay.DerivedIndexes{Zaps: c.zapsEnabled(), Social: c.statsInUse()}
}

// zapsEnabled reports cache.zaps.enabled.
func (c *RelayConfig) zapsEnabled() bool {
	return c.Cache != nil && c.Cache.Zaps != nil && c.Cache.Zaps.Enabled
}

func (c *RelayConfig) relayStatsEnabled() bool {
	return c.Cache != nil && c.Cache.Relay != nil && c.Cache.Relay.Stats != nil && c.Cache.Relay.Stats.Enabled
}

// renamedKeys are config keys from before the cache block was grouped;
// each now fails startup naming its replacement.
var renamedKeys = []struct{ old, new string }{
	{"cache.topZapped", "cache.zaps"},
	{"cache.search", "cache.profiles.search"},
	{"cache.profileStats", "cache.profiles.stats"},
	{"cache.verification", "cache.profiles.checks"},
	{"cache.scoring", "cache.profiles.scoring"},
	{"verificationWorkers", "cache.profiles.checks.workers"},
	{"cache.profiles.search.host", "cache.profiles.search.meilisearch.host"},
	{"cache.profiles.search.key", "cache.profiles.search.meilisearch.key"},
	{"cache.profiles.search.index_name", "cache.profiles.search.meilisearch.index"},
	{"cache.zaps.window", "cache.zaps.windows"},
}

// removedKeys are config keys with nothing left to set; each fails startup
// saying why.
var removedKeys = []struct{ key, why string }{
	{"cache.profiles.search.batchSize", "search takes profiles from the store in batches of its own"},
	{"cache.profiles.search.queueSize", "profiles reach search from the store; nothing is queued in memory"},
	{"cache.profiles.search.batch_size", "search takes profiles from the store in batches of its own"},
	{"cache.profiles.search.max_ch_size", "profiles reach search from the store; nothing is queued in memory"},
}

// checkRenamedKeys refuses a config (file or NCLI_* env) still using a
// key from renamedKeys.
func checkRenamedKeys(v *viper.Viper) error {
	for _, k := range renamedKeys {
		if v.IsSet(k.old) {
			return &common.CLIError{Err: fmt.Errorf("%s was renamed to %s", k.old, k.new), Code: common.CodeUsage, Input: k.old}
		}
	}
	for _, k := range removedKeys {
		if v.IsSet(k.key) {
			return &common.CLIError{Err: fmt.Errorf("%s was removed: %s", k.key, k.why), Code: common.CodeUsage, Input: k.key}
		}
	}
	return nil
}

// parseConfigDuration reads a config duration: Go's units (ms, s, m, h),
// else d, w and mo.
func parseConfigDuration(key, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil {
		d, err = utils.ParseDuration(raw)
	}
	if err == nil && d <= 0 {
		err = errors.New("must be positive")
	}
	if err != nil {
		return 0, &common.CLIError{Err: fmt.Errorf("%s: %w", key, err), Code: common.CodeInvalidInput, Input: raw}
	}
	return d, nil
}

// zapRanking turns cache.zaps into nmilat's config; it's also the
// validation (invalid_input) for initConfig.
func zapRanking(cfg *RelayConfig) (relay.ZapRankingConfig, error) {
	var out relay.ZapRankingConfig
	if cfg.Cache == nil || cfg.Cache.Zaps == nil {
		return out, nil
	}
	z := cfg.Cache.Zaps
	out.Enabled = z.Enabled
	invalid := func(key string, err error) error {
		return &common.CLIError{Err: fmt.Errorf("%s: %w", key, err), Code: common.CodeInvalidInput}
	}
	if z.Windows != nil {
		ws, err := relay.ParseZapWindows(z.Windows)
		if err != nil {
			return out, invalid("cache.zaps.windows", err)
		}
		out.Windows = ws
	}
	if z.Coins != nil {
		cs, err := relay.ParseCoins(z.Coins)
		if err != nil {
			return out, invalid("cache.zaps.coins", err)
		}
		out.Coins = cs
	}
	if z.Limit < 0 || z.MaxLimit < 0 {
		return out, invalid("cache.zaps", errors.New("limit and maxLimit must be >= 0"))
	}
	maxLimit := z.MaxLimit
	if maxLimit == 0 {
		maxLimit = relay.DefaultZapMaxLimit
	}
	if z.Limit > maxLimit {
		return out, invalid("cache.zaps.limit", fmt.Errorf("%d exceeds maxLimit %d", z.Limit, maxLimit))
	}
	out.Limit, out.MaxLimit = z.Limit, z.MaxLimit
	if z.Refresh != "" {
		d, err := parseConfigDuration("cache.zaps.refresh", z.Refresh)
		if err != nil {
			return out, err
		}
		out.Refresh = d
	}
	return out, nil
}

// defaultRelayStatsInterval is cache.relay.stats.interval when unset.
const defaultRelayStatsInterval = time.Minute

func relayStatsInterval(cfg *RelayConfig) (time.Duration, error) {
	if cfg.Cache == nil || cfg.Cache.Relay == nil || cfg.Cache.Relay.Stats == nil || cfg.Cache.Relay.Stats.Interval == "" {
		return defaultRelayStatsInterval, nil
	}
	return parseConfigDuration("cache.relay.stats.interval", cfg.Cache.Relay.Stats.Interval)
}

// checkCacheConfig validates the cache block: features that sign need
// nip11.privkey, the published stats need nip11.url, and every value parses.
func checkCacheConfig(cfg *RelayConfig) error {
	for _, f := range []struct {
		key string
		on  bool
	}{
		{"cache.zaps.enabled", cfg.zapsEnabled()},
		{"cache.profiles.stats.enabled", cfg.profileStatsEnabled()},
		{"cache.relay.stats.enabled", cfg.relayStatsEnabled()},
	} {
		if f.on && cfg.Nip11.PrivKey == "" {
			return &common.CLIError{Err: fmt.Errorf("%s requires nip11.privkey", f.key), Code: common.CodeUsage}
		}
	}
	if cfg.relayStatsEnabled() && cfg.Nip11.URL == "" {
		return &common.CLIError{Err: errors.New("cache.relay.stats.enabled requires nip11.url"), Code: common.CodeUsage}
	}
	if cfg.relayStatsEnabled() {
		if _, err := relay.NormalizeRelayURL(cfg.Nip11.URL); err != nil {
			return &common.CLIError{Err: fmt.Errorf("nip11.url: %w", err), Code: common.CodeInvalidInput, Input: cfg.Nip11.URL}
		}
	}
	if _, err := zapRanking(cfg); err != nil {
		return err
	}
	if _, err := relayStatsInterval(cfg); err != nil {
		return err
	}
	return checkVerificationConfig(cfg)
}
