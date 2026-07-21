// Package relay implements the `ncli relay` command, which runs the
// Nostr relay server (WebSocket protocol, NIP-11 metadata, optional search).
package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/common/meilisearch"
	"github.com/ohstr/nmilat/nip11"
	_ "github.com/ohstr/nmilat/nip57"
	_ "github.com/ohstr/nmilat/nip65"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/search"
	"github.com/ohstr/nmilat/utils"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	defaultPort = 5500
	logFileName = "nrelay.log"

	// defaultMaxMessageLength mirrors nmilat's relay/session.go
	// wsDefaultReadLimit: when Limitation.MaxMessageLength is 0, the relay's
	// websocket session actually enforces this many bytes rather than 0.
	// Advertising 0 in the NIP-11 document would tell clients the relay
	// accepts no messages at all, so we advertise the real enforced value
	// instead. Keep this in sync if nmilat's default ever changes.
	defaultMaxMessageLength = 1_101_005

	defaultMaxLimit = 555_555
	// defaultMaxSubscriptions and defaultMaxIndexableTags mirror nmilat's
	// own relay/subscription.go and relay/store.go defaults, for the same
	// advertise-the-real-enforced-value reason as defaultMaxMessageLength.
	defaultMaxSubscriptions = 355
	defaultMaxIndexableTags = 5

	// defaultCacheWindow and defaultTopZappedLimit mirror nmilat's own
	// relay.SessionConfig defaults (DefaultCacheWindow/DefaultCacheLimit in
	// defaultSessionConfig). Session.go's WithSessionConfig replaces the SDK
	// defaults wholesale rather than merging into them, so NewServer must
	// re-apply these itself -- see cache.topZapped.window handling in
	// service.go.
	defaultCacheWindow    = 24 * time.Hour
	defaultTopZappedLimit = 50

	// Session engine tuning, applied only when not set in config (see
	// initConfig and NewServer).
	defaultOutgoingBufferSize      = 1024
	defaultMaxConcurrentStoreTasks = 2048
	defaultVerificationWorkers     = 50
)

var (
	config RelayConfig
)

type RelayConfig struct {
	Nip11 nip11.Metadata `mapstructure:"nip11"`

	LogDir       string `mapstructure:"logdir"`
	RelayNotesDb string `mapstructure:"store"`
	Port         int    `mapstructure:"port"`

	Logs       *LogConfig        `mapstructure:"logs"`
	Cache      *CacheConfig      `mapstructure:"cache"`
	Pow        *PowConfig        `mapstructure:"pow"`
	Membership *MembershipConfig `mapstructure:"membership"`
	AgentAuth  *AgentAuthConfig  `mapstructure:"agent_auth"`

	HandshakeTimeout string `mapstructure:"handshakeTimeout"`
	PingInterval     string `mapstructure:"pingInterval"`
	PongTimeout      string `mapstructure:"pongTimeout"`
	WriteTimeout     string `mapstructure:"writeTimeout"`

	// Session engine tuning. Zero means "use the built-in default" (see the
	// default* constants above and initConfig).
	OutgoingBufferSize      int `mapstructure:"outgoingBufferSize"`
	MaxConcurrentStoreTasks int `mapstructure:"maxConcurrentStoreTasks"`
	VerificationWorkers     int `mapstructure:"verificationWorkers"`
}

// PowConfig configures NIP-13 proof-of-work enforcement for published
// events. Omitting the `pow:` block entirely, or leaving Min at 0, means no
// requirement -- the relay accepts events regardless of PoW, same as before
// this config existed.
type PowConfig struct {
	// Strict, if true, makes the relay reject (OK false, "pow: ...") any
	// event whose real difficulty falls below Min. If false (the
	// default), Min is advisory only: still advertised via NIP-11
	// min_pow_difficulty so clients can mine to it ahead of time, but
	// under-difficulty events are accepted anyway. This is how you
	// announce an upcoming requirement before actually turning on
	// rejection.
	Strict bool `mapstructure:"strict"`

	// Min is the minimum NIP-13 leading-zero-bit difficulty required of
	// published events. 0 (the default) means no requirement: the relay
	// accepts every event regardless of proof-of-work, whether or not
	// Strict is set -- Strict alone has nothing to enforce.
	Min int `mapstructure:"min"`
}

type LogConfig struct {
	Filename   string `mapstructure:"filename"`
	MaxSize    int    `mapstructure:"maxSize"`
	MaxBackups int    `mapstructure:"maxBackups"`
	MaxAge     int    `mapstructure:"maxAge"`
	Compress   bool   `mapstructure:"compress"`
}

type SearchConfig struct {
	Enabled   bool   `mapstructure:"enabled"`
	Readonly  bool   `mapstructure:"readonly"`
	Host      string `mapstructure:"host"`
	Key       string `mapstructure:"key"`
	IndexName string `mapstructure:"index_name"`
	BatchSize int    `mapstructure:"batch_size"`
	MaxChSize int    `mapstructure:"max_ch_size"`
}

// CacheConfig groups the relay's optional serving-layer features: the
// signed "top zapped" cache response and Meilisearch-backed search both
// live here rather than as more flat top-level relay config fields.
type CacheConfig struct {
	// TopZapped configures the signed "top zapped" cache response. Omitting
	// this block entirely is identical to `{enabled: false}`.
	TopZapped *TopZappedConfig `mapstructure:"topZapped"`

	// Search enables Meilisearch-backed profile search (optional).
	Search *SearchConfig `mapstructure:"search"`
}

// TopZappedConfig configures the relay's signed "top zapped" cache
// response (see handler_cache.go in nmilat/relay).
type TopZappedConfig struct {
	// Enabled turns on the signed "top zapped" cache response. Off by
	// default; when on, nip11.privkey becomes mandatory (see initConfig).
	Enabled bool `mapstructure:"enabled"`

	// Window bounds the default time range for "top-zapped" queries when a
	// client's cache filter omits its own window (see the `cache` filter
	// action in handler_cache.go), as a duration string using the same
	// units as filter since/until values: h, mo, m, s, d, w (e.g. "24h",
	// "7d", "2w", "1mo"). Empty or unparseable falls back to
	// defaultCacheWindow (24h).
	Window string `mapstructure:"window"`
}

// MembershipConfig configures NIP-43 relay membership: enforcement of
// relay-authored role/list/add/remove-user/invite-response events, and the
// self-service join/leave/invite flow. Off by default -- omitting this
// block entirely, or leaving enabled false, means the relay behaves
// exactly as it did before NIP-43 existed.
type MembershipConfig struct {
	// Enabled turns on NIP-43: the relay starts enforcing that role
	// definitions, membership lists, add/remove-user, and invite-response
	// events are signed by the relay's own key, and starts serving the
	// join/leave/invite flow. Requires nip11.privkey (see initConfig) --
	// the relay signs all of these events with it.
	Enabled bool `mapstructure:"enabled"`

	// InviteTTL bounds how long an issued invite claim (kind 28935,
	// requested via REQ) stays valid, as a Go duration string (e.g.
	// "24h"). Empty or unparseable falls back to the relay SDK's own
	// default.
	InviteTTL string `mapstructure:"inviteTTL"`

	// InviteMaxUses caps how many times a single invite claim may be
	// consumed via a Join Request. 0 means unlimited.
	InviteMaxUses int `mapstructure:"inviteMaxUses"`

	// PublishAddRemoveEvents, if true, makes the relay also publish a
	// signed kind:8000/8001 event whenever membership changes via
	// Join/Leave Request, in addition to updating its own internal
	// membership store (which is authoritative either way).
	PublishAddRemoveEvents bool `mapstructure:"publishAddRemoveEvents"`
}

// AgentAuthConfig configures NIP-AA: an agent key presenting a valid
// NIP-OA credential during NIP-42 AUTH gains virtual membership derived
// from its owner's NIP-43 membership, without separate enrollment. Off by
// default. Requires membership.required (see initConfig) -- enabling this
// genuinely changes AUTH-time behavior (a non-member with no credential
// now fails AUTH itself, rather than succeeding AUTH and failing a later
// gate), so it only makes sense once membership is actually enforced.
type AgentAuthConfig struct {
	// Enabled turns on NIP-AA.
	Enabled bool `mapstructure:"enabled"`

	// FreshnessWindow bounds how far an AUTH event's created_at may drift
	// from now before NIP-AA rejects it, as a Go duration string. Empty or
	// unparseable falls back to the relay SDK's own default (the
	// spec-recommended ±120s).
	FreshnessWindow string `mapstructure:"freshnessWindow"`

	// KindEnforcement, if true, additionally checks a virtual member's
	// retained credential's kind= clauses against every EVENT it submits.
	// Off by default -- per spec, kind= clauses are advisory only unless a
	// relay opts into enforcing them.
	KindEnforcement bool `mapstructure:"kindEnforcement"`
}

func NewRelayCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "relay",
		Short: "Run the relay server, or operate one that's already running",
		Long: `Bare invocation runs the Nostr relay server: WebSocket protocol,
NIP-11 metadata, and optional search. Its "stats", "reindex", and "clear"
subcommands instead operate a relay that's already running, over NIP-98
authenticated HTTP -- including triggering a live search/zap reindex.`,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := initConfig(); err != nil {
				return common.RuntimeError(cmd, err)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := RunRelay(); err != nil {
				return common.RuntimeError(cmd, err)
			}
			return nil
		},
	}

	addRemoteAdminCommands(cmd)
	addContextCommands(cmd)
	return cmd
}

// initConfig unmarshals viper's already-loaded config state into relay's own
// RelayConfig and applies relay-specific defaults/validation. It does not
// reload the config file: root's InitConfig (cli/ncli/root.go) already did
// that once via the nearest ancestor's PersistentPreRun, before this PreRunE
// runs -- reloading here would just redundantly re-read the same file.
//
// Every validation failure here is classified directly as a *common.CLIError
// (rather than at PreRunE's own common.RuntimeError call) -- wrapCLIError
// keeps an already-classified error's code as-is when a caller re-wraps it,
// so this one classification covers the single call site for free.
func initConfig() error {
	if used := viper.ConfigFileUsed(); used != "" {
		ev := log.Info().Str("config", used)
		if common.ActiveRelayContext != "" {
			ev = ev.Str("relay_context", common.ActiveRelayContext)
		}
		ev.Msg("using config file")
	}

	if err := viper.Unmarshal(&config); err != nil {
		return &common.CLIError{Err: fmt.Errorf("unable to decode config: %w", err), Code: common.CodeInvalidInput}
	}

	if config.Nip11.PrivKey != "" {
		derivedPub, err := utils.GetPublicKey(config.Nip11.PrivKey)
		if err != nil {
			// input left blank -- PrivKey is private-key material.
			return &common.CLIError{Err: fmt.Errorf("invalid nip11.privkey: %w", err), Code: common.CodeInvalidInput}
		}

		if config.Nip11.PubKey == "" {
			config.Nip11.PubKey = derivedPub
			log.Info().Str("pubkey", derivedPub).Msg("auto-populated pubkey from privkey")
		} else if config.Nip11.PubKey != derivedPub {
			return &common.CLIError{
				Err:   fmt.Errorf("nip11.pubkey (%s) does not match the key derived from nip11.privkey (%s)", config.Nip11.PubKey, derivedPub),
				Code:  common.CodeInvalidInput,
				Input: config.Nip11.PubKey,
			}
		}
	}

	if config.Nip11.PubKey == "" {
		return &common.CLIError{Err: errors.New("nip11.pubkey is required (or nip11.privkey, to derive it)"), Code: common.CodeUsage}
	}

	if config.RelayNotesDb == "" {
		return &common.CLIError{Err: errors.New("store is required"), Code: common.CodeUsage}
	}

	if config.Cache != nil && config.Cache.TopZapped != nil && config.Cache.TopZapped.Enabled && config.Nip11.PrivKey == "" {
		return &common.CLIError{Err: errors.New("cache.topZapped.enabled requires nip11.privkey"), Code: common.CodeUsage}
	}

	// auth_required needs nip11.url to validate AUTH events against, or
	// every AUTH attempt fails closed.
	if config.Nip11.Limitation.AuthRequired && config.Nip11.URL == "" {
		return &common.CLIError{Err: errors.New("nip11.limitation.auth_required requires nip11.url"), Code: common.CodeUsage}
	}

	if config.Pow != nil {
		if config.Pow.Min < 0 {
			return &common.CLIError{Err: errors.New("pow.min must be >= 0"), Code: common.CodeInvalidInput}
		}
		if config.Pow.Strict && config.Pow.Min == 0 {
			log.Warn().Msg("pow.strict is true but pow.min is 0 -- there is no difficulty requirement to enforce")
		}
		if config.Pow.Min > 0 {
			mode := "advisory (advertised via NIP-11, not enforced)"
			if config.Pow.Strict {
				mode = "enforced"
			}
			log.Info().Int("min_pow_difficulty", config.Pow.Min).Str("mode", mode).Msg("proof-of-work requirement configured")
		}
	}

	if config.Membership != nil {
		if config.Membership.Enabled && config.Nip11.PrivKey == "" {
			return &common.CLIError{Err: errors.New("membership.enabled requires nip11.privkey"), Code: common.CodeUsage}
		}
		if config.Membership.InviteMaxUses < 0 {
			return &common.CLIError{Err: errors.New("membership.inviteMaxUses must be >= 0"), Code: common.CodeUsage}
		}
	}
	if config.Nip11.Limitation.MembershipRequired {
		if config.Membership == nil || !config.Membership.Enabled {
			return &common.CLIError{Err: errors.New("nip11.limitation.membership_required requires membership.enabled"), Code: common.CodeUsage}
		}
		if !config.Nip11.Limitation.AuthRequired {
			return &common.CLIError{Err: errors.New("nip11.limitation.membership_required requires nip11.limitation.auth_required"), Code: common.CodeUsage}
		}
	}

	if config.AgentAuth != nil && config.AgentAuth.Enabled && !config.Nip11.Limitation.MembershipRequired {
		return &common.CLIError{Err: errors.New("agent_auth.enabled requires nip11.limitation.membership_required"), Code: common.CodeUsage}
	}

	if config.Port == 0 {
		config.Port = defaultPort
	}
	if config.OutgoingBufferSize == 0 {
		config.OutgoingBufferSize = defaultOutgoingBufferSize
	}
	if config.MaxConcurrentStoreTasks == 0 {
		config.MaxConcurrentStoreTasks = defaultMaxConcurrentStoreTasks
	}
	if config.VerificationWorkers == 0 {
		config.VerificationWorkers = defaultVerificationWorkers
	}

	// Set default NIP-11 metadata if missing
	if config.Nip11.Name == "" {
		config.Nip11.Name = "ncli Relay"
	}
	if config.Nip11.Description == "" {
		config.Nip11.Description = "ncli relay"
	}

	// Preserve fields before resetting defaults
	priv := config.Nip11.PrivKey
	url := config.Nip11.URL
	build := common.ReadBuildInfo()

	// Self mirrors PubKey, populated only when membership.enabled -- an
	// explicit opt-in, not a side effect of nip11.privkey merely being set
	// for some unrelated reason (e.g. cache.topZapped, which also requires
	// it). Enabled already guarantees PrivKey != "" (validated above), and
	// PubKey == the key derived from PrivKey (validated earlier still), so
	// this is the same operational identity, not a second one.
	var self string
	if config.Membership != nil && config.Membership.Enabled {
		self = config.Nip11.PubKey
	}

	// Start from whatever the user configured under nip11.limitation and
	// only fill in a field left at its zero value — a prior version of this
	// code unconditionally overwrote all four with hardcoded defaults,
	// silently discarding anything the user set.
	limitation := config.Nip11.Limitation
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

	// pow.min/pow.strict are the single source of truth for
	// Limitation.MinPowDifficulty/StrictPow -- those two Limitation fields
	// are mapstructure:"-" specifically so nip11.limitation in YAML can't
	// set them directly, avoiding two config paths for the same value.
	// config.Pow == nil leaves both at their zero value (0/false), i.e. no
	// requirement, same as omitting the block.
	if config.Pow != nil {
		limitation.MinPowDifficulty = config.Pow.Min
		limitation.StrictPow = config.Pow.Strict
	}

	config.Nip11 = nip11.Metadata{
		Name:        config.Nip11.Name,
		PubKey:      config.Nip11.PubKey,
		Contact:     config.Nip11.Contact,
		Description: config.Nip11.Description,
		Software:    build.Software,
		Version:     build.Version,
		Limitation:  limitation,
		PrivKey:     priv,
		URL:         url,
		Self:        self,
	}

	return nil
}

// RunRelay starts the relay server and blocks until SIGINT/SIGTERM. Startup
// failures are returned as plain errors (classified generic/internal by
// NewRelayCommand's RunE) rather than log.Fatal -- the previous log.Fatal
// calls bypassed the CLIError/JSON-error path entirely, so a --json caller
// got a bare styled log line on stderr instead of the same structured
// {"error","code"} shape every other failure produces.
func RunRelay() error {
	// Initialize Logging
	cwd, _ := os.Getwd()
	var logWriter *lumberjack.Logger
	if config.Logs != nil {
		logWriter = &lumberjack.Logger{
			Filename:   config.Logs.Filename,
			MaxSize:    config.Logs.MaxSize,
			MaxBackups: config.Logs.MaxBackups,
			MaxAge:     config.Logs.MaxAge,
			Compress:   config.Logs.Compress,
		}
	} else {
		logDir := config.LogDir
		if logDir == "" {
			logDir = cwd
		}
		logWriter = &lumberjack.Logger{
			Filename:   filepath.Join(logDir, logFileName),
			MaxSize:    100,
			MaxBackups: 3,
			MaxAge:     28,
			Compress:   true,
		}
	}

	common.ConfigureLogging(common.WithConsole(), common.WithFileWriter(logWriter))
	// Log the effective rotation settings actually in force -- not
	// config.Logs directly, which is nil whenever the config omits the
	// optional `logs:` block (i.e. whenever the defaults above are the ones
	// being used) and would otherwise print the unhelpful "logs=null".
	log.Info().
		Str("file", logWriter.Filename).
		Int("max_size_mb", logWriter.MaxSize).
		Int("max_backups", logWriter.MaxBackups).
		Int("max_age_days", logWriter.MaxAge).
		Bool("compress", logWriter.Compress).
		Msg("logging initialized")

	// nip11.pubkey and store are both already validated by initConfig,
	// which PreRunE guarantees ran successfully before Run (this function)
	// is ever called.

	// relay.NewEventStore doesn't create its parent directory, unlike the
	// lumberjack log writer above, which does this internally.
	if err := os.MkdirAll(filepath.Dir(config.RelayNotesDb), 0755); err != nil {
		return fmt.Errorf("failed to create directory for event store db: %w", err)
	}

	store, err := relay.NewEventStore(config.RelayNotesDb, &config.Nip11.Limitation)
	if err != nil {
		return fmt.Errorf("failed to connect to event store: %w", err)
	}

	var searchService search.Service
	var searchCfg *SearchConfig
	if config.Cache != nil {
		searchCfg = config.Cache.Search
	}
	if searchCfg != nil && searchCfg.Enabled {
		searchClient := meilisearch.NewMeiliClient(
			searchCfg.Host,
			searchCfg.Key,
			searchCfg.IndexName,
		)
		svc := search.NewService(
			searchClient,
			searchCfg.BatchSize,
			searchCfg.MaxChSize,
		)
		if err := svc.Initialize(context.Background()); err != nil {
			log.Warn().Err(err).Msg("failed to initialize search service (soft fail)")
		}
		searchService = svc

		if searchCfg.Readonly {
			searchService = search.NewReadOnlyService(svc)
		}
	}

	s := NewServer(store, searchService)

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM)

	<-stopCh
	s.Stop()
	return nil
}
