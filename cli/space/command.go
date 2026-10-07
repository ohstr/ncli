// Package space implements `ncli space`: a generic command surface for
// NIP-53 meeting spaces. A space is the persistent, addressable record of
// where a meeting lives and who hosts it -- the spec deliberately leaves
// its transport ("service"/"endpoint") unspecified, so a space can point
// at anything. ncli's own voice/audio huddle protocol is just one thing a
// space can point at, not the reason this command exists. "join" is
// implemented in cli/huddle (it needs huddle's own resolution/dial/TUI
// code) but mounted only here, not under "ncli huddle" -- voice is one
// option a space can enable, not a separate top-level thing to join.
// "ncli huddle" itself now covers only what isn't about a space at all:
// listing the relay's currently-occupied ephemeral rooms.
package space

import (
	"github.com/ohstr/ncli/cli/common"
	"github.com/spf13/cobra"
)

// NewSpaceCommand builds the `ncli space` command tree.
func NewSpaceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "space",
		Short: "Create, list, and join NIP-53 meeting spaces",
		Long: `A space (kind:30312) is a published, addressable record of where a
meeting lives and who hosts it -- durable even when nobody is in it, unlike
a huddle room, which exists only while occupied. Its "service"/"endpoint"
tag names the transport; the spec leaves that transport unspecified on
purpose, so a space can describe any meeting technology.

"space create" defaults that transport to ncli's own voice/audio huddle
protocol on --relay -- that's what makes the space joinable with "space
join" afterwards. Pointing --service/--endpoint at something else instead
describes a space ncli can't dial itself, which is still a perfectly
valid space.`,
		Example: `  ncli space create standup
  ncli space list --relay wss://relay.example
  ncli space show standup
  ncli space join standup`,
		RunE: common.RequireSubcommand,
	}

	cmd.PersistentFlags().String("relay", "", "Relay the space lives on (falls back to the first configured prefs relay)")
	cmd.PersistentFlags().String("identity", "", "Identity to sign/authenticate with -- vault label, nsec, npub, hex, nprofile, or nip-05")

	cmd.AddCommand(newCreateCommand())
	cmd.AddCommand(newListCommand())
	cmd.AddCommand(newShowCommand())
	// newJoinCommand wraps huddle.NewJoinCommand -- mounted only here, not
	// under "ncli huddle" -- see this package's own doc comment for why.
	cmd.AddCommand(newJoinCommand())
	cmd.AddCommand(newChatCommand())

	return cmd
}
