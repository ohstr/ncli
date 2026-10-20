package relay

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZapIndexFollowsTopZapped(t *testing.T) {
	prev := config
	t.Cleanup(func() { config = prev })

	config = RelayConfig{}
	require.Equal(t, "top-zapped is not enabled on this relay (cache.topZapped.enabled)", zapIndex(nil).Available())

	config = RelayConfig{Cache: &CacheConfig{TopZapped: &TopZappedConfig{Enabled: false}}}
	require.NotEmpty(t, zapIndex(nil).Available())

	config = RelayConfig{Cache: &CacheConfig{TopZapped: &TopZappedConfig{Enabled: true}}}
	require.Empty(t, zapIndex(nil).Available())
}

func TestSearchIndexUnavailableWithoutSearch(t *testing.T) {
	prev := config
	t.Cleanup(func() { config = prev })

	config = RelayConfig{}
	require.Equal(t, "search is not enabled on this relay (cache.search.enabled)", searchIndex(nil, nil, nil).Available())
}
