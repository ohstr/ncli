// Package blossom implements the "ncli blossom" command tree: a client for
// the Blossom media/blob-storage protocol (NIP-B7, BUD-01..12) built on
// nmilat's nipB7 and nipB7/client packages.
package blossom

import (
	"github.com/ohstr/ncli/cli/common"
	"github.com/spf13/cobra"
)

// NewBlossomCommand builds the `ncli blossom` command tree: upload,
// download, list, rm, mirror, server-list management, and blob reports.
func NewBlossomCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "blossom",
		Short: "Upload, fetch, and manage content on Blossom media servers",
		Long: `A client for the Blossom protocol (BUD-01..12): content-addressed blob
storage authenticated with a Nostr identity instead of a login.

Every write (upload, rm, mirror) targets every server from --server, or
the default list from "ncli blossom servers add" -- reporting a result
per (item, server) pair, and exiting non-zero if any pair failed.
"download" tries the configured servers in order, stopping at the first
that answers; "list" queries one server by default, or every server with
--all.`,
		RunE: common.RequireSubcommand,
	}

	cmd.PersistentFlags().String("identity", "", "Identity to sign with -- vault label, nsec, npub, hex, nprofile, or nip-05")
	cmd.PersistentFlags().StringArray("server", nil, "Blossom server URL to use for this invocation (repeatable; overrides the configured default list without changing it)")
	cmd.PersistentFlags().Duration("timeout", defaultTimeout, "Max time to wait per server before giving up on it")

	cmd.AddCommand(newUploadCommand())
	cmd.AddCommand(newDownloadCommand())
	cmd.AddCommand(newListCommand())
	cmd.AddCommand(newRmCommand())
	cmd.AddCommand(newMirrorCommand())
	cmd.AddCommand(newServersCommand())
	cmd.AddCommand(newReportCommand())

	return cmd
}
