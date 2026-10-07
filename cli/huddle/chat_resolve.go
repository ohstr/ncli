package huddle

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
)

// ResolveChatActivity resolves arg (an naddr, or a 30312:/30313: coordinate)
// to the activity kind:1311 chat is scoped to and the relay chat lives on,
// the same way `space join` does. Errors are already classified.
func ResolveChatActivity(ctx context.Context, cmd *cobra.Command, arg, relayFlag string) (activity string, chatRelay *url.URL, err error) {
	ref, err := parseActivityRef(arg)
	if errors.Is(err, errNotActivityRef) {
		return "", nil, common.InvalidInputError(cmd, arg, errors.New("chat needs a space or session: an naddr or a 30312:<pubkey>:<d> coordinate, not a room id"))
	}
	if err != nil {
		return "", nil, common.InvalidInputError(cmd, arg, err)
	}
	targets, err := joinTargets(relayFlag, ref)
	if err != nil {
		return "", nil, common.InvocationError(cmd, err)
	}

	var target *joinTarget
	err = common.WithSpinner(cmd, fmt.Sprintf("Looking up %s", ref.Address()), func() error {
		var rErr error
		target, rErr = resolveJoinTarget(ctx, ref, targets)
		return rErr
	})
	switch {
	case err == nil:
		return target.Activity, target.ChatRelay, nil
	case errors.Is(err, client.ErrNoReachableTargets):
		return "", nil, common.NetworkError(cmd, arg, err)
	case errors.Is(err, errActivityNotFound):
		return "", nil, common.NotFoundError(cmd, arg, err)
	default:
		return "", nil, common.RuntimeError(cmd, err)
	}
}
