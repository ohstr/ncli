// Package groups implements `ncli groups`: the self-service NIP-29
// relay-hosted-groups command surface. Every write here is a plain signed
// event published over the normal websocket transport (the same one
// "ncli publish"/"ncli huddle" use) -- NIP-29 group actions are identified
// by the event's own signature, not session auth, so they live under their
// own top-level command rather than under "ncli relay", which is the
// NIP-98-signed HTTP surface for administering someone else's relay.
package groups

import (
	"github.com/ohstr/ncli/cli/common"
	"github.com/spf13/cobra"
)

// NewGroupsCommand builds the `ncli groups` command tree.
func NewGroupsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "groups",
		Short: "Create and manage NIP-29 relay-hosted groups",
		Long: `Self-service NIP-29 group actions: create/edit/delete a group, invite/
join/leave, manage members and pins, and delete a group's own event.

Every write below is a plain signed nostr event, published to the group's
relay the same way "ncli publish" sends any other event -- there is no
separate admin HTTP surface to authenticate against. Each one also attaches
the NIP-29 "previous" tag automatically (up to 3 references to the group's
own recent events), which is why these are dedicated commands rather than a
hand-written YAML event: populating that tag needs a relay round trip no
YAML file can do.

"groups list"/"groups show" need no identity -- they are plain reads. A
private group's roster is gated by NIP-42 relay auth on the relay side;
until ncli's own connections can authenticate (tracked separately), those
two commands only ever see what an anonymous connection is allowed to see,
which for a private group (the default on creation) is nothing.`,
		Example: `  ncli groups create standup
  ncli groups edit standup --name "Standup" --public
  ncli groups members add standup <pubkey>
  ncli groups list --relay wss://relay.example`,
		RunE: common.RequireSubcommand,
	}

	cmd.PersistentFlags().String("relay", "", "Relay the group lives on (falls back to the first configured prefs relay)")
	cmd.PersistentFlags().String("identity", "", "Identity to sign group events with -- vault label, nsec, npub, hex, nprofile, or nip-05")

	cmd.AddCommand(newCreateCommand())
	cmd.AddCommand(newEditCommand())
	cmd.AddCommand(newDeleteCommand())
	cmd.AddCommand(newInviteCommand())
	cmd.AddCommand(newJoinCommand())
	cmd.AddCommand(newLeaveCommand())
	cmd.AddCommand(newMembersCommand())
	cmd.AddCommand(newPinsCommand())
	cmd.AddCommand(newDeleteEventCommand())
	cmd.AddCommand(newListCommand())
	cmd.AddCommand(newShowCommand())

	return cmd
}
