package event

import (
	"context"
	"fmt"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip22"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

type threadView struct {
	Ancestors     []*nip01.Event `json:"ancestors"`
	MissingParent string         `json:"missing_parent,omitempty"`
	Event         *nip01.Event   `json:"event"`
	Replies       []*nip01.Event `json:"replies"`
}

func newThreadCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "thread <ref>",
		Short: "Show an event's parents up to the root, and its direct replies",
		Long: `Walks up from the event to the thread's root through NIP-10 reply
markers (kind 1, and positional e tags on older events) or NIP-22 parent
tags (kind 1111 comments), then lists the event's direct replies.

Parents that can't be found stop the walk; their id is reported as
missing_parent. Replies that don't verify are dropped.`,
		Example: `  ncli event thread nevent1...
  ncli event thread note1... --depth 5 --limit 20 --json`,
		Args: common.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			refs, _, err := collectRefs(cmd, args)
			if err != nil {
				return err
			}
			if len(refs) != 1 {
				return common.InvalidInputError(cmd, "", fmt.Errorf("thread takes exactly one event, got %d", len(refs)))
			}
			res, closeAuth, err := newResolver(cmd)
			if err != nil {
				return err
			}
			defer closeAuth()
			depth, _ := cmd.Flags().GetInt("depth")
			limit, _ := cmd.Flags().GetInt("limit")

			view := threadView{Ancestors: []*nip01.Event{}, Replies: []*nip01.Event{}}
			err = common.WithSpinner(cmd, "walking the thread", func() error {
				start := res.resolve(ctx, refs, false)[0]
				if start.best() == nil {
					return missingError(cmd, res, []*lookup{start})
				}
				view.Event = start.best().Event

				cur := view.Event
				for i := 0; i < depth; i++ {
					parent := parentOf(cur)
					if parent == nil {
						break
					}
					l := res.resolve(ctx, []*ref{parent}, false)[0]
					best := l.best()
					if best == nil || best.Event.Verify(nip01.WithoutPowCheck()) != nil {
						view.MissingParent = parent.Input
						break
					}
					view.Ancestors = append([]*nip01.Event{best.Event}, view.Ancestors...)
					cur = best.Event
				}

				if !res.offline {
					view.Replies = res.replies(ctx, start, view.Event, limit)
				}
				return nil
			})
			if err != nil {
				return err
			}

			if jsonMode(cmd) {
				return printJSON(cmd, view)
			}
			for _, a := range view.Ancestors {
				outf(cmd, "↑ %s\n", oneLine(a))
			}
			if view.MissingParent != "" {
				outf(cmd, "↑ (missing parent %s)\n", view.MissingParent)
			}
			outf(cmd, "● %s\n", oneLine(view.Event))
			for _, r := range view.Replies {
				outf(cmd, "  ↳ %s\n", oneLine(r))
			}
			return nil
		},
	}
	cmd.Flags().Int("depth", 50, "Max parents to walk up")
	cmd.Flags().Int("limit", 100, "Max replies to fetch")
	return cmd
}

// parentOf returns a reference to the event ev replies to, or nil for a
// root.
func parentOf(ev *nip01.Event) *ref {
	if ev.Kind == nip22.KindComment {
		for _, t := range ev.Tags {
			if len(t) < 2 {
				continue
			}
			switch t[0] {
			case "e":
				return pointerRef(t[1], tagHint(t))
			case "a":
				if r, ok := parseCoordinate(t[1]); ok {
					r.Input = t[1]
					r.Hints = tagHint(t)
					return r
				}
			}
		}
		return nil
	}

	var root, reply, last []string
	marked := false
	for _, t := range ev.Tags {
		if len(t) < 2 || t[0] != "e" {
			continue
		}
		marker := ""
		if len(t) >= 4 {
			marker = t[3]
		}
		switch marker {
		case "reply":
			reply, marked = t, true
		case "root":
			root, marked = t, true
		case "mention":
			marked = true
		default:
			last = t
		}
	}
	switch {
	case reply != nil:
		return pointerRef(reply[1], tagHint(reply))
	case root != nil:
		return pointerRef(root[1], tagHint(root))
	case !marked && last != nil:
		return pointerRef(last[1], tagHint(last))
	}
	return nil
}

func pointerRef(id string, hints []string) *ref {
	return &ref{Input: id, ID: strings.ToLower(id), Kind: -1, Hints: hints}
}

func tagHint(t []string) []string {
	if len(t) >= 3 && strings.HasPrefix(t[2], "ws") {
		return []string{t[2]}
	}
	return nil
}

// replies fetches ev's direct replies from the relays ev was looked up on
// and its author's outbox, oldest first.
func (r *resolver) replies(ctx context.Context, start *lookup, ev *nip01.Event, limit int) []*nip01.Event {
	relays := append(r.firstRelays(start.Ref), r.outboxRelays(ctx, ev.PubKey, start.Ref.Hints)...)
	ts := targets(relays, nil)
	if len(ts) == 0 {
		return []*nip01.Event{}
	}
	filters := nip01.NewSubscriptionFilterGroup(
		&nip01.SubscriptionFilter{Kinds: []int{1}, Tags: map[string][]string{"e": {ev.ID}}, Limit: limit},
		&nip01.SubscriptionFilter{Kinds: []int{nip22.KindComment}, Tags: map[string][]string{"e": {ev.ID}}, Limit: limit},
	)
	seen := map[string]bool{}
	out := []*nip01.Event{}
	for _, res := range readAll(ctx, ts, filters, r.timeout, r.auth) {
		if res.Err != nil {
			log.Warn().Msgf("%s: %v", res.Target, res.Err)
		}
		for _, reply := range res.Events {
			if seen[reply.ID] {
				continue
			}
			seen[reply.ID] = true
			if p := parentOf(reply); p == nil || p.Addr || !strings.EqualFold(p.ID, ev.ID) {
				continue
			}
			if reply.Verify(nip01.WithoutPowCheck()) != nil {
				log.Warn().Msgf("dropping reply %s: it does not verify", reply.ID)
				continue
			}
			out = append(out, reply)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func oneLine(ev *nip01.Event) string {
	content := strings.Join(strings.Fields(ev.Content), " ")
	if len(content) > 80 {
		content = content[:77] + "..."
	}
	return fmt.Sprintf("%s  %s  %s", short(ev.ID), short(npub(ev.PubKey)), content)
}
