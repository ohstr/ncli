package space

import (
	"os/signal"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip53"
	"github.com/spf13/cobra"
)

func newCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <id>",
		Short: "Publish a new NIP-53 meeting space",
		Long: `Publishes kind:30312. The id becomes both the space's "d" tag (its
addressable identity, 30312:<pubkey>:<id>) and its "room" tag -- ncli's own
convention is that --service plus that id together resolve to a huddle
room (see "ncli huddle join"'s own docs), so there is nothing meaningful to
set the room tag to independently.

--service defaults to --relay, which is what makes the space joinable with
"ncli space join"/"ncli huddle join" afterwards. Set --service/--endpoint
explicitly to describe a space pointing at some other meeting transport
instead -- still a valid space, just not one ncli itself can dial.

The signing identity is always recorded as the space's Host.`,
		Example: `  ncli space create standup
  ncli space create standup --summary "Daily sync" --hashtag standup
  ncli space create standup --service https://meet.example/standup   # not ncli-dialable`,
		Args: common.ExactArgs(1),
		RunE: runCreate,
	}

	cmd.Flags().String("summary", "", "Short description of the space")
	cmd.Flags().String("image", "", "Image URL")
	cmd.Flags().StringArray("hashtag", nil, "Hashtag to tag the space with (repeatable)")
	cmd.Flags().String("service", "", "Meeting transport URL (defaults to --relay, which ncli itself can dial)")
	cmd.Flags().String("endpoint", "", "Full transport endpoint, if different from --service")

	return cmd
}

func runCreate(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	id := args[0]

	relayFlag, _ := cmd.Flags().GetString("relay")
	relayURL, err := resolveRelay(cmd, relayFlag)
	if err != nil {
		return err
	}

	identityFlag, _ := cmd.Flags().GetString("identity")
	pubKeyHex, privKeyHex, err := resolveIdentity(cmd, identityFlag)
	if err != nil {
		return err
	}

	service, _ := cmd.Flags().GetString("service")
	if service == "" {
		service = relayURL.String()
	}
	endpoint, _ := cmd.Flags().GetString("endpoint")
	summary, _ := cmd.Flags().GetString("summary")
	image, _ := cmd.Flags().GetString("image")
	hashtags, _ := cmd.Flags().GetStringArray("hashtag")

	ev := nip53.NewMeetingSpace(nip53.MeetingSpaceParams{
		Pubkey:     pubKeyHex,
		Identifier: id,
		Room:       id,
		Summary:    summary,
		Image:      image,
		Status:     nip53.SpaceStatusOpen,
		Service:    service,
		Endpoint:   endpoint,
		Hashtags:   hashtags,
		Providers:  []nip53.Participant{{Pubkey: pubKeyHex, Role: nip53.RoleHost}},
	})

	return signAndPublish(ctx, cmd, relayURL, ev, privKeyHex)
}
