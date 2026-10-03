package huddle

import (
	"sort"
	"strings"
	"sync"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip53"
)

// wrapText breaks s into display lines no wider than width runes, splitting on
// spaces where it can and hard-breaking a word that is longer than the whole
// width. Embedded newlines are honored, since a pasted multi-line message is
// not one long line.
//
// Width is counted in runes rather than bytes: a message in any non-ASCII
// script would otherwise wrap several columns early.
func wrapText(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}

	var lines []string
	for _, paragraph := range strings.Split(s, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			// A blank line in the original is content: it separates
			// paragraphs, and dropping it would reflow the message.
			lines = append(lines, "")
			continue
		}

		current := make([]rune, 0, width)
		flush := func() {
			lines = append(lines, string(current))
			current = current[:0]
		}
		for _, word := range words {
			runes := []rune(word)
			// A word longer than the width can never fit on a line of its
			// own, so it is cut rather than left to overflow the panel.
			for len(runes) > width {
				if len(current) > 0 {
					flush()
				}
				lines = append(lines, string(runes[:width]))
				runes = runes[width:]
			}
			switch {
			case len(current) == 0:
				current = append(current, runes...)
			case len(current)+1+len(runes) <= width:
				current = append(current, ' ')
				current = append(current, runes...)
			default:
				flush()
				current = append(current, runes...)
			}
		}
		if len(current) > 0 {
			flush()
		}
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

// maxChatDepth caps how far a reply chain is indented. Threads nest without
// limit, but a terminal does not: past a few levels the indentation eats the
// message. Deeper replies render at this depth rather than being hidden, so a
// conversation never silently loses a message to its own nesting.
const maxChatDepth = 4

// chatMessage is one kind:1311 message in a huddle's conversation.
type chatMessage struct {
	ID        string
	Author    string
	Content   string
	CreatedAt uint64
	// Parent is the "e" tag: the message this one replies to, empty for a
	// root. It may name a message that has not arrived (or never will).
	Parent string
	Quotes []string
}

// chatLine is one rendered row: a message plus how deep in its thread it sits.
type chatLine struct {
	chatMessage
	Depth int
	// Orphan marks a reply whose parent is not in the log, so the view can say
	// "replying to something not here" rather than silently showing it as a
	// root and implying it started the thread.
	Orphan bool
}

// chatLog holds a huddle's conversation. It is safe for concurrent use: the
// subscription goroutine adds, and tview's event loop reads to render.
//
// Only messages whose activity matches the joined one are kept -- a relay may
// answer a broad filter with more than was asked for, and a message tagged for
// another meeting is not part of this conversation.
type chatLog struct {
	mu       sync.Mutex
	activity string
	byID     map[string]chatMessage
	order    []string
	// revision counts accepted messages, so the view can tell "nothing has
	// changed" from "repaint needed" without rebuilding the thread on every
	// tick -- and without a repaint stealing the selection out from under
	// someone reading scrollback.
	revision uint64
}

func newChatLog(activity string) *chatLog {
	return &chatLog{activity: activity, byID: map[string]chatMessage{}}
}

// add records a kind:1311 event, reporting whether it was new. A malformed
// event, one for another activity, or a duplicate is dropped.
func (c *chatLog) add(event *nip01.Event) bool {
	if event == nil || event.Kind != nip53.KindLiveChatMessage {
		return false
	}
	parsed, err := nip53.ParseLiveChatMessage(event)
	if err != nil {
		return false
	}
	if parsed.Activity != c.activity {
		return false
	}
	// An event with no id cannot be deduplicated or replied to, and nothing
	// legitimate produces one.
	if event.ID == "" {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, seen := c.byID[event.ID]; seen {
		return false
	}
	c.byID[event.ID] = chatMessage{
		ID:        event.ID,
		Author:    event.PubKey,
		Content:   event.Content,
		CreatedAt: event.CreatedAt,
		Parent:    parsed.Parent,
		Quotes:    parsed.Quotes,
	}
	c.order = append(c.order, event.ID)
	c.revision++
	return true
}

// len is how many messages are held.
func (c *chatLog) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.byID)
}

// rev is the current revision, which changes only when a message is accepted.
func (c *chatLog) rev() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revision
}

// lines renders the conversation in thread order: roots oldest-first, each
// followed by its replies, recursively.
//
// The order is recomputed from scratch on every call rather than maintained
// incrementally, because a reply can arrive before the message it answers --
// relays deliver stored events in no guaranteed order. Rebuilding means a late
// parent re-nests its replies on the next draw instead of leaving them
// stranded at the root.
func (c *chatLog) lines() []chatLine {
	c.mu.Lock()
	defer c.mu.Unlock()

	sorted := make([]chatMessage, 0, len(c.byID))
	for _, id := range c.order {
		sorted = append(sorted, c.byID[id])
	}
	// created_at is author-controlled and ties are common at one-second
	// resolution, so id breaks them -- any stable rule beats a shuffling view.
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].CreatedAt != sorted[j].CreatedAt {
			return sorted[i].CreatedAt < sorted[j].CreatedAt
		}
		return sorted[i].ID < sorted[j].ID
	})

	children := map[string][]chatMessage{}
	var roots []chatMessage
	for _, msg := range sorted {
		// A message naming itself as its parent would recurse forever; treat
		// it as a root, the same as any unknown parent.
		if msg.Parent == "" || msg.Parent == msg.ID {
			roots = append(roots, msg)
			continue
		}
		if _, known := c.byID[msg.Parent]; !known {
			roots = append(roots, msg)
			continue
		}
		children[msg.Parent] = append(children[msg.Parent], msg)
	}

	lines := make([]chatLine, 0, len(sorted))
	// `e` tags come from other people's events, so a cycle (A replies to B, B
	// replies to A) is something a peer can author. Walking with a visited set
	// means a cycle costs a dropped nesting, not a hung terminal.
	visited := map[string]bool{}

	var walk func(msg chatMessage, depth int, orphan bool)
	walk = func(msg chatMessage, depth int, orphan bool) {
		if visited[msg.ID] {
			return
		}
		visited[msg.ID] = true

		capped := depth
		if capped > maxChatDepth {
			capped = maxChatDepth
		}
		lines = append(lines, chatLine{chatMessage: msg, Depth: capped, Orphan: orphan})

		for _, child := range children[msg.ID] {
			walk(child, depth+1, false)
		}
	}

	for _, root := range roots {
		walk(root, 0, root.Parent != "")
	}

	// A message inside a cycle is reachable from no root, so it would never be
	// walked above. Appending the leftovers keeps "every message is displayed"
	// true whatever the tags say.
	for _, msg := range sorted {
		if !visited[msg.ID] {
			visited[msg.ID] = true
			lines = append(lines, chatLine{chatMessage: msg, Depth: 0, Orphan: true})
		}
	}
	return lines
}
