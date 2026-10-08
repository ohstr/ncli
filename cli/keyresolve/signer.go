package keyresolve

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/ncli/signer"
	"github.com/spf13/cobra"
)

// signerDialTimeout bounds connecting to a socket signer.
const signerDialTimeout = 10 * time.Second

// ResolveSigner turns value into a Signer: a bunker+unix:// (or unix://)
// URI dials that socket signer; anything else resolves like --identity to
// a private key held in this process. Call the returned close when done.
func ResolveSigner(cmd *cobra.Command, jsonMode bool, value string) (client.Signer, func(), error) {
	if signer.IsURI(value) {
		if _, err := signer.ParseURI(value); err != nil {
			return nil, nil, common.InvalidInputError(cmd, value, err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), signerDialTimeout)
		defer cancel()
		c, err := signer.Dial(ctx, value)
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
	var re *signer.RemoteError
	if errors.As(err, &re) {
		switch {
		case re.Denied():
			return common.AuthError(cmd, err)
		case re.Invalid():
			return common.InvalidInputError(cmd, input, err)
		}
		return common.RuntimeError(cmd, err)
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) || errors.Is(err, signer.ErrConnClosed) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return common.NetworkError(cmd, "", err)
	}
	return common.RuntimeError(cmd, err)
}
