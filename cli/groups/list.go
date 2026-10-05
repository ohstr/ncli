package groups

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip29"
	"github.com/spf13/cobra"
)

// defaultQueryTimeout bounds every groups read command's wait for a
// response -- a slow relay shouldn't hang the command indefinitely.
const defaultQueryTimeout = 10 * time.Second

// groupSummary is "groups list"'s one row per group, in both its text and
// --json shapes.
type groupSummary struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	About   string `json:"about,omitempty"`
	Private bool   `json:"private"`
	Closed  bool   `json:"closed"`
}

func newListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the groups a relay knows about",
		Long: `Reads the relay's own mirrored kind:39000 metadata events -- a plain
read, no identity needed. A private group (the default on creation) is
invisible to an anonymous connection; see "ncli groups --help" for why
that's not a bug in this command.`,
		Example: `  ncli groups list
  ncli groups list --relay wss://relay.example`,
		Args: common.NoArgs,
		RunE: runList,
	}

	cmd.Flags().Duration("timeout", defaultQueryTimeout, "Query timeout")

	return cmd
}

func runList(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	relayFlag, _ := cmd.Flags().GetString("relay")
	relayURL, err := resolveRelay(cmd, relayFlag)
	if err != nil {
		return err
	}

	targets, err := client.TargetsFromRelayList([]string{relayURL.String()})
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	filters := nip01.NewSubscriptionFilterGroup(nip01.NewFilter().WithKinds(nip29.KindGroupMetadata))
	timeout, _ := cmd.Flags().GetDuration("timeout")

	var events []*nip01.Event
	err = common.WithSpinner(cmd, fmt.Sprintf("Listing groups on %s", relayURL.Host), func() error {
		var qErr error
		events, qErr = client.QueryTargets(ctx, targets, filters, timeout)
		return qErr
	})
	if err != nil {
		if errors.Is(err, client.ErrNoReachableTargets) {
			return common.NetworkError(cmd, relayURL.String(), err)
		}
		return common.RuntimeError(cmd, err)
	}

	var groupList []groupSummary
	for _, ev := range events {
		meta, perr := nip29.ParseGroupMetadata(ev)
		if perr != nil {
			continue
		}
		groupList = append(groupList, groupSummary{
			ID: meta.ID, Name: meta.Name, About: meta.About,
			Private: meta.Private, Closed: meta.Closed,
		})
	}

	jsonMode, _ := cmd.Flags().GetBool("json")
	if jsonMode {
		if groupList == nil {
			groupList = []groupSummary{}
		}
		common.PrintJSON(map[string]any{"groups": groupList})
		return nil
	}

	if len(groupList) == 0 {
		fmt.Println("(no groups found -- a private group is invisible to an anonymous connection)")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tVISIBILITY\tMEMBERSHIP")
	for _, g := range groupList {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", g.ID, dashIfEmpty(g.Name), visibilityLabel(g.Private), membershipLabel(g.Closed))
	}
	return tw.Flush()
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func visibilityLabel(private bool) string {
	if private {
		return "private"
	}
	return "public"
}

func membershipLabel(closed bool) string {
	if closed {
		return "closed"
	}
	return "open"
}
