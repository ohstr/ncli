package ncli

import (
	"errors"
	"fmt"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip65"
	"github.com/ohstr/nmilat/nipB7"
	"github.com/spf13/cobra"
)

// kindContacts is NIP-02's contact list. nmilat has named constants for the
// other three kinds profile reads but not this one, so it's spelled out here
// rather than left as a bare 3 at the call site.
const kindContacts = 3

// profileView is everything a profile lookup gathered, and the shape --json
// emits. Each section is independently optional: relays/blossom/following are
// absent when the identity never published that record, which is not an error
// and is rendered as "not published" rather than as an empty list.
type profileView struct {
	Npub      string                  `json:"npub"`
	PubKeyHex string                  `json:"pubkey"`
	Metadata  *common.ProfileMetadata `json:"metadata,omitempty"`
	Nip05     *nip05Status            `json:"nip05,omitempty"`
	Following *int                    `json:"following,omitempty"`
	Relays    []profileRelay          `json:"relays,omitempty"`
	Blossom   []string                `json:"blossom_servers,omitempty"`
	UpdatedAt uint64                  `json:"updated_at,omitempty"`
	Queried   int                     `json:"queried_relays"`
}

// profileRelay is one NIP-65 entry. nip65.RelayEntry carries no JSON tags,
// so emitting it directly would put Go field names (URL/Read/Write) in the
// --json contract; this fixes the wire shape independently of the library's
// Go shape.
type profileRelay struct {
	URL   string `json:"url"`
	Read  bool   `json:"read"`
	Write bool   `json:"write"`
}

// nip05Status is the outcome of checking a profile's claimed nip-05 address
// against the domain that would have to vouch for it.
type nip05Status struct {
	Address  string `json:"address"`
	Verified bool   `json:"verified"`
	// Error is set when the lookup itself couldn't complete (DNS, HTTP,
	// malformed document) -- distinct from a lookup that succeeded and
	// disagreed, which is Verified:false with no Error.
	Error string `json:"error,omitempty"`
}

func newProfileCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile <identifier>",
		Short: "Display an identity's published profile",
		Long: `Display the profile metadata, following count, relay list, Blossom
servers and lightning address published by an identity. Records are
merged across all targets.`,
		Example: `  ncli profile npub1...
  ncli profile name@example.com
  ncli profile satoshi --json`,
		Args: common.ExactArgs(1),
		RunE: runProfile,
	}

	cmd.Flags().StringSliceP("relays", "s", nil, "Comma-separated relay URLs to query (defaults to `ncli prefs relays`)")
	cmd.Flags().Duration("timeout", 10*time.Second, "Per-relay query timeout")
	cmd.Flags().Bool("no-verify", false, "Skip the HTTPS nip-05 verification round trip")

	return cmd
}

func init() {
	RootCmd.AddCommand(newProfileCommand())
}

func runProfile(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	jsonMode, _ := cmd.Flags().GetBool("json")

	resolved, err := client.ResolveIdentifier(args[0])
	if err != nil {
		return keyresolve.ClassifyIdentifierError(cmd, args[0], err)
	}

	targets, err := profileTargets(cmd)
	if err != nil {
		return err
	}

	// One subscription for all four kinds: they're all single-event records
	// for one author, so asking for them separately would just cost four
	// round trips to say the same thing.
	filters := nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds:   []int{0, kindContacts, nip65.KindRelayListMetadata, nipB7.KindBlossomServerList},
		Authors: []string{resolved.PubKeyHex},
	})

	timeout, _ := cmd.Flags().GetDuration("timeout")

	var events []*nip01.Event
	err = common.WithSpinner(cmd, fmt.Sprintf("looking up %s", shortNpub(resolved.Npub)), func() error {
		var qErr error
		// QueryTargets, not client.Find: Find stops at the first target with
		// any match, which would miss a kind:10002 held only on a relay later
		// in the list.
		events, qErr = client.QueryTargets(ctx, targets, filters, timeout)
		return qErr
	})
	if err != nil {
		if errors.Is(err, client.ErrNoReachableTargets) {
			return common.NetworkError(cmd, "", err)
		}
		return common.RuntimeError(cmd, err)
	}

	view := buildProfileView(resolved, events, len(targets.Relays))

	if noVerify, _ := cmd.Flags().GetBool("no-verify"); !noVerify {
		view.Nip05 = verifyNip05(cmd, view)
	}

	if jsonMode {
		common.PrintJSON(view)
		return nil
	}
	renderProfile(view)
	return nil
}

// profileTargets resolves --relays, falling back to the saved prefs list --
// the same precedence find and dump use.
func profileTargets(cmd *cobra.Command) (*client.TargetsSpec, error) {
	relays, _ := cmd.Flags().GetStringSlice("relays")
	if len(relays) > 0 {
		targets, err := client.TargetsFromRelayList(relays)
		if err != nil {
			return nil, common.InvalidInputError(cmd, strings.Join(relays, ","), err)
		}
		return targets, nil
	}
	targets, err := client.TargetsFromPrefs()
	if err != nil {
		return nil, common.NotFoundError(cmd, "", err)
	}
	return targets, nil
}

// buildProfileView reduces the raw event soup to one record per kind, newest
// wins. A relay returning a stale copy of a replaceable event alongside a
// fresh one from elsewhere is normal, which is why this compares CreatedAt
// rather than trusting arrival order.
func buildProfileView(resolved *client.IdentityInspection, events []*nip01.Event, queried int) *profileView {
	view := &profileView{Npub: resolved.Npub, PubKeyHex: resolved.PubKeyHex, Queried: queried}

	newest := map[int]*nip01.Event{}
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if cur, ok := newest[ev.Kind]; !ok || ev.CreatedAt > cur.CreatedAt {
			newest[ev.Kind] = ev
		}
	}

	if ev := newest[0]; ev != nil {
		if meta, err := common.ParseProfileMetadata(ev.Content); err == nil {
			view.Metadata = meta
			if meta.Nip05 != "" {
				view.Nip05 = &nip05Status{Address: meta.Nip05}
			}
		}
		view.UpdatedAt = ev.CreatedAt
	}

	if ev := newest[kindContacts]; ev != nil {
		n := 0
		for _, tag := range ev.Tags {
			if len(tag) > 0 && tag[0] == "p" {
				n++
			}
		}
		view.Following = &n
	}

	if ev := newest[nip65.KindRelayListMetadata]; ev != nil {
		if list, err := nip65.ParseRelayList(ev); err == nil {
			for _, r := range list.Relays {
				view.Relays = append(view.Relays, profileRelay{URL: r.URL, Read: r.Read, Write: r.Write})
			}
		}
	}

	if ev := newest[nipB7.KindBlossomServerList]; ev != nil {
		if list, err := nipB7.ParseBlossomServerList(ev); err == nil {
			view.Blossom = list.Servers
		}
	}

	return view
}

// verifyNip05 re-resolves the profile's claimed nip-05 address and checks it
// points back at this pubkey. A lookup that can't complete is reported as
// such rather than as a failed verification -- an unreachable domain says
// nothing about whether the claim is true.
func verifyNip05(cmd *cobra.Command, view *profileView) *nip05Status {
	if view.Nip05 == nil || view.Nip05.Address == "" {
		return view.Nip05
	}
	status := &nip05Status{Address: view.Nip05.Address}
	_ = common.WithSpinner(cmd, fmt.Sprintf("verifying %s", status.Address), func() error {
		hex, err := client.ResolveNip05(status.Address)
		if err != nil {
			status.Error = err.Error()
			return nil
		}
		status.Verified = strings.EqualFold(hex, view.PubKeyHex)
		return nil
	})
	return status
}

func shortNpub(npub string) string {
	if len(npub) <= 20 {
		return npub
	}
	return npub[:12] + "…" + npub[len(npub)-4:]
}
