package prefs

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
)

// This lives here rather than in client/spec.go because prefs owns the relay
// list and has to validate every entry going into it -- and because spec.go
// sits in a package that pulls in bbolt and the TUI, which a consumer that
// only wants to read the relay list shouldn't inherit. client re-exports
// ResolveRelayURL, so existing callers are unaffected.

// ResolveRelayURL parses a relay input into its primary connection URL and,
// when raw has no explicit ws(s):// scheme, a ws:// fallback candidate --
// wss:// is tried first (see connectRelayWithFallback/
// readEventsWithFallback), falling back to ws:// only if that fails to
// connect. An explicit scheme is taken at face value with no fallback:
// writing "ws://" or "wss://" already says exactly what's wanted.
func ResolveRelayURL(raw string) (primary *url.URL, fallback *url.URL, err error) {
	if !strings.Contains(raw, "://") {
		if !looksLikeRelayHost(raw) {
			return nil, nil, fmt.Errorf("invalid relay URL %s", raw)
		}
		u, err := url.Parse("wss://" + raw)
		if err != nil || u.Host == "" {
			return nil, nil, fmt.Errorf("invalid relay URL %s", raw)
		}
		f, err := url.Parse("ws://" + raw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid relay URL %s", raw)
		}
		return u, f, nil
	}

	uri, err := url.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid relay URL %s: %w", raw, err)
	} else if !slices.Contains([]string{"ws", "wss"}, uri.Scheme) {
		return nil, nil, fmt.Errorf("invalid relay URL %s, unsupported scheme", raw)
	} else if uri.Host == "" {
		return nil, nil, fmt.Errorf("invalid relay URL %s, empty host", raw)
	}
	return uri, nil, nil
}

// looksLikeRelayHost is ResolveRelayURL's gate on schemeless input: any
// bare string technically parses as a syntactically "valid" single-label
// URL host, which would otherwise swallow plain typos ("not-a-relay-url")
// and local store paths ("../notes.db") as relay candidates instead of
// letting them fail with a clear error / fall through to the file-path
// check in flowSpecFromString. A path separator rules out a host outright;
// otherwise this requires a dot (domain-like), "localhost", or a bare IP --
// the same shape every schemeless relay input in practice actually has.
func looksLikeRelayHost(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, `/\`) {
		return false
	}
	host := raw
	if h, _, err := net.SplitHostPort(raw); err == nil {
		host = h
	}
	return host == "localhost" || strings.Contains(host, ".") || net.ParseIP(host) != nil
}
