package relay

import (
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogsConfig(t *testing.T) {
	const validPubkey = "bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d"

	t.Run("level and file decode", func(t *testing.T) {
		cfg := loadRelayConfigFromYAML(t, `
nip11:
  pubkey: "`+validPubkey+`"
store: "test.db"
logs:
  level: warn
  file: false
`)
		require.NotNil(t, cfg.Logs)
		assert.Equal(t, "warn", cfg.Logs.Level)
		require.NotNil(t, cfg.Logs.File)
		assert.False(t, *cfg.Logs.File)
	})

	t.Run("parseLogLevel", func(t *testing.T) {
		for in, want := range map[string]zerolog.Level{
			"debug": zerolog.DebugLevel,
			"INFO":  zerolog.InfoLevel,
			"warn":  zerolog.WarnLevel,
			"error": zerolog.ErrorLevel,
		} {
			got, err := parseLogLevel(in)
			require.NoError(t, err, in)
			assert.Equal(t, want, got, in)
		}
		_, err := parseLogLevel("trace")
		assert.Error(t, err)
	})

	t.Run("a logs block without filename falls back to logdir", func(t *testing.T) {
		prev := config
		t.Cleanup(func() { config = prev })
		dir := t.TempDir()
		config = RelayConfig{LogDir: dir}

		w := newLogWriter(LogConfig{Level: "warn"})
		assert.Equal(t, filepath.Join(dir, logFileName), w.Filename)
		assert.Equal(t, 100, w.MaxSize)
		assert.True(t, w.Compress)

		w = newLogWriter(LogConfig{Filename: "custom.log", MaxSize: 5})
		assert.Equal(t, "custom.log", w.Filename)
		assert.Equal(t, 5, w.MaxSize)
	})
}
