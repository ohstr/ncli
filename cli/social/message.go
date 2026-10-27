package social

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip10"
)

// Message is one DM or group message as dm read, groups read and groups
// feed print it, so a caller handles both with one parser. Pubkeys are hex.
type Message struct {
	ID        string   `json:"id"`
	Channel   string   `json:"channel"` // "dm" or "group"
	Group     string   `json:"group,omitempty"`
	Wrap      string   `json:"wrap,omitempty"` // the gift wrap a DM came in
	From      string   `json:"from"`
	CreatedAt uint64   `json:"created_at"`
	Kind      int      `json:"kind"`
	Subject   string   `json:"subject,omitempty"`
	ReplyTo   string   `json:"reply_to,omitempty"`
	Root      string   `json:"root,omitempty"`
	Mentions  []string `json:"mentions"`
	Content   string   `json:"content"`
}

// FromEvent reads ev's threading (NIP-10 markers, else NIP-17's unmarked
// "e"), subject and "p" tags into a Message.
func FromEvent(ev *nip01.Event, channel string) Message {
	m := Message{
		ID: ev.ID, Channel: channel, From: strings.ToLower(ev.PubKey),
		CreatedAt: ev.CreatedAt, Kind: ev.Kind, Content: ev.Content, Mentions: []string{},
	}
	seen := map[string]bool{}
	hasE := false
	for _, t := range ev.Tags {
		if len(t) < 2 {
			continue
		}
		switch t[0] {
		case "p":
			pk := strings.ToLower(t[1])
			if !seen[pk] {
				seen[pk] = true
				m.Mentions = append(m.Mentions, pk)
			}
		case "subject":
			m.Subject = t[1]
		case "h":
			m.Group = t[1]
		case "q":
			// A group chat reply quotes its parent (NIP-C7).
			if channel == "group" && m.ReplyTo == "" {
				m.ReplyTo = t[1]
			}
		case "e":
			hasE = true
			if len(t) >= 4 {
				switch t[3] {
				case "reply":
					m.ReplyTo = t[1]
				case "root":
					m.Root = t[1]
				}
			} else if m.ReplyTo == "" {
				m.ReplyTo = t[1]
			}
		}
	}
	if hasE && m.ReplyTo == "" && m.Root != "" {
		// A direct reply to the root names only the root.
		m.ReplyTo = m.Root
	}
	if m.Root == "" && m.ReplyTo != "" {
		if root, _, _ := nip10.Root(ev); root != ev.ID {
			m.Root = root
		}
	}
	return m
}

// SortMessages orders messages oldest first, ties by id.
func SortMessages(ms []Message) {
	sort.SliceStable(ms, func(i, j int) bool {
		if ms[i].CreatedAt != ms[j].CreatedAt {
			return ms[i].CreatedAt < ms[j].CreatedAt
		}
		return ms[i].ID < ms[j].ID
	})
}

// PrintMessage writes m as text: a header line, then the content indented.
func PrintMessage(w io.Writer, m Message) {
	at := time.Unix(int64(m.CreatedAt), 0).UTC().Format("2006-01-02 15:04")
	head := fmt.Sprintf("%s  %s", at, Npub(m.From))
	if m.Group != "" {
		head += "  in " + m.Group
	}
	if m.Subject != "" {
		head += "  [" + m.Subject + "]"
	}
	if m.ReplyTo != "" {
		head += "  ↩ " + short(m.ReplyTo)
	}
	fmt.Fprintf(w, "%s  %s\n", head, short(m.ID))
	for _, line := range strings.Split(m.Content, "\n") {
		fmt.Fprintf(w, "  %s\n", line)
	}
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
