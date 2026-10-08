package main

import (
	"os"
	"strings"

	"github.com/ohstr/ncli/cli/blossom"
	"github.com/ohstr/ncli/cli/bunker"
	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/groups"
	"github.com/ohstr/ncli/cli/huddle"
	"github.com/ohstr/ncli/cli/ncli"
	relaycli "github.com/ohstr/ncli/cli/relay"
	signercli "github.com/ohstr/ncli/cli/signer"
	"github.com/ohstr/ncli/cli/space"
	"github.com/spf13/cobra"
)

func init() {
	// Config loading and logging setup, for every command except those
	// (e.g. version) that define their own no-op PersistentPreRun to opt
	// out.
	ncli.RootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		ncli.InitConfig()
	}

	// Register the relay server command -- it mounts "stats", "reindex",
	// and "clear" as its own children, for operating a relay that's
	// already running over NIP-98 authenticated HTTP; see NewRelayCommand.
	// "delegate" is mounted under "id" instead, in cli/ncli/id.go's init().
	ncli.RootCmd.AddCommand(relaycli.NewRelayCommand())

	// Register the NIP-46 remote-signer ("bunker") command -- mounts
	// "attach"/"status"/"stop"/"sessions"/"connect" as its own children;
	// see NewBunkerCommand.
	ncli.RootCmd.AddCommand(bunker.NewBunkerCommand())

	// Register the local unix-socket signer ("signer") -- mounts
	// "serve"/"status"/"check".
	ncli.RootCmd.AddCommand(signercli.NewSignerCommand())

	// Register the Blossom media-server ("blossom") command -- mounts
	// "upload"/"download"/"list"/"rm"/"mirror"/"servers"/"report" as its
	// own children; see NewBlossomCommand.
	ncli.RootCmd.AddCommand(blossom.NewBlossomCommand())

	// Register the huddle voice-room client ("huddle") -- mounts "list" as
	// its own child, for listing the ephemeral transport rooms occupied on
	// a relay running with huddles enabled. Joining one lives under
	// "space" instead (see below); see NewHuddleCommand.
	ncli.RootCmd.AddCommand(huddle.NewHuddleCommand())

	// Register the NIP-29 relay-hosted-groups client ("groups") -- mounts
	// create/edit/delete/invite/join/leave/members/pins/delete-event/list/
	// show as its own children; see NewGroupsCommand.
	ncli.RootCmd.AddCommand(groups.NewGroupsCommand())

	// Register the generic NIP-53 meeting-space command ("space") --
	// mounts create/list/show plus huddle's own "join" (implemented in
	// cli/huddle, mounted only here) as its children; see NewSpaceCommand.
	ncli.RootCmd.AddCommand(space.NewSpaceCommand())
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
	cmd, err := ncli.RootCmd.ExecuteC()
	if err != nil {
		err = classifyRootErr(cmd, err)
		common.EmitError(cmd, err)
		os.Exit(common.ExitCode(err))
	}
}

// classifyRootErr catches the failures cobra raises inside ExecuteC before
// any Args validator or RunE runs -- an unknown flag, an unknown command, a
// required flag left unset on a command that doesn't call
// ValidateRequiredFlags itself. Left alone these come back as bare errors, so
// ExitCode fell through to CodeInternal/exit 1 and cobra printed its own
// report alongside ncli's. Routing them through InvocationError gives them
// the same "usage", exit 2, Error-then-help shape as every other
// mis-invocation.
func classifyRootErr(cmd *cobra.Command, err error) error {
	if _, ok := err.(*common.CLIError); ok {
		return err // already classified further down the call stack
	}
	// Flag parsing is what failed, so cmd's own --json is unreliable here --
	// re-scan argv for it, or a script would get a human-shaped line.
	if cmd != nil && jsonRequested(os.Args[1:]) {
		_ = cmd.Flags().Set("json", "true")
	}
	return common.InvocationError(cmd, err)
}

// jsonRequested hand-parses --json out of argv, last occurrence winning and
// stopping at a bare "--". Only used when cobra's own parse already failed.
func jsonRequested(args []string) bool {
	found := false
	for _, a := range args {
		switch {
		case a == "--":
			return found
		case a == "--json":
			found = true
		case strings.HasPrefix(a, "--json="):
			found = a[len("--json="):] == "true" || a[len("--json="):] == "1"
		}
	}
	return found
}
