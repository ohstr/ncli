package signer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/signer"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip46"
	"github.com/spf13/cobra"
)

func newStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check that a signer is answering on its socket",
		Long: `Pings the signer and prints its pubkey, policy hash and uptime. Exits 6
(network) when nothing answers, so it works as a liveness/readiness probe.`,
		Example: `  ncli signer status --socket /run/signer/agent.sock
  ncli signer status --socket bunker+unix:///run/signer/agent.sock --json`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := common.NoArgs(cmd, args); err != nil {
				return err
			}
			if err := cmd.ValidateRequiredFlags(); err != nil {
				return common.InvocationOrHelp(cmd, args, err)
			}
			return nil
		},
		RunE: runStatus,
	}
	cmd.Flags().String("socket", "", "Signer socket path or bunker+unix:// URI (required)")
	cmd.Flags().Duration("timeout", 5*time.Second, "Give up after this long")
	_ = cmd.MarkFlagRequired("socket")
	return cmd
}

func runStatus(cmd *cobra.Command, _ []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	socketFlag, _ := cmd.Flags().GetString("socket")
	timeout, _ := cmd.Flags().GetDuration("timeout")

	path, err := signer.ParseURI(socketFlag)
	if err != nil {
		return common.InvalidInputError(cmd, socketFlag, err)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()

	c, err := signer.Dial(ctx, path)
	if err != nil {
		return common.NetworkError(cmd, path, fmt.Errorf("signer not answering: %w", err))
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Call(ctx, nip46.MethodPing); err != nil {
		return common.NetworkError(cmd, path, fmt.Errorf("ping: %w", err))
	}
	st, err := c.Status(ctx)
	if err != nil {
		var re *signer.RemoteError
		if errors.As(err, &re) {
			return common.UnsupportedError(cmd, path, fmt.Errorf("signer has no status method: %w", err))
		}
		return common.NetworkError(cmd, path, err)
	}

	npub, _ := nip19.EncodePublicKey(st.PubKey)
	if jsonMode {
		common.PrintJSON(map[string]any{
			"ok":            true,
			"socket":        path,
			"pubkey":        st.PubKey,
			"npub":          npub,
			"policy_sha256": st.PolicySHA256,
			"rules":         st.Rules,
			"loaded_at":     st.LoadedAt,
			"uptime_s":      st.UptimeS,
			"version":       st.Version,
		})
		return nil
	}
	fmt.Println("socket: ", path)
	fmt.Println("npub:   ", npub)
	fmt.Println("pubkey: ", st.PubKey)
	fmt.Println("policy: ", st.PolicySHA256)
	fmt.Println("rules:  ", st.Rules)
	fmt.Println("loaded: ", time.Unix(st.LoadedAt, 0).UTC().Format(time.RFC3339))
	fmt.Println("uptime: ", (time.Duration(st.UptimeS) * time.Second).String())
	if st.Version != "" {
		fmt.Println("version:", st.Version)
	}
	return nil
}
