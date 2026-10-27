// Package event implements `ncli event`: fetch, verify and inspect any
// Nostr event, whoever signed it. An event can be named by an nevent,
// note, naddr, hex id or kind:pubkey:d coordinate, or given in full as
// JSON (an argument, --file, or stdin). Nothing here needs a vault
// identity except "unwrap", which decrypts.
package event

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/spf13/cobra"
)

// NewEventCommand builds the `ncli event` command tree.
func NewEventCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "event",
		Short: "Fetch, verify and inspect any Nostr event",
		Long: `Work with any event, whoever signed it -- no vault identity needed.

An event is named by an nevent1/note1/naddr1 (a nostr: prefix is fine), a
hex id, or a kind:pubkey:d coordinate, or given in full: event JSON as an
argument, --file (repeatable), or stdin ("-", or piped with no argument).
Input may be one event, a JSON array, one event per line, or relay
["EVENT", ...] messages.

Relays are tried in order: the reference's own relay hints, then --relays
(or, without it, "ncli prefs relays"), then the author's NIP-65 write
relays. --only-hints, --no-hints and --no-outbox narrow that; --offline
never touches the network.`,
		Example: `  ncli event verify nevent1...
  ncli event get note1... -s wss://relay.example
  ncli event show naddr1...
  cat event.json | ncli event verify --author npub1...`,
		RunE: common.RequireSubcommand,
	}

	pf := cmd.PersistentFlags()
	pf.StringP("relays", "s", "", `Comma-separated relay URLs and/or local .db paths to look in (default: "ncli prefs relays")`)
	pf.StringArrayP("file", "f", nil, `Read events from a file (repeatable; "-" is stdin)`)
	pf.Bool("only-hints", false, "Only ask the relays named inside the nevent/naddr")
	pf.Bool("no-hints", false, "Ignore relay hints inside the nevent/naddr")
	pf.Bool("no-outbox", false, "Don't look up the author's NIP-65 write relays")
	pf.Bool("offline", false, "Never touch the network: only events given in full are read")
	pf.Duration("timeout", 10*time.Second, "Max time to wait per relay (0 = wait forever)")
	pf.String("auth-identity", "", "Log in (NIP-42) to relays that require it: bunker+unix:///path.sock, bunker://..., or a vault label/nsec (only the login is signed)")

	cmd.AddCommand(newVerifyCommand())
	cmd.AddCommand(newGetCommand())
	cmd.AddCommand(newShowCommand())
	cmd.AddCommand(newIDCommand())
	cmd.AddCommand(newNewCommand())
	cmd.AddCommand(newEncodeCommand())
	cmd.AddCommand(newSeenCommand())
	cmd.AddCommand(newRebroadcastCommand())
	cmd.AddCommand(newThreadCommand())
	cmd.AddCommand(newDiffCommand())
	cmd.AddCommand(newUnwrapCommand())
	cmd.AddCommand(newWrapCommand())
	cmd.AddCommand(newValidateCommand())

	return cmd
}

func jsonMode(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("json")
	return v
}

// printJSON writes v as indented JSON to the command's stdout.
func printJSON(cmd *cobra.Command, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return nil
}

// printShaped prints one value as an object when the input was one event,
// otherwise as a list -- the same shape rule "id sign" follows.
func printShaped[T any](cmd *cobra.Command, items []T, single bool) error {
	if single && len(items) == 1 {
		return printJSON(cmd, items[0])
	}
	if items == nil {
		items = []T{}
	}
	return printJSON(cmd, items)
}

func outf(cmd *cobra.Command, format string, a ...any) {
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), format, a...)
}
