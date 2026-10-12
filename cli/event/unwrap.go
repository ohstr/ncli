package event

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os/signal"
	"strings"
	"syscall"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/go-flokicoin/crypto/schnorr"
	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip44"
	"github.com/ohstr/nmilat/nip59"
	"github.com/spf13/cobra"
)

type unwrapped struct {
	Input  string       `json:"input"`
	WrapID string       `json:"wrap_id,omitempty"`
	SealID string       `json:"seal_id"`
	Sender string       `json:"sender"`
	Rumor  *nip01.Event `json:"rumor"`
}

func newUnwrapCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unwrap [ref...]",
		Short: "Decrypt NIP-59 gift wraps addressed to you",
		Long: `Opens each kind 1059 gift wrap (or kind 13 seal) with --identity's
private key: decrypts the wrap, checks the seal's signature, decrypts
the inner event (the "rumor", e.g. a NIP-17 direct message) and checks
it claims the same author as the seal.

Needs a private key: a vault label or nsec. A bunker can't be used, as
decryption happens here. Prints the rumor and its verified sender.`,
		Example: `  ncli event unwrap nevent1... --identity me
  ncli find --kinds 1059 --tag p=<my-hex> -s wss://inbox.example | ncli event unwrap --identity me --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			identity, _ := cmd.Flags().GetString("identity")
			priv, err := keyresolve.ResolveIdentity(cmd, identity, "event.identity")
			if err != nil {
				return err
			}

			refs, single, err := collectRefs(cmd, args)
			if err != nil {
				return err
			}
			res, closeAuth, err := newResolver(cmd)
			if err != nil {
				return err
			}
			defer closeAuth()

			var lookups []*lookup
			_ = common.WithSpinner(cmd, fmt.Sprintf("looking up %d event(s)", len(refs)), func() error {
				lookups = res.resolve(ctx, refs, false)
				return nil
			})
			hits, missing := found(lookups)
			if err := missingError(cmd, res, missing); err != nil {
				return err
			}

			out := make([]unwrapped, 0, len(hits))
			for _, l := range hits {
				u, err := unwrapEvent(l.best().Event, priv)
				if err != nil {
					return common.InvalidInputError(cmd, l.Ref.Input, err)
				}
				u.Input = l.Ref.Input
				out = append(out, u)
			}

			if jsonMode(cmd) {
				return printShaped(cmd, out, single)
			}
			for i, u := range out {
				if i > 0 {
					outf(cmd, "\n")
				}
				outf(cmd, "from     %s\n", npub(u.Sender))
				outf(cmd, "kind     %d · %s\n", u.Rumor.Kind, describeKind(u.Rumor.Kind))
				for _, t := range u.Rumor.Tags {
					outf(cmd, "tag      %s\n", strings.Join(t, "  "))
				}
				outf(cmd, "content\n")
				for _, line := range strings.Split(u.Rumor.Content, "\n") {
					outf(cmd, "  %s\n", line)
				}
			}
			return nil
		},
	}
	cmd.Flags().String("identity", "", "Recipient key: vault label or nsec (or set NCLI_EVENT_IDENTITY)")
	return cmd
}

// unwrapEvent opens a gift wrap or a seal with the recipient's key.
func unwrapEvent(ev *nip01.Event, privHex string) (unwrapped, error) {
	var u unwrapped
	privBytes, err := hex.DecodeString(privHex)
	if err != nil {
		return u, errors.New("identity is not a private key")
	}
	priv, _ := btcec.PrivKeyFromBytes(privBytes)

	seal := ev
	switch ev.Kind {
	case nip59.KindGiftWrap:
		if err := ev.Verify(nip01.WithoutPowCheck()); err != nil {
			return u, fmt.Errorf("gift wrap does not verify: %w", err)
		}
		u.WrapID = ev.ID
		inner, err := decryptFrom(priv, ev.PubKey, ev.Content)
		if err != nil {
			return u, fmt.Errorf("gift wrap: %w", err)
		}
		if seal, err = decodeEvent([]byte(inner)); err != nil {
			return u, fmt.Errorf("gift wrap: %w", err)
		}
		if seal.Kind != nip59.KindSeal {
			return u, fmt.Errorf("gift wrap holds kind %d, not a seal", seal.Kind)
		}
	case nip59.KindSeal:
	default:
		return u, fmt.Errorf("kind %d is not a gift wrap (1059) or seal (13)", ev.Kind)
	}

	if err := seal.Verify(nip01.WithoutPowCheck()); err != nil {
		return u, fmt.Errorf("seal does not verify: %w", err)
	}
	inner, err := decryptFrom(priv, seal.PubKey, seal.Content)
	if err != nil {
		return u, fmt.Errorf("seal: %w", err)
	}
	rumor, err := decodeEvent([]byte(inner))
	if err != nil {
		return u, fmt.Errorf("seal: %w", err)
	}
	if !strings.EqualFold(rumor.PubKey, seal.PubKey) {
		return u, fmt.Errorf("rumor claims author %s but the seal is signed by %s", rumor.PubKey, seal.PubKey)
	}
	u.SealID, u.Sender, u.Rumor = seal.ID, seal.PubKey, rumor
	return u, nil
}

func decryptFrom(priv *btcec.PrivateKey, senderHex, payload string) (string, error) {
	pubBytes, err := hex.DecodeString(senderHex)
	if err != nil {
		return "", fmt.Errorf("bad sender key: %w", err)
	}
	pub, err := schnorr.ParsePubKey(pubBytes)
	if err != nil {
		return "", fmt.Errorf("bad sender key: %w", err)
	}
	key, err := nip44.GenerateConversationKey(priv, pub)
	if err != nil {
		return "", err
	}
	plain, err := nip44.Decrypt(payload, key)
	if err != nil {
		return "", fmt.Errorf("cannot decrypt (not addressed to this identity?): %w", err)
	}
	return plain, nil
}
