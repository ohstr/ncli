package client

import (
	"io"
	"os"
	"testing"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
)

// TestMain silences the shared zerolog logger for the whole client package
// test binary before any test runs. Code under test (the relay/nmilat SDK
// and ncli's own client package) logs through the same process-global
// zerolog singleton production code uses -- without a baseline like this,
// any test that doesn't call withTestLogging inherits zerolog's compiled-in
// default (DebugLevel, JSON to stderr) and floods `go test` output with
// logs from the functions under test rather than the tests themselves.
func TestMain(m *testing.M) {
	zerolog.SetGlobalLevel(zerolog.Disabled)
	zlog.Logger = zlog.Output(io.Discard)
	os.Exit(m.Run())
}

// withTestLogging points the shared zerolog logger at level for the duration
// of the calling test, and restores the prior global level/logger once the
// test finishes. By default (no extraWriters) the reconfigured logger still
// writes to io.Discard -- callers use this to raise the level so log calls
// actually execute (useful when paired with extraWriters below), not to make
// output appear on the terminal. Pass extraWriters (e.g. a test-owned
// in-memory buffer) when the test needs to assert on log content.
//
// zerolog's logger is a single process-global singleton -- the same one
// ncli's own CLI entrypoint (cli/ncli/root.go) configures once in main -- so
// any test that changes it must put it back, or the change leaks into every
// test that runs afterward in the same `go test` binary/process.
func withTestLogging(t *testing.T, level zerolog.Level, extraWriters ...io.Writer) {
	t.Helper()

	prevLevel := zerolog.GlobalLevel()
	prevLogger := zlog.Logger

	writers := make([]io.Writer, 0, len(extraWriters)+1)
	if len(extraWriters) == 0 {
		writers = append(writers, io.Discard)
	}
	for _, w := range extraWriters {
		writers = append(writers, zerolog.ConsoleWriter{Out: w, NoColor: true})
	}

	zerolog.SetGlobalLevel(level)
	zlog.Logger = zlog.Output(zerolog.MultiLevelWriter(writers...))

	t.Cleanup(func() {
		zerolog.SetGlobalLevel(prevLevel)
		zlog.Logger = prevLogger
	})
}
