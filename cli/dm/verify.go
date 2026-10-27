package dm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/nmilat/nip59"
	"github.com/spf13/cobra"
)

// verdict is dm verify's --json result.
type verdict struct {
	Verified bool            `json:"verified"`
	From     string          `json:"from"`
	Reason   string          `json:"reason,omitempty"`
	Message  *social.Message `json:"message,omitempty"`
}

func newVerifyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify <proof.json|-> --from <who>",
		Short: "Check a proof of one private message, without any key",
		Long: `Checks a "dm prove" proof offline: the seal is a kind 13 signed by
--from, the keys' MAC holds over its payload, the payload decrypts and
unpads, the message's id is its hash and its author is --from. Prints
the message, its recipients ("p" tags) and when it was written.

Exits 1 if anything fails; no key is involved.`,
		Example: `  ncli dm verify proof.json --from npub1...
  ncli dm verify - --from alice@example.com --json < proof.json`,
		Args: common.ExactArgs(1),
		RunE: runVerify,
	}
	cmd.Flags().String("from", "", "Expected sender: npub, hex, nprofile, nip-05 or vault label (required)")
	return cmd
}

func runVerify(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	fromFlag, _ := cmd.Flags().GetString("from")
	if fromFlag == "" {
		return common.InvocationError(cmd, errors.New("--from is required"))
	}
	from, err := keyresolve.Pubkey(cmd, fromFlag)
	if err != nil {
		return err
	}
	var raw []byte
	if args[0] == "-" {
		raw, err = io.ReadAll(cmd.InOrStdin())
	} else {
		raw, err = os.ReadFile(args[0])
	}
	if err != nil {
		return common.InvalidInputError(cmd, args[0], err)
	}
	var proof nip59.Proof
	if err := json.Unmarshal(raw, &proof); err != nil {
		return common.InvalidInputError(cmd, args[0], fmt.Errorf("not a proof: %w", err))
	}

	v := verdict{From: from}
	rumor, vErr := nip59.VerifyProof(&proof, from)
	if vErr == nil {
		m := social.FromEvent(rumor, "dm")
		v.Verified, v.Message = true, &m
	} else {
		v.Reason = vErr.Error()
	}
	if jsonMode {
		common.PrintJSON(v)
	} else if v.Verified {
		fmt.Fprintf(cmd.OutOrStdout(), "verified: %s wrote\n", social.Npub(from))
		social.PrintMessage(cmd.OutOrStdout(), *v.Message)
		for _, p := range v.Message.Mentions {
			fmt.Fprintf(cmd.OutOrStdout(), "to %s\n", social.Npub(p))
		}
	}
	if vErr != nil {
		return common.RuntimeError(cmd, fmt.Errorf("proof does not verify: %w", vErr))
	}
	return nil
}
