package blossom

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipB7"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func newServersCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "servers",
		Short: "Manage the default Blossom server list",
		Long:  `Manage the server list "ncli blossom" commands fall back to when not given explicit --server flags.`,
		RunE:  common.RequireSubcommand,
	}

	cmd.AddCommand(newServersAddCommand())
	cmd.AddCommand(newServersRemoveCommand())
	cmd.AddCommand(newServersListCommand())
	cmd.AddCommand(newServersDiscoverCommand())

	return cmd
}

// requirePublishIdentity checks, in an Args validator (i.e. before RunE
// runs), that --identity is present whenever --publish is -- catching a
// missing --identity before any prefs mutation happens, rather than after
// (a prior version of add checked this mid-RunE, so a missing --identity
// left the server already added to prefs.yaml before the command failed).
func requirePublishIdentity(cmd *cobra.Command, publish bool) error {
	if !publish {
		return nil
	}
	if identity, _ := cmd.Flags().GetString("identity"); identity == "" {
		return common.UsageError(cmd, fmt.Errorf("--identity is required with --publish"))
	}
	return nil
}

// publishServerList signs and publishes servers as a kind:10063 BUD-03
// server-list event to the configured Nostr relays (client.TargetsFromPrefs
// -- the ordinary relay list, distinct from the Blossom server list this
// command manages), returning the published event's ID.
func publishServerList(ctx context.Context, cmd *cobra.Command, jsonMode bool, identity string, servers []string) (eventID string, err error) {
	privKeyHex, pubKeyHex, err := resolveIdentity(cmd, jsonMode, identity)
	if err != nil {
		return "", err
	}
	event, err := nipB7.NewBlossomServerList(pubKeyHex, servers)
	if err != nil {
		return "", common.RuntimeError(cmd, err)
	}
	if err := event.Sign(privKeyHex); err != nil {
		return "", common.RuntimeError(cmd, err)
	}
	targets, err := client.TargetsFromPrefs()
	if err != nil {
		return "", common.NotFoundError(cmd, "", err)
	}
	report, err := client.PublishToTargets(ctx, targets, []*nip01.Event{event})
	if err != nil {
		return "", common.NetworkError(cmd, "", err)
	}
	if !report.AllSucceeded() {
		return "", common.NetworkError(cmd, "", fmt.Errorf("published server list to %d/%d relays", report.Succeeded, report.Attempted))
	}
	return event.ID, nil
}

func newServersAddCommand() *cobra.Command {
	var publish bool

	cmd := &cobra.Command{
		Use:   "add <server-url>",
		Short: "Add a server to the default list",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return common.UsageError(cmd, fmt.Errorf("exactly one server url is required"))
			}
			return requirePublishIdentity(cmd, publish)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			jsonMode, _ := cmd.Flags().GetBool("json")

			prefs, err := client.LoadPrefs()
			if err != nil {
				return common.RuntimeError(cmd, err)
			}

			added, err := prefs.AddBlossomServer(args[0])
			if err != nil {
				return common.InvalidInputError(cmd, args[0], err)
			}

			if added {
				if err := client.SavePrefs(prefs); err != nil {
					return common.RuntimeError(cmd, err)
				}
			}

			var publishedEventID string
			if publish {
				identity, _ := cmd.Flags().GetString("identity")
				publishedEventID, err = publishServerList(ctx, cmd, jsonMode, identity, prefs.BlossomServers)
				if err != nil {
					return err
				}
			}

			if jsonMode {
				out := map[string]any{"server": args[0], "added": added}
				if publish {
					out["published"] = publishedEventID
				}
				common.PrintJSON(out)
				return nil
			}

			if added {
				log.Info().Str("server", args[0]).Msg("added")
			} else {
				log.Info().Str("server", args[0]).Msg("already configured")
			}
			if publish {
				log.Info().Str("event", publishedEventID).Msg("published server list")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&publish, "publish", false, "Also publish the updated server list as a signed kind:10063 event (BUD-03) to your configured relays")

	return cmd
}

func newServersRemoveCommand() *cobra.Command {
	var publish bool

	cmd := &cobra.Command{
		Use:   "remove <server-url>",
		Short: "Remove a server from the default list",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return common.UsageError(cmd, fmt.Errorf("exactly one server url is required"))
			}
			return requirePublishIdentity(cmd, publish)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			jsonMode, _ := cmd.Flags().GetBool("json")

			prefs, err := client.LoadPrefs()
			if err != nil {
				return common.RuntimeError(cmd, err)
			}

			removed := prefs.RemoveBlossomServer(args[0])
			if removed {
				if err := client.SavePrefs(prefs); err != nil {
					return common.RuntimeError(cmd, err)
				}
			}

			var publishedEventID string
			if publish {
				identity, _ := cmd.Flags().GetString("identity")
				var err error
				publishedEventID, err = publishServerList(ctx, cmd, jsonMode, identity, prefs.BlossomServers)
				if err != nil {
					return err
				}
			}

			if jsonMode {
				out := map[string]any{"server": args[0], "removed": removed}
				if publish {
					out["published"] = publishedEventID
				}
				common.PrintJSON(out)
				return nil
			}
			if removed {
				log.Info().Str("server", args[0]).Msg("removed")
			} else {
				log.Info().Str("server", args[0]).Msg("not configured")
			}
			if publish {
				log.Info().Str("event", publishedEventID).Msg("published server list")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&publish, "publish", false, "Also publish the updated (shorter) server list as a signed kind:10063 event (BUD-03) to your configured relays")

	return cmd
}

func newServersListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the default servers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode, _ := cmd.Flags().GetBool("json")

			prefs, err := client.LoadPrefs()
			if err != nil {
				return common.RuntimeError(cmd, err)
			}

			if jsonMode {
				servers := prefs.BlossomServers
				if servers == nil {
					servers = []string{}
				}
				common.PrintJSON(map[string]any{"servers": servers})
				return nil
			}

			if len(prefs.BlossomServers) == 0 {
				fmt.Println("no blossom servers configured")
				return nil
			}
			for _, s := range prefs.BlossomServers {
				fmt.Println(s)
			}
			return nil
		},
	}
}

// newServersDiscoverCommand implements BUD-03 discovery of *another*
// identity's published server list -- the counterpart to add/remove/list,
// which only ever manage the local user's own list. It resolves
// <identifier> the same way --identity is resolved everywhere else in this
// package, queries the user's own configured Nostr relays (client.
// TargetsFromPrefs -- distinct from the Blossom server list this command
// family manages) for that pubkey's most recent kind:10063 event, and
// prints the servers it declares.
func newServersDiscoverCommand() *cobra.Command {
	var add bool

	cmd := &cobra.Command{
		Use:   "discover <identifier>",
		Short: "Discover another identity's published Blossom server list (BUD-03)",
		Long: `Resolves <identifier> (vault label/nsec/npub/hex pubkey/nprofile/nip-05)
to a pubkey, then queries your configured Nostr relays for that pubkey's
most recent kind:10063 server-list event, and prints the servers it
declares.

Unlike "servers add/remove/list", which manage your own default list,
this looks up someone else's published servers.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			jsonMode, _ := cmd.Flags().GetBool("json")

			resolved, err := client.ResolveIdentifier(args[0])
			if err != nil {
				return keyresolve.ClassifyIdentifierError(cmd, args[0], err)
			}

			targets, err := client.TargetsFromPrefs()
			if err != nil {
				return common.NotFoundError(cmd, "", err)
			}

			filters := nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
				Kinds:   []int{nipB7.KindBlossomServerList},
				Authors: []string{resolved.PubKeyHex},
				Limit:   1,
			})

			timeout, _ := cmd.Flags().GetDuration("timeout")
			events, err := client.QueryTargets(ctx, targets, filters, timeout)
			if err != nil {
				if errors.Is(err, client.ErrNoReachableTargets) {
					return common.NetworkError(cmd, "", err)
				}
				return common.RuntimeError(cmd, err)
			}

			winner := latestBlossomServerList(events)
			if winner == nil {
				return common.NotFoundError(cmd, resolved.Npub, fmt.Errorf("no published blossom server list (kind:%d) found for %s", nipB7.KindBlossomServerList, resolved.Npub))
			}

			if err := nipB7.ValidateBlossomServerList(winner); err != nil {
				return common.NetworkError(cmd, resolved.Npub, fmt.Errorf("invalid server list event received from a relay: %w", err))
			}
			serverList, err := nipB7.ParseBlossomServerList(winner)
			if err != nil {
				return common.NetworkError(cmd, resolved.Npub, err)
			}
			servers := serverList.Servers
			if servers == nil {
				servers = []string{}
			}

			if add && len(servers) > 0 {
				prefs, err := client.LoadPrefs()
				if err != nil {
					return common.RuntimeError(cmd, err)
				}
				var addedAny bool
				for _, s := range servers {
					wasAdded, err := prefs.AddBlossomServer(s)
					if err != nil {
						return common.InvalidInputError(cmd, s, err)
					}
					if wasAdded {
						addedAny = true
					}
				}
				if addedAny {
					if err := client.SavePrefs(prefs); err != nil {
						return common.RuntimeError(cmd, err)
					}
				}
				log.Info().Int("count", len(servers)).Bool("added", addedAny).Msg("merged discovered servers into local list")
			}

			if jsonMode {
				common.PrintJSON(map[string]any{
					"pubkey":  resolved.PubKeyHex,
					"servers": servers,
					"event":   winner.ID,
				})
				return nil
			}

			if len(servers) == 0 {
				fmt.Println("no servers listed")
				return nil
			}
			for _, s := range servers {
				fmt.Println(s)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&add, "add", false, "Also add each discovered server to your own default list (prefs.yaml)")
	cmd.Flags().Duration("timeout", defaultTimeout, "Max time to wait per relay before giving up on it (0 = wait forever)")

	return cmd
}

// latestBlossomServerList returns the event in events with the highest
// CreatedAt, or nil for an empty slice. Kind:10063 is a NIP-01 replaceable
// event (kinds 10000-19999), so a well-behaved relay should only ever
// return the latest one per (pubkey, kind) -- but this doesn't assume
// that: if more than one comes back anyway, the most recently created one
// wins.
func latestBlossomServerList(events []*nip01.Event) *nip01.Event {
	var winner *nip01.Event
	for _, ev := range events {
		if winner == nil || ev.CreatedAt > winner.CreatedAt {
			winner = ev
		}
	}
	return winner
}
