package dm

import (
	"fmt"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip17"
	"github.com/ohstr/nmilat/nip59"
	"github.com/spf13/cobra"
)

// sent is dm send's --json result.
type sent struct {
	ID     string                `json:"id"`
	To     string                `json:"to"`
	Wraps  []string              `json:"wraps"`
	Report *client.PublishReport `json:"report"`
}

func newSendCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send --to <who> <text|->",
		Short: "Send a private message",
		Long: `Seals the message with --signer and gift-wraps it to --to, then
publishes it to -s/--relays. A copy is wrapped to yourself too, so you
can read and prove what you sent; --no-self-copy skips it.

--to takes an npub, hex, nprofile, nip-05 or vault label. Prints the
message's id (the rumor's).

The signer is asked to sign two kind 13 seals and to nip44_encrypt to
--to and to yourself.`,
		Example: `  ncli dm send --to npub1... "on my way" -s wss://inbox.example
  echo "long text" | ncli dm send --to alice@example.com - -s wss://inbox.example --json`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return common.InvocationError(cmd, fmt.Errorf("one message argument expected, got %d (quote it)", len(args)))
			}
			return nil
		},
		RunE: runSend,
	}
	cmd.Flags().String("to", "", "Recipient: npub, hex, nprofile, nip-05 or vault label (required)")
	cmd.Flags().StringP("reply", "r", "", "Id of the message this answers (hex, note or nevent)")
	cmd.Flags().String("subject", "", "Conversation subject")
	cmd.Flags().Bool("no-self-copy", false, "Don't wrap a copy to yourself")
	social.AddRelaysFlag(cmd, "Comma-separated relays to publish to (required)")
	social.AddSignerFlag(cmd)
	return cmd
}

func runSend(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	jsonMode, _ := cmd.Flags().GetBool("json")

	toFlag, _ := cmd.Flags().GetString("to")
	if toFlag == "" {
		return common.InvocationError(cmd, fmt.Errorf("--to is required"))
	}
	text, err := social.Text(cmd, args, 0)
	if err != nil {
		return err
	}
	targets, err := social.Relays(cmd)
	if err != nil {
		return err
	}
	to, err := keyresolve.Pubkey(cmd, toFlag)
	if err != nil {
		return err
	}
	opts := []nip17.ChatMessageOption{nip17.WithRecipients(to)}
	if s, _ := cmd.Flags().GetString("subject"); s != "" {
		opts = append(opts, nip17.WithSubject(s))
	}
	if r, _ := cmd.Flags().GetString("reply"); r != "" {
		id, err := social.EventID(cmd, r)
		if err != nil {
			return err
		}
		opts = append(opts, nip17.WithReply(id, ""))
	}

	signer, closeSigner, err := social.Signer(cmd)
	if err != nil {
		return err
	}
	defer closeSigner()
	me := strings.ToLower(signer.PubKey())

	rumor := nip17.NewChatMessage(text, opts...)
	rumor.CreatedAt = uint64(time.Now().Unix())
	wraps, err := sealTo(cmd, signer, rumor, to)
	if err != nil {
		return err
	}
	if noCopy, _ := cmd.Flags().GetBool("no-self-copy"); !noCopy && to != me {
		self, err := sealTo(cmd, signer, rumor.Copy(), me)
		if err != nil {
			return err
		}
		wraps = append(wraps, self...)
	}

	report, err := social.Publish(ctx, cmd, targets, wraps, signer)
	if err != nil {
		return err
	}
	out := sent{ID: rumor.ID, To: to, Report: report, Wraps: []string{}}
	for _, w := range wraps {
		out.Wraps = append(out.Wraps, w.ID)
	}
	if jsonMode {
		common.PrintJSON(out)
	} else {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "sent %s to %s\n", rumor.ID, social.Npub(to))
		social.PrintReportLines(cmd, report)
	}
	return social.ReportError(cmd, report)
}

// sealTo seals rumor to recipient with signer and wraps it.
func sealTo(cmd *cobra.Command, signer client.Crypto, rumor *nip01.Event, recipient string) ([]*nip01.Event, error) {
	seal, err := nip59.Seal(cmd.Context(), signer, rumor, recipient)
	if err != nil {
		return nil, social.SignerError(cmd, err)
	}
	wrap, err := nip59.WrapSeal(seal, recipient)
	if err != nil {
		return nil, common.RuntimeError(cmd, err)
	}
	return []*nip01.Event{wrap}, nil
}
