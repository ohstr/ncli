package ncli

import (
	"fmt"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var prefsCmd = &cobra.Command{
	Use:     "prefs",
	Short:   "Manage persistent ncli preferences",
	Long:    `Manage the preferences ncli persists across projects.`,
	Example: `  ncli prefs relays list`,
	RunE:    common.RequireSubcommand,
}

var prefsRelaysCmd = &cobra.Command{
	Use:     "relays",
	Short:   "Manage the default relay list",
	Long:    `Manage the relay list used when a command is given no explicit targets.`,
	Example: `  ncli prefs relays list`,
	RunE:    common.RequireSubcommand,
}

var prefsRelaysAddCmd = &cobra.Command{
	Use:     "add <relay-url>",
	Short:   "Add a relay to the default list",
	Example: `  ncli prefs relays add wss://relay.example.com`,
	Args:    common.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonMode, _ := cmd.Flags().GetBool("json")

		prefs, err := client.LoadPrefs()
		if err != nil {
			return common.RuntimeError(cmd, err)
		}

		added, err := prefs.AddRelay(args[0])
		if err != nil {
			return common.InvalidInputError(cmd, args[0], err)
		}

		if added {
			if err := client.SavePrefs(prefs); err != nil {
				return common.RuntimeError(cmd, err)
			}
		}

		// --json is a documented global flag on every subcommand -- these
		// mutations used to ignore it entirely (no stdout output at all),
		// unlike every other command with a --json mode (e.g. "id --save
		// --json" reports {"saved": true/false} the same way).
		if jsonMode {
			common.PrintJSON(map[string]any{"relay": args[0], "added": added})
			return nil
		}

		if added {
			log.Info().Str("relay", args[0]).Msg("added")
		} else {
			log.Info().Str("relay", args[0]).Msg("already configured")
		}
		return nil
	},
}

var prefsRelaysRemoveCmd = &cobra.Command{
	Use:     "remove <relay-url>",
	Short:   "Remove a relay from the default list",
	Example: `  ncli prefs relays remove wss://relay.example.com`,
	Args:    common.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonMode, _ := cmd.Flags().GetBool("json")

		prefs, err := client.LoadPrefs()
		if err != nil {
			return common.RuntimeError(cmd, err)
		}

		removed := prefs.RemoveRelay(args[0])
		if removed {
			if err := client.SavePrefs(prefs); err != nil {
				return common.RuntimeError(cmd, err)
			}
		}

		if jsonMode {
			common.PrintJSON(map[string]any{"relay": args[0], "removed": removed})
			return nil
		}

		if removed {
			log.Info().Str("relay", args[0]).Msg("removed")
		} else {
			log.Info().Str("relay", args[0]).Msg("not configured")
		}
		return nil
	},
}

var prefsRelaysListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List the default relays",
	Example: `  ncli prefs relays list`,
	Args:    common.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonMode, _ := cmd.Flags().GetBool("json")

		prefs, err := client.LoadPrefs()
		if err != nil {
			return common.RuntimeError(cmd, err)
		}

		if jsonMode {
			relays := prefs.Relays
			if relays == nil {
				relays = []string{}
			}
			common.PrintJSON(map[string]any{"relays": relays})
			return nil
		}

		if len(prefs.Relays) == 0 {
			fmt.Println("no relays configured")
			return nil
		}
		for _, r := range prefs.Relays {
			fmt.Println(r)
		}
		return nil
	},
}

var prefsRelaysClearCmd = &cobra.Command{
	Use:     "clear",
	Short:   "Remove all default relays",
	Example: `  ncli prefs relays clear`,
	Args:    common.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := client.SavePrefs(&client.Prefs{}); err != nil {
			return common.RuntimeError(cmd, err)
		}

		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			common.PrintJSON(map[string]any{"cleared": true})
			return nil
		}
		log.Info().Msg("cleared")
		return nil
	},
}

var prefsPathCmd = &cobra.Command{
	Use:     "path",
	Short:   "Print the prefs.yaml file path",
	Example: `  ncli prefs path`,
	Args:    common.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := client.PrefsPath()
		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			common.PrintJSON(map[string]any{"path": path})
			return nil
		}
		fmt.Println(path)
		return nil
	},
}

func init() {
	RootCmd.AddCommand(prefsCmd)

	prefsCmd.AddCommand(prefsPathCmd)

	prefsRelaysCmd.AddCommand(prefsRelaysAddCmd)
	prefsRelaysCmd.AddCommand(prefsRelaysRemoveCmd)
	prefsRelaysCmd.AddCommand(prefsRelaysListCmd)
	prefsRelaysCmd.AddCommand(prefsRelaysClearCmd)
	prefsCmd.AddCommand(prefsRelaysCmd)
}
