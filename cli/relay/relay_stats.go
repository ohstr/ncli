package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
	"github.com/rs/zerolog/log"
)

// relayStatsPublisher publishes the relay's NIP-66 kind 30166 about itself
// (cache.relay.stats), signed with nip11.privkey. Each tick it rebuilds the
// event only when the store or the NIP-11 document changed, and stores it
// only when a tag or the content differs.
type relayStatsPublisher struct {
	store    *relay.EventStore
	privkey  string
	url      string
	interval time.Duration
	// info is the current NIP-11 document (with its supported NIPs) and the
	// NIP-86 kind rules.
	info     func() (md nip11.Metadata, allowed, disallowed []int)
	profiles bool
	zaps     *relay.ZapRankingConfig

	ticked      bool
	lastCommits uint64
	lastInfo    string
}

func newRelayStatsPublisher(store *relay.EventStore, info func() (nip11.Metadata, []int, []int)) *relayStatsPublisher {
	interval, _ := relayStatsInterval(&config) // validated by initConfig
	p := &relayStatsPublisher{
		store: store, privkey: config.Nip11.PrivKey, url: config.Nip11.URL, interval: interval, info: info,
		profiles: config.searchEnabled() || config.profileStatsEnabled(),
	}
	if config.zapsEnabled() {
		z, _ := zapRanking(&config)
		p.zaps = &z
	}
	return p
}

// run publishes now and then every interval until ctx ends.
func (p *relayStatsPublisher) run(ctx context.Context) {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		if _, err := p.tick(ctx); err != nil && ctx.Err() == nil {
			log.Warn().Err(err).Msg("failed to publish the relay's NIP-66 stats")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// tick publishes when something changed; it reports whether it stored a
// new event.
func (p *relayStatsPublisher) tick(ctx context.Context) (bool, error) {
	md, allowed, disallowed := p.info()
	infoJSON, err := json.Marshal(md)
	if err != nil {
		return false, err
	}
	commits := p.store.Commits()
	if p.ticked && commits == p.lastCommits && string(infoJSON) == p.lastInfo {
		return false, nil
	}
	counts := p.store.Counts()
	if !counts.Ready {
		// Partial numbers while an older store is being counted.
		return false, nil
	}
	d := relay.RelayDiscovery{
		URL: p.url, Info: string(infoJSON), Metadata: &md,
		AllowedKinds: allowed, DisallowedKinds: disallowed,
		Counts: counts, Profiles: p.profiles, Zaps: p.zaps,
	}
	if p.zaps != nil {
		if d.ZapTotals, err = p.store.ZapTotals(ctx, *p.zaps); err != nil {
			return false, err
		}
	}
	ev, err := d.Event()
	if err != nil {
		return false, err
	}
	published, err := p.store.PublishSelfSigned(ctx, p.privkey, ev)
	if err != nil {
		return false, err
	}
	p.ticked, p.lastCommits, p.lastInfo = true, commits, string(infoJSON)
	return published, nil
}
