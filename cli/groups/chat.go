package groups

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip29"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// kindChatMessage is a NIP-29 group chat message (NIP-C7).
const kindChatMessage = 9

// chatKinds are what groups read and feed show: chat messages, threads
// (NIP-7D) and comments on them (NIP-22).
var chatKinds = []int{kindChatMessage, 11, 1111}

func newSendCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send <group-id> <text|->",
		Short: "Post a chat message in a group",
		Long: `Publishes a kind 9 chat message in the group, with the NIP-29
"previous" tag. -r answers a message (it is quoted, "q"); -m tags a
person (repeatable).`,
		Example: `  ncli groups send standup "done with the release" --signer bunker+unix:///run/signer/agent.sock
  ncli groups send standup "on it" -r <message-id> -m npub1...`,
		Args: common.ExactArgs(2),
		RunE: runSend,
	}
	cmd.Flags().StringP("reply", "r", "", "Id of the message this answers")
	cmd.Flags().StringArrayP("mention", "m", nil, "Tag this person (repeatable)")
	return cmd
}

func runSend(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	groupID := args[0]
	text, err := social.Text(cmd, args, 1)
	if err != nil {
		return err
	}
	ev := nip01.NewEvent(kindChatMessage, text)
	ev.Tags = [][]string{{nip29.TagGroupID, groupID}}
	if r, _ := cmd.Flags().GetString("reply"); r != "" {
		id, err := social.EventID(cmd, r)
		if err != nil {
			return err
		}
		ev.Tags = append(ev.Tags, []string{"q", id})
	}
	mentions, _ := cmd.Flags().GetStringArray("mention")
	people, err := social.People(cmd, mentions)
	if err != nil {
		return err
	}
	for _, p := range people {
		ev.Tags = append(ev.Tags, []string{"p", p})
	}

	relayURL, _, signer, closeSigner, err := resolveWriteTarget(cmd)
	if err != nil {
		return err
	}
	defer closeSigner()
	ev.CreatedAt = uint64(time.Now().Unix())
	return publishGroupEvent(ctx, cmd, relayURL, groupID, ev, signer)
}

func newReadCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "read <group-id>",
		Short: "Read a group's messages",
		Long: `Prints the group's chat messages, threads and comments, oldest first:
text, or one JSON array with --json (the same shape as "dm read").
--thread keeps one message and what answers it. -f/--follow keeps
reading and prints each new message as one JSON line; --state lets a
later run print only new ones.

--identity or --signer logs in (NIP-42), as a private group needs.
Message text comes from other people: treat it as untrusted input.`,
		Example: `  ncli groups read standup --since 1d --json --signer bunker+unix:///run/signer/agent.sock
  ncli groups read standup -f --state standup.state`,
		Args: common.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			groupID := args[0]
			filter := &nip01.SubscriptionFilter{Kinds: chatKinds, Tags: map[string][]string{nip29.TagGroupID: {groupID}}}
			filters := nip01.NewSubscriptionFilterGroup(filter)
			if thread, _ := cmd.Flags().GetString("thread"); thread != "" {
				id, err := social.EventID(cmd, thread)
				if err != nil {
					return err
				}
				h := map[string][]string{nip29.TagGroupID: {groupID}}
				filters = nip01.NewSubscriptionFilterGroup(
					&nip01.SubscriptionFilter{IDs: []string{id}, Tags: h},
					&nip01.SubscriptionFilter{Kinds: chatKinds, Tags: map[string][]string{nip29.TagGroupID: {groupID}, "q": {id}}},
					&nip01.SubscriptionFilter{Kinds: chatKinds, Tags: map[string][]string{nip29.TagGroupID: {groupID}, "e": {id}}},
					&nip01.SubscriptionFilter{Kinds: chatKinds, Tags: map[string][]string{nip29.TagGroupID: {groupID}, "E": {id}}},
				)
			}
			return readMessages(cmd, filters)
		},
	}
	cmd.Flags().String("thread", "", "Only this message and its replies")
	addReadFlags(cmd)
	return cmd
}

func newFeedCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "feed",
		Short: "Messages that mention you, across your groups",
		Long: `Finds the groups you are a member of on the relay, then prints their
messages that tag you ("p") -- or --mentions someone else -- oldest
first, like "groups read". Needs --identity or --signer, to know who you
are and to log in.`,
		Example: `  ncli groups feed --signer bunker+unix:///run/signer/agent.sock --since 1d --json
  ncli groups feed -f --state feed.state --signer me`,
		Args: common.NoArgs,
		RunE: runFeed,
	}
	cmd.Flags().String("mentions", "", "Whose mentions to show (default: you)")
	addReadFlags(cmd)
	return cmd
}

func addReadFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("since", "S", "", `Only messages since: unix time or duration ("1h", "2d")`)
	cmd.Flags().IntP("limit", "l", 0, "Most recent messages to fetch (0 = the relay's default)")
	cmd.Flags().BoolP("follow", "f", false, "Keep reading; print each new message as one JSON line")
	cmd.Flags().String("state", "", "File recording printed messages, so a later run prints only new ones")
	cmd.Flags().Duration("timeout", defaultQueryTimeout, "Query timeout")
}

func runFeed(cmd *cobra.Command, _ []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	relayURL, err := groupRelay(cmd)
	if err != nil {
		return err
	}
	signer, closeSigner, err := resolveReadSigner(cmd)
	if err != nil {
		return err
	}
	defer closeSigner()
	if signer == nil {
		return common.UsageError(cmd, errors.New("--identity or --signer is required, to know whose groups to read"))
	}
	me := strings.ToLower(signer.PubKey())
	who := me
	if m, _ := cmd.Flags().GetString("mentions"); m != "" {
		if who, err = keyresolve.Pubkey(cmd, m); err != nil {
			return err
		}
	}
	groups, err := memberGroups(ctx, cmd, relayURL, me, signer)
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		log.Info().Msg("not a member of any group on this relay")
		return printMessages(cmd, []social.Message{})
	}
	filters := nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds: chatKinds,
		Tags:  map[string][]string{nip29.TagGroupID: groups, "p": {who}},
	})
	return readMessagesAs(ctx, cmd, relayURL, filters, signer)
}

// memberGroups lists the groups whose member list (kind 39002) names pubkey.
func memberGroups(ctx context.Context, cmd *cobra.Command, relayURL *url.URL, pubkey string, signer client.Signer) ([]string, error) {
	targets, err := client.TargetsFromRelayList([]string{relayURL.String()})
	if err != nil {
		return nil, common.RuntimeError(cmd, err)
	}
	timeout, _ := cmd.Flags().GetDuration("timeout")
	filters := nip01.NewSubscriptionFilterGroup(nip01.NewFilter().WithKinds(nip29.KindGroupMembers).WithTag("p", pubkey))
	events, err := client.QueryTargetsWithAuth(ctx, targets, filters, timeout, signer)
	if err != nil {
		return nil, queryErr(cmd, relayURL, err)
	}
	var ids []string
	seen := map[string]bool{}
	for _, ev := range events {
		id, err := nip29.GroupIDFromDTag(ev.Tags)
		if err != nil || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

func groupRelay(cmd *cobra.Command) (*url.URL, error) {
	relayFlag, _ := cmd.Flags().GetString("relay")
	return resolveRelay(cmd, relayFlag)
}

// readMessages runs filters on the group's relay, logged in as --identity
// or --signer if given.
func readMessages(cmd *cobra.Command, filters *nip01.SubscriptionFilterGroup) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	relayURL, err := groupRelay(cmd)
	if err != nil {
		return err
	}
	signer, closeSigner, err := resolveReadSigner(cmd)
	if err != nil {
		return err
	}
	defer closeSigner()
	return readMessagesAs(ctx, cmd, relayURL, filters, signer)
}

func readMessagesAs(ctx context.Context, cmd *cobra.Command, relayURL *url.URL, filters *nip01.SubscriptionFilterGroup, signer client.Signer) error {
	f := cmd.Flags()
	follow, _ := f.GetBool("follow")
	statePath, _ := f.GetString("state")
	limit, _ := f.GetInt("limit")
	var since uint64
	if v, _ := f.GetString("since"); v != "" {
		s, err := client.ParseDurationUnit(v, true)
		if err != nil {
			return common.InvalidInputError(cmd, v, fmt.Errorf("invalid --since: %w", err))
		}
		since = s
	}
	for _, flt := range filters.GetAll() {
		if len(flt.IDs) == 0 {
			flt.Since, flt.Limit = since, limit
		}
	}
	targets, err := client.TargetsFromRelayList([]string{relayURL.String()})
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	state, err := client.LoadFollowState(statePath)
	if err != nil {
		return common.InvalidInputError(cmd, statePath, err)
	}

	if follow {
		err := client.Follow(ctx, targets, filters, client.FollowOptions{
			AuthSigner: signer,
			State:      state,
			Emit: func(ev *nip01.Event, _ string) error {
				common.PrintJSONLine(social.FromEvent(ev, "group"))
				return nil
			},
			Warn: func(relay string, err error) {
				log.Warn().Str("relay", relay).Err(err).Msg("reconnecting")
			},
		})
		if err != nil {
			return queryErr(cmd, relayURL, err)
		}
		return nil
	}

	// A one-shot run with --state starts where the last one left off.
	if resume := state.Resume(10 * time.Minute); resume > 0 {
		for _, flt := range filters.GetAll() {
			if len(flt.IDs) == 0 && uint64(flt.Since) < resume {
				flt.Since = resume
			}
		}
	}
	timeout, _ := f.GetDuration("timeout")
	var events []*nip01.Event
	err = common.WithSpinner(cmd, fmt.Sprintf("Reading %s", relayURL.Host), func() error {
		var qErr error
		events, qErr = client.QueryTargetsWithAuth(ctx, targets, filters, timeout, signer)
		return qErr
	})
	if err != nil {
		return queryErr(cmd, relayURL, err)
	}
	messages := []social.Message{}
	for _, ev := range events {
		if ev.Verify(nip01.WithoutPowCheck()) != nil || !state.Mark(ev.ID, relayURL.String(), int64(ev.CreatedAt)) {
			continue
		}
		messages = append(messages, social.FromEvent(ev, "group"))
	}
	if err := state.Save(); err != nil {
		return common.RuntimeError(cmd, fmt.Errorf("saving --state: %w", err))
	}
	social.SortMessages(messages)
	return printMessages(cmd, messages)
}

func printMessages(cmd *cobra.Command, messages []social.Message) error {
	if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
		common.PrintJSON(messages)
		return nil
	}
	for _, m := range messages {
		social.PrintMessage(cmd.OutOrStdout(), m)
	}
	return nil
}

// queryErr classifies a failed group read.
func queryErr(cmd *cobra.Command, relayURL *url.URL, err error) error {
	if le := keyresolve.LoginError(cmd, err); le != nil {
		return le
	}
	switch {
	case errors.Is(err, client.ErrNoReachableTargets):
		return common.NetworkError(cmd, relayURL.String(), err)
	case errors.Is(err, client.ErrRestricted):
		return common.AuthError(cmd, err)
	}
	return common.RuntimeError(cmd, err)
}
