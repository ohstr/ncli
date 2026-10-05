package space

import (
	"errors"
	"fmt"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/huddle"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip53"
	"github.com/spf13/cobra"
)

// newJoinCommand wraps huddle.NewJoinCommand so the id can be omitted
// when --relay has exactly one open space -- the same "sole entry, use
// it" convenience already applied to vault identities elsewhere in this
// CLI (see cli/keyresolve). With zero or more than one open space, this
// errors rather than guessing which one was meant.
func newJoinCommand() *cobra.Command {
	cmd := huddle.NewJoinCommand()
	cmd.Use = "join [room|space]"
	cmd.Short = "Join a space's huddle call and watch who is talking"
	cmd.Long += `

The argument may be omitted entirely when --relay has exactly one open
space -- it's resolved and joined the same as if you'd typed its
30312:<pubkey>:<d> coordinate yourself, chat included.`
	cmd.Args = common.MaximumNArgs(1)

	innerRunE := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			return innerRunE(cmd, args)
		}
		address, err := soleOpenSpaceAddress(cmd)
		if err != nil {
			return err
		}
		return innerRunE(cmd, []string{address})
	}

	return cmd
}

// soleOpenSpaceAddress resolves --relay's one open space and returns its
// full 30312:<pubkey>:<d> coordinate -- not just its bare id, so this
// stays unambiguous even if two different pubkeys happen to use the same
// identifier, and resolves through the same activity path a coordinate
// typed by hand would (bringing chat along with it).
func soleOpenSpaceAddress(cmd *cobra.Command) (string, error) {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	relayFlag, _ := cmd.Flags().GetString("relay")
	relayURL, err := resolveRelay(cmd, relayFlag)
	if err != nil {
		return "", err
	}

	targets, err := client.TargetsFromRelayList([]string{relayURL.String()})
	if err != nil {
		return "", common.RuntimeError(cmd, err)
	}

	filters := nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{Kinds: []int{nip53.KindMeetingSpace}})

	var events []*nip01.Event
	err = common.WithSpinner(cmd, fmt.Sprintf("Finding the open space on %s", relayURL.Host), func() error {
		var qErr error
		events, qErr = client.QueryTargets(ctx, targets, filters, defaultQueryTimeout)
		return qErr
	})
	if err != nil {
		if errors.Is(err, client.ErrNoReachableTargets) {
			return "", common.NetworkError(cmd, relayURL.String(), err)
		}
		return "", common.RuntimeError(cmd, err)
	}

	spaces := selectSpaces(events, time.Now(), nip53.DefaultStaleWindow, false)
	switch len(spaces) {
	case 0:
		return "", common.NotFoundError(cmd, relayURL.String(), fmt.Errorf("no open spaces on %s -- pass one's id, or create one with \"ncli space create\"", relayURL.Host))
	case 1:
		return spaces[0].Address, nil
	default:
		ids := make([]string, len(spaces))
		for i, s := range spaces {
			ids[i] = s.Identifier
		}
		return "", common.InvocationError(cmd, fmt.Errorf("more than one open space on %s (%s) -- pass the one you want", relayURL.Host, strings.Join(ids, ", ")))
	}
}
