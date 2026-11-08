package relay

import (
	"errors"
	"net/netip"
	"strings"

	"github.com/rs/zerolog/log"
)

// warn records a config warning. Every warning initConfig finds goes
// through here rather than straight to the log, so configWarnings can list
// it in --check --json's "warnings" and the relay logs it once at startup.
func (c *RelayConfig) warn(msg string) {
	c.warnings = append(c.warnings, msg)
}

// configWarnings are all the warnings c deserves: the ones initConfig
// recorded, then those derived from the whole config. Never nil.
func (c *RelayConfig) configWarnings() []string {
	warnings := append([]string{}, c.warnings...)
	warnings = append(warnings, c.limitsWarnings()...)
	if c.loopbackProxyUncounted() {
		warnings = append(warnings, loopbackProxyWarning)
	}
	return warnings
}

// logConfigWarnings logs each of c's warnings once and returns them.
func (c *RelayConfig) logConfigWarnings() []string {
	warnings := c.configWarnings()
	for _, w := range warnings {
		log.Warn().Msg(w)
	}
	return warnings
}

// portIgnoredWarning is the warning for a port that listen overrides.
const portIgnoredWarning = "port is ignored: listen sets the relay's address"

// powStrictWithoutMinWarning is the warning for pow.strict with no minimum.
const powStrictWithoutMinWarning = "pow.strict is true but pow.min is 0 -- there is no difficulty requirement to enforce"

// loopbackProxyWarning is the warning for per-IP limits on a relay bound to
// loopback whose proxy isn't trusted.
const loopbackProxyWarning = "listen is on loopback, so clients probably arrive through a local proxy: add it to limits.trustedProxies or every client shares one per-IP limit"

// loopbackProxyUncounted reports whether c binds a loopback address, limits
// per IP, and trusts no loopback proxy: then a local reverse proxy is every
// client's IP, and they all share one per-IP bucket. listen: localhost
// binds 127.0.0.1 only (Go listens on one address of a hostname, IPv4
// first), so trusting ::1 alone does not count for it.
func (c *RelayConfig) loopbackProxyUncounted() bool {
	l := c.Limits
	if l == nil || !l.perIPSet() || c.Listen == "" {
		return false
	}
	host, _, err := splitListen(c.Listen)
	if err != nil {
		return false
	}
	var loopback netip.Addr
	if strings.EqualFold(host, "localhost") {
		loopback = netip.MustParseAddr("127.0.0.1")
	} else if addr, err := netip.ParseAddr(host); err == nil && addr.Unmap().IsLoopback() {
		loopback = addr.Unmap().WithZone("")
	} else {
		return false
	}
	for _, p := range l.TrustedProxies {
		prefix, err := parseTrustedProxy(p)
		if err == nil && prefix.Contains(loopback) {
			return false
		}
	}
	return true
}

// parseTrustedProxy reads a limits.trustedProxies entry the way nmilat's
// rate limiter does (ratelimit.parsePrefix, unexported): trimmed, without a
// zone, and unmapped, so "::ffff:127.0.0.0/104" is 127.0.0.0/8.
func parseTrustedProxy(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	addr, bits, hasBits := strings.Cut(s, "/")
	if host, _, zoned := strings.Cut(addr, "%"); zoned {
		addr = host
	}
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return netip.Prefix{}, err
	}
	if !hasBits {
		a = a.Unmap()
		return netip.PrefixFrom(a, a.BitLen()), nil
	}
	p, err := netip.ParsePrefix(addr + "/" + bits)
	if err != nil {
		return netip.Prefix{}, err
	}
	if a.Is4In6() {
		if p.Bits() < 96 {
			return netip.Prefix{}, errors.New("an IPv4-mapped prefix needs at least 96 bits")
		}
		p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
	}
	return p.Masked(), nil
}

// perIPSet reports whether l limits anything per IP.
func (l *LimitsConfig) perIPSet() bool {
	return l.Events.PerIP.Events > 0 || l.Reads.PerIP.Events > 0 || l.HTTP.PerIP.Events > 0 ||
		l.Blossom.Uploads.PerIP.Events > 0 || l.Blossom.Downloads.PerIP.Events > 0 || l.ConnectionsPerIP > 0
}
