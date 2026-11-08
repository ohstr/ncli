package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// loadUnknownKeys loads path through the real loader and returns the keys
// the relay would report.
func loadUnknownKeys(t *testing.T, path string) []string {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	require.NoError(t, common.LoadViperConfig(path))
	var cfg RelayConfig
	unknown, err := decodeRelayConfig(viper.GetViper(), &cfg)
	require.NoError(t, err)
	return unknown
}

// Every shipped relay config (examples, deploy stacks; the Helm chart
// renders a stack's relay.yaml as is) must decode with no unknown key.
func TestShippedRelayConfigsHaveNoUnknownKeys(t *testing.T) {
	var files []string
	for _, pattern := range []string{
		"../../examples/relay/*.yaml",
		"../../deploy/stacks/*/relay.yaml",
		"../../integration/*/relay.yaml",
		"../../integration/*/stress-relay.yaml",
		"../../integration/*/relay/relay.yaml",
	} {
		m, err := filepath.Glob(pattern)
		require.NoError(t, err)
		files = append(files, m...)
	}
	require.GreaterOrEqual(t, len(files), len(scenarioExampleFiles)+4)
	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			require.Empty(t, loadUnknownKeys(t, f))
		})
	}
}

func writeRelayYAML(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "relay.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

const typoConfig = `
nip11:
  pubkey: bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d
  limitaton:
    auth_required: true
store: test.db
htpBridge:
  query:
    enabled: true
httpBridge:
  qurey:
    enabled: true
`

func TestDecodeRelayConfigReportsEveryUnknownKey(t *testing.T) {
	require.Equal(t,
		[]string{"htpbridge", "httpBridge.qurey", "nip11.limitaton"},
		loadUnknownKeys(t, writeRelayYAML(t, typoConfig)))
}

func TestDecodeRelayConfigSkipsSharedAndFreeFormKeys(t *testing.T) {
	p := writeRelayYAML(t, `
nip11:
  pubkey: bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d
store: test.db
log_dir: /tmp/logs
vault:
  password: x
signer: bunker+unix:///tmp/s.sock
groups:
  identity: alice
huddle:
  identity: alice
space:
  identity: alice
cache:
  profiles:
    scoring:
      anyName: 1
`)
	require.Empty(t, loadUnknownKeys(t, p))
}

// Only the exact shared leaf keys are exempt: the rest of the same section
// (the old flat query: block, a stray event: setting) is still reported.
func TestDecodeRelayConfigSharedKeysAreExactLeaves(t *testing.T) {
	const head = `
nip11:
  pubkey: bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d
store: test.db
`
	for _, tc := range []struct {
		body string
		want []string
	}{
		{"query:\n  enabled: true\n", []string{"query"}},
		{"query:\n  identity: alice\n", nil},
		{"query:\n  identity: alice\n  enabled: true\n", []string{"query.enabled"}},
		{"event:\n  enabled: true\n", []string{"event"}},
		{"event:\n  identity: alice\n", nil},
		{"vault:\n  password: x\n  path: y\n", []string{"vault.path"}},
		{"delegate:\n  issuer: alice\n", nil},
		{"signer:\n  identity: alice\n", nil},
	} {
		t.Run(tc.body, func(t *testing.T) {
			got := loadUnknownKeys(t, writeRelayYAML(t, head+tc.body))
			if tc.want == nil {
				require.Empty(t, got)
			} else {
				require.Equal(t, tc.want, got)
			}
		})
	}
}

func TestDecodeRelayConfigIgnoresEnv(t *testing.T) {
	t.Setenv("NCLI_HTTPBRIDGE_QUREY", "1")
	t.Setenv("NCLI_BOGUS", "1")
	p := writeRelayYAML(t, `
nip11:
  pubkey: bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d
store: test.db
`)
	viper.Reset()
	t.Cleanup(viper.Reset)
	require.NoError(t, common.LoadViperConfig(p))
	viper.SetEnvPrefix("NCLI")
	viper.AutomaticEnv()
	var cfg RelayConfig
	unknown, err := decodeRelayConfig(viper.GetViper(), &cfg)
	require.NoError(t, err)
	require.Empty(t, unknown)
}

func TestInitConfigUnknownKeys(t *testing.T) {
	prev := config
	t.Cleanup(func() { config = prev })
	p := writeRelayYAML(t, typoConfig)

	t.Run("check fails naming every key", func(t *testing.T) {
		viper.Reset()
		t.Cleanup(viper.Reset)
		require.NoError(t, common.LoadViperConfig(p))
		config = RelayConfig{}
		err := initConfig(true)
		var ce *common.CLIError
		require.True(t, errors.As(err, &ce), "%v", err)
		require.Equal(t, common.CodeInvalidInput, ce.Code)
		require.Equal(t, "unknown config keys: htpbridge, httpBridge.qurey, nip11.limitaton", ce.Error())
		require.Empty(t, ce.Input)
	})

	t.Run("check names a lone key as input", func(t *testing.T) {
		viper.Reset()
		t.Cleanup(viper.Reset)
		require.NoError(t, common.LoadViperConfig(writeRelayYAML(t, `
nip11:
  pubkey: bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d
store: test.db
prot: 1
`)))
		config = RelayConfig{}
		var ce *common.CLIError
		require.True(t, errors.As(initConfig(true), &ce))
		require.Equal(t, "prot", ce.Input)
	})

	t.Run("start warns and goes on", func(t *testing.T) {
		viper.Reset()
		t.Cleanup(viper.Reset)
		require.NoError(t, common.LoadViperConfig(p))
		config = RelayConfig{}
		var buf bytes.Buffer
		prevLog := log.Logger
		t.Cleanup(func() { log.Logger = prevLog })
		log.Logger = zerolog.New(&buf)
		require.NoError(t, initConfig(false))
		for _, k := range []string{"htpbridge", "httpBridge.qurey", "nip11.limitaton"} {
			require.Contains(t, buf.String(), `{"level":"warn","key":"`+k+`","message":"unknown config key, ignored"}`)
		}
	})
}

// -c/--context goes through the same check: --check fails, a start warns.
func TestRelayContextUnknownKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	env, xdgHome := relayContextRunTestEnv(t)
	cwd := t.TempDir()

	cfg := writeRelayYAML(t, `
nip11:
  pubkey: bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d
store: `+filepath.Join(t.TempDir(), "store.db")+`
logdir: `+t.TempDir()+`
httpBridge:
  qurey:
    enabled: true
`)
	prefsPath := filepath.Join(xdgHome, ".ncli", "prefs.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(prefsPath), 0o755))
	require.NoError(t, os.WriteFile(prefsPath, []byte("relay_contexts:\n  typo: "+cfg+"\n"), 0o644))

	cmd := exec.Command(bin, "relay", "-c", "typo", "--check", "--json")
	cmd.Env = env
	cmd.Dir = cwd
	_, err := cmd.Output()
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "%v", err)
	require.Equal(t, 3, exitErr.ExitCode())
	lines := strings.Split(strings.TrimSpace(string(exitErr.Stderr)), "\n")
	var payload struct{ Error, Code, Input string }
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &payload), "%s", exitErr.Stderr)
	require.Equal(t, "invalid_input", payload.Code)
	require.Equal(t, "httpBridge.qurey", payload.Input)

	r := startRelay(t, bin, env, cwd, "-c", "typo", "--json")
	require.Contains(t, r.Stderr.String(), `"key":"httpBridge.qurey"`)
	r.stop()
	waitPortFree(t, "localhost:5500", 5*time.Second)
}
