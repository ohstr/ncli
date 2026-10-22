// Package relay implements the `ncli relay` command, which runs the
// Nostr relay server (WebSocket protocol, NIP-11 metadata, optional search).
package relay

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/common/meilisearch"
	"github.com/ohstr/ncli/cli/relay/adminplane"
	"github.com/ohstr/ncli/cli/relay/metrics"
	"github.com/ohstr/nmilat/nip11"
	_ "github.com/ohstr/nmilat/nip29/relayreg"
	_ "github.com/ohstr/nmilat/nip47/relayreg"
	_ "github.com/ohstr/nmilat/nip48/relayreg"
	_ "github.com/ohstr/nmilat/nip53/relayreg"
	_ "github.com/ohstr/nmilat/nip57/relayreg"
	_ "github.com/ohstr/nmilat/nip65/relayreg"
	_ "github.com/ohstr/nmilat/nip71/relayreg"
	_ "github.com/ohstr/nmilat/nip88/relayreg"
	_ "github.com/ohstr/nmilat/nip90/relayreg"
	_ "github.com/ohstr/nmilat/nipA0/relayreg"
	_ "github.com/ohstr/nmilat/nipAZ/relayreg"
	_ "github.com/ohstr/nmilat/nipB0/relayreg"
	_ "github.com/ohstr/nmilat/nipB7/relayreg"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/search"
	"github.com/ohstr/nmilat/utils"
	"github.com/rs/zerolog"
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

	// Session engine tuning, applied only when not set in config (see
	// initConfig and NewServer).
	defaultOutgoingBufferSize      = 1024
	defaultMaxConcurrentStoreTasks = 2048
	defaultVerificationWorkers     = 50
)

var (
	config RelayConfig

	// adminSettings is config.Admin validated by initConfig; nil when the
	// admin listener is off.
	adminSettings *adminplane.Settings

	// errNoRelayConfig is returned wherever viper resolved no config
	// source at all -- no --config, no current `ncli relay context`, and
	// no ncli.yaml/relay.yaml in the working directory -- so callers can
	// say that plainly instead of blaming a specific (nonexistent)
	// required field. Shared with getAdminConfig in admin.go, which hits
	// the same situation via a different missing field (nip11.privkey).
	errNoRelayConfig = errors.New("no relay config found; pass --config <file> or --context <name>")
)

type RelayConfig struct {
	Nip11 nip11.Metadata `mapstructure:"nip11"`

	LogDir       string `mapstructure:"logdir"`
	RelayNotesDb string `mapstructure:"store"`
	Port         int    `mapstructure:"port"`

	Logs       *LogConfig         `mapstructure:"logs"`
	Cache      *CacheConfig       `mapstructure:"cache"`
	Pow        *PowConfig         `mapstructure:"pow"`
	Membership *MembershipConfig  `mapstructure:"membership"`
	Nip86      *Nip86Config       `mapstructure:"nip86"`
	AgentAuth  *AgentAuthConfig   `mapstructure:"agent_auth"`
	Groups     *GroupsConfig      `mapstructure:"groups"`
	Huddle     *HuddleConfig      `mapstructure:"huddle"`
	HTTPBridge *HTTPBridgeConfig  `mapstructure:"httpBridge"`
	Admin      *adminplane.Config `mapstructure:"admin"`

	HandshakeTimeout string `mapstructure:"handshakeTimeout"`
	PingInterval     string `mapstructure:"pingInterval"`
	PongTimeout      string `mapstructure:"pongTimeout"`
	WriteTimeout     string `mapstructure:"writeTimeout"`

	// Session engine tuning. Zero means "use the built-in default" (see the
	// default* constants above and initConfig).
	OutgoingBufferSize      int `mapstructure:"outgoingBufferSize"`
	MaxConcurrentStoreTasks int `mapstructure:"maxConcurrentStoreTasks"`
	// VerificationWorkers is cache.profiles.checks.workers, resolved.
	VerificationWorkers int `mapstructure:"-"`

	// PprofAddr, when set (e.g. "127.0.0.1:6060"), serves net/http/pprof
	// there. Unauthenticated: keep it on loopback.
	PprofAddr string `mapstructure:"pprofAddr"`

	// Homepage, on unless false, shows browsers that open the relay's URL a
	// page built from its NIP-11 document.
	Homepage *bool `mapstructure:"homepage"`
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
	// Level is debug, info (default), warn or error.
	Level string `mapstructure:"level"`
	// File false turns off the log file, e.g. when a container runtime
	// already keeps stderr.
	File       *bool  `mapstructure:"file"`
	Filename   string `mapstructure:"filename"`
	MaxSize    int    `mapstructure:"maxSize"`
	MaxBackups int    `mapstructure:"maxBackups"`
	MaxAge     int    `mapstructure:"maxAge"`
	Compress   bool   `mapstructure:"compress"`
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

// Nip86Config configures NIP-86, the Relay Management API: membership
// administration over HTTP on the relay's own URL, so a host's app can add or
// remove a guest and mint an invite code without a terminal. Omitting the
// `nip86:` block leaves it off.
type Nip86Config struct {
	// Enabled serves the API for requests to the relay URL carrying
	// Content-Type: application/nostr+json+rpc. The WebSocket upgrade and the
	// NIP-11 document on that same URL are unaffected.
	//
	// The moderation methods (bans, kind rules, blocked IPs, the relay's
	// NIP-11 name/description/icon) work on any relay; the NIP-43 membership
	// methods are added when membership.enabled is on.
	Enabled bool `mapstructure:"enabled"`

	// Admins are the hex pubkeys allowed to call it, in addition to
	// nip11.pubkey (which is always allowed, so an existing operator keeps
	// working). This is the point of the block: the relay's own key is
	// otherwise the only admin identity, and administering from an app would
	// mean carrying the relay's secret key on a phone.
	Admins []string `mapstructure:"admins"`

	// AllowedOrigins is the CORS allowlist for browser callers. Empty answers
	// any origin, which is safe here because authorization is a signed NIP-98
	// header rather than a cookie, so a hostile page gains nothing from being
	// allowed to send the request.
	AllowedOrigins []string `mapstructure:"allowedOrigins"`
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

// HTTPBridgeConfig groups the NIP-98-authenticated HTTP endpoints that each
// offer a one-shot alternative to something normally done over the
// WebSocket connection: POST /query (a REQ/EOSE round trip) and POST
// /events (publishing one already-signed event). A bare top-level `query:`
// or `events:` key names a wire path, not a concept, and gives an operator
// skimming the config no reason to read the two together even though
// they're the same feature's two halves -- `httpBridge:` is what a REQ or
// EVENT becomes when there's no socket to carry it.
//
// Config shape (was flat `query:`/`events:` before this grouping existed):
//
//	httpBridge:
//	  query:
//	    enabled: true
//	  events:
//	    enabled: true
type HTTPBridgeConfig struct {
	Query  *QueryConfig  `mapstructure:"query"`
	Events *EventsConfig `mapstructure:"events"`
}

// QueryConfig configures the POST /query bridge: a one-shot HTTP alternative
// to a WebSocket REQ/EOSE round trip (nmilat's relay.NewQueryHandler). Off by
// default -- omitting the block leaves it unmounted.
//
// This isn't named nip98 (that's just the request-signing transport, shared
// with the /admin endpoints above and not specific to this feature) or
// nipcw/nip_cw. The underlying spec really is buzz's own NIP-CW
// (https://github.com/block/buzz/blob/main/docs/nips/NIP-CW.md), but that
// short name is already `ncli decode`'s name for something unrelated --
// NIP-CASH's Circle Wallet circlehub1... connection -- so reusing it here
// would collide with ncli's own existing vocabulary, not just nmilat's.
type QueryConfig struct {
	// Enabled mounts the handler at /query. NIP-98 authenticates every
	// request (proves identity and binds it to this body/method/URL) on its
	// own, but NIP-CW's Access Scoping section requires a plain filter
	// through this endpoint to get the same access control an equivalent
	// REQ would -- so when nip11.limitation.membership_required is set, a
	// result is further gated to the NIP-98 signer's own NIP-43 membership,
	// exactly like processRequest's REQ/COUNT check. With
	// membership_required unset (the common case), any validly-signed
	// caller is served, the same as an anonymous REQ would be.
	Enabled bool `mapstructure:"enabled"`
}

// EventsConfig configures the POST /events bridge: the write-side
// counterpart to QueryConfig's POST /query (nmilat's relay.NewEventsHandler)
// -- a NIP-98-authenticated HTTP alternative to opening a WebSocket purely
// to publish one already-signed event. Off by default -- omitting the block
// leaves it unmounted.
type EventsConfig struct {
	// Enabled mounts the handler at /events. The NIP-98 signer must match
	// the submitted event's own pubkey (stricter than the WS EVENT path,
	// which has no such blanket requirement) -- see
	// relay.NewEventsHandler's doc comment. membership_required and the
	// relay's own self-authored-kind guard are enforced exactly as they
	// would be over WS.
	Enabled bool `mapstructure:"enabled"`
}

// removedRelayCommands maps relay subcommands that no longer exist to what
// replaced them, so `ncli relay stats` says where it went instead of trying
// to start a relay.
var removedRelayCommands = map[string]string{
	"stats":   "ncli relay status",
	"reindex": "ncli relay index rebuild <search|zaps>",
	"clear":   "ncli relay index drop <search|zaps>",
}

// relayArgs rejects positional arguments: `relay` itself takes none, so one
// is a mistyped or removed subcommand.
func relayArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if repl, ok := removedRelayCommands[args[0]]; ok {
		return common.UsageError(cmd, fmt.Errorf("`ncli relay %s` was removed; use `%s`", args[0], repl))
	}
	return common.UsageError(cmd, fmt.Errorf("unknown command %q for \"ncli relay\"", args[0]))
}

func NewRelayCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "relay",
		Short: "Run and operate a Nostr relay",
		Example: `  ncli relay --config relay.yaml
  ncli relay --context myrelay
  ncli relay status --config relay.yaml`,
		Args: relayArgs,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			ctxName, _ := cmd.Flags().GetString("context")
			if ctxName != "" {
				if err := runOrCreateRelayContext(cmd, ctxName); err != nil {
					return err
				}
			}
			if err := initConfig(); err != nil {
				return common.RuntimeError(cmd, err)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode, _ := cmd.Flags().GetBool("json")
			if check, _ := cmd.Flags().GetBool("check"); check {
				// initConfig, in PreRunE, already validated everything.
				if jsonMode {
					common.PrintJSON(map[string]any{"ok": true, "config": viper.ConfigFileUsed()})
				} else {
					fmt.Printf("%s is valid\n", viper.ConfigFileUsed())
				}
				return nil
			}
			if err := RunRelay(jsonMode); err != nil {
				return common.RuntimeError(cmd, err)
			}
			return nil
		},
	}

	cmd.Flags().StringP("context", "c", "", `run directly against a named relay context, creating it (minimal config + new identity) if it doesn't exist yet -- mutually exclusive with --config`)
	cmd.Flags().String("identity", "", `identity for a newly created context's relay key (vault label or nsec); a fresh one is generated and saved to the vault (labeled after the context) if omitted`)
	cmd.Flags().Bool("check", false, "validate the config and exit without starting the relay")

	addRemoteAdminCommands(cmd)
	addMetricsCommands(cmd)
	addDashboardCommand(cmd)
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
	used := viper.ConfigFileUsed()
	if used != "" {
		ev := log.Info().Str("config", used)
		if common.ActiveRelayContext != "" {
			ev = ev.Str("relay_context", common.ActiveRelayContext)
		}
		ev.Msg("using config file")
	} else {
		return &common.CLIError{Err: errNoRelayConfig, Code: common.CodeUsage, Help: common.HelpAfterError}
	}

	if err := checkRenamedKeys(viper.GetViper()); err != nil {
		return err
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

	if err := checkCacheConfig(&config); err != nil {
		return err
	}

	// auth_required needs nip11.url to validate AUTH events against, or
	// every AUTH attempt fails closed.
	if config.Nip11.Limitation.AuthRequired && config.Nip11.URL == "" {
		return &common.CLIError{Err: errors.New("nip11.limitation.auth_required requires nip11.url"), Code: common.CodeUsage}
	}

	if config.Huddle != nil && config.Huddle.Enabled {
		// A joining client authenticates with a NIP-42 event naming this relay,
		// so with no configured URL there is nothing to validate it against and
		// every join would fail closed.
		if config.Nip11.URL == "" {
			return &common.CLIError{Err: errors.New("huddle.enabled requires nip11.url"), Code: common.CodeUsage}
		}
		if config.Huddle.MaxRooms < 0 {
			return &common.CLIError{Err: errors.New("huddle.maxRooms must be >= 0"), Code: common.CodeInvalidInput}
		}
		if err := checkHuddleDuration("huddle.authTimeout", config.Huddle.AuthTimeout); err != nil {
			return err
		}
		if err := checkHuddleDuration("huddle.pingInterval", config.Huddle.PingInterval); err != nil {
			return err
		}
		if _, _, err := parseUDPPortRange(config.Huddle.UDPPortRange); err != nil {
			return err
		}
	}

	if config.Logs != nil && config.Logs.Level != "" {
		if _, err := parseLogLevel(config.Logs.Level); err != nil {
			return &common.CLIError{Err: err, Code: common.CodeInvalidInput, Input: config.Logs.Level}
		}
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

	if err := checkGroupsConfig(&config); err != nil {
		return &common.CLIError{Err: err, Code: common.CodeInvalidInput}
	}

	if config.Port == 0 {
		config.Port = defaultPort
	}

	settings, err := adminplane.Resolve(config.Admin, config.Port, nip86Admins())
	if err != nil {
		var ce *adminplane.ConfigError
		if errors.As(err, &ce) && ce.Usage {
			return &common.CLIError{Err: err, Code: common.CodeUsage}
		}
		input := ""
		if ce != nil {
			input = ce.Input
		}
		return &common.CLIError{Err: err, Code: common.CodeInvalidInput, Input: input}
	}
	adminSettings = settings
	if config.OutgoingBufferSize == 0 {
		config.OutgoingBufferSize = defaultOutgoingBufferSize
	}
	if config.MaxConcurrentStoreTasks == 0 {
		config.MaxConcurrentStoreTasks = defaultMaxConcurrentStoreTasks
	}
	config.VerificationWorkers = defaultVerificationWorkers
	if c := config.profiles().Checks; c != nil && c.Workers > 0 {
		config.VerificationWorkers = c.Workers
	}

	finalizeNip11(&config)

	if err := checkMaxStatsTargets(&config); err != nil {
		return err
	}

	return nil
}

// statsTargetBytes is roughly what one #d pubkey costs in a REQ:
// 64 hex characters, quotes and a comma.
const statsTargetBytes = 67

// checkMaxStatsTargets rejects a cache.profiles.stats.maxTargets no client
// could reach: a REQ naming that many pubkeys would exceed
// nip11.limitation.max_message_length.
func checkMaxStatsTargets(cfg *RelayConfig) error {
	st := cfg.profiles().Stats
	if st == nil {
		return nil
	}
	n := st.MaxTargets
	if n < 0 {
		return &common.CLIError{Err: errors.New("cache.profiles.stats.maxTargets must be >= 0"), Code: common.CodeInvalidInput}
	}
	if ceiling := cfg.Nip11.Limitation.MaxMessageLength / statsTargetBytes; int64(n) > ceiling {
		return &common.CLIError{
			Err:  fmt.Errorf("cache.profiles.stats.maxTargets %d exceeds what nip11.limitation.max_message_length (%d bytes) allows: at most %d", n, cfg.Nip11.Limitation.MaxMessageLength, ceiling),
			Code: common.CodeInvalidInput,
		}
	}
	return nil
}

// finalizeNip11 fills cfg.Nip11's defaults and startup-derived fields.
// It edits the decoded struct in place rather than rebuilding it, so every
// config-settable field (icon, and any nip11.Metadata adds later) survives.
func finalizeNip11(cfg *RelayConfig) {
	md := &cfg.Nip11
	if md.Name == "" {
		md.Name = "ncli Relay"
	}
	if md.Description == "" {
		md.Description = "ncli relay"
	}

	// Always build info, never config: a relay can't misreport what it runs.
	build := common.ReadBuildInfo()
	md.Software = build.Software
	md.Version = build.Version

	// Self mirrors PubKey, populated only when membership.enabled -- an
	// explicit opt-in, not a side effect of nip11.privkey merely being set
	// for some unrelated reason (e.g. cache.zaps, which also requires
	// it). Enabled already guarantees PrivKey != "" (validated above), and
	// PubKey == the key derived from PrivKey (validated earlier still), so
	// this is the same operational identity, not a second one.
	md.Self = ""
	if cfg.Membership != nil && cfg.Membership.Enabled {
		md.Self = md.PubKey
	}

	// Start from whatever the user configured under nip11.limitation and
	// only fill in a field left at its zero value.
	if md.Limitation.MaxLimit == 0 {
		md.Limitation.MaxLimit = defaultMaxLimit
	}
	if md.Limitation.MaxMessageLength == 0 {
		md.Limitation.MaxMessageLength = defaultMaxMessageLength
	}
	if md.Limitation.MaxSubscriptions == 0 {
		md.Limitation.MaxSubscriptions = defaultMaxSubscriptions
	}
	if md.Limitation.MaxIndexableTags == 0 {
		md.Limitation.MaxIndexableTags = defaultMaxIndexableTags
	}

	// pow.min/pow.strict are the single source of truth for
	// Limitation.MinPowDifficulty/StrictPow -- those two Limitation fields
	// are mapstructure:"-" specifically so nip11.limitation in YAML can't
	// set them directly, avoiding two config paths for the same value.
	// cfg.Pow == nil leaves both at their zero value (0/false), i.e. no
	// requirement, same as omitting the block.
	if cfg.Pow != nil {
		md.Limitation.MinPowDifficulty = cfg.Pow.Min
		md.Limitation.StrictPow = cfg.Pow.Strict
	}
}

// RunRelay starts the relay server and blocks until SIGINT/SIGTERM. Startup
// failures are returned as plain errors (classified generic/internal by
// NewRelayCommand's RunE) rather than log.Fatal -- the previous log.Fatal
// calls bypassed the CLIError/JSON-error path entirely, so a --json caller
// got a bare styled log line on stderr instead of the same structured
// {"error","code"} shape every other failure produces.
func RunRelay(jsonMode bool) error {
	logs := LogConfig{}
	if config.Logs != nil {
		logs = *config.Logs
	}
	level := zerolog.InfoLevel
	if logs.Level != "" {
		// Already validated by initConfig.
		level, _ = parseLogLevel(logs.Level)
	}
	opts := []common.LoggingOption{common.WithConsole(), common.WithJSON(jsonMode), common.WithLevel(level)}
	var logWriter *lumberjack.Logger
	if logs.File == nil || *logs.File {
		logWriter = newLogWriter(logs)
		opts = append(opts, common.WithFileWriter(logWriter), common.WithJSONFile())
	}
	common.ConfigureLogging(opts...)
	redirectStdLog(log.Logger)
	if logWriter != nil {
		log.Info().
			Str("log_level", level.String()).
			Str("file", logWriter.Filename).
			Int("max_size_mb", logWriter.MaxSize).
			Int("max_backups", logWriter.MaxBackups).
			Int("max_age_days", logWriter.MaxAge).
			Bool("compress", logWriter.Compress).
			Msg("logging initialized")
	} else {
		log.Info().Str("log_level", level.String()).Str("file", "off").Msg("logging initialized")
	}

	// nip11.pubkey and store are both already validated by initConfig,
	// which PreRunE guarantees ran successfully before Run (this function)
	// is ever called.

	// relay.NewEventStore doesn't create its parent directory, unlike the
	// lumberjack log writer above, which does this internally.
	if err := os.MkdirAll(filepath.Dir(config.RelayNotesDb), 0755); err != nil {
		return fmt.Errorf("failed to create directory for event store db: %w", err)
	}

	m := metrics.New(common.ReadBuildInfo().Version)
	store, err := relay.NewEventStore(config.RelayNotesDb, &config.Nip11.Limitation, relay.WithEventStoreObserver(m))
	if err != nil {
		return fmt.Errorf("failed to connect to event store: %w", err)
	}

	var searchService search.Service
	searchCfg := config.searchConfig()
	if searchCfg != nil && searchCfg.Enabled {
		searchClient := meilisearch.NewMeiliClient(
			searchCfg.Meilisearch.Host,
			searchCfg.Meilisearch.Key,
			searchCfg.Meilisearch.Index,
		)
		svc := search.NewService(
			searchClient,
			searchCfg.BatchSize,
			searchCfg.QueueSize,
		)
		if err := svc.Initialize(context.Background()); err != nil {
			log.Warn().Err(err).Msg("failed to initialize search service (soft fail)")
		}
		searchService = svc

		if searchCfg.Readonly {
			searchService = search.NewReadOnlyService(svc)
		}
	}

	s := newServer(store, searchService, m)
	if err := s.startAdmin(); err != nil {
		s.Stop()
		return err
	}

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM)

	<-stopCh
	s.Stop()
	return nil
}

// redirectStdLog sends the standard library's log package to l at debug.
// net/http's HTTP/2 client logs protocol noise from the profile checks there
// (e.g. "protocol error: received DATA on a HEAD request").
func redirectStdLog(l zerolog.Logger) {
	stdlog.SetFlags(0)
	stdlog.SetOutput(stdLogWriter{l})
}

type stdLogWriter struct{ l zerolog.Logger }

func (w stdLogWriter) Write(p []byte) (int, error) {
	w.l.Debug().Str("source", "stdlib").Msg(strings.TrimSpace(string(p)))
	return len(p), nil
}

// newLogWriter builds the rotating log file. Rotation settings apply only
// with a filename; without one it's <logdir>/nrelay.log with the defaults.
func newLogWriter(logs LogConfig) *lumberjack.Logger {
	if logs.Filename != "" {
		return &lumberjack.Logger{
			Filename:   logs.Filename,
			MaxSize:    logs.MaxSize,
			MaxBackups: logs.MaxBackups,
			MaxAge:     logs.MaxAge,
			Compress:   logs.Compress,
		}
	}
	logDir := config.LogDir
	if logDir == "" {
		logDir, _ = os.Getwd()
	}
	return &lumberjack.Logger{
		Filename:   filepath.Join(logDir, logFileName),
		MaxSize:    100,
		MaxBackups: 3,
		MaxAge:     28,
		Compress:   true,
	}
}

// parseLogLevel accepts the levels logs.level documents.
func parseLogLevel(s string) (zerolog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return zerolog.DebugLevel, nil
	case "info":
		return zerolog.InfoLevel, nil
	case "warn", "warning":
		return zerolog.WarnLevel, nil
	case "error":
		return zerolog.ErrorLevel, nil
	}
	return zerolog.NoLevel, fmt.Errorf("invalid logs.level %q: want debug, info, warn or error", s)
}
