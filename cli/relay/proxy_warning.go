package relay

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"

	"github.com/rs/zerolog/log"
)

// maxUntrustedProxyWarnings caps how many peers untrustedProxyWarner names,
// so a client writing its own X-Forwarded-For from many addresses can't
// fill the log.
const maxUntrustedProxyWarnings = 8

// untrustedProxyWarner warns, once per peer, when a request carries
// X-Forwarded-For or X-Real-IP from a peer limits.trustedProxies doesn't
// list. The limiter then counts that peer, not the client it names: behind
// a reverse proxy missing from trustedProxies, every client shares one
// per-IP limit. Startup can only guess at this (loopbackProxyUncounted);
// here the proxy shows itself.
type untrustedProxyWarner struct {
	trusted []netip.Prefix
	warn    func(msg string)

	mu     sync.Mutex
	warned map[netip.Addr]bool
}

// newUntrustedProxyWarner returns the warner for c, or nil when c limits
// nothing per IP, where who a request comes from counts for nothing.
func newUntrustedProxyWarner(c *RelayConfig) *untrustedProxyWarner {
	l := c.Limits
	if l == nil || !l.perIPSet() {
		return nil
	}
	w := &untrustedProxyWarner{
		warn:   func(msg string) { log.Warn().Msg(msg) },
		warned: map[netip.Addr]bool{},
	}
	for _, p := range l.TrustedProxies {
		if prefix, err := parseTrustedProxy(p); err == nil {
			w.trusted = append(w.trusted, prefix)
		}
	}
	return w
}

// untrustedProxyWarning is the warning for peer forwarding header.
func untrustedProxyWarning(header string, peer netip.Addr) string {
	return fmt.Sprintf("requests carry %s from %s, which is not in limits.trustedProxies: every client behind it shares one per-IP limit", header, peer)
}

// wrap checks every request before next serves it. A nil warner is next.
func (w *untrustedProxyWarner) wrap(next http.Handler) http.Handler {
	if w == nil {
		return next
	}
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.check(r)
		next.ServeHTTP(rw, r)
	})
}

func (w *untrustedProxyWarner) check(r *http.Request) {
	header := ""
	switch {
	case r.Header.Get("X-Forwarded-For") != "":
		header = "X-Forwarded-For"
	case r.Header.Get("X-Real-IP") != "":
		header = "X-Real-IP"
	default:
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return
	}
	peer = peer.Unmap().WithZone("")
	for _, p := range w.trusted {
		if p.Contains(peer) {
			return
		}
	}
	w.mu.Lock()
	first := !w.warned[peer] && len(w.warned) < maxUntrustedProxyWarnings
	if first {
		w.warned[peer] = true
	}
	w.mu.Unlock()
	if first {
		w.warn(untrustedProxyWarning(header, peer))
	}
}
