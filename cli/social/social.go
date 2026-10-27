// Package social holds what the commands that talk on Nostr through a
// signer share: dm, event's social actions, groups chat and profile writes.
package social

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/utils"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// SignerFlagHelp describes --signer the same way everywhere.
const SignerFlagHelp = "Sign through bunker+unix:///path.sock, bunker://..., or an identity (vault label or nsec); also NCLI_SIGNER"

// AddSignerFlag adds --signer to cmd.
func AddSignerFlag(cmd *cobra.Command) {
	cmd.Flags().String("signer", "", SignerFlagHelp)
}

// AddRelaysFlag adds -s/--relays to cmd.
func AddRelaysFlag(cmd *cobra.Command, help string) {
	cmd.Flags().StringP("relays", "s", "", help)
}

// Signer resolves --signer, else NCLI_SIGNER. The signer also encrypts,
// decrypts and logs in (NIP-42). Call the returned close when done.
func Signer(cmd *cobra.Command) (client.Crypto, func(), error) {
	value, _ := cmd.Flags().GetString("signer")
	if value == "" {
		value = viper.GetString("signer")
	}
	if value == "" {
		return nil, nil, common.UsageError(cmd, errors.New("--signer is required (or set NCLI_SIGNER)"))
	}
	jsonMode, _ := cmd.Flags().GetBool("json")
	s, closeSigner, err := keyresolve.ResolveSigner(cmd, jsonMode, value)
	if err != nil {
		return nil, nil, err
	}
	c, ok := s.(client.Crypto)
	if !ok {
		closeSigner()
		return nil, nil, common.UnsupportedError(cmd, "", errors.New("this signer can't encrypt or decrypt"))
	}
	return c, closeSigner, nil
}

// Relays reads -s/--relays, which these commands require: a private
// message or a group post never falls back to the default relay list.
func Relays(cmd *cobra.Command) (*client.TargetsSpec, error) {
	value, _ := cmd.Flags().GetString("relays")
	if strings.TrimSpace(value) == "" {
		return nil, common.UsageError(cmd, errors.New("-s/--relays is required"))
	}
	targets, err := client.TargetsFromRelayList(SplitList(value))
	if err != nil {
		return nil, common.InvalidInputError(cmd, value, err)
	}
	for _, t := range targets.Relays {
		if t.Type != client.FlOW_REMOTE {
			return nil, common.InvalidInputError(cmd, t.Label(), errors.New("a relay URL is required, not a local file"))
		}
	}
	return targets, nil
}

// SplitList splits a comma-separated value, dropping blanks.
func SplitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Text is the message text: args[i], or stdin when it is "-". Empty text is
// refused.
func Text(cmd *cobra.Command, args []string, i int) (string, error) {
	if len(args) <= i {
		return "", common.InvocationError(cmd, errors.New("message text is required (or - to read it from stdin)"))
	}
	text := args[i]
	if text == "-" {
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", common.RuntimeError(cmd, fmt.Errorf("read stdin: %w", err))
		}
		text = strings.TrimRight(string(raw), "\n")
	}
	if strings.TrimSpace(text) == "" {
		return "", common.InvalidInputError(cmd, "", errors.New("message text is empty"))
	}
	return text, nil
}

// People resolves each value with keyresolve.Pubkey, keeping order and
// dropping repeats.
func People(cmd *cobra.Command, values []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, v := range values {
		pk, err := keyresolve.Pubkey(cmd, v)
		if err != nil {
			return nil, err
		}
		if !seen[pk] {
			seen[pk] = true
			out = append(out, pk)
		}
	}
	return out, nil
}

// EventID reads an event id given as hex, note or nevent.
func EventID(cmd *cobra.Command, value string) (string, error) {
	v := strings.TrimSpace(value)
	var id string
	var err error
	switch {
	case strings.HasPrefix(v, "note1"):
		id, err = nip19.DecodeNote(v)
	case strings.HasPrefix(v, "nevent1"):
		var p *nip19.EventPointer
		if p, err = nip19.DecodeEvent(v); err == nil {
			id = p.ID
		}
	default:
		id, err = v, utils.Validate32Key(v)
	}
	if err != nil {
		return "", common.InvalidInputError(cmd, value, fmt.Errorf("not an event id (hex, note or nevent): %w", err))
	}
	return strings.ToLower(id), nil
}

// Npub encodes a hex pubkey, or returns it unchanged if it can't.
func Npub(hex string) string {
	n, err := nip19.EncodePublicKey(hex)
	if err != nil {
		return hex
	}
	return n
}

// Publish sends events to targets, logging in with signer when a relay
// asks, and returns the report.
func Publish(ctx context.Context, cmd *cobra.Command, targets *client.TargetsSpec, events []*nip01.Event, signer client.Signer) (*client.PublishReport, error) {
	var report *client.PublishReport
	err := common.WithSpinner(cmd, fmt.Sprintf("publishing to %d relay(s)", len(targets.Relays)), func() error {
		var pErr error
		report, pErr = client.PublishToTargets(ctx, targets, events, signer)
		return pErr
	})
	if err != nil {
		if errors.Is(err, client.ErrNoReachableTargets) {
			return nil, common.NetworkError(cmd, "", err)
		}
		if le := keyresolve.LoginError(cmd, err); le != nil {
			return nil, le
		}
		return nil, common.RuntimeError(cmd, err)
	}
	return report, nil
}

// ReportError turns a report with a failed attempt into the command's
// error, nil if every relay accepted. The relay's NIP-01 reason prefix picks
// the code, as in "ncli publish".
func ReportError(cmd *cobra.Command, report *client.PublishReport) error {
	if report.AllSucceeded() {
		return nil
	}
	if report.LoginError != nil {
		if le := keyresolve.LoginError(cmd, report.LoginError); le != nil {
			return le
		}
	}
	var reason string
	for _, r := range report.Results {
		if !r.Accepted {
			reason = r.Error
			break
		}
	}
	err := fmt.Errorf("%d of %d publish attempts failed: %s", report.Failed, report.Attempted, reason)
	_, msg, _ := strings.Cut(reason, "relay rejected event: ")
	if msg == "" {
		msg = reason
	}
	switch {
	case strings.HasPrefix(msg, "duplicate:"):
		return common.ConflictError(cmd, "", err)
	case strings.HasPrefix(msg, "restricted:"), strings.HasPrefix(msg, "auth-required:"):
		return common.AuthError(cmd, err)
	case strings.HasPrefix(msg, "invalid:"):
		return common.InvalidInputError(cmd, "", err)
	}
	return common.RuntimeError(cmd, err)
}

// PrintReportLines prints one line per relay result, as "ncli publish" does.
func PrintReportLines(cmd *cobra.Command, report *client.PublishReport) {
	for _, r := range report.Results {
		if r.Accepted {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "published %s to %s\n", r.ID, r.Relay)
		} else {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "failed %s to %s: %s\n", r.ID, r.Relay, r.Error)
		}
	}
}

// SignerError classifies a signing, encrypting or decrypting failure.
func SignerError(cmd *cobra.Command, err error) error {
	return keyresolve.SignerError(cmd, "", err)
}
