package blossom

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipB7"
	bclient "github.com/ohstr/nmilat/nipB7/client"
	"github.com/spf13/cobra"
)

const (
	defaultAuthTTL = 2 * time.Minute
	defaultTimeout = 30 * time.Second
)

// uploadErrorHint is appended to any upload/mirror failure. nipB7/client's
// Upload/Mirror return an error both for real transport failures and for
// "the PUT succeeded but the server's JSON response was malformed or
// failed BlobDescriptor.Validate()" (confirmed via its own tests) -- these
// two cases aren't distinguishable from here via errors.Is/As on the
// current SDK error types, so every failure gets this disclaimer rather
// than one that only sometimes applies.
const uploadErrorHint = " (if the file was already sent to the server, it may exist there despite this error -- check with `ncli blossom list` before retrying)"

// newHTTPClient returns a Blossom HTTP client bounded by timeout -- the
// zero value's http.DefaultClient has no timeout at all, which would let
// one unreachable server hang a whole invocation.
func newHTTPClient(timeout time.Duration) *bclient.Client {
	return &bclient.Client{HTTPClient: &http.Client{Timeout: timeout}}
}

// resolveServers returns the server list a subcommand should operate
// against: explicit --server flags if any were given, else the configured
// default list from prefs.yaml.
func resolveServers(cmd *cobra.Command) ([]string, error) {
	explicit, _ := cmd.Flags().GetStringArray("server")
	if len(explicit) > 0 {
		return explicit, nil
	}
	return client.BlossomServersFromPrefs()
}

// resolveIdentity resolves the --identity string into a private key and
// its pubkey, via the same client.ResolveIdentifier ->
// keyresolve.ResolveSigningKey chain "id sign" uses. Returns an already-
// classified *common.CLIError on any failure, including a pubkey-only
// identity with no private key to sign with.
func resolveIdentity(cmd *cobra.Command, jsonMode bool, identity string) (privKeyHex, pubKeyHex string, err error) {
	resolved, err := client.ResolveIdentifier(identity)
	if err != nil {
		return "", "", keyresolve.ClassifyIdentifierError(cmd, identity, err)
	}
	privKeyHex, err = keyresolve.ResolveSigningKey(cmd, jsonMode, resolved)
	if err != nil {
		return "", "", err
	}
	if privKeyHex == "" {
		return "", "", common.AuthError(cmd, fmt.Errorf("identity %q has no private key available to sign with", common.RedactSecretInput(identity)))
	}
	return privKeyHex, resolved.PubKeyHex, nil
}

// buildAuth signs a BUD-11 kind:24242 Authorization token for verb, scoped
// to hashes if given (nil/empty means unscoped -- valid for any blob).
// It's left unscoped to any particular server ("server" tag omitted) so
// one signed token can be reused across every target server in a single
// multi-server operation.
func buildAuth(privKeyHex, pubKeyHex, verb string, hashes []string, ttl time.Duration) (*nip01.Event, error) {
	event := nipB7.NewAuthorization(nipB7.AuthorizationParams{
		Pubkey:     pubKeyHex,
		Verb:       verb,
		Content:    "ncli blossom " + verb,
		Expiration: time.Now().Add(ttl),
		Hashes:     hashes,
	})
	if err := event.Sign(privKeyHex); err != nil {
		return nil, fmt.Errorf("failed to sign blossom authorization: %w", err)
	}
	return event, nil
}

// classifyHTTPError maps a Blossom server error to the right CLIError
// code: AuthError for a 401/403 rejection, NotFoundError for a 404,
// NetworkError for everything else -- including *bclient.PaymentRequiredError,
// whose CodeNetwork retryable=true fits "retry once payment is settled"
// better than any other existing code. A payment-required error is
// rewrapped so its Cashu/Lightning details survive into the final message
// (see describeError).
func classifyHTTPError(cmd *cobra.Command, input string, err error) error {
	if _, ok := errors.AsType[*bclient.PaymentRequiredError](err); ok {
		return common.NetworkError(cmd, input, errors.New(describeError(err)))
	}
	if httpErr, ok := errors.AsType[*bclient.HTTPError](err); ok {
		switch httpErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return common.AuthError(cmd, err)
		case http.StatusNotFound:
			return common.NotFoundError(cmd, input, err)
		}
	}
	return common.NetworkError(cmd, input, err)
}

// describeError renders err for a report/error message, surfacing
// *bclient.PaymentRequiredError's Cashu/Lightning details explicitly --
// its own Error() string (inherited from the embedded HTTPError) doesn't
// include them, so a caller that only printed err.Error() would otherwise
// show a bare "402 Payment Required" with no way to actually pay.
func describeError(err error) string {
	payErr, ok := errors.AsType[*bclient.PaymentRequiredError](err)
	if !ok {
		return err.Error()
	}
	parts := []string{err.Error()}
	if payErr.Payment.Cashu != "" {
		parts = append(parts, fmt.Sprintf("cashu=%s", payErr.Payment.Cashu))
	}
	if payErr.Payment.Lightning != "" {
		parts = append(parts, fmt.Sprintf("lightning=%s", payErr.Payment.Lightning))
	}
	return strings.Join(parts, "; ")
}

// serverResult is one (item, server) outcome from a fan-out write
// operation (upload/rm/mirror) -- the HTTP analogue of client.PublishResult.
type serverResult struct {
	Item   string `json:"item"`
	Server string `json:"server"`
	OK     bool   `json:"ok"`
	URL    string `json:"url,omitempty"`
	Sha256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Type   string `json:"type,omitempty"`
	Error  string `json:"error,omitempty"`
}

// fanoutReport summarizes a write operation's attempt against every (item,
// server) pair -- the full cross product, mirroring client.PublishReport's
// shape for "ncli publish"'s (event, relay) cross product.
type fanoutReport struct {
	Attempted int            `json:"attempted"`
	Succeeded int            `json:"succeeded"`
	Failed    int            `json:"failed"`
	Results   []serverResult `json:"results"`
}

func (r *fanoutReport) add(res serverResult) {
	r.Attempted++
	if res.OK {
		r.Succeeded++
	} else {
		r.Failed++
	}
	r.Results = append(r.Results, res)
}

func (r *fanoutReport) allSucceeded() bool { return r.Failed == 0 }

// printFanoutReport prints report to stdout -- as JSON if jsonMode, else
// one line per (item, server) result plus a summary line, matching
// "ncli publish"'s text-mode report.
func printFanoutReport(jsonMode bool, report *fanoutReport) {
	if jsonMode {
		common.PrintJSON(report)
		return
	}
	for _, r := range report.Results {
		switch {
		case r.OK && r.URL != "":
			fmt.Printf("ok   %s -> %s: %s\n", r.Item, r.Server, r.URL)
		case r.OK:
			// rm has no URL to show -- only upload/mirror populate one.
			fmt.Printf("ok   %s -> %s\n", r.Item, r.Server)
		default:
			fmt.Printf("fail %s -> %s: %s\n", r.Item, r.Server, r.Error)
		}
	}
	fmt.Printf("attempted %d, succeeded %d, failed %d\n", report.Attempted, report.Succeeded, report.Failed)
}
