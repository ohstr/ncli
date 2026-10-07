package groups

import (
	"errors"
	"fmt"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip29"
	"github.com/spf13/cobra"
)

// showQueryLimit covers one copy of each of the three kinds queried, plus
// headroom for a non-compliant relay that hands back more than the single
// latest copy a parameterized-replaceable kind is supposed to have.
const showQueryLimit = 12

// groupAdmin is nip29.Admin with JSON tags (the SDK type has none).
type groupAdmin struct {
	Pubkey string   `json:"pubkey"`
	Roles  []string `json:"roles"`
}

// groupDetail is "groups show"'s --json shape; a kind that never showed up
// (e.g. a relay that doesn't publish kind:39001) is simply omitted rather
// than present-but-empty, since the spec says a client shouldn't assume
// either kind is present at all.
type groupDetail struct {
	Metadata *groupSummary `json:"metadata,omitempty"`
	Admins   []groupAdmin  `json:"admins,omitempty"`
	Members  []string      `json:"members,omitempty"`
}

func newShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <group-id>",
		Short: "Show one group's metadata, admins, and members",
		Long: `Reads the relay's own mirrored kind:39000/39001/39002 events. --identity
is optional here (unlike every write in this tree, which requires it):
given, it authenticates (NIP-42) so a member can see their own private
group; omitted, the read stays anonymous, which a private group (the
default on creation) is invisible to.`,
		Example: `  ncli groups show standup
  ncli groups show standup --identity mykey`,
		Args: common.ExactArgs(1),
		RunE: runShow,
	}

	cmd.Flags().Duration("timeout", defaultQueryTimeout, "Query timeout")

	return cmd
}

func runShow(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	groupID := args[0]

	relayFlag, _ := cmd.Flags().GetString("relay")
	relayURL, err := resolveRelay(cmd, relayFlag)
	if err != nil {
		return err
	}

	targets, err := client.TargetsFromRelayList([]string{relayURL.String()})
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	filters := nip01.NewSubscriptionFilterGroup(
		nip01.NewFilter().
			WithKinds(nip29.KindGroupMetadata, nip29.KindGroupAdmins, nip29.KindGroupMembers).
			WithTag("d", groupID).
			WithLimit(showQueryLimit),
	)
	timeout, _ := cmd.Flags().GetDuration("timeout")

	identityFlag, _ := cmd.Flags().GetString("identity")
	privKeyHex, err := resolveIdentityOptional(cmd, identityFlag)
	if err != nil {
		return err
	}

	var events []*nip01.Event
	err = common.WithSpinner(cmd, fmt.Sprintf("Querying %s for %s", relayURL.Host, groupID), func() error {
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

	// Newest first, so "first one seen per kind" below is the latest copy
	// even if a non-compliant relay handed back more than one.
	sort.Slice(events, func(i, j int) bool { return events[i].CreatedAt > events[j].CreatedAt })

	var detail groupDetail
	for _, ev := range events {
		switch ev.Kind {
		case nip29.KindGroupMetadata:
			if detail.Metadata == nil {
				if meta, perr := nip29.ParseGroupMetadata(ev); perr == nil {
					detail.Metadata = &groupSummary{
						ID: meta.ID, Name: meta.Name, About: meta.About,
						Private: meta.Private, Closed: meta.Closed,
						Parent: meta.Parent, Children: meta.Children,
					}
				}
			}
		case nip29.KindGroupAdmins:
			if detail.Admins == nil {
				if admins, perr := nip29.ParseGroupAdmins(ev); perr == nil {
					for _, a := range admins.Admins {
						detail.Admins = append(detail.Admins, groupAdmin{Pubkey: a.Pubkey, Roles: a.Roles})
					}
				}
			}
		case nip29.KindGroupMembers:
			if detail.Members == nil {
				if members, perr := nip29.ParseGroupMembers(ev); perr == nil {
					detail.Members = members.Members
				}
			}
		}
	}

	jsonMode, _ := cmd.Flags().GetBool("json")
	if jsonMode {
		common.PrintJSON(detail)
		return nil
	}

	if detail.Metadata == nil && detail.Admins == nil && detail.Members == nil {
		if privKeyHex == "" {
			fmt.Println("(nothing found -- a private group is invisible to an anonymous connection)")
		} else {
			fmt.Println("(nothing found)")
		}
		return nil
	}

	if m := detail.Metadata; m != nil {
		fmt.Printf("id:         %s\n", m.ID)
		fmt.Printf("name:       %s\n", dashIfEmpty(m.Name))
		fmt.Printf("about:      %s\n", dashIfEmpty(m.About))
		fmt.Printf("visibility: %s\n", visibilityLabel(m.Private))
		fmt.Printf("membership: %s\n", membershipLabel(m.Closed))
		fmt.Printf("parent:     %s\n", dashIfEmpty(m.Parent))
		if len(m.Children) > 0 {
			fmt.Printf("children:   %s\n", strings.Join(m.Children, ", "))
		}
	} else {
		fmt.Printf("id:         %s\n", groupID)
		fmt.Println("(no metadata found)")
	}

	if len(detail.Admins) == 0 {
		fmt.Println("admins:     (none found)")
	} else {
		fmt.Println("admins:")
		for _, a := range detail.Admins {
			fmt.Printf("  %s\t%s\n", a.Pubkey, dashIfEmpty(strings.Join(a.Roles, ",")))
		}
	}

	if len(detail.Members) == 0 {
		fmt.Println("members:    (none found)")
	} else {
		fmt.Println("members:")
		for _, p := range detail.Members {
			fmt.Printf("  %s\n", p)
		}
	}

	return nil
}
