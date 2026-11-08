package relay

import (
	"fmt"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/relay/ratelimit"
)

// LimitsConfig is the relay's rate limits. A limit that isn't set is no
// limit: there are no defaults, and without a limits block the relay limits
// nothing (Blossom's own size and concurrency caps aside).
//
//	limits:
//	  events:   {perPubkey: {events: 30, per: 1m, burst: 60}, perIP: {events: 120, per: 1m}}
//	  reads:    {perIP: {events: 60, per: 1m}}
//	  http:     {perIP: {events: 60, per: 1m}}
//	  blossom:
//	    uploads:   {perPubkey: {events: 20, per: 1h}}
//	    downloads: {perIP: {events: 600, per: 1m}}
//	  connectionsPerIP: 20
//	  exempt: members
//	  trustedProxies: [127.0.0.1]
type LimitsConfig struct {
	// Events limits EVENT writes, over the WebSocket and POST /events. Over
	// the WebSocket, PerPubkey counts only on a connection NIP-42
	// authenticated as the author.
	Events LimitConfig `mapstructure:"events"`
	// Reads limits REQ, COUNT and NEG-OPEN; an AUTH takes from PerIP.
	Reads LimitConfig `mapstructure:"reads"`
	// HTTP limits the HTTP bridge: POST /query, /count and /events.
	HTTP    LimitConfig        `mapstructure:"http"`
	Blossom BlossomLimitConfig `mapstructure:"blossom"`
	// ConnectionsPerIP caps one IP's open WebSocket connections, the
	// relay's and huddles' together.
	ConnectionsPerIP int `mapstructure:"connectionsPerIP"`
	// Exempt is none (the default) or members: NIP-43 members and admins
	// (the relay key, nip86.admins) skip the per-pubkey limits. A banned
	// key never does. Per-IP limits apply to everyone.
	Exempt string `mapstructure:"exempt"`
	// TrustedProxies are the reverse proxies (CIDRs or IPs) whose
	// X-Forwarded-For names the client; each must set or append it.
	// X-Real-IP counts only without X-Forwarded-For. See
	// ratelimit.Config.TrustedProxies.
	TrustedProxies []string `mapstructure:"trustedProxies"`
}

// BlossomLimitConfig limits Blossom requests. Its maxBlobBytes,
// maxUploadsPerSigner and maxConcurrentUploads are separate and keep their
// defaults.
type BlossomLimitConfig struct {
	Uploads LimitConfig `mapstructure:"uploads"`
	// Downloads is every other Blossom request: GET, HEAD, DELETE.
	Downloads LimitConfig `mapstructure:"downloads"`
}

// LimitConfig is one action's limits, each counted on its own.
type LimitConfig struct {
	PerPubkey RateConfig `mapstructure:"perPubkey"`
	PerIP     RateConfig `mapstructure:"perIP"`
}

// RateConfig allows Events per Per, up to Burst at once (default Events).
type RateConfig struct {
	Events int    `mapstructure:"events"`
	Per    string `mapstructure:"per"`
	Burst  int    `mapstructure:"burst"`
}

const (
	exemptNone    = "none"
	exemptMembers = "members"
)

func limitsError(input, format string, args ...any) error {
	return &common.CLIError{Err: fmt.Errorf(format, args...), Code: common.CodeInvalidInput, Input: input}
}

// rate converts r, validated by check.
func (r RateConfig) rate() ratelimit.Rate {
	per, _ := time.ParseDuration(r.Per)
	return ratelimit.Rate{Events: r.Events, Per: per, Burst: r.Burst}
}

func (r RateConfig) check(key string) error {
	if r.Events < 0 {
		return limitsError(key+".events", "%s.events must be >= 0", key)
	}
	if r.Burst < 0 {
		return limitsError(key+".burst", "%s.burst must be >= 0", key)
	}
	if r.Events == 0 && (r.Per != "" || r.Burst != 0) {
		return limitsError(key+".events", "%s sets per or burst without events", key)
	}
	if r.Events > 0 && r.Per == "" {
		return limitsError(key+".per", "%s.events needs per (such as 1m)", key)
	}
	if r.Per != "" {
		d, err := time.ParseDuration(r.Per)
		if err != nil || d <= 0 {
			return limitsError(r.Per, "%s.per must be a positive duration such as 1m, not %q", key, r.Per)
		}
	}
	// What the limiter can't count, it refuses: a token finer than 1ns, or
	// a burst whose window overflows. Asking it keeps the two in step.
	if _, err := ratelimit.New(ratelimit.Config{Event: ratelimit.Limit{PerIP: r.rate()}}); err != nil {
		if rate := r.rate(); rate.Per/time.Duration(rate.Events) <= 0 {
			return limitsError(key+".events", "%s: %d events per %s is under 1ns each", key, r.Events, r.Per)
		}
		return limitsError(key+".burst", "%s.burst %d is too large for %d events per %s", key, r.Burst, r.Events, r.Per)
	}
	return nil
}

func (l LimitConfig) check(key string) error {
	if err := l.PerPubkey.check(key + ".perPubkey"); err != nil {
		return err
	}
	return l.PerIP.check(key + ".perIP")
}

func (l LimitConfig) limit() ratelimit.Limit {
	return ratelimit.Limit{PerPubkey: l.PerPubkey.rate(), PerIP: l.PerIP.rate()}
}

func (l LimitConfig) set() bool { return l.PerPubkey.Events > 0 || l.PerIP.Events > 0 }

// checkLimits validates c.Limits.
func checkLimits(c *RelayConfig) error {
	l := c.Limits
	if l == nil {
		return nil
	}
	for _, lim := range []struct {
		key string
		LimitConfig
	}{
		{"limits.events", l.Events},
		{"limits.reads", l.Reads},
		{"limits.http", l.HTTP},
		{"limits.blossom.uploads", l.Blossom.Uploads},
		{"limits.blossom.downloads", l.Blossom.Downloads},
	} {
		if err := lim.check(lim.key); err != nil {
			return err
		}
	}
	if l.ConnectionsPerIP < 0 {
		return limitsError("limits.connectionsPerIP", "limits.connectionsPerIP must be >= 0")
	}
	switch l.Exempt {
	case "", exemptNone, exemptMembers:
	default:
		return limitsError(l.Exempt, "limits.exempt must be none or members, not %q", l.Exempt)
	}
	for _, p := range l.TrustedProxies {
		if _, err := ratelimit.New(ratelimit.Config{TrustedProxies: []string{p}}); err != nil {
			return limitsError(p, "limits.trustedProxies: %q is not an IP or CIDR", p)
		}
	}
	return nil
}

// limitsSet reports whether c limits anything at all.
func (c *RelayConfig) limitsSet() bool {
	l := c.Limits
	if l == nil {
		return false
	}
	return l.Events.set() || l.Reads.set() || l.HTTP.set() || l.Blossom.Uploads.set() || l.Blossom.Downloads.set() || l.ConnectionsPerIP > 0
}

// noLimitsWarning is the warning for a public relay that limits nothing.
const noLimitsWarning = "no rate limits configured (limits:)"

// Warnings for a per-pubkey limit that is set but never (or hardly ever)
// applies.
const (
	downloadsPerPubkeyWarning = "limits.blossom.downloads.perPubkey counts only DELETEs: downloads are public (blossom.membersOnly.download is false), so a GET or HEAD has no signer to count"
	uploadsPerPubkeyWarning   = "limits.blossom.uploads.perPubkey never applies: only members may upload and limits.exempt is members"
	eventsPerPubkeyWarning    = "limits.events.perPubkey never applies: only members may write (nip11.limitation.membership_required) and limits.exempt is members"
)

// limitsWarnings are the warnings c's rate limits deserve, for --check and
// startup alike: a relay anyone may write to without logging in, with no
// limits, takes floods as fast as they come; and a per-pubkey limit no
// request is ever counted against limits nothing while looking like it
// does.
func (c *RelayConfig) limitsWarnings() []string {
	warnings := []string{}
	if !c.Nip11.Limitation.AuthRequired && !c.limitsSet() {
		warnings = append(warnings, noLimitsWarning)
	}
	l := c.Limits
	if l == nil {
		return warnings
	}
	exemptMembersOn := l.Exempt == exemptMembers
	if c.Blossom != nil && c.Blossom.Enabled {
		if l.Blossom.Downloads.PerPubkey.Events > 0 && !c.Blossom.MembersOnly.Download {
			warnings = append(warnings, downloadsPerPubkeyWarning)
		}
		if l.Blossom.Uploads.PerPubkey.Events > 0 && exemptMembersOn && c.blossomUploadMembersOnly() {
			warnings = append(warnings, uploadsPerPubkeyWarning)
		}
	}
	// An agent's NIP-AA membership is its session's, not the member list
	// the exemption reads, so with agent_auth its writes are still counted.
	agents := c.AgentAuth != nil && c.AgentAuth.Enabled
	if l.Events.PerPubkey.Events > 0 && exemptMembersOn && c.Nip11.Limitation.MembershipRequired && !agents {
		warnings = append(warnings, eventsPerPubkeyWarning)
	}
	return warnings
}

// rateLimitConfig maps c.Limits to the limiter's config. exempt is used
// when limits.exempt is members; onRefuse, when set, is told of each
// refusal. ok is false when c limits nothing.
func (c *RelayConfig) rateLimitConfig(exempt func(pubkey string) bool, onRefuse func(action, by string)) (cfg ratelimit.Config, ok bool) {
	if !c.limitsSet() {
		return ratelimit.Config{}, false
	}
	l := c.Limits
	cfg = ratelimit.Config{
		Event:          l.Events.limit(),
		Read:           l.Reads.limit(),
		HTTP:           l.HTTP.limit(),
		Upload:         l.Blossom.Uploads.limit(),
		Download:       l.Blossom.Downloads.limit(),
		MaxConnsPerIP:  l.ConnectionsPerIP,
		TrustedProxies: l.TrustedProxies,
		OnRefuse:       onRefuse,
	}
	if l.Exempt == exemptMembers {
		cfg.Exempt = exempt
	}
	return cfg, true
}

// newRateLimiter builds the one limiter the WebSocket, the HTTP bridge,
// Blossom and huddles share, or nil when c limits nothing. onRefuse counts
// refusals (metrics.RateLimited).
func newRateLimiter(c *RelayConfig, exempt func(pubkey string) bool, onRefuse func(action, by string)) (*ratelimit.Limiter, error) {
	cfg, ok := c.rateLimitConfig(exempt, onRefuse)
	if !ok {
		return nil, nil
	}
	return ratelimit.New(cfg)
}
