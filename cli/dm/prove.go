package dm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip59"
	"github.com/spf13/cobra"
)

func newProveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prove <wrap.json|-|wrap-id> --from <who>",
		Short: "Prove what one private message says, to anyone",
		Long: `Opens one gift wrap through --signer and prints a proof: the sender's
signed seal and the message keys of that one payload,
{"seal": {...}, "keys": {"chacha_key", "chacha_nonce", "hmac_key"}}.
Anyone can check it with "dm verify", without a key. It opens no other
message: no conversation or private key is in it.

The wrap is a JSON file, - for stdin, or an id (hex, note, nevent)
fetched from -s/--relays. --from is the sender to prove; it may be you,
for the copy of a message you sent.

The signer must allow nip44_message_keys for --from (bunker+unix:// or
an identity; most remote bunkers don't offer it).`,
		Example: `  ncli dm prove <wrap-id> --from npub1... -s wss://inbox.example -o proof.json
  ncli find --ids <wrap-id> -s wss://inbox.example | ncli dm prove - --from npub1...`,
		Args: common.ExactArgs(1),
		RunE: runProve,
	}
	cmd.Flags().String("from", "", "Sender of the message: npub, hex, nprofile, nip-05 or vault label (required)")
	cmd.Flags().StringP("out", "o", "", "Write the proof to this file instead of stdout")
	cmd.Flags().Duration("timeout", 30*time.Second, "Max time to wait per relay")
	social.AddRelaysFlag(cmd, "Comma-separated relays to fetch the wrap from, when given an id")
	social.AddSignerFlag(cmd)
	return cmd
}

func runProve(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	fromFlag, _ := cmd.Flags().GetString("from")
	if fromFlag == "" {
		return common.InvocationError(cmd, errors.New("--from is required"))
	}
	from, err := keyresolve.Pubkey(cmd, fromFlag)
	if err != nil {
		return err
	}
	wrap, err := loadWrap(cmd, args[0])
	if err != nil {
		return err
	}
	signer, closeSigner, err := social.Signer(cmd)
	if err != nil {
		return err
	}
	defer closeSigner()
	keyer, ok := signer.(client.MessageKeyer)
	if !ok {
		return common.UnsupportedError(cmd, "", errors.New("this signer can't reveal message keys"))
	}

	o, out, err := openWrap(ctx, signer, wrap, []string{from})
	if err != nil {
		return social.SignerError(cmd, err)
	}
	switch out {
	case skipped:
		if o != nil && o.Seal != nil && !strings.EqualFold(o.Seal.PubKey, from) {
			return common.InvalidInputError(cmd, args[0], fmt.Errorf("the message is from %s, not --from", social.Npub(o.Seal.PubKey)))
		}
		return common.AuthError(cmd, errors.New("the signer refused to open this message"))
	case invalid:
		return common.InvalidInputError(cmd, args[0], errors.New("not a gift wrap this signer can open"))
	}
	proof, err := nip59.Prove(ctx, keyer, o.Seal, o.Seal.PubKey)
	if err != nil {
		if transportFailure(err) {
			return social.SignerError(cmd, err)
		}
		return keyresolve.SignerError(cmd, "", err)
	}
	raw, err := json.MarshalIndent(proof, "", "  ")
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	if path, _ := cmd.Flags().GetString("out"); path != "" {
		if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
			return common.RuntimeError(cmd, err)
		}
		return nil
	}
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
	return nil
}

// loadWrap reads a gift wrap from a file, stdin ("-"), or relays by id.
func loadWrap(cmd *cobra.Command, ref string) (*nip01.Event, error) {
	var raw []byte
	var err error
	switch {
	case ref == "-":
		raw, err = io.ReadAll(cmd.InOrStdin())
	case fileExists(ref):
		raw, err = os.ReadFile(ref)
	default:
		return fetchWrap(cmd, ref)
	}
	if err != nil {
		return nil, common.RuntimeError(cmd, err)
	}
	evs, err := client.ParseEvents(raw, ref)
	if err != nil || len(evs) != 1 {
		if err == nil {
			err = fmt.Errorf("expected one event, got %d", len(evs))
		}
		return nil, common.InvalidInputError(cmd, ref, err)
	}
	return evs[0], nil
}

func fetchWrap(cmd *cobra.Command, ref string) (*nip01.Event, error) {
	id, err := social.EventID(cmd, ref)
	if err != nil {
		return nil, err
	}
	targets, err := social.Relays(cmd)
	if err != nil {
		return nil, err
	}
	timeout, _ := cmd.Flags().GetDuration("timeout")
	var evs []*nip01.Event
	err = common.WithSpinner(cmd, "fetching the wrap", func() error {
		var qErr error
		evs, qErr = client.QueryTargets(cmd.Context(), targets, nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{IDs: []string{id}}), timeout)
		return qErr
	})
	if err != nil {
		return nil, readError(cmd, err)
	}
	for _, ev := range evs {
		if ev.ID == id {
			return ev, nil
		}
	}
	return nil, common.NotFoundError(cmd, ref, errors.New("no relay has this event"))
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
