package ncli

import (
	"fmt"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
)

// registerQueryFlags adds the targets+filters query trio shared by find,
// dump, and miner check's live mode: a combined --targets YAML file
// (relays and/or filters), --relays as its comma-separated command-line
// alternative, and the inline filter flags. prefix is prepended to
// --targets/--relays' descriptions (e.g. "Live mode: " for miner check,
// which also has a file mode) -- pass "" to leave them sentence-cased.
func registerQueryFlags(cmd *cobra.Command, prefix string) {
	targetsDesc := "Path to a YAML targets/filters file"
	relaysDesc := "Comma-separated relay URLs and/or local .db paths"
	if prefix != "" {
		targetsDesc = prefix + "path to a YAML targets/filters file"
		relaysDesc = prefix + "comma-separated relay URLs and/or local .db paths"
	}

	cmd.Flags().StringP("targets", "t", "", targetsDesc)
	cmd.MarkFlagFilename("targets", "yaml", "yml")

	cmd.Flags().StringP("relays", "s", "", relaysDesc)

	registerInlineFilterFlags(cmd)
}

// queryMutualExclusionCheck enforces registerQueryFlags' two options, not a
// mix: --targets (whose file may declare its own relays/filters) versus
// --relays and/or inline filter flags on the command line.
func queryMutualExclusionCheck(cmd *cobra.Command) error {
	if cmd.Flags().Changed("targets") && (cmd.Flags().Changed("relays") || inlineFilterFlagsChanged(cmd)) {
		return fmt.Errorf("--targets is mutually exclusive with --relays and inline filter flags; a --targets file may declare its own relays/filters")
	}
	return nil
}

// resolveQuery builds the (targets, filters) pair registerQueryFlags'
// flags describe: --targets' file (relays plus its own embedded filters,
// if any), or --relays (falling back to the configured prefs relays if
// omitted) paired with any inline filter flags. Every failure here is
// classified at the source (common.InvalidInputError/NotFoundError)
// rather than at find/dump/miner check's own call sites -- wrapCLIError
// keeps an already-classified error's code when a caller re-wraps it via
// UsageError/RuntimeError, so this one classification covers every caller.
func resolveQuery(cmd *cobra.Command) (*client.TargetsSpec, []*client.FilterSpec, error) {
	if cmd.Flags().Changed("targets") {
		targetsPath, err := validateArgFile(cmd, "targets", true, ".yaml", ".yml")
		if err != nil {
			return nil, nil, err
		}
		targetsSpec, err := client.LoadTargetsSpec(targetsPath)
		if err != nil {
			return nil, nil, common.InvalidInputError(cmd, targetsPath, err)
		}
		return targetsSpec, targetsSpec.Filters, nil
	}

	var targetsSpec *client.TargetsSpec
	var err error
	if cmd.Flags().Changed("relays") {
		relaysVal, _ := cmd.Flags().GetString("relays")
		targetsSpec, err = client.TargetsFromRelayList(splitCommaSeparated(relaysVal))
		if err != nil {
			return nil, nil, common.InvalidInputError(cmd, relaysVal, err)
		}
	} else {
		targetsSpec, err = client.TargetsFromPrefs()
		if err != nil {
			// Dominant real-world case is "no relays configured" -- a
			// missing-precondition state (with the exact remediation
			// command already in the message), not a malformed value.
			return nil, nil, common.NotFoundError(cmd, "", err)
		}
	}

	filtersSpec, err := inlineFilterSpecSlice(cmd)
	if err != nil {
		return nil, nil, err
	}
	return targetsSpec, filtersSpec, nil
}
