package ncli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/spf13/cobra"
)

// inlineFilterFlagNames lists every flag registerInlineFilterFlags adds --
// shared by inlineFilterFlagsChanged (mutual-exclusion check against
// --filters) and buildInlineFilterSpec (assembly).
var inlineFilterFlagNames = []string{"kinds", "authors", "ids", "since", "until", "limit", "search", "tag"}

// registerInlineFilterFlags adds a single-filter alternative to
// --filters <file.yaml>: every flag set here describes one filter (all
// fields ANDed together, per SubscriptionFilter's own semantics) -- unlike a
// filters file, there is no OR-across-multiple-filters concept here. Callers
// must reject combining these with --filters (see inlineFilterFlagsChanged)
// rather than merging them: FilterSpec's model already fixes two composition
// rules (OR across a file's filters, AND across one filter's fields), and
// neither matches "file plus some extra flags" cleanly enough to guess at.
func registerInlineFilterFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("kinds", "k", "", `Comma-separated event kinds, e.g. "1,7"`)
	cmd.Flags().StringP("authors", "a", "", "Comma-separated author pubkeys (hex, prefix, or nip-05)")
	cmd.Flags().StringP("ids", "i", "", "Comma-separated event IDs (hex, or a prefix)")
	cmd.Flags().String("since", "", `Absolute unix timestamp or relative duration ("1h", "1w 2d", ...), looking backward`)
	cmd.Flags().String("until", "", "Absolute unix timestamp or relative duration, looking forward")
	cmd.Flags().IntP("limit", "l", 0, "Max events to return")
	cmd.Flags().String("search", "", "NIP-50 profile search (name/about/nip05/lud16), not note content")
	cmd.Flags().StringArray("tag", nil, `Repeatable tag filter as "key=value", e.g. --tag e=<id> --tag p=<pubkey>`)
}

// inlineFilterFlagsChanged reports whether the caller set any inline filter
// flag from registerInlineFilterFlags.
func inlineFilterFlagsChanged(cmd *cobra.Command) bool {
	for _, name := range inlineFilterFlagNames {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

// buildInlineFilterSpec builds a single *client.FilterSpec from cmd's inline
// filter flags, or (nil, nil) if none were set.
func buildInlineFilterSpec(cmd *cobra.Command) (*client.FilterSpec, error) {
	if !inlineFilterFlagsChanged(cmd) {
		return nil, nil
	}

	filter := &nip01.SubscriptionFilter{}

	if v, _ := cmd.Flags().GetString("kinds"); v != "" {
		kinds, err := parseCommaSeparatedInts("kinds", v)
		if err != nil {
			return nil, common.InvalidInputError(cmd, v, err)
		}
		filter.Kinds = kinds
	}

	if v, _ := cmd.Flags().GetString("authors"); v != "" {
		filter.Authors = splitCommaSeparated(v)
	}

	if v, _ := cmd.Flags().GetString("ids"); v != "" {
		filter.IDs = splitCommaSeparated(v)
	}

	if v, _ := cmd.Flags().GetString("since"); v != "" {
		since, err := client.ParseDurationUnit(v, true)
		if err != nil {
			return nil, common.InvalidInputError(cmd, v, fmt.Errorf("invalid --since %q: %w", v, err))
		}
		filter.Since = since
	}

	if v, _ := cmd.Flags().GetString("until"); v != "" {
		until, err := client.ParseDurationUnit(v, false)
		if err != nil {
			return nil, common.InvalidInputError(cmd, v, fmt.Errorf("invalid --until %q: %w", v, err))
		}
		filter.Until = until
	}

	if cmd.Flags().Changed("limit") {
		limit, _ := cmd.Flags().GetInt("limit")
		filter.Limit = limit
	}

	if v, _ := cmd.Flags().GetString("search"); v != "" {
		filter.Search = v
	}

	if tags, _ := cmd.Flags().GetStringArray("tag"); len(tags) > 0 {
		filter.Tags = make(map[string][]string, len(tags))
		for _, t := range tags {
			key, value, ok := strings.Cut(t, "=")
			if !ok || key == "" {
				return nil, common.InvalidInputError(cmd, t, fmt.Errorf(`invalid --tag %q: must be in the form "key=value"`, t))
			}
			filter.Tags[key] = append(filter.Tags[key], value)
		}
	}

	return client.NewFilterSpec(filter), nil
}

// inlineFilterSpecSlice wraps buildInlineFilterSpec's single-filter result
// in a slice, or (nil, nil) if no inline filter flag was set -- the shape
// resolveQuery (cli/ncli/query.go) needs to hand off to client.Find/
// DumpFromTargets/CheckPOWLive alongside a --targets file's own
// []*FilterSpec.
func inlineFilterSpecSlice(cmd *cobra.Command) ([]*client.FilterSpec, error) {
	inline, err := buildInlineFilterSpec(cmd)
	if err != nil || inline == nil {
		return nil, err
	}
	return []*client.FilterSpec{inline}, nil
}

func splitCommaSeparated(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseCommaSeparatedInts(flagName, v string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid --%s value %q: must be an integer", flagName, p)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--%s must contain at least one integer", flagName)
	}
	return out, nil
}
