package groups

import (
	"context"
	"net/url"
	"os/signal"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip29"
	"github.com/spf13/cobra"
)

// metadataQueryTimeout bounds how long "groups edit" waits for the group's
// current kind:39000 metadata before giving up and editing from a blank
// slate -- a slow relay here shouldn't hang the command indefinitely.
const metadataQueryTimeout = 10 * time.Second

func newEditCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit <group-id>",
		Short: "Edit a group's metadata",
		Long: `kind:9002 replaces the group's entire metadata, not just the fields named
here -- the relay's mirrored kind:39000 ends up with exactly what this event
carries, nothing merged in from before. So this command reads the group's
current metadata first and only overrides the fields a flag explicitly
named, to avoid silently clearing everything else (visibility included) on
an edit that only meant to change the name.`,
		Example: `  ncli groups edit standup --name "Standup" --about "Daily sync"
  ncli groups edit standup --public --open`,
		Args: common.ExactArgs(1),
		RunE: runEdit,
	}

	cmd.Flags().String("name", "", "Group display name")
	cmd.Flags().String("about", "", "Group description")
	cmd.Flags().String("picture", "", "Group picture URL")
	cmd.Flags().String("banner", "", "Group banner URL")
	cmd.Flags().Bool("private", false, "Require membership to read the group's content")
	cmd.Flags().Bool("public", false, "Anyone can read the group's content")
	cmd.Flags().Bool("closed", false, "Require an invite/approval to join")
	cmd.Flags().Bool("open", false, "Anyone can join")
	cmd.MarkFlagsMutuallyExclusive("private", "public")
	cmd.MarkFlagsMutuallyExclusive("closed", "open")

	return cmd
}

func runEdit(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	groupID := args[0]

	relayURL, pubKeyHex, privKeyHex, err := resolveWriteTarget(cmd)
	if err != nil {
		return err
	}

	current, err := currentGroupMetadata(ctx, relayURL, groupID)
	if err != nil {
		return common.NetworkError(cmd, relayURL.String(), err)
	}

	params := mergeEditParams(current, pubKeyHex, groupID, editFlagsFromCmd(cmd))

	ev := nip29.NewEditMetadata(pubKeyHex, groupID, params)
	return publishGroupEvent(ctx, cmd, relayURL, groupID, ev, privKeyHex)
}

// editFlags is runEdit's cobra flags, read once up front so
// mergeEditParams stays pure and testable without a *cobra.Command.
type editFlags struct {
	name, about, picture, banner             string
	nameSet, aboutSet, pictureSet, bannerSet bool
	private, public, closed, open            bool
}

func editFlagsFromCmd(cmd *cobra.Command) editFlags {
	var f editFlags
	f.name, _ = cmd.Flags().GetString("name")
	f.nameSet = cmd.Flags().Changed("name")
	f.about, _ = cmd.Flags().GetString("about")
	f.aboutSet = cmd.Flags().Changed("about")
	f.picture, _ = cmd.Flags().GetString("picture")
	f.pictureSet = cmd.Flags().Changed("picture")
	f.banner, _ = cmd.Flags().GetString("banner")
	f.bannerSet = cmd.Flags().Changed("banner")
	f.private = cmd.Flags().Changed("private")
	f.public = cmd.Flags().Changed("public")
	f.closed = cmd.Flags().Changed("closed")
	f.open = cmd.Flags().Changed("open")
	return f
}

// mergeEditParams layers f's explicitly-changed fields onto current,
// leaving everything else as-is -- kind:9002 replaces the group's entire
// metadata, so without this merge, an edit that only meant to change the
// name would silently clear visibility, about, pictures, and everything
// else back to blank/public/open.
func mergeEditParams(current *nip29.GroupMetadata, pubKeyHex, groupID string, f editFlags) nip29.GroupMetadataParams {
	params := nip29.GroupMetadataParams{
		SelfPubkey:        pubKeyHex,
		ID:                groupID,
		Name:              current.Name,
		Picture:           current.Picture,
		Banner:            current.Banner,
		About:             current.About,
		Parent:            current.Parent,
		Private:           current.Private,
		Restricted:        current.Restricted,
		Hidden:            current.Hidden,
		Closed:            current.Closed,
		LiveKit:           current.LiveKit,
		SupportedKinds:    current.SupportedKinds,
		SupportedKindsSet: current.SupportedKindsSet,
	}

	if f.nameSet {
		params.Name = f.name
	}
	if f.aboutSet {
		params.About = f.about
	}
	if f.pictureSet {
		params.Picture = f.picture
	}
	if f.bannerSet {
		params.Banner = f.banner
	}
	if f.private {
		params.Private = true
	}
	if f.public {
		params.Private = false
	}
	if f.closed {
		params.Closed = true
	}
	if f.open {
		params.Closed = false
	}
	return params
}

// currentGroupMetadata looks up groupID's current kind:39000 on relayURL,
// returning a zero-value *nip29.GroupMetadata (not an error) if the group
// has no metadata event yet -- a brand-new group, or one on a relay that
// hasn't mirrored it yet, simply edits from a blank slate.
func currentGroupMetadata(ctx context.Context, relayURL *url.URL, groupID string) (*nip29.GroupMetadata, error) {
	targets, err := client.TargetsFromRelayList([]string{relayURL.String()})
	if err != nil {
		return nil, err
	}

	filters := nip01.NewSubscriptionFilterGroup(
		nip01.NewFilter().WithKinds(nip29.KindGroupMetadata).WithTag("d", groupID).WithLimit(1),
	)

	events, err := client.QueryTargets(ctx, targets, filters, metadataQueryTimeout)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return &nip29.GroupMetadata{}, nil
	}

	meta, err := nip29.ParseGroupMetadata(events[0])
	if err != nil {
		// A malformed existing metadata event shouldn't block the edit --
		// fall back to blank rather than refusing to let the caller fix it.
		return &nip29.GroupMetadata{}, nil
	}
	return meta, nil
}
