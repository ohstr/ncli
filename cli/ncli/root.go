// Package ncli assembles the root cobra command and its direct
// subcommands: find, miner, dump, apply, and version.
package ncli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	defaultConfigFilename = "ncli"
	defaultLogDirName     = "logs"
)

var (
	cfgFile   string
	LogWriter io.Writer
)

var RootCmd = &cobra.Command{
	Use:   "ncli",
	Short: "Nostr relay & toolkit CLI",
	Long: `A single binary for running and operating Nostr relays: serve, stream,
sync, inspect, export, delegate, administer, and mine events.`,
}

func init() {
	RootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", fmt.Sprintf("config file (default is $HOME/%s.yaml)", defaultConfigFilename))

	// --json and --quiet used to be redeclared locally, inconsistently, on
	// whichever commands happened to want them (some as --json, some as
	// -q/--quiet with subtly different semantics per command). Declaring
	// them once here, persistently, means every command and its --json
	// error reporting (see common.EmitError) get them uniformly for free.
	RootCmd.PersistentFlags().Bool("json", false, "Output structured JSON instead of text, where the command supports it; also switches error reporting to structured JSON on stderr")
	RootCmd.PersistentFlags().BoolP("quiet", "q", false, "Suppress informational narration on stderr (warnings/errors still shown)")
}

// resolveConfigFile returns the config file path to load, in priority
// order: an explicit --config flag; a saved `ncli relay context` (see
// client.Prefs), consulted only when the working directory has no
// ncli.yaml/relay.yaml of its own -- a directory's own config keeps the
// priority it had before contexts existed; or "" to let
// common.LoadViperConfig fall back to its existing cwd/home search
// unchanged. common.ActiveRelayContext is set as a side effect whenever a
// context supplies the path, so downstream "using config file" log lines
// can name it.
func resolveConfigFile() string {
	common.ActiveRelayContext = ""
	if cfgFile != "" {
		return cfgFile
	}

	cwd, _ := os.Getwd()
	if common.FindLocalConfigFile(cwd) != "" {
		return ""
	}

	prefs, err := client.LoadPrefs()
	if err != nil {
		return ""
	}
	path, ok := prefs.CurrentRelayContextPath()
	if !ok {
		return ""
	}

	common.ActiveRelayContext = prefs.CurrentRelayContext
	return path
}

// InitConfig loads viper config and sets up logging (log dir, crash log
// path, console+file writers). It's not wired up as a
// cobra.OnInitialize/PersistentPreRun here because cmd/ncli/main.go
// reparents RootCmd's subcommands onto its own root command, which would
// orphan a PersistentPreRun set on RootCmd; the caller is responsible for
// invoking this from whatever command tree actually gets executed, and for
// letting individual leaf commands (e.g. version) opt out.
func InitConfig() {
	if err := common.LoadViperConfig(resolveConfigFile()); err != nil {
		log.Warn().Err(err).Msg("config error")
	}

	viper.SetEnvPrefix("NCLI")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// Logging setup
	logDir := viper.GetString("log_dir")
	if logDir == "" {
		logDir = filepath.Join(common.AppConfigDir(), defaultLogDirName)
	}

	// Ensure log directory exists
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Error().Err(err).Msg("failed to create log directory")
		return
	}

	// Record the crash log path for RedirectStderrToCrashLog, called only
	// around the TUI's lifetime (see client.Client.run) -- redirecting
	// stderr here, for every command, would swallow cobra's own usage/error
	// output for commands that never touch the terminal via tcell.
	common.CrashLogPath = filepath.Join(logDir, "crash.log")

	LogWriter = &lumberjack.Logger{
		Filename:   filepath.Join(logDir, "ncli.log"),
		MaxSize:    100, // megabytes
		MaxBackups: 3,
		MaxAge:     28,   // days
		Compress:   true, // disabled by default
	}

	// --json/--quiet are root persistent flags, so their parsed values are
	// visible here via RootCmd's own FlagSet regardless of which
	// subcommand was actually invoked (cobra's persistent-flag inheritance
	// shares the same underlying *pflag.Flag). Applying --json centrally
	// means every command's mid-run log narration (not just its final
	// top-level failure, see common.EmitError) becomes JSON lines on
	// stderr too, instead of only the last error being structured.
	jsonMode, _ := RootCmd.PersistentFlags().GetBool("json")
	common.ConfigureLogging(common.WithConsole(), common.WithFileWriter(LogWriter), common.WithJSON(jsonMode))

	// Applying --quiet centrally, once, replaces what used to be find's
	// own one-off zerolog.SetGlobalLevel call -- now every command drops
	// info-level narration the same way.
	if quiet, _ := RootCmd.PersistentFlags().GetBool("quiet"); quiet {
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	}
}
