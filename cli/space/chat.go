package space

import (
	"errors"
	"fmt"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/huddle"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip53"
	"github.com/spf13/cobra"
)

// chatMessage is one kind:1311 message as `space chat list` prints it.
type chatMessage struct {
	ID        string   `json:"id"`
	Pubkey    string   `json:"pubkey"`
	Content   string   `json:"content"`
	CreatedAt uint64   `json:"created_at"`
	Parent    string   `json:"parent,omitempty"`
	Quotes    []string `json:"quotes,omitempty"`
}

// newChatCommand is a space's conversation (kind:1311) without joining
// its call -- what `space join`'s chat panel does, one action at a time.
func newChatCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Send or read a space's conversation (kind:1311)",
		Long: `Chat belongs to an activity: a space's naddr or its 30312:<pubkey>:<d>
coordinate (or a 30313 session's). A bare room id has no conversation.`,
		Example: `  ncli space chat send 30312:<pubkey>:standup "hello"
  ncli space chat list naddr1...`,
		RunE: common.RequireSubcommand,
	}

	send := &cobra.Command{
		Use:   "send <space> <text>",
		Short: "Post a message to a space's conversation",
		Example: `  ncli space chat send 30312:<pubkey>:standup "hello"
  ncli space chat send naddr1... "agreed" --reply <event-id>`,
		Args: common.ExactArgs(2),
		RunE: runChatSend,
	}
	send.Flags().String("reply", "", "Event id of the message this replies to")
	send.Flags().StringArray("quote", nil, "Event id to quote (repeatable)")
	cmd.AddCommand(send)

	list := &cobra.Command{
		Use:     "list <space>",
		Short:   "List a space's conversation, oldest first",
		Example: `  ncli space chat list 30312:<pubkey>:standup --limit 50`,
		Args:    common.ExactArgs(1),
		RunE:    runChatList,
	}
	list.Flags().Int("limit", 200, "Most recent messages to fetch")
	list.Flags().Duration("timeout", 10*time.Second, "How long to wait for the relay")
	cmd.AddCommand(list)

	return cmd
}

func runChatSend(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	content := args[1]
	if content == "" {
		return common.InvalidInputError(cmd, "", errors.New("message text is empty"))
	}
	reply, _ := cmd.Flags().GetString("reply")
	quotes, _ := cmd.Flags().GetStringArray("quote")
	for _, id := range append([]string{reply}, quotes...) {
		if id != "" && !isEventID(id) {
			return common.InvalidInputError(cmd, id, errors.New("not a 64-char hex event id"))
		}
	}

	identityFlag, _ := cmd.Flags().GetString("identity")
	pubKeyHex, privKeyHex, err := resolveIdentity(cmd, identityFlag)
	if err != nil {
		return err
	}

	relayFlag, _ := cmd.Flags().GetString("relay")
	activity, chatRelay, err := huddle.ResolveChatActivity(ctx, cmd, args[0], relayFlag)
	if err != nil {
		return err
	}

	ev := nip53.NewLiveChatMessage(nip53.LiveChatMessageParams{
		Pubkey:        pubKeyHex,
		Activity:      activity,
		ActivityRelay: chatRelay.String(),
		Parent:        reply,
		Quotes:        quotes,
		Content:       content,
	})
	return signAndPublish(ctx, cmd, chatRelay, ev, privKeyHex)
}

func runChatList(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	limit, _ := cmd.Flags().GetInt("limit")
	if limit <= 0 {
		return common.InvalidInputError(cmd, fmt.Sprint(limit), errors.New("--limit must be positive"))
	}
	timeout, _ := cmd.Flags().GetDuration("timeout")

	// Anonymous unless --identity is given: chat on an open space is public.
	var privKeyHex string
	if identityFlag, _ := cmd.Flags().GetString("identity"); identityFlag != "" {
		var err error
		if _, privKeyHex, err = resolveIdentity(cmd, identityFlag); err != nil {
			return err
		}
	}

	relayFlag, _ := cmd.Flags().GetString("relay")
	activity, chatRelay, err := huddle.ResolveChatActivity(ctx, cmd, args[0], relayFlag)
	if err != nil {
		return err
	}

	targets, err := client.TargetsFromRelayList([]string{chatRelay.String()})
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	filters := nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds: []int{nip53.KindLiveChatMessage},
		Tags:  map[string][]string{"a": {activity}},
		Limit: limit,
	})
	var events []*nip01.Event
	err = common.WithSpinner(cmd, fmt.Sprintf("Reading chat on %s", chatRelay.Host), func() error {
		var qErr error
		events, qErr = client.QueryTargetsWithAuth(ctx, targets, filters, timeout, privKeyHex)
		return qErr
	})
	if err != nil {
		if errors.Is(err, client.ErrNoReachableTargets) {
			return common.NetworkError(cmd, chatRelay.String(), err)
		}
		if errors.Is(err, client.ErrRestricted) {
			return common.AuthError(cmd, err)
		}
		return common.RuntimeError(cmd, err)
	}

	messages := []chatMessage{}
	seen := map[string]bool{}
	for _, ev := range events {
		if ev == nil || seen[ev.ID] {
			continue
		}
		parsed, perr := nip53.ParseLiveChatMessage(ev)
		if perr != nil || parsed.Activity != activity {
			continue
		}
		seen[ev.ID] = true
		messages = append(messages, chatMessage{
			ID: ev.ID, Pubkey: ev.PubKey, Content: ev.Content, CreatedAt: ev.CreatedAt,
			Parent: parsed.Parent, Quotes: parsed.Quotes,
		})
	}
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].CreatedAt < messages[j].CreatedAt })

	if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
		common.PrintJSON(map[string]any{"activity": activity, "messages": messages})
		return nil
	}
	if len(messages) == 0 {
		fmt.Println("(no messages)")
		return nil
	}
	for _, m := range messages {
		ts := time.Unix(int64(m.CreatedAt), 0).UTC().Format("2006-01-02 15:04:05")
		fmt.Printf("%s  %s  %s  %s\n", ts, m.ID[:8], m.Pubkey[:8], m.Content)
	}
	return nil
}

func isEventID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
