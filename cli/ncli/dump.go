package ncli

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
)

var dumpCmd = &cobra.Command{
	Use:   "dump",
	Short: "Export events to JSON",
	Long: `Export events matching a filter to a JSON file, merged and deduplicated
by event ID across every target.

Targets and filters come from --targets (a YAML file that may declare
both), or from --relays plus inline filter flags -- pick one, not a mix.
Omitting both --targets and --relays falls back to every relay configured
via "ncli prefs relays add".`,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := cmd.ValidateRequiredFlags(); err != nil {
			return common.UsageError(cmd, err)
		}
		if err := queryMutualExclusionCheck(cmd); err != nil {
			return common.UsageError(cmd, err)
		}
		if _, err := validateArgFile(cmd, "out", false, ".json", ".jsonp"); err != nil {
			return common.UsageError(cmd, err)
		}
		if cmd.Flags().Changed("targets") {
			if _, err := validateArgFile(cmd, "targets", true, ".yaml", ".yml"); err != nil {
				return common.UsageError(cmd, err)
			}
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		timeout, _ := cmd.Flags().GetDuration("timeout")

		outFile, err := validateArgFile(cmd, "out", false, ".json", ".jsonp")
		if err != nil {
			return common.RuntimeError(cmd, err)
		}

		targetsSpec, filtersSpec, err := resolveQuery(cmd)
		if err != nil {
			return common.RuntimeError(cmd, err)
		}

		if err := client.ResolveFilterAuthors(filtersSpec); err != nil {
			return common.NetworkError(cmd, "", err)
		}

		if err := client.DumpFromTargets(ctx, targetsSpec, outFile, filtersSpec, timeout); err != nil {
			if errors.Is(err, client.ErrNoReachableTargets) {
				return common.NetworkError(cmd, "", err)
			}
			return common.RuntimeError(cmd, err)
		}
		return nil
	},
}

func init() {
	RootCmd.AddCommand(dumpCmd)

	registerQueryFlags(dumpCmd, "")

	dumpCmd.Flags().StringP("out", "o", "", "Output JSON file path")
	dumpCmd.MarkFlagRequired("out")
	dumpCmd.MarkFlagFilename("out", "json", "jsonp")

	dumpCmd.Flags().Duration("timeout", 30*time.Second, "Max time to wait per target before giving up on it (0 = wait forever)")
}

// validateArgFile classifies every real validation failure as
// common.CodeInvalidInput with absPath as the echoed input, directly here
// rather than at each of its call sites (find/dump/miner's Args validators
// and RunE bodies) -- wrapCLIError (see cli/common/errors.go) keeps an
// already-classified error's code as-is when a caller re-wraps it via
// UsageError/RuntimeError, so this one classification covers every call
// site for free. The first two failures stay plain errors: both are
// effectively unreachable (the flag is always registered; the CWD is
// always resolvable), so they fall through to whatever generic code the
// caller's own wrapper assigns instead of a specific one.
func validateArgFile(cmd *cobra.Command, name string, shouldExist bool, validExtensions ...string) (string, error) {

	path, err := cmd.Flags().GetString(name)
	if err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("could not determine absolute path for %s: %w", name, err)
	}

	if shouldExist {
		info, err := os.Stat(absPath)
		if os.IsNotExist(err) {
			return "", common.InvalidInputError(cmd, absPath, fmt.Errorf("%s does not exist: %s", name, absPath))
		}
		if err != nil {
			return "", common.InvalidInputError(cmd, absPath, fmt.Errorf("could not access %s: %w", name, err))
		}
		if info.IsDir() {
			return "", common.InvalidInputError(cmd, absPath, fmt.Errorf("%s is a directory, not a file: %s", name, absPath))
		}
	}

	ext := strings.ToLower(filepath.Ext(absPath))
	for _, validExt := range validExtensions {
		if ext == validExt {
			return absPath, nil
		}
	}
	return "", common.InvalidInputError(cmd, absPath, fmt.Errorf("%s must have one of the following extensions: %v", name, validExtensions))
}
