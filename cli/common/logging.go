package common

import (
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/term"
)

// CrashLogPath is the path ncli's root command resolves its crash log to
// (logdir/crash.log). Set once during InitConfig; read by RedirectStderrToCrashLog
// callers that don't otherwise know the configured log directory (e.g. the
// client package, which can't import cli/ncli without an import cycle).
var CrashLogPath string

type loggingConfig struct {
	console    bool
	fileWriter io.Writer
	jsonOutput bool
}

// LoggingOption configures ConfigureLogging.
type LoggingOption func(*loggingConfig)

// WithConsole adds a colored console writer to os.Stderr. Stderr, not
// stdout, so log narration never mixes with a command's actual data output
// on stdout (e.g. find's JSON result, dump's/apply's file writes, id's
// --json output) -- the standard split between diagnostics and output that
// lets a script or an AI agent parse a command's stdout as clean data.
func WithConsole() LoggingOption {
	return func(c *loggingConfig) { c.console = true }
}

// WithFileWriter adds a plain (non-colored) copy of every log line to w.
func WithFileWriter(w io.Writer) LoggingOption {
	return func(c *loggingConfig) { c.fileWriter = w }
}

// WithJSON switches the console writer (not the file writer, which stays
// human-readable for later inspection) from zerolog's colored ConsoleWriter
// to zerolog's native JSON-lines output. Without this, --json only
// restructured a command's final top-level failure (see common.EmitError)
// -- mid-run narration from log.Info/Warn/Error calls (e.g. "target
// unreachable, skipping" from a partial query failure that doesn't fail
// the whole command) still printed as human console lines regardless of
// --json, which defeats the point for a script/agent parsing stderr.
func WithJSON(enabled bool) LoggingOption {
	return func(c *loggingConfig) { c.jsonOutput = enabled }
}

// current holds the options from the most recent ConfigureLogging call, so
// SuspendConsole/ResumeConsole can drop and restore the console writer
// without callers having to remember their own configuration.
var current loggingConfig

// ConfigureLogging points the process-global zerolog logger at the writers
// selected by opts. This is the one place ncli's CLI entrypoints (root,
// apply, relay, reindex) set up logging, instead of each hand-rolling its
// own slightly different ConsoleWriter/MultiLevelWriter -- which previously
// left color, timestamp formatting, and level handling to drift
// independently across commands. Calling it with no options is a no-op.
func ConfigureLogging(opts ...LoggingOption) {
	var cfg loggingConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	current = cfg
	apply(cfg)
}

// SuspendConsole drops the console writer while keeping any configured file
// writer, without forgetting the console setting -- callers taking over the
// terminal (e.g. the TUI's tcell screen) must call this first, since any
// concurrent console log write would corrupt the TUI's rendering. Pairs with
// ResumeConsole. A no-op if console logging isn't currently configured.
func SuspendConsole() {
	if !current.console {
		return
	}
	apply(loggingConfig{fileWriter: current.fileWriter})
}

// ResumeConsole restores the console writer dropped by SuspendConsole.
func ResumeConsole() {
	apply(current)
}

// RedirectStderrToCrashLog points fd 2 (os.Stderr) at path for as long as the
// TUI holds the terminal, so an uncaught Go runtime fatal error (concurrent
// map write, stack overflow, ...) lands in the crash log instead of
// corrupting the TUI's rendering or vanishing into the alternate screen.
// Scoped to the TUI's lifetime only -- outside of it, stderr should reach the
// real terminal so command-line usage errors are actually visible, which is
// the reason this used to live in every command's startup path and isn't
// anymore. The returned restore func points fd 2 back at the original
// stderr; call it once the TUI exits.
func RedirectStderrToCrashLog(path string) (restore func(), err error) {
	crashFile, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	restoreStderr, err := redirectStderr(crashFile)
	if err != nil {
		crashFile.Close()
		return nil, err
	}

	return func() {
		restoreStderr()
		crashFile.Close()
	}, nil
}

func apply(cfg loggingConfig) {
	var writers []io.Writer
	if cfg.console {
		if cfg.jsonOutput {
			writers = append(writers, os.Stderr)
		} else {
			// NoColor is keyed off whether stderr is an actual terminal, not
			// --json (handled above) -- otherwise a redirected/piped/
			// captured stderr in text mode (an agent showing it verbatim, a
			// log aggregator, a file) gets literal ANSI escape bytes mixed
			// into what's supposed to be a plain timestamped line. Same
			// term.IsTerminal check apply's/ping's own TUI-vs-headless
			// decision already uses (client/client.go, client/ping.go).
			writers = append(writers, zerolog.ConsoleWriter{
				Out:        os.Stderr,
				TimeFormat: time.RFC3339,
				NoColor:    !term.IsTerminal(int(os.Stderr.Fd())),
			})
		}
	}
	if cfg.fileWriter != nil {
		writers = append(writers, zerolog.ConsoleWriter{Out: cfg.fileWriter, TimeFormat: time.RFC3339, NoColor: true})
	}
	if len(writers) == 0 {
		return
	}

	w := io.Writer(zerolog.MultiLevelWriter(writers...))
	if len(writers) == 1 {
		w = writers[0]
	}
	log.Logger = zerolog.New(w).With().Timestamp().Logger()
}
