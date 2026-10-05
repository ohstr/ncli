// Package space implements `ncli space`: a generic command surface for
// NIP-53 meeting spaces. A space is the persistent, addressable record of
// where a meeting lives and who hosts it -- the spec deliberately leaves
// its transport ("service"/"endpoint") unspecified, so a space can point
// at anything. ncli's own voice/audio huddle protocol is just one thing a
// space can point at, not the reason this command exists; "ncli huddle"
// keeps its exact own behavior (it's about the ephemeral transport call,
// not the persistent space), and "join" here is that same huddle-join
// code, reused rather than reimplemented, under this more general name.
package space

import (
	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/huddle"
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
join"/"ncli huddle join" afterwards. Pointing --service/--endpoint at
something else instead describes a space ncli can't dial itself, which is
still a perfectly valid space.`,
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
	// Reused, not reimplemented: same flags, same resolution/dial/TUI path
	// as "ncli huddle join" -- see huddle.NewJoinCommand's own doc comment.
	// Short is overridden since "huddle"'s own phrasing ("watch who is
	// talking") reads oddly listed among create/list/show here; Long and
	// the RunE/flags are left exactly as huddle defines them.
	joinCmd := huddle.NewJoinCommand()
	joinCmd.Short = "Join a space's huddle call and watch who is talking"
	cmd.AddCommand(joinCmd)

	return cmd
}
