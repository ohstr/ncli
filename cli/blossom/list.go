package blossom

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipB7"
	bclient "github.com/ohstr/nmilat/nipB7/client"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func newListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [identifier]",
		Short: "List blobs stored under a pubkey",
		Long: `identifier accepts a vault label, npub, hex pubkey, nprofile or nip-05
address, and defaults to --identity's pubkey.`,
		Example: `  ncli blossom list --identity satoshi
  ncli blossom list --identity satoshi --all
  ncli blossom list name@example.com`,
		Args: common.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			jsonMode, _ := cmd.Flags().GetBool("json")

			var pubKeyHex string
			if len(args) == 1 {
				resolved, err := client.ResolveIdentifier(args[0])
				if err != nil {
					return keyresolve.ClassifyIdentifierError(cmd, args[0], err)
				}
				pubKeyHex = resolved.PubKeyHex
			}

			var auth *nip01.Event
			if identity, _ := cmd.Flags().GetString("identity"); identity != "" {
				privKeyHex, resolvedPub, err := resolveIdentity(cmd, jsonMode, identity)
				if err != nil {
					return err
				}
				if pubKeyHex == "" {
					pubKeyHex = resolvedPub
				}
				ttl, _ := cmd.Flags().GetDuration("auth-ttl")
				auth, err = buildAuth(privKeyHex, resolvedPub, nipB7.VerbList, nil, ttl)
				if err != nil {
					return common.RuntimeError(cmd, err)
				}
			}
			if pubKeyHex == "" {
				return common.InvocationOrHelp(cmd, args, fmt.Errorf("a pubkey argument or --identity is required"))
			}

			servers, err := resolveServers(cmd)
			if err != nil {
				return common.NotFoundError(cmd, "", err)
			}

			var query nipB7.ListQuery
			query.Cursor, _ = cmd.Flags().GetString("cursor")
			query.Limit, _ = cmd.Flags().GetInt("limit")
			query.Since, _ = cmd.Flags().GetInt64("since")
			query.Until, _ = cmd.Flags().GetInt64("until")

			timeout, _ := cmd.Flags().GetDuration("timeout")
			hc := newHTTPClient(timeout)

			var descriptors []nipB7.BlobDescriptor
			all, _ := cmd.Flags().GetBool("all")
			message := fmt.Sprintf("listing blobs on %s", servers[0])
			if all {
				message = fmt.Sprintf("listing blobs on %d server(s)", len(servers))
			}
			err = common.WithSpinner(cmd, message, func() error {
				var lErr error
				if all {
					descriptors, lErr = listAllServers(ctx, hc, servers, pubKeyHex, query, auth)
				} else {
					descriptors, lErr = hc.List(ctx, servers[0], pubKeyHex, query, auth)
				}
				return lErr
			})
			if err != nil {
				return classifyListError(cmd, pubKeyHex, err)
			}

			if jsonMode {
				if descriptors == nil {
					descriptors = []nipB7.BlobDescriptor{}
				}
				common.PrintJSON(descriptors)
				return nil
			}

			if len(descriptors) == 0 {
				fmt.Println("no blobs found")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "HASH\tSIZE\tTYPE\tUPLOADED\tURL")
			for _, d := range descriptors {
				// A BlobDescriptor is the server's JSON: its hash, MIME type and
				// URL are all server-controlled text, escaped before printing.
				_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n", social.Inline(d.Sha256), d.Size, social.Inline(d.Type), time.Unix(d.Uploaded, 0).Format(time.RFC3339), social.Inline(d.URL))
			}
			_ = w.Flush()
			return nil
		},
	}

	cmd.Flags().Bool("all", false, "Query every configured server and merge results by hash")
	cmd.Flags().String("cursor", "", "Pagination cursor (BUD-12)")
	cmd.Flags().Int("limit", 0, "Max results to return")
	cmd.Flags().Int64("since", 0, "Only blobs uploaded at/after this unix timestamp")
	cmd.Flags().Int64("until", 0, "Only blobs uploaded at/before this unix timestamp")
	cmd.Flags().Duration("auth-ttl", defaultAuthTTL, "How long the signed authorization token stays valid (only used with --identity)")

	return cmd
}

// listAllServers queries every server in servers and merges the results,
// deduping by Sha256 (first-seen wins) and sorting newest-first. query is
// passed to every server as-is (so each server does its own pagination),
// but query.Limit is then re-applied as a cap on the merged, newest-first
// result too -- otherwise "--all --limit N" across S servers could return
// up to N*S entries instead of the N a caller asked for. A server that
// answers counts as a success even with no blobs: the others' failures are
// then logged and skipped. Only when every server failed is there an
// error, an *allListsFailedError.
func listAllServers(ctx context.Context, hc *bclient.Client, servers []string, pubKeyHex string, query nipB7.ListQuery, auth *nip01.Event) ([]nipB7.BlobDescriptor, error) {
	seen := make(map[string]bool)
	var merged []nipB7.BlobDescriptor
	var errs []error
	answered := 0
	for _, server := range servers {
		descriptors, err := hc.List(ctx, server, pubKeyHex, query, auth)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", server, optionalEndpointError(err)))
			continue
		}
		answered++
		for _, d := range descriptors {
			if seen[d.Sha256] {
				continue
			}
			seen[d.Sha256] = true
			merged = append(merged, d)
		}
	}
	if answered == 0 && len(errs) > 0 {
		return nil, &allListsFailedError{errs: errs}
	}
	for _, err := range errs {
		log.Warn().Err(err).Msg("skipping a server that failed to list")
	}
	nipB7.SortDescending(merged)
	if query.Limit > 0 && len(merged) > query.Limit {
		merged = merged[:query.Limit]
	}
	return merged, nil
}

// classifyListError reports a list failure. BUD-12 list never answers 404
// for anything it serves -- an unknown pubkey is an empty [] -- so a 404,
// like a 405, 501 or redirect, means the server has no list endpoint:
// UnsupportedError (exit 8), which an agent should not retry, rather than
// not_found, which reads as "this pubkey has nothing". Under --all, every
// server failed, and the run takes the most actionable of their codes: a
// retryable one (network, conflict) if any server's failure may pass, else
// any other failure (auth, invalid_input), and unsupported only when no
// server has a list endpoint.
func classifyListError(cmd *cobra.Command, input string, err error) error {
	all, ok := errors.AsType[*allListsFailedError](err)
	if !ok {
		return classifyHTTPError(cmd, input, optionalEndpointError(err))
	}
	rank := func(code common.ErrorCode) int {
		switch code {
		case common.CodeNetwork, common.CodeConflict:
			return 0
		case common.CodeUnsupported:
			return 2
		}
		return 1
	}
	var chosen *common.CLIError
	for _, e := range all.errs {
		ce, ok := classifyHTTPError(cmd, input, e).(*common.CLIError)
		if ok && (chosen == nil || rank(ce.Code) < rank(chosen.Code)) {
			chosen = ce
		}
	}
	if chosen == nil {
		return common.NetworkError(cmd, input, all)
	}
	// Every server's failure is reported, under the chosen code.
	chosen.Err = all
	return chosen
}

// allListsFailedError is list --all's failure when no server answered: each
// server's error, in order.
type allListsFailedError struct{ errs []error }

func (e *allListsFailedError) Error() string   { return errors.Join(e.errs...).Error() }
func (e *allListsFailedError) Unwrap() []error { return e.errs }
