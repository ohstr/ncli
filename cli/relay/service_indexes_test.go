package relay

import (
	"testing"

	"github.com/ohstr/ncli/cli/relay/jobs"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/search"
	"github.com/stretchr/testify/require"
)

// fakeSearch is a search service searchIndex only checks for nil.
type fakeSearch struct{ search.Service }

// cacheOn builds a cache block with each feature on or off.
func cacheOn(search, readonly, stats, zaps bool) RelayConfig {
	return RelayConfig{Cache: &CacheConfig{
		Profiles: &ProfilesConfig{Search: &SearchConfig{Enabled: search, Readonly: readonly}, Stats: &ProfileStatsConfig{Enabled: stats}},
		Zaps:     &ZapsConfig{Enabled: zaps},
	}}
}

func TestIndexAvailabilityFollowsTheCacheBlock(t *testing.T) {
	prev := config
	t.Cleanup(func() { config = prev })

	const (
		noSearch = "search is not enabled on this relay (cache.profiles.search.enabled)"
		noZaps   = "zap ranking is not enabled on this relay (cache.zaps.enabled)"
		noStats  = "stats are not enabled on this relay (cache.profiles.search.enabled or cache.profiles.stats.enabled)"
	)
	for _, c := range []struct {
		name               string
		cfg                RelayConfig
		search, zaps, stat string
	}{
		{"nothing configured", RelayConfig{}, noSearch, noZaps, noStats},
		{"everything off", cacheOn(false, false, false, false), noSearch, noZaps, noStats},
		{"zaps only", cacheOn(false, false, false, true), noSearch, "", noStats},
		{"stats only", cacheOn(false, false, true, false), noSearch, noZaps, ""},
		// Search scores use the stats index too.
		{"search config without a service", cacheOn(true, false, false, false), noSearch, noZaps, ""},
		// Read-only search writes no scores.
		{"read-only search", cacheOn(true, true, false, false), noSearch, noZaps, noStats},
		{"everything on", cacheOn(true, false, true, true), noSearch, "", ""},
	} {
		config = c.cfg
		require.Equal(t, c.search, searchIndex(nil, nil).Available(jobs.KindRebuild), c.name)
		require.Equal(t, c.zaps, zapIndex(nil).Available(jobs.KindRebuild), c.name)
		require.Equal(t, c.stat, statsIndex(nil).Available(jobs.KindRebuild), c.name)

		// The store keeps exactly the indexes that can be rebuilt.
		ix := config.derivedIndexes()
		require.Equal(t, c.zaps == "", ix.Zaps, c.name)
		require.Equal(t, c.stat == "", ix.Social, c.name)

		// Leftover data can be dropped whatever the feature; search can't.
		require.Empty(t, zapIndex(nil).Available(jobs.KindDrop), c.name)
		require.Empty(t, statsIndex(nil).Available(jobs.KindDrop), c.name)
		require.Equal(t, c.search, searchIndex(nil, nil).Available(jobs.KindDrop), c.name)
	}

	config = RelayConfig{Cache: &CacheConfig{Profiles: &ProfilesConfig{Search: &SearchConfig{Enabled: true, Readonly: true}}}}
	require.Equal(t, "search is read-only on this relay (cache.profiles.search.readonly)", searchIndex(nil, fakeSearch{}).Available(jobs.KindRebuild))
}

func TestDerivedIndexesFollowTheCacheBlock(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  RelayConfig
		want relay.DerivedIndexes
	}{
		{"no cache block", RelayConfig{}, relay.DerivedIndexes{}},
		{"zaps", cacheOn(false, false, false, true), relay.DerivedIndexes{Zaps: true}},
		{"stats", cacheOn(false, false, true, false), relay.DerivedIndexes{Social: true}},
		{"search", cacheOn(true, false, false, false), relay.DerivedIndexes{Social: true}},
		{"read-only search", cacheOn(true, true, false, false), relay.DerivedIndexes{}},
		{"relay stats alone", RelayConfig{Cache: &CacheConfig{Relay: &RelayCacheConfig{Stats: &RelayStatsConfig{Enabled: true}}}}, relay.DerivedIndexes{}},
		{"everything", cacheOn(true, false, true, true), relay.DerivedIndexes{Zaps: true, Social: true}},
	} {
		require.Equal(t, c.want, c.cfg.derivedIndexes(), c.name)
	}
}
