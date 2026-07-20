package ncli

import (
	"errors"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/spf13/cobra"
)

var (
	findCmd = &cobra.Command{
		Use:   "find [identifier]",
		Short: "Query events by ID and/or filter",
		Long: `Look up events by ID and/or filter across one or more relays or local
stores, stopping at the first target with any match.

The identifier is a positional argument, event-shaped or author-shaped:
  - event: a plain hex event ID, or a note1.../nevent1... NIP-19 string
    (an nevent's embedded relay/author/kind hints are ignored -- only its
    ID is used)
  - author: npub1..., nprofile1... (relay hints likewise ignored), or a
    nip-05 "name@domain" address -- matches that author's events, ANDed
    with any other filters given. With no other filters at all, defaults
    to just their profile (kind 0) rather than everything they've
    published -- pass --kinds explicitly to widen it.

--authors (inline, or inside --targets) also accepts nip-05 addresses
mixed freely with hex pubkeys, for combining an author with other filters
like --kinds/--limit.

Targets and filters come from --targets (a YAML file that may declare
both), or from --relays plus inline filter flags -- pick one, not a mix.
Omitting both --targets and --relays falls back to the relays configured
via "ncli prefs relays add".

Result is always a single JSON array on stdout, whether or not anything
matched -- safe to pipe into jq or parse directly. Progress and errors go
to stderr; pass --quiet to drop the progress narration too, for callers
that can't rely on the two streams being captured separately.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cmd.ValidateRequiredFlags(); err != nil {
				return common.UsageError(cmd, err)
			}
			if err := queryMutualExclusionCheck(cmd); err != nil {
				return common.UsageError(cmd, err)
			}
			if len(args) > 1 {
				return common.UsageError(cmd, fmt.Errorf("at most one identifier argument is allowed"))
			}
			if !cmd.Flags().Changed("targets") && len(args) == 0 && !inlineFilterFlagsChanged(cmd) {
				return common.UsageError(cmd, fmt.Errorf("at least one of an identifier argument, an inline filter flag, or --targets is required"))
			}
			if cmd.Flags().Changed("targets") {
				if _, err := validateArgFile(cmd, "targets", true, ".yaml", ".yml"); err != nil {
					return common.UsageError(cmd, err)
				}
			}
			if cmd.Flags().Changed("out") {
				if _, err := validateArgFile(cmd, "out", false, ".json", ".jsonp"); err != nil {
					return common.UsageError(cmd, err)
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			timeout, _ := cmd.Flags().GetDuration("timeout")

			var identifier *client.FindIdentifier
			var err error
			if len(args) == 1 {
				identifier, err = client.ResolveFindIdentifier(args[0])
				if err != nil {
					return classifyIdentifierError(cmd, args[0], err)
				}
			}

			targetsSpec, filtersSpec, err := resolveQuery(cmd)
			if err != nil {
				return common.RuntimeError(cmd, err)
			}

			if err := client.ResolveFilterAuthors(filtersSpec); err != nil {
				return common.NetworkError(cmd, "", err)
			}

			idFilter, filtersSpec := mergeFindIdentifier(identifier, filtersSpec)

			var outPath string
			if cmd.Flags().Changed("out") {
				outPath, err = validateArgFile(cmd, "out", false, ".json", ".jsonp")
				if err != nil {
					return common.RuntimeError(cmd, err)
				}
			}

			if err := client.Find(ctx, idFilter, filtersSpec, targetsSpec, outPath, timeout); err != nil {
				if errors.Is(err, client.ErrNoReachableTargets) {
					return common.NetworkError(cmd, "", err)
				}
				return common.RuntimeError(cmd, err)
			}
			return nil
		},
	}
)

func init() {
	RootCmd.AddCommand(findCmd)

	registerQueryFlags(findCmd, "")

	findCmd.Flags().StringP("out", "o", "", "Also save the result to this JSON file path")
	findCmd.MarkFlagFilename("out", "json", "jsonp")

	findCmd.Flags().Duration("timeout", 30*time.Second, "Max time to wait per target before giving up on it (0 = wait forever)")
}

// mergeFindIdentifier combines find's resolved positional identifier
// (nil if none was given) with its filters. An event ID is specific enough
// to stand alone -- it's returned as its own OR'd filter rather than merged
// into filtersSpec. An author instead needs to AND with the other filters
// (--kinds, --limit, ...) to mean what a user expects -- "this person's
// kind-1 notes," not "this person's anything, OR any kind-1 note from
// anyone" -- so it's merged into every filter (each targets.yaml OR-branch
// gets ANDed with the author). If no filters were given at all, a bare
// author-only filter would fetch every event that author has ever
// published, of every kind, unbounded -- a bare `ncli find <npub>` means
// "look this person up," so it defaults to just their profile (kind 0)
// instead. filtersSpec's backing slice/elements are mutated in place; the
// returned slice is only a different value when a fresh filter was added.
func mergeFindIdentifier(identifier *client.FindIdentifier, filtersSpec []*client.FilterSpec) (*nip01.SubscriptionFilter, []*client.FilterSpec) {
	if identifier == nil {
		return nil, filtersSpec
	}

	var idFilter *nip01.SubscriptionFilter
	if identifier.ID != "" {
		idFilter = &nip01.SubscriptionFilter{IDs: []string{identifier.ID}}
	}
	if identifier.Author != "" {
		if len(filtersSpec) == 0 {
			filtersSpec = []*client.FilterSpec{client.NewFilterSpec(&nip01.SubscriptionFilter{Kinds: []int{0}})}
		}
		for _, f := range filtersSpec {
			f.Authors = append(f.Authors, identifier.Author)
		}
	}
	return idFilter, filtersSpec
}
