package groups

import (
	"errors"
	"fmt"
	"os/signal"
	"sort"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip29"
	"github.com/spf13/cobra"
)

// treeResult is "groups tree"'s --json shape: every group this connection
// can see, keyed by id, plus the root ids (no parent, or a parent this
// connection can't see) in display order -- a client walks Roots and
// follows each node's own Children to render the rest, the same
// assembly NIP-29's "Subgroups" section itself describes (fetch
// {"kinds":[39000]}, build the tree locally from each group's own
// Parent tag).
type treeResult struct {
	Roots []string                 `json:"roots"`
	Nodes map[string]*groupSummary `json:"nodes"`
}

func newTreeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tree",
		Short: "Show every group as a NIP-29 Subgroups hierarchy",
		Long: `Reads every kind:39000 this connection can see (same query "groups list"
sends) and assembles the parent/child tree locally from each group's own
Parent tag -- the assembly NIP-29's "Subgroups" section itself describes,
not something the relay computes for you. A group whose parent this
connection can't see (a private parent, read anonymously) is listed as
its own root rather than silently dropped.

--identity is optional, exactly like "groups list"/"groups show": given,
it authenticates so a member sees their own private groups and
subgroups; omitted, only what's visible anonymously is shown.`,
		Example: `  ncli groups tree
  ncli groups tree --relay wss://relay.example
  ncli groups tree --identity mykey --json`,
		Args: common.NoArgs,
		RunE: runTree,
	}

	cmd.Flags().Duration("timeout", defaultQueryTimeout, "Query timeout")

	return cmd
}

func runTree(cmd *cobra.Command, args []string) error {
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

	timeout, _ := cmd.Flags().GetDuration("timeout")

	identityFlag, _ := cmd.Flags().GetString("identity")
	privKeyHex, err := resolveIdentityOptional(cmd, identityFlag)
	if err != nil {
		return err
	}

	filters := nip01.NewSubscriptionFilterGroup(nip01.NewFilter().WithKinds(nip29.KindGroupMetadata))

	var events []*nip01.Event
	err = common.WithSpinner(cmd, fmt.Sprintf("Building the group tree on %s", relayURL.Host), func() error {
		var qErr error
		events, qErr = client.QueryTargetsWithAuth(ctx, targets, filters, timeout, privKeyHex)
		return qErr
	})
	if err != nil {
		if errors.Is(err, client.ErrNoReachableTargets) {
			return common.NetworkError(cmd, relayURL.String(), err)
		}
		if errors.Is(err, client.ErrRestricted) {
			return common.AuthError(cmd, err)
		}
		return common.RuntimeError(cmd, err)
	}

	result := buildTree(events)

	jsonMode, _ := cmd.Flags().GetBool("json")
	if jsonMode {
		common.PrintJSON(result)
		return nil
	}

	if len(result.Nodes) == 0 {
		if privKeyHex == "" {
			fmt.Println("(no groups found -- a private group is invisible to an anonymous connection)")
		} else {
			fmt.Println("(no groups found)")
		}
		return nil
	}

	for _, id := range result.Roots {
		printTreeNode(result, id, 0)
	}
	return nil
}

// buildTree assembles a treeResult from every kind:39000 event seen,
// deterministically ordering Roots and each node's own Children by id so
// text-mode output is stable across runs against the same data.
func buildTree(events []*nip01.Event) treeResult {
	nodes := make(map[string]*groupSummary)
	for _, ev := range events {
		if ev.Kind != nip29.KindGroupMetadata {
			continue
		}
		meta, perr := nip29.ParseGroupMetadata(ev)
		if perr != nil {
			continue
		}
		nodes[meta.ID] = &groupSummary{
			ID: meta.ID, Name: meta.Name, About: meta.About,
			Private: meta.Private, Closed: meta.Closed,
			Parent: meta.Parent,
		}
	}

	// Children assembled from each node's own Parent, per NIP-29's own
	// recommended assembly -- not copied from the relay's own Children
	// tag, so a node whose parent this connection can't see still ends
	// up correctly listed as a root below rather than silently dropped.
	var roots []string
	for id, node := range nodes {
		if node.Parent == "" || nodes[node.Parent] == nil {
			roots = append(roots, id)
			continue
		}
		parent := nodes[node.Parent]
		parent.Children = append(parent.Children, id)
	}
	sort.Strings(roots)
	for _, node := range nodes {
		sort.Strings(node.Children)
	}

	return treeResult{Roots: roots, Nodes: nodes}
}

func printTreeNode(result treeResult, id string, depth int) {
	node := result.Nodes[id]
	if node == nil {
		return
	}
	indent := ""
	for i := 0; i < depth; i++ {
		indent += "  "
	}
	label := id
	if node.Name != "" {
		label = fmt.Sprintf("%s (%s)", id, node.Name)
	}
	fmt.Printf("%s%s [%s/%s]\n", indent, label, visibilityLabel(node.Private), membershipLabel(node.Closed))
	for _, childID := range node.Children {
		printTreeNode(result, childID, depth+1)
	}
}
