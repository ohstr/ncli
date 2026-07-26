package blossom

import (
	"fmt"
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nipB7"
	"github.com/spf13/cobra"
)

func newMirrorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mirror <source-url>",
		Short: "Ask your Blossom server(s) to fetch and store a blob from a URL",
		Long: `Sign a BUD-11 authorization and PUT /mirror to every target server
(--server, or the configured default list) -- each server fetches
source-url itself; no bytes pass through ncli. Reports a result per
server.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return common.UsageError(cmd, fmt.Errorf("exactly one source URL is required"))
			}
			if identity, _ := cmd.Flags().GetString("identity"); identity == "" {
				return common.UsageError(cmd, fmt.Errorf("--identity is required"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			jsonMode, _ := cmd.Flags().GetBool("json")
			sourceURL := args[0]

			servers, err := resolveServers(cmd)
			if err != nil {
				return common.NotFoundError(cmd, "", err)
			}

			identity, _ := cmd.Flags().GetString("identity")
			privKeyHex, pubKeyHex, err := resolveIdentity(cmd, jsonMode, identity)
			if err != nil {
				return err
			}

			// BUD-04 doesn't pin its own verb; servers conventionally
			// require the upload verb for PUT /mirror since mirroring is
			// a form of upload (see nipB7's VerbUpload doc comment).
			ttl, _ := cmd.Flags().GetDuration("auth-ttl")

			timeout, _ := cmd.Flags().GetDuration("timeout")
			hc := newHTTPClient(timeout)

			report := &fanoutReport{}
			for _, server := range servers {
				res := serverResult{Item: sourceURL, Server: server}

				// Signed fresh per server, not once for the whole batch --
				// see the identical comment in upload.go.
				auth, err := buildAuth(privKeyHex, pubKeyHex, nipB7.VerbUpload, nil, ttl)
				if err != nil {
					res.Error = err.Error()
					report.add(res)
					continue
				}

				descriptor, err := hc.Mirror(ctx, server, sourceURL, auth)
				if err != nil {
					res.Error = describeError(err) + uploadErrorHint
				} else {
					res.OK = true
					res.URL = descriptor.URL
					res.Sha256 = descriptor.Sha256
					res.Size = descriptor.Size
					res.Type = descriptor.Type
				}
				report.add(res)
			}

			printFanoutReport(jsonMode, report)
			if !report.allSucceeded() {
				return common.RuntimeError(cmd, fmt.Errorf("%d of %d mirrors failed", report.Failed, report.Attempted))
			}
			return nil
		},
	}

	cmd.Flags().Duration("auth-ttl", defaultAuthTTL, "How long the signed authorization token stays valid")

	return cmd
}
