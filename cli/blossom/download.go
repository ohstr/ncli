package blossom

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipB7"
	bclient "github.com/ohstr/nmilat/nipB7/client"
	"github.com/spf13/cobra"
)

// safeExtRE matches a bare file extension -- letters and digits only.
// ParseURI/ExtractHashFromURL don't themselves constrain "ext" beyond
// "whatever follows the last dot," so a crafted "blossom:<hash>.txt/etc"-
// shaped input can otherwise smuggle a "/" into it. That can't actually
// escape the current directory (any ".." in ext shifts the hash/ext split
// point enough to fail IsSHA256Hex first), but it can still turn the
// default "<hash>.<ext>" output filename into an unintended nested path.
var safeExtRE = regexp.MustCompile(`^[A-Za-z0-9]*$`)

func newDownloadCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "download <hash|blossom-uri|url>",
		Short: "Download a blob by hash, blossom: URI, or server URL",
		Long: `Accepts a bare sha256 hash, a "blossom:<hash>.<ext>" URI (BUD-10), or a
server URL ending in a hash -- tries the configured servers in order
(--server, or the default list), stopping at the first that answers.

Writes to --output, or "<hash>.<ext>" in the current directory if
omitted, or streams to stdout with "-o -" (suppressing the summary line).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			jsonMode, _ := cmd.Flags().GetBool("json")

			hash, ext, err := normalizeDownloadTarget(args[0])
			if err != nil {
				return common.InvalidInputError(cmd, args[0], err)
			}

			servers, err := resolveServers(cmd)
			if err != nil {
				return common.NotFoundError(cmd, "", err)
			}

			var auth *nip01.Event
			if identity, _ := cmd.Flags().GetString("identity"); identity != "" {
				privKeyHex, pubKeyHex, err := resolveIdentity(cmd, jsonMode, identity)
				if err != nil {
					return err
				}
				ttl, _ := cmd.Flags().GetDuration("auth-ttl")
				auth, err = buildAuth(privKeyHex, pubKeyHex, nipB7.VerbGet, []string{hash}, ttl)
				if err != nil {
					return common.RuntimeError(cmd, err)
				}
			}

			timeout, _ := cmd.Flags().GetDuration("timeout")
			hc := newHTTPClient(timeout)

			resp, usedServer, err := hc.GetFromServers(ctx, servers, hash, bclient.GetOptions{Ext: ext, Auth: auth})
			if err != nil {
				return classifyHTTPError(cmd, hash, err)
			}
			defer resp.Body.Close()

			outPath, _ := cmd.Flags().GetString("output")
			toStdout := outPath == "-"
			if outPath == "" {
				if ext != "" {
					outPath = hash + "." + ext
				} else {
					outPath = hash
				}
			}

			var out io.Writer = os.Stdout
			var createdPath string
			if !toStdout {
				f, err := os.Create(outPath)
				if err != nil {
					return common.RuntimeError(cmd, err)
				}
				defer f.Close()
				out = f
				createdPath = outPath
			}

			written, err := io.Copy(out, resp.Body)
			if err != nil {
				if createdPath != "" {
					// Best-effort cleanup: don't leave a truncated,
					// corrupt-looking file behind after a failed transfer.
					os.Remove(createdPath)
				}
				return common.NetworkError(cmd, usedServer, err)
			}

			if toStdout {
				return nil
			}
			if jsonMode {
				common.PrintJSON(map[string]any{"hash": hash, "server": usedServer, "path": outPath, "bytes": written})
			} else {
				fmt.Println("downloaded", hash, "from", usedServer, "->", outPath, fmt.Sprintf("(%d bytes)", written))
			}
			return nil
		},
	}

	cmd.Flags().StringP("output", "o", "", `Output file path, or "-" for stdout (default "<hash>.<ext>" in the current directory)`)
	cmd.Flags().Duration("auth-ttl", defaultAuthTTL, "How long the signed authorization token stays valid (only used with --identity)")

	return cmd
}

// normalizeDownloadTarget accepts a bare sha256 hash, a "blossom:" URI
// (BUD-10), or a full server URL ending in a hash, and normalizes any of
// them to (hash, ext). ext is sanitized to a bare letters/digits extension
// (see safeExtRE) -- silently dropped, not rejected, since it's only ever
// a cosmetic naming hint, never load-bearing for locating the blob itself.
func normalizeDownloadTarget(raw string) (hash, ext string, err error) {
	if u, uriErr := nipB7.ParseURI(raw); uriErr == nil {
		return u.Hash, sanitizeExt(u.Ext), nil
	}
	if h, e, ok := nipB7.ExtractHashFromURL(raw); ok {
		return h, sanitizeExt(e), nil
	}
	if nipB7.IsSHA256Hex(raw) {
		return raw, "", nil
	}
	return "", "", fmt.Errorf("%q is not a valid sha256 hash, blossom: URI, or hash-shaped URL", raw)
}

// sanitizeExt returns ext unchanged if it's a bare letters/digits
// extension, or "" otherwise.
func sanitizeExt(ext string) string {
	if safeExtRE.MatchString(ext) {
		return ext
	}
	return ""
}
