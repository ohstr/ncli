package event

import (
	"errors"
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip59"
	"github.com/spf13/cobra"
)

func newWrapCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wrap --to <who> [event-json|-]",
		Short: "Seal and gift-wrap an unsigned event to someone (NIP-59)",
		Long: `Low level: makes each given event a rumor by --signer, seals it with
--signer and gift-wraps it to --to under a one-time key. Prints the
wrap(s); publish them with "ncli publish -e -". For a chat message,
"ncli dm send" does all of it.`,
		Example: `  ncli event new --kind 14 --content hi --tag p=<hex> | ncli event wrap --to npub1... --signer me | ncli publish -e - -s wss://inbox.example`,
		RunE:    runWrap,
	}
	cmd.Flags().String("to", "", "Recipient: npub, hex, nprofile, nip-05 or vault label (required)")
	social.AddSignerFlag(cmd)
	return cmd
}

func runWrap(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	toFlag, _ := cmd.Flags().GetString("to")
	if toFlag == "" {
		return common.InvocationError(cmd, errors.New("--to is required"))
	}
	refs, single, err := collectRefs(cmd, args)
	if err != nil {
		return err
	}
	var rumors []*nip01.Event
	for _, r := range refs {
		if !r.inline() {
			return common.InvalidInputError(cmd, r.Input, errors.New("give the event itself as JSON, not a reference"))
		}
		rumors = append(rumors, r.Event)
	}
	to, err := keyresolve.Pubkey(cmd, toFlag)
	if err != nil {
		return err
	}
	signer, closeSigner, err := social.Signer(cmd)
	if err != nil {
		return err
	}
	defer closeSigner()

	wraps := make([]*nip01.Event, 0, len(rumors))
	for _, rumor := range rumors {
		seal, err := nip59.Seal(ctx, signer, rumor.Copy(), to)
		if err != nil {
			return social.SignerError(cmd, err)
		}
		wrap, err := nip59.WrapSeal(seal, to)
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
		wraps = append(wraps, wrap)
	}
	return printShaped(cmd, wraps, single)
}
