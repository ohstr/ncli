package blossom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipB7"
	bclient "github.com/ohstr/nmilat/nipB7/client"
	"github.com/rs/zerolog/log"
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
		Long: `Writes to --output, or to "<hash>.<ext>" in the current directory. Use
"-o -" to stream to stdout.

A full http(s) URL is fetched as is first, and its bytes must hash to the
sha256 in its path; if that fails, the hash is asked of --server or the
configured servers, when there are any.`,
		Example: `  ncli blossom download <hash>
  ncli blossom download <hash> -o -
  ncli blossom download https://relay.example.com/media/<hash>.png`,
		Args: common.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			jsonMode, _ := cmd.Flags().GetBool("json")

			hash, ext, err := normalizeDownloadTarget(args[0])
			if err != nil {
				return common.InvalidInputError(cmd, args[0], err)
			}

			// A full URL names where the blob is: try it as is first, and
			// the servers by hash only when it fails. Without one, the
			// servers are all there is.
			directURL := directDownloadURL(args[0])
			servers, err := resolveServers(cmd)
			if err != nil {
				if directURL == "" {
					return common.NotFoundError(cmd, "", err)
				}
				servers = nil
			}

			var directAuth, auth *nip01.Event
			if identity, _ := cmd.Flags().GetString("identity"); identity != "" {
				privKeyHex, pubKeyHex, err := resolveIdentity(cmd, jsonMode, identity)
				if err != nil {
					return err
				}
				ttl, _ := cmd.Flags().GetDuration("auth-ttl")
				directAuth, auth, err = downloadAuths(privKeyHex, pubKeyHex, hash, ttl, directURL, servers)
				if err != nil {
					return common.RuntimeError(cmd, err)
				}
			}

			timeout, _ := cmd.Flags().GetDuration("timeout")
			hc := newHTTPClient(timeout)

			var body io.ReadCloser
			var usedServer string
			err = common.WithSpinner(cmd, fmt.Sprintf("downloading %s", shortHash(hash)), func() error {
				if directURL != "" {
					f, dErr := fetchVerified(ctx, hc.HTTPClient, directURL, hash, directAuth)
					if dErr == nil {
						body, usedServer = f, directURL
						return nil
					}
					if len(servers) == 0 || ctx.Err() != nil {
						return dErr
					}
					log.Warn().Err(dErr).Str("url", directURL).Msg("falling back to the servers by hash")
				}
				resp, server, gErr := hc.GetFromServers(ctx, servers, hash, bclient.GetOptions{Ext: ext, Auth: auth})
				if gErr != nil {
					return gErr
				}
				body, usedServer = resp.Body, server
				return nil
			})
			if err != nil {
				return classifyHTTPError(cmd, hash, err)
			}
			defer func() { _ = body.Close() }()

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
				defer func() { _ = f.Close() }()
				out = f
				createdPath = outPath
			}

			written, err := io.Copy(out, body)
			if err != nil {
				if createdPath != "" {
					// Best-effort cleanup: don't leave a truncated,
					// corrupt-looking file behind after a failed transfer.
					_ = os.Remove(createdPath)
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

// directDownloadURL returns raw when it is an http(s) URL naming a blob,
// which download fetches as is before asking any server by hash: the URL
// says where the blob is, and its host may be on no server list at all.
func directDownloadURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	if _, _, ok := nipB7.ExtractHashFromURL(raw); !ok {
		return ""
	}
	return raw
}

// downloadAuths signs download's get tokens: direct for the URL's host
// alone, byHash for the configured servers. Separate, so a URL host that
// is on no server list never holds a token it could replay against the
// user's servers. direct is nil without a URL; byHash is nil when only the
// URL will be asked.
func downloadAuths(privKeyHex, pubKeyHex, hash string, ttl time.Duration, directURL string, servers []string) (direct, byHash *nip01.Event, err error) {
	if directURL != "" {
		if direct, err = buildAuth(privKeyHex, pubKeyHex, nipB7.VerbGet, []string{hash}, ttl, directURL); err != nil {
			return nil, nil, err
		}
	}
	if directURL == "" || len(servers) > 0 {
		if byHash, err = buildAuth(privKeyHex, pubKeyHex, nipB7.VerbGet, []string{hash}, ttl, servers...); err != nil {
			return nil, nil, err
		}
	}
	return direct, byHash, nil
}

// fetchVerified GETs rawURL with auth (if any) as its BUD-11 header and
// returns the body in a temporary file, removed on Close, once its sha256
// matches hash. Buffered rather than streamed so a URL that serves the
// wrong bytes can still fall back to the servers without anything having
// reached the output.
func fetchVerified(ctx context.Context, hc *http.Client, rawURL, hash string, auth *nip01.Event) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if auth != nil {
		header, err := nipB7.EncodeAuthHeader(auth)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", header)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &bclient.HTTPError{Server: rawURL, StatusCode: resp.StatusCode, Reason: nipB7.ReasonFromResponse(resp.Header)}
	}

	f, err := os.CreateTemp("", "ncli-blossom-download-*")
	if err != nil {
		return nil, err
	}
	tmp := &tempFile{File: f}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != hash {
		_ = tmp.Close()
		return nil, &hashMismatchError{url: rawURL, got: got, want: hash}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	return tmp, nil
}

// hashMismatchError is a URL that answered with bytes that are not the
// blob its path names. classifyHTTPError reports it as not_found: no
// source served a blob with that hash, the same outcome as a server's 404
// for it, and retrying the same URL serves the same wrong bytes.
type hashMismatchError struct{ url, got, want string }

func (e *hashMismatchError) Error() string {
	return fmt.Sprintf("%s served a blob whose sha256 is %s, not %s", e.url, e.got, e.want)
}

// tempFile is a temporary file that removes itself on Close.
type tempFile struct{ *os.File }

func (t *tempFile) Close() error {
	err := t.File.Close()
	_ = os.Remove(t.Name())
	return err
}

// sanitizeExt returns ext unchanged if it's a bare letters/digits
// extension, or "" otherwise.
func sanitizeExt(ext string) string {
	if safeExtRE.MatchString(ext) {
		return ext
	}
	return ""
}
