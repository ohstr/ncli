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
	"github.com/ohstr/nmilat/utils"
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
		Long: `Reads the relay's own mirrored kind:39000 metadata events. --identity
is optional here (unlike every write in this tree, which requires it):
given, it authenticates (NIP-42) so a member can see their own private
group; omitted, the read stays anonymous, which a private group (the
default on creation) is invisible to.

--mine/--member narrow the listing to groups a specific pubkey belongs
to (a second kind:39002 query, "#p" tagged to that pubkey, merged against
the usual kind:39000 one) -- the "what groups am I in" shape, as opposed
to the default "what groups exist" one. --mine resolves that pubkey from
--identity, so it requires one; --member takes any pubkey directly, no
identity required (though an authenticated read is still needed to see
that pubkey's own private groups, same as the unscoped listing).`,
		Example: `  ncli groups list
  ncli groups list --relay wss://relay.example
  ncli groups list --identity mykey
  ncli groups list --identity mykey --mine
  ncli groups list --member <pubkey>`,
		Args: common.NoArgs,
		RunE: runList,
	}

	cmd.Flags().Duration("timeout", defaultQueryTimeout, "Query timeout")
	cmd.Flags().Bool("mine", false, "Only list groups --identity is a member of")
	cmd.Flags().String("member", "", "Only list groups this pubkey is a member of")
	cmd.MarkFlagsMutuallyExclusive("mine", "member")

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

	timeout, _ := cmd.Flags().GetDuration("timeout")

	identityFlag, _ := cmd.Flags().GetString("identity")
	privKeyHex, err := resolveIdentityOptional(cmd, identityFlag)
	if err != nil {
		return err
	}

	mineFlag, _ := cmd.Flags().GetBool("mine")
	memberFlag, _ := cmd.Flags().GetString("member")

	var scopeToPubkey string
	switch {
	case memberFlag != "":
		if err := utils.Validate32Key(memberFlag); err != nil {
			return common.InvalidInputError(cmd, memberFlag, err)
		}
		scopeToPubkey = memberFlag
	case mineFlag:
		if privKeyHex == "" {
			return common.UsageError(cmd, errors.New("--mine requires --identity, to know which pubkey to scope to"))
		}
		scopeToPubkey, err = client.GetPublicKey(privKeyHex)
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
	}

	filterBuilders := []*nip01.SubscriptionFilter{nip01.NewFilter().WithKinds(nip29.KindGroupMetadata)}
	if scopeToPubkey != "" {
		filterBuilders = append(filterBuilders, nip01.NewFilter().WithKinds(nip29.KindGroupMembers).WithTag("p", scopeToPubkey))
	}
	filters := nip01.NewSubscriptionFilterGroup(filterBuilders...)

	var events []*nip01.Event
	err = common.WithSpinner(cmd, fmt.Sprintf("Listing groups on %s", relayURL.Host), func() error {
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

	var memberGroupIDs map[string]bool
	if scopeToPubkey != "" {
		memberGroupIDs = make(map[string]bool)
		for _, ev := range events {
			if ev.Kind != nip29.KindGroupMembers {
				continue
			}
			if gm, perr := nip29.ParseGroupMembers(ev); perr == nil {
				memberGroupIDs[gm.ID] = true
			}
		}
	}

	var groupList []groupSummary
	for _, ev := range events {
		if ev.Kind != nip29.KindGroupMetadata {
			continue
		}
		meta, perr := nip29.ParseGroupMetadata(ev)
		if perr != nil {
			continue
		}
		if memberGroupIDs != nil && !memberGroupIDs[meta.ID] {
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
		switch {
		case scopeToPubkey != "" && privKeyHex == "":
			fmt.Println("(no groups found -- a private group that pubkey belongs to is invisible to an anonymous connection)")
		case scopeToPubkey != "":
			fmt.Println("(no groups found -- that pubkey is not a member of any group visible to this connection)")
		case privKeyHex == "":
			fmt.Println("(no groups found -- a private group is invisible to an anonymous connection)")
		default:
			fmt.Println("(no groups found)")
		}
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
