// Package dm implements `ncli dm`: NIP-17 private messages sealed, opened
// and proved through a signer that holds the key.
package dm

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/nip59"
	"github.com/ohstr/nmilat/nipLS"
	"github.com/spf13/cobra"
)

// wrapBackdate is how far NIP-59 may move a gift wrap's created_at into the
// past, so a --since on wraps must reach back that much further.
const wrapBackdate = 2 * 24 * time.Hour

// NewDMCommand builds `ncli dm`.
func NewDMCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dm",
		Short: "Send, read and prove NIP-17 private messages through a signer",
		Long: `Private messages (NIP-17): each message is sealed and signed by the
sender, then gift-wrapped under a one-time key (NIP-59), so relays see
neither the sender nor the content.

--signer holds the key: bunker+unix:///path.sock (ncli signer serve),
bunker://..., or an identity. It seals, opens and logs in (NIP-42); the
key never reaches this process when it is a socket or a bunker.

-s/--relays is required: a message is never sent to, or read from, the
default relay list.

"dm prove" shows one message to a third party; "dm verify" checks it
without any key.`,
		Example: `  ncli dm send --to npub1... "hi" -s wss://inbox.example --signer bunker+unix:///run/signer/agent.sock
  ncli dm read --from npub1... -s wss://inbox.example --json
  ncli dm read --from npub1... -s wss://inbox.example -f --state dm.state
  ncli dm prove <wrap-id> --from npub1... -s wss://inbox.example -o proof.json
  ncli dm verify proof.json --from npub1...`,
		RunE: common.RequireSubcommand,
	}
	cmd.AddCommand(newSendCommand())
	cmd.AddCommand(newReadCommand())
	cmd.AddCommand(newProveCommand())
	cmd.AddCommand(newVerifyCommand())
	return cmd
}

// outcome is what opening one gift wrap came to.
type outcome int

const (
	opened  outcome = iota
	skipped         // from a sender not asked for, or the signer refused
	invalid         // not addressed to us, or malformed
)

// openWrap opens wrap through signer, only for seals by from. A failure to
// reach the signer is returned as an error; everything else is an outcome.
func openWrap(ctx context.Context, signer client.Crypto, wrap *nip01.Event, from []string) (*nip59.Opened, outcome, error) {
	o, err := nip59.Unwrap(ctx, signer, wrap, nip59.OpenOptions{From: from})
	switch {
	case err == nil:
		if o.Rumor.ID == "" {
			_ = nip59.PrepareRumor(o.Rumor, o.Sender)
		}
		return o, opened, nil
	case errors.Is(err, nip59.ErrForeignSender), errors.Is(err, nip46.ErrDenied):
		return o, skipped, nil
	case transportFailure(err):
		return nil, invalid, err
	}
	return nil, invalid, nil
}

// transportFailure reports whether err is the signer being unreachable,
// rather than a wrap it could not open.
func transportFailure(err error) bool {
	var ne net.Error
	return errors.Is(err, nipLS.ErrConnClosed) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne)
}

// message turns an opened wrap into the shared message shape.
func message(o *nip59.Opened) social.Message {
	m := social.FromEvent(o.Rumor, "dm")
	m.From = strings.ToLower(o.Sender)
	if o.Wrap != nil {
		m.Wrap = o.Wrap.ID
	}
	return m
}
