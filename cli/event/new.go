package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip40"
	"github.com/spf13/cobra"
)

func newNewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new",
		Short: "Build an unsigned event from flags",
		Long: `Prints an unsigned event built from flags, ready for "ncli id sign -e -"
or a socket signer. With --pubkey the id is filled in too.

--tag key=value adds a two-element tag; --tag-json adds any tag as a JSON
array (e.g. '["e","<id>","wss://relay","reply"]'). --tag tags come first,
then --tag-json tags, each in the order given.`,
		Example: `  ncli event new --content "hello" --tag t=nostr --pubkey npub1...
  ncli event new --kind 1111 --content-file reply.txt --tag-json '["E","<id>","",""]'
  ncli event new --content hi --pubkey me | ncli id sign -e - -o - --identity me | ncli publish -e -`,
		Args: common.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, _ := cmd.Flags().GetInt("kind")
			content, err := contentFromFlags(cmd)
			if err != nil {
				return err
			}
			tags, err := tagsFromFlags(cmd)
			if err != nil {
				return err
			}
			ev := &nip01.Event{Kind: kind, Content: content, Tags: tags}

			now := time.Now()
			if at, _ := cmd.Flags().GetInt64("created-at"); at > 0 {
				ev.CreatedAt = uint64(at)
			} else {
				ev.CreatedAt = uint64(now.Unix())
			}
			if exp, _ := cmd.Flags().GetDuration("expiration"); exp > 0 {
				nip40.AddExpiration(ev, time.Unix(int64(ev.CreatedAt), 0).Add(exp))
			}

			if p, _ := cmd.Flags().GetString("pubkey"); p != "" {
				insp, err := client.ResolveIdentifier(p)
				if err != nil {
					return keyresolve.ClassifyIdentifierError(cmd, p, err)
				}
				if err := fillID(ev, insp.PubKeyHex, now); err != nil {
					return common.InvalidInputError(cmd, p, err)
				}
			}
			return printJSON(cmd, ev)
		},
	}
	cmd.Flags().IntP("kind", "k", 1, "Event kind")
	cmd.Flags().StringP("content", "c", "", "Event content")
	cmd.Flags().String("content-file", "", `Read content from a file ("-" is stdin; one trailing newline is dropped)`)
	cmd.Flags().StringArray("tag", nil, "Tag as key=value (repeatable)")
	cmd.Flags().StringArray("tag-json", nil, `Tag as a JSON array of strings (repeatable), e.g. '["p","<hex>","wss://relay"]'`)
	cmd.Flags().String("pubkey", "", "Author: npub, hex, nprofile, nip-05 or vault label; fills pubkey and id")
	cmd.Flags().Int64("created-at", 0, "Unix time (default: now)")
	cmd.Flags().Duration("expiration", 0, "Add a NIP-40 expiration this long after created_at")
	return cmd
}

func contentFromFlags(cmd *cobra.Command) (string, error) {
	content, _ := cmd.Flags().GetString("content")
	path, _ := cmd.Flags().GetString("content-file")
	if path == "" {
		return content, nil
	}
	if cmd.Flags().Changed("content") {
		return "", common.InvocationError(cmd, errors.New("use --content or --content-file, not both"))
	}
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(cmd.InOrStdin())
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", common.InvalidInputError(cmd, path, err)
	}
	s := string(data)
	s = strings.TrimSuffix(s, "\n")
	return strings.TrimSuffix(s, "\r"), nil
}

func tagsFromFlags(cmd *cobra.Command) ([][]string, error) {
	tags := [][]string{}
	pairs, _ := cmd.Flags().GetStringArray("tag")
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, common.InvalidInputError(cmd, p, fmt.Errorf("--tag %q must be key=value", p))
		}
		tags = append(tags, []string{k, v})
	}
	raws, _ := cmd.Flags().GetStringArray("tag-json")
	for _, raw := range raws {
		var t []string
		if err := json.Unmarshal([]byte(raw), &t); err != nil || len(t) == 0 {
			return nil, common.InvalidInputError(cmd, raw, fmt.Errorf("--tag-json must be a non-empty JSON array of strings"))
		}
		tags = append(tags, t)
	}
	return tags, nil
}
