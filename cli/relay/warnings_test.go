package relay

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every config warning, the ones initConfig finds while checking and the
// ones derived from the whole config, comes out of configWarnings, which is
// what --check --json lists and startup logs.
func TestConfigWarnings_Collected(t *testing.T) {
	require.NoError(t, loadRelayEnvConfig(t, envRelayYAML+"port: 6000\nlisten: 127.0.0.1:7000\npow:\n  strict: true\n"))
	assert.Equal(t, []string{powStrictWithoutMinWarning, portIgnoredWarning, noLimitsWarning}, config.configWarnings())

	// Loading again starts over rather than piling up the earlier ones.
	require.NoError(t, loadRelayEnvConfig(t, envRelayYAML+"listen: 127.0.0.1:7000\nport: 7000\nlimits:\n  connectionsPerIP: 5\n  trustedProxies: [127.0.0.1]\n"))
	assert.Equal(t, []string{}, config.configWarnings(), "a port equal to listen's is not ignored")
}
