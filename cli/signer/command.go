// Package signer is the "ncli signer" command: run a local policy signer
// on a unix socket, probe one, or dry-run a policy.
package signer

import (
	"github.com/ohstr/ncli/cli/common"
	"github.com/spf13/cobra"
)

// NewSignerCommand builds "ncli signer".
func NewSignerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "signer",
		Short: "Sign for local processes over a unix socket, gated by a policy",
		Long: `Holds one Nostr key in memory and signs for other processes over a unix
socket: the NIP-46 method set as newline-delimited JSON, with no relay and
no encryption. The socket's file permissions are the authentication; a
policy file decides what gets signed (default deny).

Clients use the URI bunker+unix:///path/to.sock, e.g.
"ncli id sign --signer bunker+unix:///run/signer/agent.sock".`,
		Example: `  ncli signer serve --socket /run/signer/agent.sock --policy policy.yaml --state-dir /var/lib/signer
  ncli signer status --socket /run/signer/agent.sock
  ncli signer check --policy policy.yaml -e event.json`,
		RunE: common.RequireSubcommand,
	}
	cmd.AddCommand(newServeCommand())
	cmd.AddCommand(newStatusCommand())
	cmd.AddCommand(newCheckCommand())
	return cmd
}
