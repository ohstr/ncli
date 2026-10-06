package groups

import (
	"context"
	"net/url"
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip29"
	"github.com/spf13/cobra"
)

// randomGroupIDBytes is the entropy behind a generated group id: 8 bytes
// (16 hex characters) is short enough to type back if needed, long enough
// that two independently generated ids won't collide in practice.
const randomGroupIDBytes = 8

func newCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create [group-id]",
		Short: "Create a new NIP-29 group",
		Long: `Existence is established by the relay reacting to this event -- there is
no separate "create" step beyond publishing it, and any id the creator
picks is valid. A random id is generated if group-id is omitted.

The group is created private and closed by default on a relay following
the nmilat NIP-29 implementation; use "groups edit" afterwards to open it
up.

--parent makes this a NIP-29 subgroup: a second, immediate kind:9002
sets the new group's parent, which the relay then mirrors onto the
parent's own child list. You must be an admin of both groups (you
already are of the one just created); the two must also share the same
--private/--public setting, since the relay rejects linking groups with
different visibility.`,
		Example: `  ncli groups create standup
  ncli groups create
  ncli groups create standup-notes --parent standup`,
		Args: common.MaximumNArgs(1),
		RunE: runCreate,
	}
	cmd.Flags().String("parent", "", "Parent group id -- creates this group as a subgroup of it")
	return cmd
}

func runCreate(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	groupID := ""
	if len(args) == 1 {
		groupID = args[0]
	}
	if groupID == "" {
		id, err := randomHex(randomGroupIDBytes)
		if err != nil {
			return common.RuntimeError(cmd, err)
		}
		groupID = id
	}

	relayURL, pubKeyHex, privKeyHex, err := resolveWriteTarget(cmd)
	if err != nil {
		return err
	}

	ev := nip29.NewCreateGroup(pubKeyHex, groupID)

	parent, _ := cmd.Flags().GetString("parent")
	if parent == "" {
		return publishGroupEvent(ctx, cmd, relayURL, groupID, ev, privKeyHex)
	}
	return createWithParent(ctx, cmd, relayURL, groupID, parent, ev, privKeyHex)
}

// createWithParent publishes the create event, then -- only once that
// succeeds -- a second kind:9002 setting the new group's parent. Private
// and Closed are restated as true (the create default) since kind:9002
// is a full replace and nothing else has had a chance to change them yet.
// Both reports are combined into a single JSON value under --json, never
// two concatenated ones, so a scripted caller gets one well-formed result.
func createWithParent(ctx context.Context, cmd *cobra.Command, relayURL *url.URL, groupID, parent string, createEv *nip01.Event, privKeyHex string) error {
	createReport, err := signAndPublish(ctx, cmd, relayURL, groupID, createEv, privKeyHex)
	if err != nil {
		return err
	}
	if !createReport.AllSucceeded() {
		printPublishReport(cmd, createReport)
		return failedPublishErr(cmd, createReport)
	}

	parentEv := nip29.NewEditMetadata("", groupID, nip29.GroupMetadataParams{
		Parent:  parent,
		Private: true,
		Closed:  true,
	})
	parentReport, err := signAndPublish(ctx, cmd, relayURL, groupID, parentEv, privKeyHex)
	if err != nil {
		return err
	}

	jsonMode, _ := cmd.Flags().GetBool("json")
	if jsonMode {
		common.PrintJSON(map[string]any{"create": createReport, "set_parent": parentReport})
		return failedPublishErr(cmd, parentReport)
	}
	printPublishReport(cmd, createReport)
	printPublishReport(cmd, parentReport)
	return failedPublishErr(cmd, parentReport)
}
