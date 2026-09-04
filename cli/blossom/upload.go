package blossom

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipB7"
	bclient "github.com/ohstr/nmilat/nipB7/client"
	"github.com/spf13/cobra"
)

func newUploadCommand() *cobra.Command {
	var optimize bool

	cmd := &cobra.Command{
		Use:   "upload <file> [file...]",
		Short: "Upload one or more files to your Blossom server(s)",
		Long: `Sign a BUD-11 authorization and PUT each file to every target server
(--server, or the configured default list), reporting a result per
(file, server) pair. Exits non-zero if any pair failed.

Pass --optimize to request server-side transcoding/optimization (BUD-05's
PUT /media) instead of a byte-for-byte store.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return common.UsageError(cmd, fmt.Errorf("at least one file is required"))
			}
			if identity, _ := cmd.Flags().GetString("identity"); identity == "" {
				return common.UsageError(cmd, fmt.Errorf("--identity is required"))
			}
			for _, path := range args {
				info, err := os.Stat(path)
				if err != nil {
					return common.InvalidInputError(cmd, path, err)
				}
				if info.IsDir() {
					return common.InvalidInputError(cmd, path, fmt.Errorf("%q is a directory, not a file", path))
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			jsonMode, _ := cmd.Flags().GetBool("json")

			servers, err := resolveServers(cmd)
			if err != nil {
				return common.NotFoundError(cmd, "", err)
			}

			identity, _ := cmd.Flags().GetString("identity")
			privKeyHex, pubKeyHex, err := resolveIdentity(cmd, jsonMode, identity)
			if err != nil {
				return err
			}

			verb := nipB7.VerbUpload
			if optimize {
				verb = nipB7.VerbMedia
			}
			ttl, _ := cmd.Flags().GetDuration("auth-ttl")

			timeout, _ := cmd.Flags().GetDuration("timeout")
			hc := newHTTPClient(timeout)

			report := &fanoutReport{}
			for _, path := range args {
				// Sniffed once per file, not once per (file, server) pair
				// -- it doesn't depend on which server it's headed to.
				contentType, err := detectContentType(path)
				if err != nil {
					for _, server := range servers {
						report.add(serverResult{Item: path, Server: server, Error: err.Error()})
					}
					continue
				}

				for _, server := range servers {
					// Signed fresh for every (file, server) pair rather
					// than once for the whole batch: a large upload can
					// take longer than one token's TTL, and a token
					// signed at the start would then expire partway
					// through instead of every request getting a full
					// fresh window.
					auth, err := buildAuth(privKeyHex, pubKeyHex, verb, nil, ttl)
					if err != nil {
						report.add(serverResult{Item: path, Server: server, Error: err.Error()})
						continue
					}
					report.add(uploadOne(ctx, hc, server, path, contentType, auth, optimize))
				}
			}

			printFanoutReport(jsonMode, report)
			if !report.allSucceeded() {
				return common.RuntimeError(cmd, fmt.Errorf("%d of %d uploads failed", report.Failed, report.Attempted))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&optimize, "optimize", false, "Ask the server to transcode/optimize the file server-side (BUD-05 PUT /media) instead of storing it as-is")
	cmd.Flags().Duration("auth-ttl", defaultAuthTTL, "How long the signed authorization token stays valid")

	return cmd
}

func uploadOne(ctx context.Context, hc *bclient.Client, server, path, contentType string, auth *nip01.Event, optimize bool) serverResult {
	res := serverResult{Item: path, Server: server}

	f, err := os.Open(path)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		res.Error = err.Error()
		return res
	}

	req := bclient.UploadRequest{Body: f, Size: info.Size(), ContentType: contentType, Auth: auth}

	var descriptor *nipB7.BlobDescriptor
	if optimize {
		descriptor, err = hc.Media(ctx, server, req)
	} else {
		descriptor, err = hc.Upload(ctx, server, req)
	}
	if err != nil {
		res.Error = describeError(err) + uploadErrorHint
		return res
	}

	res.OK = true
	res.URL = descriptor.URL
	res.Sha256 = descriptor.Sha256
	res.Size = descriptor.Size
	res.Type = descriptor.Type
	return res
}

// detectContentType sniffs path's MIME type from its first 512 bytes (the
// same stdlib convention net/http itself uses), via its own short-lived
// file handle -- called once per file rather than once per (file, server)
// pair, since the sniffed type doesn't depend on which server it's headed
// to.
func detectContentType(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return "", err
	}
	return http.DetectContentType(buf[:n]), nil
}
