package relay

import (
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/relay/adminplane"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// addMetricsCommands adds "metrics token add/list/remove": local edits to
// the tokens file Prometheus scrapers authenticate against. They never talk
// to a running relay, which reloads the file on its own.
func addMetricsCommands(cmd *cobra.Command) {
	metricsCmd := &cobra.Command{
		Use:     "metrics",
		Short:   "Manage access to the relay's Prometheus metrics",
		Example: `  ncli relay metrics token add prometheus --config relay.yaml`,
		RunE:    common.RequireSubcommand,
	}
	tokenCmd := &cobra.Command{
		Use:   "token",
		Short: "Manage metrics bearer tokens",
		Long: `The tokens file (admin.metrics.tokensFile) holds only each token's
sha256 hash. A running relay picks up changes within a second.`,
		Example: `  ncli relay metrics token add prometheus --config relay.yaml`,
		RunE:    common.RequireSubcommand,
	}
	tokenCmd.PersistentFlags().String("file", "", "tokens file (default: admin.metrics.tokensFile from the relay config)")

	tokenCmd.AddCommand(&cobra.Command{
		Use:   "add <name>",
		Short: "Create a token and print it once",
		Example: `  ncli relay metrics token add prometheus --config relay.yaml
  ncli relay metrics token add grafana-agent --file /etc/ncli/metrics.tokens`,
		Args: common.ExactArgs(1),
		RunE: runTokenAdd,
	})
	tokenCmd.AddCommand(&cobra.Command{
		Use:     "list",
		Short:   "List token names",
		Example: `  ncli relay metrics token list --config relay.yaml`,
		Args:    common.NoArgs,
		RunE:    runTokenList,
	})
	tokenCmd.AddCommand(&cobra.Command{
		Use:     "remove <name>",
		Short:   "Revoke a token",
		Example: `  ncli relay metrics token remove prometheus --config relay.yaml`,
		Args:    common.ExactArgs(1),
		RunE:    runTokenRemove,
	})
	metricsCmd.AddCommand(tokenCmd)
	cmd.AddCommand(metricsCmd)
}

func tokensFile(cmd *cobra.Command) (string, error) {
	if f, _ := cmd.Flags().GetString("file"); f != "" {
		return f, nil
	}
	if f := viper.GetString("admin.metrics.tokensFile"); f != "" {
		return f, nil
	}
	return "", &common.CLIError{
		Err:  errors.New("no tokens file: pass --file, or set admin.metrics.tokensFile in the relay config"),
		Code: common.CodeUsage, Help: common.HelpAfterError,
	}
}

func tokenError(cmd *cobra.Command, err error, input string) error {
	var ce *adminplane.ConfigError
	switch {
	case errors.Is(err, adminplane.ErrInvalidTokenName):
		return common.RuntimeError(cmd, &common.CLIError{Err: err, Code: common.CodeInvalidInput, Input: input})
	case errors.Is(err, adminplane.ErrTokenNameTaken):
		return common.RuntimeError(cmd, &common.CLIError{Err: err, Code: common.CodeConflict, Input: input})
	case errors.Is(err, adminplane.ErrTokenNotFound):
		return common.RuntimeError(cmd, &common.CLIError{Err: err, Code: common.CodeNotFound, Input: input})
	case errors.As(err, &ce):
		return common.RuntimeError(cmd, &common.CLIError{Err: err, Code: common.CodeInvalidInput, Input: ce.Input})
	}
	return common.RuntimeError(cmd, err)
}

func runTokenAdd(cmd *cobra.Command, args []string) error {
	path, err := tokensFile(cmd)
	if err != nil {
		return err
	}
	token, err := adminplane.AddToken(path, args[0], time.Now())
	if err != nil {
		return tokenError(cmd, err, args[0])
	}
	if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
		common.PrintJSON(map[string]string{"name": args[0], "token": token, "file": path})
		return nil
	}
	fmt.Println(token)
	_, _ = fmt.Fprintf(os.Stderr, "Token %q added to %s. It is shown only now: give it to your scraper.\n", args[0], path)
	return nil
}

type tokenJSON struct {
	Name    string `json:"name"`
	Created string `json:"created"`
}

func runTokenList(cmd *cobra.Command, args []string) error {
	path, err := tokensFile(cmd)
	if err != nil {
		return err
	}
	entries, err := adminplane.LoadTokens(path)
	if err != nil {
		return tokenError(cmd, err, path)
	}
	out := make([]tokenJSON, 0, len(entries))
	for _, e := range entries {
		out = append(out, tokenJSON{Name: e.Name, Created: e.Created})
	}
	if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
		common.PrintJSON(map[string]any{"file": path, "tokens": out})
		return nil
	}
	if len(out) == 0 {
		fmt.Println(" (no tokens)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tCREATED")
	for _, t := range out {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", t.Name, t.Created)
	}
	return tw.Flush()
}

func runTokenRemove(cmd *cobra.Command, args []string) error {
	path, err := tokensFile(cmd)
	if err != nil {
		return err
	}
	if err := adminplane.RemoveToken(path, args[0]); err != nil {
		return tokenError(cmd, err, args[0])
	}
	if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
		common.PrintJSON(map[string]string{"name": args[0], "status": "removed"})
		return nil
	}
	fmt.Printf("removed token %q\n", args[0])
	return nil
}
