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

func TestConfigWarnings_LoopbackProxy(t *testing.T) {
	perIP := LimitConfig{PerIP: RateConfig{Events: 5, Per: "1m"}}
	for name, c := range map[string]struct {
		listen string
		limits LimitsConfig
		want   bool
	}{
		"127.0.0.1, per-IP events":            {"127.0.0.1:7000", LimitsConfig{Events: perIP}, true},
		"::1, per-IP reads":                   {"[::1]:7000", LimitsConfig{Reads: perIP}, true},
		"localhost, connectionsPerIP":         {"localhost:7000", LimitsConfig{ConnectionsPerIP: 3}, true},
		"127.0.0.2, blossom downloads per IP": {"127.0.0.2:7000", LimitsConfig{Blossom: BlossomLimitConfig{Downloads: perIP}}, true},
		"loopback trusted by IP":              {"127.0.0.1:7000", LimitsConfig{HTTP: perIP, TrustedProxies: []string{"127.0.0.1"}}, false},
		"loopback trusted by CIDR":            {"127.0.0.1:7000", LimitsConfig{HTTP: perIP, TrustedProxies: []string{"127.0.0.0/8"}}, false},
		"::1 trusted":                         {"[::1]:7000", LimitsConfig{HTTP: perIP, TrustedProxies: []string{"::1"}}, false},
		"localhost, ::1 trusted":              {"localhost:7000", LimitsConfig{HTTP: perIP, TrustedProxies: []string{"::1"}}, true},
		"localhost, 127.0.0.1 trusted":        {"localhost:7000", LimitsConfig{HTTP: perIP, TrustedProxies: []string{"127.0.0.1"}}, false},
		"mapped loopback CIDR trusted":        {"127.0.0.1:7000", LimitsConfig{HTTP: perIP, TrustedProxies: []string{"::ffff:127.0.0.0/104"}}, false},
		"mapped loopback IP trusted":          {"127.0.0.1:7000", LimitsConfig{HTTP: perIP, TrustedProxies: []string{" ::ffff:127.0.0.1 "}}, false},
		"zoned ::1 trusted":                   {"[::1]:7000", LimitsConfig{HTTP: perIP, TrustedProxies: []string{"::1%lo"}}, false},
		"other proxy trusted":                 {"127.0.0.1:7000", LimitsConfig{HTTP: perIP, TrustedProxies: []string{"10.0.0.0/8"}}, true},
		"::1 bound, only 127.0.0.1 trusted":   {"[::1]:7000", LimitsConfig{HTTP: perIP, TrustedProxies: []string{"127.0.0.1"}}, true},
		"per-pubkey only":                     {"127.0.0.1:7000", LimitsConfig{Events: LimitConfig{PerPubkey: RateConfig{Events: 5, Per: "1m"}}}, false},
		"public address":                      {"10.0.0.5:7000", LimitsConfig{Events: perIP}, false},
		"every interface":                     {":7000", LimitsConfig{Events: perIP}, false},
		"no listen":                           {"", LimitsConfig{Events: perIP}, false},
	} {
		t.Run(name, func(t *testing.T) {
			limits := c.limits
			cfg := &RelayConfig{Listen: c.listen, Limits: &limits}
			assert.Equal(t, c.want, cfg.loopbackProxyUncounted())
			assert.Equal(t, c.want, hasWarning(cfg.configWarnings(), loopbackProxyWarning))
		})
	}
	assert.False(t, (&RelayConfig{Listen: "127.0.0.1:7000"}).loopbackProxyUncounted(), "no limits block")
}

func hasWarning(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
