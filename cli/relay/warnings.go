package relay

import (
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
// client's IP, and they all share one per-IP bucket.
func (c *RelayConfig) loopbackProxyUncounted() bool {
	l := c.Limits
	if l == nil || !l.perIPSet() || c.Listen == "" {
		return false
	}
	host, _, err := splitListen(c.Listen)
	if err != nil {
		return false
	}
	var loopback []netip.Addr
	if strings.EqualFold(host, "localhost") {
		loopback = []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1")}
	} else if addr, err := netip.ParseAddr(host); err == nil && addr.Unmap().IsLoopback() {
		loopback = []netip.Addr{addr.Unmap()}
	} else {
		return false
	}
	for _, p := range l.TrustedProxies {
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			addr, err := netip.ParseAddr(p)
			if err != nil {
				continue
			}
			addr = addr.Unmap()
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		for _, a := range loopback {
			if prefix.Contains(a) {
				return false
			}
		}
	}
	return true
}

// perIPSet reports whether l limits anything per IP.
func (l *LimitsConfig) perIPSet() bool {
	return l.Events.PerIP.Events > 0 || l.Reads.PerIP.Events > 0 || l.HTTP.PerIP.Events > 0 ||
		l.Blossom.Uploads.PerIP.Events > 0 || l.Blossom.Downloads.PerIP.Events > 0 || l.ConnectionsPerIP > 0
}
