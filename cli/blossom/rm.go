package blossom

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nipB7"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newRmCommand() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "rm <hash>",
		Short: "Delete a blob from your Blossom server(s)",
		Long: `Sign a hash-scoped BUD-11 authorization and DELETE the blob from every
target server (--server, repeatable, or the configured default list),
reporting a result per server. Requires --yes in a non-interactive
session -- there's no one to prompt, and an agent driving this over a
pipe can never answer a stdin read.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return common.UsageError(cmd, fmt.Errorf("exactly one hash is required"))
			}
			if !nipB7.IsSHA256Hex(args[0]) {
				return common.InvalidInputError(cmd, args[0], fmt.Errorf("not a valid sha256 hash"))
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
			hash := args[0]

			if !yes {
				if !term.IsTerminal(int(os.Stdin.Fd())) {
					return common.UsageError(cmd, fmt.Errorf("refusing to delete %s without --yes in a non-interactive session", hash))
				}
				if !promptYesNo(bufio.NewReader(os.Stdin), fmt.Sprintf("Delete blob %s from all target servers? [y/N] ", hash)) {
					return common.RuntimeError(cmd, fmt.Errorf("aborted"))
				}
			}

			servers, err := resolveServers(cmd)
			if err != nil {
				return common.NotFoundError(cmd, "", err)
			}

			identity, _ := cmd.Flags().GetString("identity")
			privKeyHex, pubKeyHex, err := resolveIdentity(cmd, jsonMode, identity)
			if err != nil {
				return err
			}

			ttl, _ := cmd.Flags().GetDuration("auth-ttl")

			timeout, _ := cmd.Flags().GetDuration("timeout")
			hc := newHTTPClient(timeout)

			report := &fanoutReport{}
			for _, server := range servers {
				res := serverResult{Item: hash, Server: server}

				// Signed fresh per server, not once for the whole batch --
				// see the identical comment in upload.go.
				auth, err := buildAuth(privKeyHex, pubKeyHex, nipB7.VerbDelete, []string{hash}, ttl)
				if err != nil {
					res.Error = err.Error()
					report.add(res)
					continue
				}

				if err := hc.Delete(ctx, server, hash, auth); err != nil {
					res.Error = describeError(err)
				} else {
					res.OK = true
				}
				report.add(res)
			}

			printFanoutReport(jsonMode, report)
			if !report.allSucceeded() {
				return common.RuntimeError(cmd, fmt.Errorf("%d of %d deletes failed", report.Failed, report.Attempted))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt")
	cmd.Flags().Duration("auth-ttl", defaultAuthTTL, "How long the signed authorization token stays valid")

	return cmd
}

// promptYesNo is a local copy of cli/ncli/id.go's helper of the same name
// -- kept local rather than imported to avoid reaching into package ncli
// from a sibling command package for a four-line helper.
func promptYesNo(r *bufio.Reader, prompt string) bool {
	fmt.Print(prompt)
	line, _ := r.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}
