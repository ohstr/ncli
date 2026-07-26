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
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipB7"
	bclient "github.com/ohstr/nmilat/nipB7/client"
	"github.com/spf13/cobra"
)

func newListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [identifier]",
		Short: "List blobs stored under a pubkey",
		Long: `Query one Blossom server's GET /list/<pubkey> (--server, or the first
configured server), or every configured server with --all, merged and
deduped by hash.

identifier may be a vault label, nsec, npub, hex pubkey, nprofile, or
nip-05 address, resolved to a hex pubkey; defaults to --identity's
resolved pubkey when omitted.`,
		Args: cobra.MaximumNArgs(1),
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
				return common.UsageError(cmd, fmt.Errorf("a pubkey argument or --identity is required"))
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
			if all, _ := cmd.Flags().GetBool("all"); all {
				descriptors, err = listAllServers(ctx, hc, servers, pubKeyHex, query, auth)
			} else {
				descriptors, err = hc.List(ctx, servers[0], pubKeyHex, query, auth)
			}
			if err != nil {
				return classifyHTTPError(cmd, pubKeyHex, err)
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
			fmt.Fprintln(w, "HASH\tSIZE\tTYPE\tUPLOADED\tURL")
			for _, d := range descriptors {
				fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n", d.Sha256, d.Size, d.Type, time.Unix(d.Uploaded, 0).Format(time.RFC3339), d.URL)
			}
			w.Flush()
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
// up to N*S entries instead of the N a caller asked for. Returns an
// aggregate error only if every server failed and none returned anything.
func listAllServers(ctx context.Context, hc *bclient.Client, servers []string, pubKeyHex string, query nipB7.ListQuery, auth *nip01.Event) ([]nipB7.BlobDescriptor, error) {
	seen := make(map[string]bool)
	var merged []nipB7.BlobDescriptor
	var errs []error
	for _, server := range servers {
		descriptors, err := hc.List(ctx, server, pubKeyHex, query, auth)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", server, err))
			continue
		}
		for _, d := range descriptors {
			if seen[d.Sha256] {
				continue
			}
			seen[d.Sha256] = true
			merged = append(merged, d)
		}
	}
	if len(merged) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	nipB7.SortDescending(merged)
	if query.Limit > 0 && len(merged) > query.Limit {
		merged = merged[:query.Limit]
	}
	return merged, nil
}
