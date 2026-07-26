package main

import (
	"os"

	"github.com/ohstr/ncli/cli/blossom"
	"github.com/ohstr/ncli/cli/bunker"
	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/ncli"
	relaycli "github.com/ohstr/ncli/cli/relay"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "ncli",
	Short: "Nostr relay & toolkit CLI",
	Long:  `Run and operate Nostr relays, and manage events: serve, stream, sync, inspect, export, delegate, administer, and mine.`,
}

func init() {
	// Flatten ncli subcommands into the root
	for _, c := range ncli.RootCmd.Commands() {
		ncli.RootCmd.RemoveCommand(c)
		rootCmd.AddCommand(c)
	}

	// Transfer persistent flags (e.g. --config) from ncli root
	rootCmd.PersistentFlags().AddFlagSet(ncli.RootCmd.PersistentFlags())

	// Config loading and logging setup, for every command except those
	// (e.g. version) that define their own no-op PersistentPreRun to opt
	// out. Set here, not on ncli.RootCmd, since its subcommands are
	// reparented onto rootCmd above and would no longer reach it there.
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		ncli.InitConfig()
	}

	// Register the relay server command -- it mounts "stats", "reindex",
	// and "clear" as its own children, for operating a relay that's
	// already running over NIP-98 authenticated HTTP; see NewRelayCommand.
	// "delegate" is mounted under "id" instead, in cli/ncli/id.go's init().
	rootCmd.AddCommand(relaycli.NewRelayCommand())

	// Register the NIP-46 remote-signer ("bunker") command -- mounts
	// "attach"/"status"/"stop"/"sessions"/"connect" as its own children;
	// see NewBunkerCommand.
	rootCmd.AddCommand(bunker.NewBunkerCommand())

	// Register the Blossom media-server ("blossom") command -- mounts
	// "upload"/"download"/"list"/"rm"/"mirror"/"servers"/"report" as its
	// own children; see NewBlossomCommand.
	rootCmd.AddCommand(blossom.NewBlossomCommand())
}

func main() {
	// A console writer up front, so a top-level error styles the same
	// (timestamped "ERR ...") whether it comes from an early Args
	// validator (before PersistentPreRun/InitConfig ever runs) or from a
	// RunE deep in a command. InitConfig later reconfigures this with the
	// same console writer plus a file writer; this call has no side
	// effects (no mkdir, no log file) so it doesn't step on version's
	// intentional opt-out of InitConfig.
	common.ConfigureLogging(common.WithConsole())

	// ExecuteC (not Execute) so EmitError/ExitCode can inspect the
	// resolved subcommand's --json flag -- the single point where every
	// command's failure is rendered and exited, instead of each command
	// printing (and exiting) its own way.
	cmd, err := rootCmd.ExecuteC()
	if err != nil {
		common.EmitError(cmd, err)
		os.Exit(common.ExitCode(err))
	}
}
