package relay

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
)

// addContextCommands adds "context" (and its "add"/"remove"/"use"
// children) onto cmd -- the relay root command. A named context is just a
// (name -> config file path) entry in prefs.yaml (see client.Prefs); the
// one marked current is what every relay command falls back to when
// --config is omitted and the working directory has no ncli.yaml/
// relay.yaml of its own (see cli/ncli/root.go's resolveConfigFile). This
// exists so operators juggling several relays don't have to retype
// --config, or cd into a specific relay's directory, on every invocation.
func addContextCommands(cmd *cobra.Command) {
	contextCmd := &cobra.Command{
		Use:   "context",
		Short: "List or switch the current relay config context",
		Long: `Bare invocation lists saved relay contexts (name -> config file path),
marking the current one with "*". See "add", "remove", and "use" to manage
contexts -- a context is what every relay command (stats, members,
invites, roles, and "relay" itself) falls back to when --config is
omitted and the working directory has no ncli.yaml/relay.yaml of its own.`,
		Args: cobra.NoArgs,
		RunE: runContextList,
	}

	addCmd := &cobra.Command{
		Use:   "add <name> <config-path>",
		Short: "Save a named relay context",
		Long:  `Save name -> config-path in prefs.yaml. config-path must already exist.`,
		Args:  cobra.ExactArgs(2),
		RunE:  runContextAdd,
	}
	contextCmd.AddCommand(addCmd)

	removeCmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a saved relay context",
		Args:  cobra.ExactArgs(1),
		RunE:  runContextRemove,
	}
	contextCmd.AddCommand(removeCmd)

	useCmd := &cobra.Command{
		Use:   "use <name>",
		Short: "Switch the current relay context",
		Long: `Set name as the current relay context. Every relay command falls back
to this context's config file whenever --config is omitted and there's no
ncli.yaml/relay.yaml in the working directory.`,
		Args: cobra.ExactArgs(1),
		RunE: runContextUse,
	}
	contextCmd.AddCommand(useCmd)

	cmd.AddCommand(contextCmd)
}

func runContextList(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")

	prefs, err := client.LoadPrefs()
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	if jsonMode {
		contexts := prefs.RelayContexts
		if contexts == nil {
			contexts = map[string]string{}
		}
		common.PrintJSON(map[string]any{"contexts": contexts, "current": prefs.CurrentRelayContext})
		return nil
	}

	if len(prefs.RelayContexts) == 0 {
		fmt.Println("no relay contexts configured; run `ncli relay context add <name> <config-path>`")
		return nil
	}

	names := make([]string, 0, len(prefs.RelayContexts))
	for name := range prefs.RelayContexts {
		names = append(names, name)
	}
	sort.Strings(names)

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, name := range names {
		marker := " "
		if name == prefs.CurrentRelayContext {
			marker = "*"
		}
		fmt.Fprintf(tw, "%s %s\t%s\n", marker, name, prefs.RelayContexts[name])
	}
	tw.Flush()
	return nil
}

func runContextAdd(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	name, path := args[0], args[1]

	prefs, err := client.LoadPrefs()
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	abs, err := prefs.AddRelayContext(name, path)
	if err != nil {
		return common.InvalidInputError(cmd, path, err)
	}
	if err := client.SavePrefs(prefs); err != nil {
		return common.RuntimeError(cmd, err)
	}

	if jsonMode {
		common.PrintJSON(map[string]any{"name": name, "path": abs})
		return nil
	}
	fmt.Printf("added relay context %q -> %s\n", name, abs)
	return nil
}

func runContextRemove(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	name := args[0]

	prefs, err := client.LoadPrefs()
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	removed := prefs.RemoveRelayContext(name)
	if removed {
		if err := client.SavePrefs(prefs); err != nil {
			return common.RuntimeError(cmd, err)
		}
	}

	if jsonMode {
		common.PrintJSON(map[string]any{"name": name, "removed": removed})
		return nil
	}
	if removed {
		fmt.Printf("removed relay context %q\n", name)
	} else {
		fmt.Printf("no such relay context %q\n", name)
	}
	return nil
}

func runContextUse(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	name := args[0]

	prefs, err := client.LoadPrefs()
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	if err := prefs.UseRelayContext(name); err != nil {
		return common.InvalidInputError(cmd, name, err)
	}
	if err := client.SavePrefs(prefs); err != nil {
		return common.RuntimeError(cmd, err)
	}

	path := prefs.RelayContexts[name]
	if jsonMode {
		common.PrintJSON(map[string]any{"current": name, "path": path})
		return nil
	}
	fmt.Printf("switched to relay context %q (%s)\n", name, path)
	return nil
}
