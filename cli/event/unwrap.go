package event

import (
	"errors"
	"fmt"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/nip59"
	"github.com/rs/zerolog/log"
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
private key, or through --signer: decrypts the wrap, checks the seal's
signature, decrypts the inner event (the "rumor", e.g. a NIP-17 direct
message) and checks it claims the same author as the seal, and that its
id, if it has one, is its hash.

--from (repeatable) opens only seals by those senders; any other is
skipped without being decrypted, and the output is always a list.
Prints the rumor and its verified sender.`,
		Example: `  ncli event unwrap nevent1... --identity me
  ncli find --kinds 1059 --tag p=<my-hex> -s wss://inbox.example | ncli event unwrap --signer bunker+unix:///run/signer/agent.sock --from npub1... --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			identity, _ := cmd.Flags().GetString("identity")
			signerFlag, _ := cmd.Flags().GetString("signer")
			fromFlags, _ := cmd.Flags().GetStringArray("from")
			if identity != "" && signerFlag != "" {
				return common.InvocationError(cmd, errors.New("pass --identity or --signer, not both"))
			}
			from, err := social.People(cmd, fromFlags)
			if err != nil {
				return err
			}
			var opener nip59.Opener
			if signerFlag != "" {
				s, closeSigner, err := social.Signer(cmd)
				if err != nil {
					return err
				}
				defer closeSigner()
				opener = s
			} else {
				priv, err := keyresolve.ResolveIdentity(cmd, identity, "event.identity")
				if err != nil {
					return err
				}
				s, err := client.NewLocalSigner(priv)
				if err != nil {
					return common.InvalidInputError(cmd, "", errors.New("identity is not a private key"))
				}
				opener = s
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
			skippedCount := 0
			for _, l := range hits {
				o, err := nip59.Unwrap(ctx, opener, l.best().Event, nip59.OpenOptions{From: from})
				if len(from) > 0 && (errors.Is(err, nip59.ErrForeignSender) || errors.Is(err, nip46.ErrDenied)) {
					skippedCount++
					continue
				}
				if err != nil {
					return common.InvalidInputError(cmd, l.Ref.Input, err)
				}
				u := unwrapped{Input: l.Ref.Input, SealID: o.Seal.ID, Sender: o.Sender, Rumor: o.Rumor}
				if o.Wrap != nil {
					u.WrapID = o.Wrap.ID
				}
				out = append(out, u)
			}
			if skippedCount > 0 {
				log.Info().Int("skipped", skippedCount).Msg("seals from other senders left unopened")
			}
			if len(from) > 0 {
				// Some inputs may be skipped, so the shape can't follow them.
				single = false
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
	cmd.Flags().String("signer", "", "Open through a signer instead: bunker+unix:///path.sock, bunker://... (the key stays there)")
	cmd.Flags().StringArray("from", nil, "Open only seals by this sender (repeatable); others are skipped unopened")
	return cmd
}
