package common

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigureLogging_JSONFileAndLevel(t *testing.T) {
	prevLogger, prevCurrent := log.Logger, current
	t.Cleanup(func() {
		log.Logger, current = prevLogger, prevCurrent
		apply(current)
	})

	var buf bytes.Buffer
	ConfigureLogging(WithFileWriter(&buf), WithJSONFile(), WithLevel(zerolog.WarnLevel))
	log.Info().Msg("dropped")
	log.Warn().Str("k", "v").Msg("kept")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 1)
	var rec map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &rec))
	assert.Equal(t, "kept", rec["message"])
	assert.Equal(t, "v", rec["k"])

	// SuspendConsole keeps the level and file format.
	SuspendConsole()
	log.Info().Msg("dropped")
	assert.Len(t, strings.Split(strings.TrimSpace(buf.String()), "\n"), 1)
}
