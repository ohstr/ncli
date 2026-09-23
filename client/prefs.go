package client

import (
	"errors"
	"fmt"

	"github.com/ohstr/ncli/client/prefs"
)

// TargetsFromPrefs builds a TargetsSpec purely from the prefs relay list,
// treating every entry as a remote relay -- the fallback find/dump/miner
// check use when they aren't given an explicit --targets file or --relays.
// Resolves each entry itself (rather than via PrefsRelayURLs) so a
// schemeless entry keeps its ws:// fallback candidate, not just its
// wss:// primary.
func TargetsFromPrefs() (*TargetsSpec, error) {
	p, err := prefs.Load()
	if err != nil {
		return nil, err
	}
	if len(p.Relays) == 0 {
		return nil, errors.New("no relays configured; pass the relay(s) explicitly, or run `ncli prefs relays add <url>`")
	}

	spec := &TargetsSpec{}
	for _, r := range p.Relays {
		u, fallback, err := prefs.ResolveRelayURL(r)
		if err != nil {
			return nil, fmt.Errorf("invalid relay in prefs (%s): %w", r, err)
		}
		spec.Relays = append(spec.Relays, &FlowSpec{Type: FlOW_REMOTE, Relay: r, relayURI: u, relayFallbackURI: fallback})
	}
	return spec, nil
}

// TargetsFromRelayList builds a TargetsSpec from --relays' comma-separated
// entries: each may be a ws(s):// relay URL or a local .db path, resolved
// the same way a --targets file's bare-string entries are (see
// flowSpecFromString).
func TargetsFromRelayList(entries []string) (*TargetsSpec, error) {
	if len(entries) == 0 {
		return nil, errors.New("no relays given")
	}

	spec := &TargetsSpec{}
	for _, e := range entries {
		fs, err := flowSpecFromString(e)
		if err != nil {
			return nil, fmt.Errorf("invalid relay %q: %w", e, err)
		}
		spec.Relays = append(spec.Relays, fs)
	}
	return spec, nil
}
