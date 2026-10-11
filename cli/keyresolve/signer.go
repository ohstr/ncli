package keyresolve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/nip46/bunker"
	"github.com/ohstr/nmilat/nipLS"
	"github.com/spf13/cobra"
)

// signerDialTimeout bounds connecting to a socket signer.
const signerDialTimeout = 10 * time.Second

// bunkerDialTimeout bounds pairing with a NIP-46 signer, which may wait on
// a person to approve.
const bunkerDialTimeout = 60 * time.Second

// IsSignerURI reports whether value names a signer to dial rather than an
// identity: bunker+unix:// (or unix://) or a NIP-46 bunker://.
func IsSignerURI(value string) bool {
	return nipLS.IsURI(value) || strings.HasPrefix(strings.TrimSpace(value), "bunker://")
}

// ResolveSigner turns value into a Signer: a bunker+unix:// (or unix://)
// URI dials that socket signer, a bunker:// URI pairs with that NIP-46
// signer; anything else resolves like --identity to a private key held in
// this process. Call the returned close when done.
func ResolveSigner(cmd *cobra.Command, jsonMode bool, value string) (client.Signer, func(), error) {
	if strings.HasPrefix(strings.TrimSpace(value), "bunker://") {
		if _, err := bunker.ParseURI(value); err != nil {
			return nil, nil, common.InvalidInputError(cmd, "", err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), bunkerDialTimeout)
		defer cancel()
		c, err := bunker.Dial(ctx, value, bunker.ClientOptions{Metadata: &nip46.Metadata{Name: "ncli"}})
		if err != nil {
			var refusal *nip46.Error
			if errors.As(err, &refusal) {
				return nil, nil, common.AuthError(cmd, fmt.Errorf("bunker refused to pair: %w", err))
			}
			return nil, nil, common.NetworkError(cmd, "", fmt.Errorf("bunker not answering: %w", err))
		}
		return c, func() { _ = c.Close() }, nil
	}
	if nipLS.IsURI(value) {
		if _, err := nipLS.ParseURI(value); err != nil {
			return nil, nil, common.InvalidInputError(cmd, value, err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), signerDialTimeout)
		defer cancel()
		c, err := nipLS.Dial(ctx, value)
		if err != nil {
			return nil, nil, common.NetworkError(cmd, value, fmt.Errorf("signer not answering: %w", err))
		}
		return c, func() { _ = c.Close() }, nil
	}

	resolved, err := client.ResolveIdentifier(value)
	if err != nil {
		return nil, nil, ClassifyIdentifierError(cmd, value, err)
	}
	priv, err := ResolveSigningKey(cmd, jsonMode, resolved)
	if err != nil {
		return nil, nil, err
	}
	if priv == "" {
		return nil, nil, common.AuthError(cmd, fmt.Errorf("identity %q has no private key available to sign with", common.RedactSecretInput(value)))
	}
	local, err := client.NewLocalSigner(priv)
	if err != nil {
		return nil, nil, common.RuntimeError(cmd, err)
	}
	return local, func() {}, nil
}

// SignerError classifies a Signer.Sign failure: a policy denial is auth,
// a request the signer called malformed is invalid_input, a dropped
// connection is network.
func SignerError(cmd *cobra.Command, input string, err error) error {
	var re *nipLS.Error
	if errors.As(err, &re) {
		switch {
		case errors.Is(re, nipLS.ErrDenied):
			return common.AuthError(cmd, err)
		case errors.Is(re, nipLS.ErrInvalid):
			return common.InvalidInputError(cmd, input, err)
		}
		return common.RuntimeError(cmd, err)
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) || errors.Is(err, nipLS.ErrConnClosed) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return common.NetworkError(cmd, "", err)
	}
	return common.RuntimeError(cmd, err)
}

// LoginError turns a failed NIP-42 login into its structured CLI error,
// or returns nil if err isn't one.
func LoginError(cmd *cobra.Command, err error) error {
	var le *client.LoginError
	if !errors.As(err, &le) {
		return nil
	}
	return common.LoginFailureError(cmd, le.Relay, string(le.Reason), le.Detail, err)
}
