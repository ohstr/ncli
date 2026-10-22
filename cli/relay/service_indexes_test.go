package relay

import (
	"testing"

	"github.com/ohstr/nmilat/search"
	"github.com/stretchr/testify/require"
)

// fakeSearch is a search service searchIndex only checks for nil.
type fakeSearch struct{ search.Service }

func TestIndexAvailabilityFollowsTheCacheBlock(t *testing.T) {
	prev := config
	t.Cleanup(func() { config = prev })

	const (
		noSearch = "search is not enabled on this relay (cache.profiles.search.enabled)"
		noZaps   = "zap ranking is not enabled on this relay (cache.zaps.enabled)"
		noStats  = "stats are not enabled on this relay (cache.profiles.search.enabled or cache.profiles.stats.enabled)"
	)
	on := func(search, stats, zaps bool) RelayConfig {
		return RelayConfig{Cache: &CacheConfig{
			Profiles: &ProfilesConfig{Search: &SearchConfig{Enabled: search}, Stats: &ProfileStatsConfig{Enabled: stats}},
			Zaps:     &ZapsConfig{Enabled: zaps},
		}}
	}
	for _, c := range []struct {
		name               string
		cfg                RelayConfig
		search, zaps, stat string
	}{
		{"nothing configured", RelayConfig{}, noSearch, noZaps, noStats},
		{"everything off", on(false, false, false), noSearch, noZaps, noStats},
		{"zaps only", on(false, false, true), noSearch, "", noStats},
		{"stats only", on(false, true, false), noSearch, noZaps, ""},
		// Search scores use the stats index too.
		{"search config without a service", on(true, false, false), noSearch, noZaps, ""},
		{"everything on", on(true, true, true), noSearch, "", ""},
	} {
		config = c.cfg
		require.Equal(t, c.search, searchIndex(nil, nil).Available(), c.name)
		require.Equal(t, c.zaps, zapIndex(nil).Available(), c.name)
		require.Equal(t, c.stat, statsIndex(nil).Available(), c.name)
	}

	config = RelayConfig{Cache: &CacheConfig{Profiles: &ProfilesConfig{Search: &SearchConfig{Enabled: true, Readonly: true}}}}
	require.Equal(t, "search is read-only on this relay (cache.profiles.search.readonly)", searchIndex(nil, fakeSearch{}).Available())
}
