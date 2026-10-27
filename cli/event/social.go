package event

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip09"
	"github.com/ohstr/nmilat/nip10"
	"github.com/ohstr/nmilat/nip18"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip22"
	"github.com/ohstr/nmilat/nip25"
	"github.com/spf13/cobra"
)

// published is what react, reply, post, repost and delete print with --json.
type published struct {
	ID     string                `json:"id"`
	Kind   int                   `json:"kind"`
	Event  *nip01.Event          `json:"event"`
	Report *client.PublishReport `json:"report"`
}

const toFlagHelp = "Publish only to these comma-separated relays (default: where the target was found, plus --relays or the prefs relays)"

func addPublishFlags(cmd *cobra.Command) {
	cmd.Flags().String("to", "", toFlagHelp)
	social.AddSignerFlag(cmd)
}

func newReactCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "react <ref> [emoji]",
		Short: "React to any event (NIP-25)",
		Long: `Publishes a kind 7 reaction to the event ref names: "+" by default, or
the given emoji or "-". It carries "e", "p" (the author), "k" (the
kind), "a" for an addressable event, and the group's "h" when the event
is in a NIP-29 group.`,
		Example: `  ncli event react nevent1... "👀" --signer bunker+unix:///run/signer/agent.sock
  ncli event react note1... --signer me --to wss://relay.example`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 || len(args) > 2 {
				return common.InvocationOrHelp(cmd, args, fmt.Errorf("want an event and an optional emoji, got %d argument(s)", len(args)))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			content := "+"
			if len(args) == 2 {
				content = args[1]
			}
			return actOn(cmd, args[0], func(target *nip01.Event, relay, _ string) (*nip01.Event, error) {
				return nip25.NewReaction(target, content, relay), nil
			})
		},
	}
	addPublishFlags(cmd)
	return cmd
}

func newReplyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reply <ref> <text|->",
		Short: "Reply to any event",
		Long: `Publishes a reply to the event ref names, threaded the way its kind
expects: a note (kind 1) gets a kind 1 with NIP-10 root/reply tags; a
group chat message (kind 9) gets a kind 9 quoting it ("q") in the same
group ("h"); anything else gets a NIP-22 comment (kind 1111) that keeps
the thread's root.`,
		Example: `  ncli event reply note1... "agreed" --signer me
  echo "long answer" | ncli event reply nevent1... - --signer bunker+unix:///run/signer/agent.sock`,
		Args: common.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := social.Text(cmd, args, 1)
			if err != nil {
				return err
			}
			mentions, err := mentionTags(cmd)
			if err != nil {
				return err
			}
			return actOn(cmd, args[0], func(target *nip01.Event, relay, me string) (*nip01.Event, error) {
				ev, err := replyTo(target, relay, me, text)
				if err != nil {
					return nil, err
				}
				ev.Tags = appendNewTags(ev.Tags, mentions)
				return ev, nil
			})
		},
	}
	cmd.Flags().StringArrayP("mention", "m", nil, "Also tag this person (repeatable)")
	addPublishFlags(cmd)
	return cmd
}

func newPostCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "post <text|->",
		Short: "Publish a note (kind 1)",
		Long: `Publishes a kind 1 text note. -m tags people, --hashtag adds topics,
--quote cites an event (a "q" tag, and its nevent at the end of the
text).`,
		Example: `  ncli event post "hello nostr" --signer me
  ncli event post "see this" --quote note1... -m npub1... --hashtag nostr --signer me`,
		Args: common.ExactArgs(1),
		RunE: runPost,
	}
	cmd.Flags().StringArrayP("mention", "m", nil, "Tag this person (repeatable)")
	cmd.Flags().StringArray("hashtag", nil, "Topic, without # (repeatable)")
	cmd.Flags().String("quote", "", "Event to quote")
	addPublishFlags(cmd)
	return cmd
}

func newRepostCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repost <ref>",
		Short: "Repost any event (NIP-18)",
		Long: `Publishes a repost of the event ref names: kind 6 for a note, kind 16
for anything else. The original is embedded, unless it is protected
(NIP-70).`,
		Example: `  ncli event repost note1... --signer me`,
		Args:    common.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return actOn(cmd, args[0], func(target *nip01.Event, relay, _ string) (*nip01.Event, error) {
				return nip18.NewRepost(target, relay)
			})
		},
	}
	addPublishFlags(cmd)
	return cmd
}

func newDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <ref...>",
		Short: "Ask relays to delete your own events (NIP-09)",
		Long: `Publishes a kind 5 deletion request for the events refs name, which
must all be --signer's. Relays that honour it stop serving them; copies
elsewhere remain.`,
		Example: `  ncli event delete note1... --reason "typo" --signer me`,
		Args:    common.MinimumNArgs(1),
		RunE:    runDelete,
	}
	cmd.Flags().String("reason", "", "Why, shown to readers")
	addPublishFlags(cmd)
	return cmd
}

// actOn looks up ref, builds an event about it with build, signs and
// publishes it.
func actOn(cmd *cobra.Command, refArg string, build func(target *nip01.Event, relay, me string) (*nip01.Event, error)) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	targets, foundOn, err := lookupTargets(ctx, cmd, []string{refArg})
	if err != nil {
		return err
	}
	signer, closeSigner, err := social.Signer(cmd)
	if err != nil {
		return err
	}
	defer closeSigner()
	hint := ""
	if len(foundOn) > 0 {
		hint = foundOn[0]
	}
	ev, err := build(targets[0], hint, strings.ToLower(signer.PubKey()))
	if err != nil {
		return common.InvalidInputError(cmd, refArg, err)
	}
	return signAndPublish(ctx, cmd, signer, ev, foundOn)
}

// lookupTargets resolves refs to events, and lists the relays they were
// found on.
func lookupTargets(ctx context.Context, cmd *cobra.Command, args []string) ([]*nip01.Event, []string, error) {
	refs, _, err := collectRefs(cmd, args)
	if err != nil {
		return nil, nil, err
	}
	res, closeAuth, err := newResolver(cmd)
	if err != nil {
		return nil, nil, err
	}
	defer closeAuth()
	var lookups []*lookup
	_ = common.WithSpinner(cmd, fmt.Sprintf("looking up %d event(s)", len(refs)), func() error {
		lookups = res.resolve(ctx, refs, false)
		return nil
	})
	hits, missing := found(lookups)
	if err := missingError(cmd, res, missing); err != nil {
		return nil, nil, err
	}
	var events []*nip01.Event
	var relays []string
	for _, l := range hits {
		best := l.best()
		if err := best.Event.Verify(nip01.WithoutPowCheck()); err != nil {
			return nil, nil, common.InvalidInputError(cmd, l.Ref.Input, fmt.Errorf("the event does not verify: %w", err))
		}
		events = append(events, best.Event)
		relays = append(relays, l.foundOn()...)
	}
	return events, relays, nil
}

// signAndPublish signs ev and publishes it to --to, or to foundOn plus
// --relays (or the prefs relays).
func signAndPublish(ctx context.Context, cmd *cobra.Command, signer client.Crypto, ev *nip01.Event, foundOn []string) error {
	targets, err := publishTargets(cmd, foundOn)
	if err != nil {
		return err
	}
	if ev.CreatedAt == 0 {
		ev.CreatedAt = uint64(time.Now().Unix())
	}
	if err := signer.Sign(ctx, ev); err != nil {
		return social.SignerError(cmd, err)
	}
	report, err := social.Publish(ctx, cmd, targets, []*nip01.Event{ev}, signer)
	if err != nil {
		return err
	}
	if jsonMode(cmd) {
		if err := printJSON(cmd, published{ID: ev.ID, Kind: ev.Kind, Event: ev, Report: report}); err != nil {
			return err
		}
	} else {
		social.PrintReportLines(cmd, report)
	}
	return social.ReportError(cmd, report)
}

func publishTargets(cmd *cobra.Command, foundOn []string) (*client.TargetsSpec, error) {
	if to, _ := cmd.Flags().GetString("to"); to != "" {
		t, err := client.TargetsFromRelayList(social.SplitList(to))
		if err != nil {
			return nil, common.InvalidInputError(cmd, to, err)
		}
		return t, nil
	}
	var list []string
	seen := map[string]bool{}
	add := func(r string) {
		if r != "" && !seen[r] && !strings.HasSuffix(r, ".db") {
			seen[r] = true
			list = append(list, r)
		}
	}
	for _, r := range foundOn {
		add(r)
	}
	relaysVal, _ := cmd.Flags().GetString("relays")
	for _, r := range social.SplitList(relaysVal) {
		add(r)
	}
	if len(list) == 0 {
		t, err := client.TargetsFromPrefs()
		if err != nil {
			return nil, common.NotFoundError(cmd, "", fmt.Errorf("no relay to publish to: pass --to or --relays (%w)", err))
		}
		return t, nil
	}
	t, err := client.TargetsFromRelayList(list)
	if err != nil {
		return nil, common.InvalidInputError(cmd, strings.Join(list, ","), err)
	}
	return t, nil
}

// replyTo builds a reply to parent, threaded for its kind.
func replyTo(parent *nip01.Event, relay, me, text string) (*nip01.Event, error) {
	group := nip25.TagValue(parent, "h")
	switch parent.Kind {
	case 1:
		ev := nip01.NewEvent(1, text)
		ev.Tags = nip10.ReplyTags(parent, relay)
		return ev, nil
	case 9:
		ev := nip01.NewEvent(9, text)
		ev.Tags = [][]string{nip18.QuoteTag(parent.ID, relay, parent.PubKey), {"p", strings.ToLower(parent.PubKey)}}
		if group != "" {
			ev.Tags = append(ev.Tags, []string{"h", group})
		}
		return ev, nil
	}
	parentScope := nip22.ScopeParams{
		Pointer:      nip22.PointerParams{Type: nip22.PointerEvent, Value: parent.ID, RelayHint: relay, AuthorPubkey: parent.PubKey},
		Kind:         strconv.Itoa(parent.Kind),
		AuthorPubkey: parent.PubKey,
		AuthorRelay:  relay,
	}
	if a := nip25.Address(parent); a != "" {
		parentScope.Pointer = nip22.PointerParams{Type: nip22.PointerAddress, Value: a, RelayHint: relay}
	}
	root := parentScope
	if parent.Kind == nip22.KindComment {
		if c, err := nip22.ParseComment(parent); err == nil {
			root = nip22.ScopeParams{
				Pointer:      nip22.PointerParams{Type: c.Root.Pointer.Type, Value: c.Root.Pointer.Value, RelayHint: c.Root.Pointer.RelayHint, AuthorPubkey: c.Root.Pointer.AuthorPubkey},
				Kind:         c.Root.Kind,
				AuthorPubkey: c.Root.AuthorPubkey,
				AuthorRelay:  c.Root.AuthorRelay,
			}
		}
	}
	ev, err := nip22.NewComment(nip22.CommentParams{Pubkey: me, Content: text, Root: root, Parent: parentScope})
	if err != nil {
		return nil, err
	}
	if group != "" {
		ev.Tags = append(ev.Tags, []string{"h", group})
	}
	return ev, nil
}

func mentionTags(cmd *cobra.Command) ([][]string, error) {
	values, _ := cmd.Flags().GetStringArray("mention")
	people, err := social.People(cmd, values)
	if err != nil {
		return nil, err
	}
	tags := make([][]string, 0, len(people))
	for _, p := range people {
		tags = append(tags, []string{"p", p})
	}
	return tags, nil
}

// appendNewTags adds each tag whose name and value aren't already there.
func appendNewTags(tags, extra [][]string) [][]string {
	for _, t := range extra {
		dup := false
		for _, have := range tags {
			if len(have) >= 2 && have[0] == t[0] && strings.EqualFold(have[1], t[1]) {
				dup = true
				break
			}
		}
		if !dup {
			tags = append(tags, t)
		}
	}
	return tags
}

func runPost(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	text, err := social.Text(cmd, args, 0)
	if err != nil {
		return err
	}
	tags, err := mentionTags(cmd)
	if err != nil {
		return err
	}
	hashtags, _ := cmd.Flags().GetStringArray("hashtag")
	for _, h := range hashtags {
		if h = strings.TrimPrefix(strings.TrimSpace(h), "#"); h != "" {
			tags = append(tags, []string{"t", strings.ToLower(h)})
		}
	}
	var foundOn []string
	if q, _ := cmd.Flags().GetString("quote"); q != "" {
		quoted, relays, err := lookupTargets(ctx, cmd, []string{q})
		if err != nil {
			return err
		}
		hint := ""
		if len(relays) > 0 {
			hint = relays[0]
		}
		target := quoted[0]
		tags = appendNewTags(tags, [][]string{nip18.QuoteTag(target.ID, hint, target.PubKey), {"p", strings.ToLower(target.PubKey)}})
		ptr := nip19.EventPointer{ID: target.ID, Author: target.PubKey, Kind: target.Kind}
		if hint != "" {
			ptr.Relays = []string{hint}
		}
		if nevent, err := nip19.EncodeEvent(ptr); err == nil {
			text += "\n\nnostr:" + nevent
		}
	}
	signer, closeSigner, err := social.Signer(cmd)
	if err != nil {
		return err
	}
	defer closeSigner()
	ev := nip01.NewEvent(1, text)
	ev.Tags = tags
	if ev.Tags == nil {
		ev.Tags = [][]string{}
	}
	return signAndPublish(ctx, cmd, signer, ev, foundOn)
}

func runDelete(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	targets, foundOn, err := lookupTargets(ctx, cmd, args)
	if err != nil {
		return err
	}
	signer, closeSigner, err := social.Signer(cmd)
	if err != nil {
		return err
	}
	defer closeSigner()
	reason, _ := cmd.Flags().GetString("reason")
	ev, err := nip09.NewDeletionRequest(strings.ToLower(signer.PubKey()), targets, reason)
	if err != nil {
		return common.InvalidInputError(cmd, "", errors.New("you can only delete your own events: "+err.Error()))
	}
	return signAndPublish(ctx, cmd, signer, ev, foundOn)
}
