package bunker

import (
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
	"github.com/spf13/cobra"
)

// pendingView is one pending request as `bunker pending list` prints it.
type pendingView struct {
	ID        string       `json:"id"`
	App       string       `json:"app"`
	AppName   string       `json:"app_name,omitempty"`
	Method    string       `json:"method"`
	Kind      *int         `json:"kind,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	ExpiresAt time.Time    `json:"expires_at"`
	Event     *nip01.Event `json:"event,omitempty"`
}

// newPendingCommand is the scripted form of the TUI's approval dialog.
func newPendingCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pending",
		Short: "List, approve, or reject signing requests awaiting a decision",
		Long: `Requests no remembered grant covers wait here until decided or until
they expire (5 minutes).`,
		Example: `  ncli bunker pending list
  ncli bunker pending approve <id>
  ncli bunker pending approve <id> --always --for 24h
  ncli bunker pending reject <id> --always`,
		RunE: common.RequireSubcommand,
	}

	cmd.AddCommand(&cobra.Command{
		Use:     "list",
		Short:   "List requests awaiting a decision",
		Example: `  ncli bunker pending list`,
		Args:    common.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			bunkerClient, err := dialDaemon(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = bunkerClient.Close() }()

			pending, err := bunkerClient.ListPending()
			if err != nil {
				return common.RuntimeError(cmd, err)
			}
			names := map[string]string{}
			if sessions, err := bunkerClient.ListSessions(); err == nil {
				for _, s := range sessions {
					names[s.Pubkey] = s.Nickname
				}
			}
			views := make([]pendingView, 0, len(pending))
			for _, p := range pending {
				v := pendingView{
					ID: p.ID, App: p.ClientKey, AppName: names[p.ClientKey], Method: p.Method,
					CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt, Event: p.Event,
				}
				if p.Method == nip46.MethodSignEvent {
					k := p.Kind
					v.Kind = &k
				}
				views = append(views, v)
			}

			if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
				common.PrintJSON(views)
				return nil
			}
			if len(views) == 0 {
				fmt.Println("(nothing pending)")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "ID\tAPP\tMETHOD\tKIND\tEXPIRES IN")
			for _, v := range views {
				app := v.AppName
				if app == "" {
					app = shortHex(v.App)
				}
				kind := "-"
				if v.Kind != nil {
					kind = fmt.Sprint(*v.Kind)
				}
				left := time.Until(v.ExpiresAt).Round(time.Second)
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", v.ID, app, v.Method, kind, left)
			}
			return tw.Flush()
		},
	})

	approve := &cobra.Command{
		Use:   "approve <id>",
		Short: "Approve a pending request",
		Long: `Approves once by default. --always also remembers the decision for this
app: for this kind (sign_event) or method, or with --any-kind for every
non-sensitive kind; until revoked, or for --for <duration> / --uses <n>.`,
		Example: `  ncli bunker pending approve <id>
  ncli bunker pending approve <id> --always --any-kind --for 1h`,
		Args: common.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return resolvePending(cmd, args[0], Allow)
		},
	}
	approve.Flags().Bool("always", false, "Also remember this decision for the app")
	approve.Flags().Bool("any-kind", false, "With --always on sign_event: cover every non-sensitive kind, not just this one")
	approve.Flags().Duration("for", 0, "With --always: how long the grant lasts (default until revoked)")
	approve.Flags().Int("uses", 0, "With --always: how many requests the grant covers")
	approve.MarkFlagsMutuallyExclusive("for", "uses")
	cmd.AddCommand(approve)

	reject := &cobra.Command{
		Use:     "reject <id>",
		Short:   "Reject a pending request",
		Long:    `Rejects once by default; --always also rejects this method from the app from now on.`,
		Example: `  ncli bunker pending reject <id> --always`,
		Args:    common.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return resolvePending(cmd, args[0], Deny)
		},
	}
	reject.Flags().Bool("always", false, "Also remember this rejection for the app")
	cmd.AddCommand(reject)

	return cmd
}

// resolvePending builds the same remembered grant the TUI's buttons do.
func resolvePending(cmd *cobra.Command, id string, verdict Decision) error {
	always, _ := cmd.Flags().GetBool("always")
	anyKind, forDur, uses := false, time.Duration(0), 0
	if verdict == Allow {
		anyKind, _ = cmd.Flags().GetBool("any-kind")
		forDur, _ = cmd.Flags().GetDuration("for")
		uses, _ = cmd.Flags().GetInt("uses")
		if !always && (anyKind || forDur != 0 || uses != 0) {
			return common.InvocationError(cmd, errors.New("--any-kind, --for and --uses need --always"))
		}
		if forDur < 0 {
			return common.InvalidInputError(cmd, forDur.String(), errors.New("--for must be positive"))
		}
		if uses < 0 {
			return common.InvalidInputError(cmd, fmt.Sprint(uses), errors.New("--uses must be positive"))
		}
	}

	bunkerClient, err := dialDaemon(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = bunkerClient.Close() }()

	pending, err := bunkerClient.ListPending()
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	var p *Pending
	for i := range pending {
		if pending[i].ID == id {
			p = &pending[i]
			break
		}
	}
	if p == nil {
		return common.NotFoundError(cmd, id, fmt.Errorf("no pending request %q (already decided or expired?)", id))
	}

	var remember *Grant
	if always {
		now := time.Now()
		var g Grant
		if verdict == Deny {
			g = DenyAlways(p.Method, now)
		} else {
			var kind *int
			if p.Method == nip46.MethodSignEvent && !anyKind {
				k := p.Kind
				kind = &k
			}
			if kind == nil && p.Method == nip46.MethodSignEvent && sensitiveKinds[p.Kind] {
				return common.InvalidInputError(cmd, fmt.Sprint(p.Kind), fmt.Errorf("kind %d is sensitive; it can only be granted on its own, not with --any-kind", p.Kind))
			}
			switch {
			case forDur > 0:
				g = GrantForDuration(p.Method, kind, forDur, now)
			case uses > 0:
				g = GrantForUses(p.Method, kind, uses, now)
			default:
				g = GrantForever(p.Method, kind, now)
			}
		}
		remember = &g
	}

	if verdict == Allow {
		err = bunkerClient.Approve(id, remember)
	} else {
		err = bunkerClient.Reject(id, remember)
	}
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	key, word := "approved", "approved"
	if verdict == Deny {
		key, word = "rejected", "rejected"
	}
	if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
		common.PrintJSON(map[string]any{key: true, "remembered": remember != nil})
		return nil
	}
	fmt.Println(word)
	return nil
}

// newSetGrantCommand applies a grants spec to an already-paired app, like
// the TUI's Manage Grants overlay.
func newSetGrantCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-grant <pubkey>",
		Short: "Add or replace an app's permissions from a grants file",
		Long: `Uses the same "kind: bunker" YAML spec as "connect --grants". A grant
with the same scope as an existing one replaces it.`,
		Example: `  ncli bunker sessions set-grant <pubkey> --grants grants.yaml`,
		Args:    common.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, _ := cmd.Flags().GetString("grants")
			spec, err := LoadGrantSpec(path)
			if err != nil {
				return common.InvalidInputError(cmd, path, err)
			}
			grants := spec.Resolve(time.Now())
			if len(grants) == 0 {
				return common.InvalidInputError(cmd, path, errors.New("the spec declares no grants"))
			}

			bunkerClient, err := dialDaemon(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = bunkerClient.Close() }()

			sessions, err := bunkerClient.ListSessions()
			if err != nil {
				return common.RuntimeError(cmd, err)
			}
			known := false
			for _, s := range sessions {
				known = known || s.Pubkey == args[0]
			}
			if !known {
				return common.NotFoundError(cmd, args[0], fmt.Errorf("no remembered session for %q", args[0]))
			}
			for _, g := range grants {
				if err := bunkerClient.SetGrant(args[0], g); err != nil {
					return common.RuntimeError(cmd, err)
				}
			}
			if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
				common.PrintJSON(map[string]any{"updated": true, "grants": len(grants)})
				return nil
			}
			fmt.Printf("updated (%d grants)\n", len(grants))
			return nil
		},
	}
	cmd.Flags().String("grants", "", `Path to a "kind: bunker" YAML grants spec (see examples/bunker/)`)
	_ = cmd.MarkFlagRequired("grants")
	_ = cmd.MarkFlagFilename("grants", "yaml", "yml")
	return cmd
}

// dialDaemon connects to the running daemon, or fails not_found.
func dialDaemon(cmd *cobra.Command) (BunkerClient, error) {
	c, err := DialIPC(SocketPath(), 2*time.Second)
	if err != nil {
		return nil, common.NotFoundError(cmd, "", errors.New("no bunker daemon is running"))
	}
	return c, nil
}
