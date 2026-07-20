package relay

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip11"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadRelayConfig(t *testing.T) {
	tests := []struct {
		name         string
		yamlContent  string
		expectedErr  bool
		expectedPub  string
		expectedPriv string
		checkDerive  bool
	}{
		{
			name: "Valid config with both keys",
			yamlContent: `
nip11:
  name: "Test Relay"
  pubkey: "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"
  privkey: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
store: "test.db"
`,
			expectedErr:  false,
			expectedPub:  "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d",
			expectedPriv: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		},
		{
			name: "Valid config with auto-derivation",
			yamlContent: `
nip11:
  name: "Auto Derive"
  privkey: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
store: "test.db"
`,
			expectedErr:  false,
			expectedPub:  "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d",
			expectedPriv: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			checkDerive:  true,
		},
		{
			name: "Invalid config - mismatched pubkey",
			yamlContent: `
nip11:
  pubkey: "821467621f228603a1466465a3f00fb8948698f72f161541129c595e3b9aaddc"
  privkey: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
`,
			expectedErr: true,
		},
		{
			name: "User reported scenario",
			yamlContent: `
nip11:
  name: test
  description: test
  pubkey: 821467621f228603a1466465a3f00fb8948698f72f161541129c595e3b9aaddc
`,
			expectedErr: false,
			expectedPub: "821467621f228603a1466465a3f00fb8948698f72f161541129c595e3b9aaddc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset viper and global config for each test
			viper.Reset()
			config = RelayConfig{}

			// Create temporary directory to simulate search paths
			tmpDir := t.TempDir()
			// Change CWD to tmpDir to test local file discovery if no cfgFile passed
			originalWd, _ := os.Getwd()
			defer os.Chdir(originalWd)
			os.Chdir(tmpDir)

			tmpFile := filepath.Join(tmpDir, "relay.yaml")
			err := os.WriteFile(tmpFile, []byte(tt.yamlContent), 0644)
			require.NoError(t, err)

			// In some cases we pass cfgFile, in others we let it discover
			targetCfg := ""
			if tt.name != "Valid config with auto-derivation" { // Simulate discovery for auto-derive case
				targetCfg = tmpFile
			}

			// Call the actual loading logic from common
			err = common.LoadViperConfig(targetCfg)
			if err != nil && !tt.expectedErr {
				t.Fatalf("LoadViperConfig failed: %v", err)
			}

			err = viper.Unmarshal(&config)
			require.NoError(t, err)

			// Manually run the logic from initConfig but without log.Fatal
			if config.Nip11.PrivKey != "" {
				// Derive actual pubkey for check
				derived, _ := nip11.DerivePubKey(config.Nip11.PrivKey)

				if tt.name == "Invalid config - mismatched pubkey" {
					assert.NotEqual(t, derived, config.Nip11.PubKey)
					return // Success: we detected the mismatch
				}

				// Apply derivation if needed
				if config.Nip11.PubKey == "" {
					config.Nip11.PubKey = derived
				}
			}

			if tt.expectedErr {
				if config.Nip11.PubKey == "" {
					return
				}
				// If it didn't fail unmarshaling, check if keys are empty as expected
				assert.Empty(t, config.Nip11.PubKey)
			} else {
				assert.Equal(t, tt.expectedPub, config.Nip11.PubKey)
				assert.Equal(t, tt.expectedPriv, config.Nip11.PrivKey)
			}
		})
	}
}

// loadRelayConfigFromYAML writes yamlContent to a temp relay.yaml, loads it
// via the real common.LoadViperConfig + viper.Unmarshal path (same as
// initConfig), and returns the resulting RelayConfig. initConfig() itself
// isn't called directly: its defaulting logic is exercised in isolated
// pieces below (applyPortDefault, applyNip11Reset, checkTopZappedRequiresPrivKey)
// so each subtest can check one concern without also having to satisfy
// initConfig()'s other requirements (e.g. a valid pubkey) just to reach it.
func loadRelayConfigFromYAML(t *testing.T, yamlContent string) RelayConfig {
	t.Helper()

	viper.Reset()
	var cfg RelayConfig

	tmpDir := t.TempDir()
	originalWd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { os.Chdir(originalWd) })
	require.NoError(t, os.Chdir(tmpDir))

	tmpFile := filepath.Join(tmpDir, "relay.yaml")
	require.NoError(t, os.WriteFile(tmpFile, []byte(yamlContent), 0644))

	require.NoError(t, common.LoadViperConfig(tmpFile))
	require.NoError(t, viper.Unmarshal(&cfg))

	return cfg
}

// applyPortDefault mirrors the `config.Port == 0` guard in initConfig
// (command.go:124-126).
func applyPortDefault(cfg *RelayConfig) {
	if cfg.Port == 0 {
		cfg.Port = defaultPort
	}
}

// applyNip11Reset mirrors the Nip11 field-overwrite block in initConfig
// (command.go): Name/Description default to hardcoded strings only if
// empty; PubKey/Contact/PrivKey/Delegation survive as-is. Software/Version
// are always derived from build info (not config input) - a relay must not
// be able to self-report a different software identity/version than what
// it's actually running. Limitation starts from whatever the user
// configured and only fills in fields left at their zero value with the
// hardcoded defaults below.
func applyNip11Reset(cfg *RelayConfig) {
	priv := cfg.Nip11.PrivKey
	deleg := cfg.Nip11.Delegation
	url := cfg.Nip11.URL
	build := common.ReadBuildInfo()

	if cfg.Nip11.Name == "" {
		cfg.Nip11.Name = "ncli Relay"
	}
	if cfg.Nip11.Description == "" {
		cfg.Nip11.Description = "ncli relay"
	}

	limitation := cfg.Nip11.Limitation
	if limitation.MaxLimit == 0 {
		limitation.MaxLimit = defaultMaxLimit
	}
	if limitation.MaxMessageLength == 0 {
		limitation.MaxMessageLength = defaultMaxMessageLength
	}
	if limitation.MaxSubscriptions == 0 {
		limitation.MaxSubscriptions = defaultMaxSubscriptions
	}
	if limitation.MaxIndexableTags == 0 {
		limitation.MaxIndexableTags = defaultMaxIndexableTags
	}
	if cfg.Pow != nil {
		limitation.MinPowDifficulty = cfg.Pow.Min
		limitation.StrictPow = cfg.Pow.Strict
	}

	cfg.Nip11 = nip11.Metadata{
		Name:        cfg.Nip11.Name,
		PubKey:      cfg.Nip11.PubKey,
		Contact:     cfg.Nip11.Contact,
		Description: cfg.Nip11.Description,
		Software:    build.Software,
		Version:     build.Version,
		Limitation:  limitation,
		PrivKey:     priv,
		Delegation:  deleg,
		URL:         url,
	}
}

// topZapped safely reads cfg.Cache.TopZapped.Enabled, which is nil/false
// whenever the config file omits the `cache:`/`cache.topZapped:` block
// entirely.
func topZapped(cfg *RelayConfig) bool {
	return cfg.Cache != nil && cfg.Cache.TopZapped != nil && cfg.Cache.TopZapped.Enabled
}

// checkTopZappedRequiresPrivKey mirrors the `config.Cache != nil &&
// config.Cache.TopZapped != nil && config.Cache.TopZapped.Enabled &&
// config.Nip11.PrivKey == ""` guard in initConfig (command.go).
func checkTopZappedRequiresPrivKey(cfg *RelayConfig) error {
	if topZapped(cfg) && cfg.Nip11.PrivKey == "" {
		return errors.New("cache.topZapped.enabled requires nip11.privkey")
	}
	return nil
}

// resolveCacheWindow mirrors the cache.topZapped.window defaulting/parsing
// logic in NewServer (service.go): empty or unparseable falls back to
// defaultCacheWindow rather than propagating the zero value, since
// WithSessionConfig replaces the SDK's own 24h default wholesale.
func resolveCacheWindow(cfg *RelayConfig) time.Duration {
	if cfg.Cache == nil || cfg.Cache.TopZapped == nil || cfg.Cache.TopZapped.Window == "" {
		return defaultCacheWindow
	}
	if d, err := client.ParseDuration(cfg.Cache.TopZapped.Window); err == nil {
		return d
	}
	return defaultCacheWindow
}

// checkPowMinNonNegative mirrors the `config.Pow != nil && config.Pow.Min <
// 0` guard in initConfig (command.go).
func checkPowMinNonNegative(cfg *RelayConfig) error {
	if cfg.Pow != nil && cfg.Pow.Min < 0 {
		return errors.New("pow.min must be >= 0")
	}
	return nil
}

// checkAuthRequiresURL mirrors the `config.Nip11.Limitation.AuthRequired &&
// config.Nip11.URL == ""` guard in initConfig (command.go).
func checkAuthRequiresURL(cfg *RelayConfig) error {
	if cfg.Nip11.Limitation.AuthRequired && cfg.Nip11.URL == "" {
		return errors.New("nip11.limitation.auth_required requires nip11.url")
	}
	return nil
}

func TestPowConfig(t *testing.T) {
	const validPubkey = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"

	t.Run("omitted: defaults to no requirement", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
`)
		assert.Nil(t, cfg.Pow)
		assert.NoError(t, checkPowMinNonNegative(&cfg))

		applyNip11Reset(&cfg)
		assert.Equal(t, 0, cfg.Nip11.Limitation.MinPowDifficulty)
		assert.False(t, cfg.Nip11.Limitation.StrictPow)
	})

	t.Run("min round-trips into Limitation.MinPowDifficulty regardless of strict", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
pow:
  strict: false
  min: 20
`)
		require.NotNil(t, cfg.Pow)
		assert.Equal(t, 20, cfg.Pow.Min)
		assert.False(t, cfg.Pow.Strict)

		applyNip11Reset(&cfg)
		assert.Equal(t, 20, cfg.Nip11.Limitation.MinPowDifficulty)
		assert.False(t, cfg.Nip11.Limitation.StrictPow, "advisory mode: min is advertised but not enforced")
	})

	t.Run("strict: true is copied through alongside min", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
pow:
  strict: true
  min: 20
`)
		applyNip11Reset(&cfg)
		assert.Equal(t, 20, cfg.Nip11.Limitation.MinPowDifficulty)
		assert.True(t, cfg.Nip11.Limitation.StrictPow)
	})

	t.Run("nip11.limitation.min_pow_difficulty in YAML is ignored -- pow.min is the only source", func(t *testing.T) {
		// Limitation.MinPowDifficulty is mapstructure:"-", so this key
		// under nip11.limitation can't set it even though the same key
		// name is what NIP-11 advertises on the wire.
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  limitation:
    min_pow_difficulty: 99
store: "test.db"
pow:
  min: 5
`)
		applyNip11Reset(&cfg)
		assert.Equal(t, 5, cfg.Nip11.Limitation.MinPowDifficulty)
	})

	t.Run("negative min is rejected", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
pow:
  min: -1
`)
		require.NotNil(t, cfg.Pow)
		assert.Equal(t, -1, cfg.Pow.Min)
		assert.Error(t, checkPowMinNonNegative(&cfg))
	})

	t.Run("zero min is valid", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
pow:
  min: 0
`)
		assert.NoError(t, checkPowMinNonNegative(&cfg))
	})
}

func TestTopZappedRequiresPrivKey(t *testing.T) {
	const validPubkey = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"
	const validPrivkey = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

	t.Run("omitted: defaults to false, no privkey needed", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
`)
		assert.False(t, topZapped(&cfg))
		assert.NoError(t, checkTopZappedRequiresPrivKey(&cfg))
	})

	t.Run("enabled without privkey fails", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
cache:
  topZapped:
    enabled: true
`)
		assert.True(t, topZapped(&cfg))
		assert.Error(t, checkTopZappedRequiresPrivKey(&cfg))
	})

	t.Run("enabled with privkey passes", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  privkey: "`+validPrivkey+`"
store: "test.db"
cache:
  topZapped:
    enabled: true
`)
		assert.True(t, topZapped(&cfg))
		assert.NoError(t, checkTopZappedRequiresPrivKey(&cfg))
	})

	t.Run("explicit false with no privkey passes", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
cache:
  topZapped:
    enabled: false
`)
		assert.False(t, topZapped(&cfg))
		assert.NoError(t, checkTopZappedRequiresPrivKey(&cfg))
	})
}

func TestCacheWindow(t *testing.T) {
	const validPubkey = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"

	t.Run("omitted cache block: defaults to 24h", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
`)
		assert.Nil(t, cfg.Cache)
		assert.Equal(t, defaultCacheWindow, resolveCacheWindow(&cfg))
	})

	t.Run("empty window: defaults to 24h", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
cache:
  topZapped:
    enabled: true
`)
		require.NotNil(t, cfg.Cache)
		require.NotNil(t, cfg.Cache.TopZapped)
		assert.Equal(t, "", cfg.Cache.TopZapped.Window)
		assert.Equal(t, defaultCacheWindow, resolveCacheWindow(&cfg))
	})

	t.Run("valid units parse to the expected duration", func(t *testing.T) {
		tests := []struct {
			window string
			want   time.Duration
		}{
			{"24h", 24 * time.Hour},
			{"7d", 7 * client.HoursPerDay * time.Hour},
			{"2w", 2 * client.HoursPerWeek * time.Hour},
			{"1mo", client.HoursPerMonth * time.Hour},
			{"90m", 90 * time.Minute},
		}
		for _, tt := range tests {
			t.Run(tt.window, func(t *testing.T) {
				cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
cache:
  topZapped:
    window: "`+tt.window+`"
`)
				assert.Equal(t, tt.want, resolveCacheWindow(&cfg))
			})
		}
	})

	t.Run("unparseable window falls back to 24h rather than failing config load", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
cache:
  topZapped:
    window: "not-a-duration"
`)
		require.NotNil(t, cfg.Cache.TopZapped)
		assert.Equal(t, "not-a-duration", cfg.Cache.TopZapped.Window)
		assert.Equal(t, defaultCacheWindow, resolveCacheWindow(&cfg))
	})
}

func TestAuthRequiresURL(t *testing.T) {
	const validPubkey = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"

	t.Run("auth_required omitted: defaults to false, no url needed", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
`)
		assert.NoError(t, checkAuthRequiresURL(&cfg))
	})

	t.Run("auth_required true without url fails", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  limitation:
    auth_required: true
store: "test.db"
`)
		assert.Error(t, checkAuthRequiresURL(&cfg))
	})

	t.Run("auth_required true with url passes", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  url: "wss://relay.example.com"
  limitation:
    auth_required: true
store: "test.db"
`)
		assert.NoError(t, checkAuthRequiresURL(&cfg))
	})

	t.Run("url set without auth_required passes", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  url: "wss://relay.example.com"
store: "test.db"
`)
		assert.NoError(t, checkAuthRequiresURL(&cfg))
	})
}

func TestRelayConfigDefaultsAndOverwrite(t *testing.T) {

	const validPubkey = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"

	t.Run("port omitted defaults to 5500", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
`)
		applyPortDefault(&cfg)
		assert.Equal(t, defaultPort, cfg.Port)
	})

	t.Run("port explicit is not overridden", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
port: 8080
`)
		applyPortDefault(&cfg)
		assert.Equal(t, 8080, cfg.Port)
	})

	t.Run("logs block round-trips", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
logs:
  filename: "./logs/nrelay.log"
  maxSize: 50
  maxBackups: 2
  maxAge: 14
  compress: false
`)
		require.NotNil(t, cfg.Logs)
		assert.Equal(t, "./logs/nrelay.log", cfg.Logs.Filename)
		assert.Equal(t, 50, cfg.Logs.MaxSize)
		assert.Equal(t, 2, cfg.Logs.MaxBackups)
		assert.Equal(t, 14, cfg.Logs.MaxAge)
		assert.False(t, cfg.Logs.Compress)
	})

	t.Run("search block round-trips under cache", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
cache:
  search:
    enabled: true
    readonly: true
    host: "http://localhost:7700"
    key: "masterKey"
    index_name: "ncli_events"
    batch_size: 100
    max_ch_size: 1000
`)
		require.NotNil(t, cfg.Cache)
		require.NotNil(t, cfg.Cache.Search)
		assert.True(t, cfg.Cache.Search.Enabled)
		assert.True(t, cfg.Cache.Search.Readonly)
		assert.Equal(t, "http://localhost:7700", cfg.Cache.Search.Host)
		assert.Equal(t, "masterKey", cfg.Cache.Search.Key)
		assert.Equal(t, "ncli_events", cfg.Cache.Search.IndexName)
		assert.Equal(t, 100, cfg.Cache.Search.BatchSize)
		assert.Equal(t, 1000, cfg.Cache.Search.MaxChSize)
	})

	t.Run("duration fields round-trip as raw strings", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
handshakeTimeout: "5s"
pingInterval: "30s"
pongTimeout: "60s"
writeTimeout: "60s"
`)
		assert.Equal(t, "5s", cfg.HandshakeTimeout)
		assert.Equal(t, "30s", cfg.PingInterval)
		assert.Equal(t, "60s", cfg.PongTimeout)
		assert.Equal(t, "60s", cfg.WriteTimeout)
	})

	t.Run("description is configurable, software/version are always build-derived", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  description: "my custom relay"
  software: "my-software"
  version: "9.9.9"
store: "test.db"
`)
		applyNip11Reset(&cfg)

		build := common.ReadBuildInfo()
		assert.Equal(t, "my custom relay", cfg.Nip11.Description)
		assert.Equal(t, build.Software, cfg.Nip11.Software)
		assert.Equal(t, build.Version, cfg.Nip11.Version)
	})

	t.Run("description omitted falls back to the hardcoded default", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
`)
		applyNip11Reset(&cfg)
		assert.Equal(t, "ncli relay", cfg.Nip11.Description)
	})

	t.Run("explicit limitation values are preserved, not overwritten", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  limitation:
    max_limit: 1
    max_message_length: 2
    max_subscriptions: 3
    max_indexable_tags: 4
store: "test.db"
`)
		applyNip11Reset(&cfg)

		assert.Equal(t, 1, cfg.Nip11.Limitation.MaxLimit)
		assert.EqualValues(t, 2, cfg.Nip11.Limitation.MaxMessageLength)
		assert.Equal(t, 3, cfg.Nip11.Limitation.MaxSubscriptions)
		assert.Equal(t, 4, cfg.Nip11.Limitation.MaxIndexableTags)
	})

	t.Run("omitted limitation values fall back to hardcoded defaults", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
`)
		applyNip11Reset(&cfg)

		assert.Equal(t, defaultMaxLimit, cfg.Nip11.Limitation.MaxLimit)
		assert.EqualValues(t, defaultMaxMessageLength, cfg.Nip11.Limitation.MaxMessageLength)
		assert.Equal(t, defaultMaxSubscriptions, cfg.Nip11.Limitation.MaxSubscriptions)
		assert.Equal(t, defaultMaxIndexableTags, cfg.Nip11.Limitation.MaxIndexableTags)
	})

	t.Run("limitation.auth_required survives the reset", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  limitation:
    auth_required: true
store: "test.db"
`)
		applyNip11Reset(&cfg)
		assert.True(t, cfg.Nip11.Limitation.AuthRequired)
	})

	t.Run("url survives the reset", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  url: "wss://relay.example.com"
  limitation:
    auth_required: true
store: "test.db"
`)
		applyNip11Reset(&cfg)
		assert.Equal(t, "wss://relay.example.com", cfg.Nip11.URL)
	})

	t.Run("limitation.membership_required survives the reset", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  limitation:
    membership_required: true
store: "test.db"
`)
		applyNip11Reset(&cfg)
		assert.True(t, cfg.Nip11.Limitation.MembershipRequired)
	})
}

// membershipEnabled safely reads cfg.Membership.Enabled, which is nil
// whenever the config file omits the `membership:` block entirely.
func membershipEnabled(cfg *RelayConfig) bool {
	return cfg.Membership != nil && cfg.Membership.Enabled
}

// checkMembershipRequiresPrivKey mirrors the `config.Membership.Enabled &&
// config.Nip11.PrivKey == ""` guard in initConfig (command.go).
func checkMembershipRequiresPrivKey(cfg *RelayConfig) error {
	if membershipEnabled(cfg) && cfg.Nip11.PrivKey == "" {
		return errors.New("membership.enabled requires nip11.privkey")
	}
	return nil
}

// checkMembershipInviteMaxUsesNonNegative mirrors initConfig's
// `config.Membership.InviteMaxUses < 0` guard.
func checkMembershipInviteMaxUsesNonNegative(cfg *RelayConfig) error {
	if cfg.Membership != nil && cfg.Membership.InviteMaxUses < 0 {
		return errors.New("membership.inviteMaxUses must be >= 0")
	}
	return nil
}

// checkMembershipRequiredRequiresEnabledAndAuth mirrors initConfig's
// `nip11.limitation.membership_required` cross-field chain: it requires
// both membership.enabled and nip11.limitation.auth_required.
func checkMembershipRequiredRequiresEnabledAndAuth(cfg *RelayConfig) error {
	if !cfg.Nip11.Limitation.MembershipRequired {
		return nil
	}
	if !membershipEnabled(cfg) {
		return errors.New("nip11.limitation.membership_required requires membership.enabled")
	}
	if !cfg.Nip11.Limitation.AuthRequired {
		return errors.New("nip11.limitation.membership_required requires nip11.limitation.auth_required")
	}
	return nil
}

// selfPubkey mirrors initConfig's Self-population logic: Self mirrors
// PubKey, populated only when membership.enabled (an explicit opt-in, not
// a side effect of nip11.privkey being set for some unrelated reason).
func selfPubkey(cfg *RelayConfig) string {
	if membershipEnabled(cfg) {
		return cfg.Nip11.PubKey
	}
	return ""
}

func TestMembershipConfig(t *testing.T) {
	const validPubkey = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"
	const validPrivkey = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

	t.Run("omitted: defaults to disabled, no privkey needed", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
`)
		assert.False(t, membershipEnabled(&cfg))
		assert.NoError(t, checkMembershipRequiresPrivKey(&cfg))
		assert.Equal(t, "", selfPubkey(&cfg))
	})

	t.Run("enabled without privkey fails", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
membership:
  enabled: true
`)
		assert.True(t, membershipEnabled(&cfg))
		assert.Error(t, checkMembershipRequiresPrivKey(&cfg))
	})

	t.Run("enabled with privkey round-trips and sets Self", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  privkey: "`+validPrivkey+`"
store: "test.db"
membership:
  enabled: true
  inviteTTL: "24h"
  inviteMaxUses: 1
  publishAddRemoveEvents: true
`)
		require.NotNil(t, cfg.Membership)
		assert.True(t, cfg.Membership.Enabled)
		assert.Equal(t, "24h", cfg.Membership.InviteTTL)
		assert.Equal(t, 1, cfg.Membership.InviteMaxUses)
		assert.True(t, cfg.Membership.PublishAddRemoveEvents)
		assert.NoError(t, checkMembershipRequiresPrivKey(&cfg))
		assert.Equal(t, validPubkey, selfPubkey(&cfg))
	})

	t.Run("negative inviteMaxUses rejected", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  privkey: "`+validPrivkey+`"
store: "test.db"
membership:
  enabled: true
  inviteMaxUses: -1
`)
		assert.Error(t, checkMembershipInviteMaxUsesNonNegative(&cfg))
	})

	t.Run("zero inviteMaxUses (unlimited) is valid", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  privkey: "`+validPrivkey+`"
store: "test.db"
membership:
  enabled: true
`)
		assert.NoError(t, checkMembershipInviteMaxUsesNonNegative(&cfg))
	})
}

func TestMembershipRequiresEnabledAndAuth(t *testing.T) {
	const validPubkey = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"
	const validPrivkey = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

	t.Run("omitted: no requirement, passes", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
`)
		assert.NoError(t, checkMembershipRequiredRequiresEnabledAndAuth(&cfg))
	})

	t.Run("membership_required without membership.enabled fails", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  limitation:
    membership_required: true
    auth_required: true
store: "test.db"
`)
		assert.Error(t, checkMembershipRequiredRequiresEnabledAndAuth(&cfg))
	})

	t.Run("membership_required without auth_required fails", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  privkey: "`+validPrivkey+`"
  limitation:
    membership_required: true
store: "test.db"
membership:
  enabled: true
`)
		assert.Error(t, checkMembershipRequiredRequiresEnabledAndAuth(&cfg))
	})

	t.Run("membership_required with both satisfied passes", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  privkey: "`+validPrivkey+`"
  limitation:
    membership_required: true
    auth_required: true
store: "test.db"
membership:
  enabled: true
`)
		assert.NoError(t, checkMembershipRequiredRequiresEnabledAndAuth(&cfg))
	})
}

// agentAuthEnabled safely reads cfg.AgentAuth.Enabled, which is nil
// whenever the config file omits the `agent_auth:` block entirely.
func agentAuthEnabled(cfg *RelayConfig) bool {
	return cfg.AgentAuth != nil && cfg.AgentAuth.Enabled
}

// checkAgentAuthRequiresMembershipRequired mirrors initConfig's
// `config.AgentAuth.Enabled && !config.Nip11.Limitation.MembershipRequired`
// guard.
func checkAgentAuthRequiresMembershipRequired(cfg *RelayConfig) error {
	if agentAuthEnabled(cfg) && !cfg.Nip11.Limitation.MembershipRequired {
		return errors.New("agent_auth.enabled requires nip11.limitation.membership_required")
	}
	return nil
}

func TestAgentAuthConfig(t *testing.T) {
	const validPubkey = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"
	const validPrivkey = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

	t.Run("omitted: defaults to disabled", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
`)
		assert.False(t, agentAuthEnabled(&cfg))
		assert.NoError(t, checkAgentAuthRequiresMembershipRequired(&cfg))
	})

	t.Run("enabled without membership_required fails", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  privkey: "`+validPrivkey+`"
store: "test.db"
membership:
  enabled: true
agent_auth:
  enabled: true
`)
		assert.True(t, agentAuthEnabled(&cfg))
		assert.Error(t, checkAgentAuthRequiresMembershipRequired(&cfg))
	})

	t.Run("enabled with membership_required round-trips", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
  privkey: "`+validPrivkey+`"
  limitation:
    auth_required: true
    membership_required: true
store: "test.db"
membership:
  enabled: true
agent_auth:
  enabled: true
  freshnessWindow: "120s"
  kindEnforcement: true
`)
		require.NotNil(t, cfg.AgentAuth)
		assert.True(t, cfg.AgentAuth.Enabled)
		assert.Equal(t, "120s", cfg.AgentAuth.FreshnessWindow)
		assert.True(t, cfg.AgentAuth.KindEnforcement)
		assert.NoError(t, checkAgentAuthRequiresMembershipRequired(&cfg))
	})
}
