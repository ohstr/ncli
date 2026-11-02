package social

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	// Labels are the event's t tags' values, as its author set them
	// (TagLabels): its author's word, like its content.
	Labels []string `json:"labels,omitempty"`
	// AttestedBy and AttestValid are the event's NIP-OA auth tag, checked
	// (CheckAttestation): its owner (hex) and whether it holds. Both are
	// absent with no auth tag. AttestTrusted: valid, by an owner the reader
	// trusts (AttestView). The reader sets it; no event can.
	AttestedBy    string `json:"attested_by,omitempty"`
	AttestValid   *bool  `json:"attest_valid,omitempty"`
	AttestTrusted bool   `json:"attest_trusted,omitempty"`
	attestWord    string
	Content       string `json:"content"`
	// Type is "reaction" (a NIP-25 kind 7) or "delete" (a NIP-09 kind 5);
	// empty for a message.
	Type    string   `json:"type,omitempty"`
	Emoji   string   `json:"emoji,omitempty"`   // a reaction's content
	Target  string   `json:"target,omitempty"`  // the event a reaction is to
	Targets []string `json:"targets,omitempty"` // the events a deletion names
	// Reactions are the reactions to this message still standing, when a
	// reader folds them in (dm read --fold).
	Reactions []Reaction `json:"reactions,omitempty"`
}

// Reaction is one reaction folded into the message it is to.
type Reaction struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	Emoji     string `json:"emoji"`
	CreatedAt uint64 `json:"created_at"`
}

// Kinds FromEvent gives a meaning beyond a message.
const (
	KindDeletion = 5 // NIP-09
	KindReaction = 7 // NIP-25
)

// FromEvent reads ev's threading (NIP-10 markers, else NIP-17's unmarked
// "e"), subject and "p" tags into a Message.
func FromEvent(ev *nip01.Event, channel string) Message {
	m := Message{
		ID: ev.ID, Channel: channel, From: strings.ToLower(ev.PubKey),
		CreatedAt: ev.CreatedAt, Kind: ev.Kind, Content: ev.Content, Mentions: []string{},
		Labels: TagLabels(ev.Tags),
	}
	setAttestation(&m, ev)
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
			marker := ""
			if len(t) >= 4 {
				marker = t[3]
			}
			switch marker {
			case "reply":
				m.ReplyTo = t[1]
			case "root":
				m.Root = t[1]
			case "mention":
			default:
				// No marker (NIP-17), or NIP-22's parent "e", whose 4th
				// field is the parent's author, not a marker.
				if m.ReplyTo == "" {
					m.ReplyTo = t[1]
				}
			}
		case "E":
			// NIP-22's root.
			m.Root = t[1]
		}
	}
	if hasE && m.ReplyTo == "" && m.Root != "" {
		// A direct reply to the root names only the root.
		m.ReplyTo = m.Root
	}
	if m.Root == "" && m.ReplyTo != "" && ev.Kind == 1 {
		if root, _, _ := nip10.Root(ev); root != ev.ID {
			m.Root = root
		}
	}
	switch ev.Kind {
	case KindReaction:
		// NIP-25: the last "e" is the event reacted to; it is no reply.
		m.Type, m.Emoji, m.ReplyTo, m.Root = "reaction", ev.Content, "", ""
		for _, t := range ev.Tags {
			if len(t) >= 2 && t[0] == "e" {
				m.Target = strings.ToLower(t[1])
			}
		}
	case KindDeletion:
		// NIP-09: every "e" is an event its author asks to drop.
		m.Type, m.ReplyTo, m.Root, m.Targets = "delete", "", "", []string{}
		for _, t := range ev.Tags {
			if len(t) >= 2 && t[0] == "e" {
				m.Targets = append(m.Targets, strings.ToLower(t[1]))
			}
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
// Every field another member controls is escaped (Inline): the header and
// a reaction print on one line each, and each content line stays indented,
// so no text can start a line that looks like another message. Its labels
// print in the header, every rune but printable ASCII escaped (Labels),
// after the author and before the subject, the first of the author's own
// text; a message with no labels prints "labels: -" there, so a subject never stands where they print,
// and the subject's look-alike letters print escaped (Subject).
func PrintMessage(w io.Writer, m Message) {
	at := time.Unix(int64(m.CreatedAt), 0).UTC().Format("2006-01-02 15:04")
	head := fmt.Sprintf("%s  %s", at, Inline(Npub(m.From)))
	// the attestation, the client's column, right after the author
	if a := AttestColumn(m.AttestedBy, m.AttestValid, m.AttestTrusted, m.attestWord); a != "" {
		head += "  " + a
	}
	if m.Group != "" {
		head += "  in " + Inline(m.Group)
	}
	// the labels before the subject, the author's own text: a subject
	// can't stand where they print
	if len(m.Labels) > 0 {
		head += "  labels: " + Labels(m.Labels)
	} else {
		head += "  labels: -"
	}
	if m.Subject != "" {
		head += "  [" + Title(m.Subject) + "]"
	}
	if m.ReplyTo != "" {
		head += "  ↩ " + short(m.ReplyTo)
	}
	_, _ = fmt.Fprintf(w, "%s  %s\n", head, short(m.ID))
	switch m.Type {
	case "reaction":
		_, _ = fmt.Fprintf(w, "  %s on %s\n", ShowEmoji(m.Emoji), short(m.Target))
		return
	case "delete":
		ids := make([]string, len(m.Targets))
		for i, id := range m.Targets {
			ids[i] = short(id)
		}
		_, _ = fmt.Fprintf(w, "  removed %s\n", strings.Join(ids, ", "))
		return
	}
	WriteBlock(w, "  ", m.Content)
	if len(m.Reactions) > 0 {
		// "reactions: 👀 2, 💬 1", in the order each emoji first came;
		// reactions that show alike (every "…") count together.
		var emojis []string
		n := map[string]int{}
		for _, r := range m.Reactions {
			e := ShowEmoji(r.Emoji)
			if n[e] == 0 {
				emojis = append(emojis, e)
			}
			n[e]++
		}
		for i, e := range emojis {
			emojis[i] = fmt.Sprintf("%s %d", e, n[e])
		}
		_, _ = fmt.Fprintf(w, "  reactions: %s\n", strings.Join(emojis, ", "))
	}
}

// Fold applies deletions and folds reactions into the messages they are
// to. A deletion counts only for events by its own author (NIP-09): it
// removes them, and is itself dropped. A reaction to a message in ms joins
// that message's Reactions, oldest first, and goes with it if it was
// deleted; one to an event not in ms stays in place as a reaction. Order
// is kept.
func Fold(ms []Message) []Message {
	byID := map[string]Message{}
	for _, m := range ms {
		byID[m.ID] = m
	}
	deleted := map[string]bool{}
	for _, m := range ms {
		if m.Type != "delete" {
			continue
		}
		for _, id := range m.Targets {
			if t, ok := byID[id]; ok && t.From == m.From && t.ID != m.ID {
				deleted[id] = true
			}
		}
	}
	// The reactions each message left standing gets.
	reactions := map[string][]Reaction{}
	for _, m := range ms {
		if m.Type == "reaction" && !deleted[m.ID] {
			if t, ok := byID[m.Target]; ok && t.Type == "" && !deleted[t.ID] {
				reactions[t.ID] = append(reactions[t.ID], Reaction{ID: m.ID, From: m.From, Emoji: m.Emoji, CreatedAt: m.CreatedAt})
			}
		}
	}
	out := []Message{}
	for _, m := range ms {
		switch {
		case deleted[m.ID], m.Type == "delete":
			continue
		case m.Type == "reaction":
			if t, ok := byID[m.Target]; ok && t.Type == "" {
				continue // folded into its message, or gone with it
			}
		case m.Type == "":
			m.Reactions = reactions[m.ID]
			sort.SliceStable(m.Reactions, func(i, j int) bool { return m.Reactions[i].CreatedAt < m.Reactions[j].CreatedAt })
		}
		out = append(out, m)
	}
	return out
}

// short is an id's first 12 bytes, escaped: a rumor's ids and tags are its
// author's text, never checked as hex.
func short(id string) string {
	if len(id) > 12 {
		id = id[:12]
	}
	return Inline(id)
}

// MaxEmojiRunes is the longest reaction ShowEmoji shows: enough for an
// emoji's longest ZWJ sequence (a couple with two skin tones is 10 runes),
// a flag's tag sequence or a short ":shortcode:".
const MaxEmojiRunes = 10

// ShowEmoji is a reaction's content as text shows it: as it is when it is
// a short run of printable runes, else "…" (a reaction is no message).
func ShowEmoji(e string) string {
	if utf8.RuneCountInString(e) > MaxEmojiRunes || !utf8.ValidString(e) {
		return "…"
	}
	rs := []rune(e)
	for i, r := range rs {
		switch {
		case !unsafeRune(r):
		case r == '\u200d' && i > 0 && i < len(rs)-1 && !unsafeRune(rs[i-1]) && !unsafeRune(rs[i+1]):
			// a ZWJ sequence's joiner, between two visible runes
		case isTag(r) && flagTags(rs):
		default:
			return "…"
		}
	}
	return e
}

func isTag(r rune) bool { return r >= 0xE0020 && r <= 0xE007F }

// flagTags reports whether rs is a flag's tag sequence: the black flag,
// tags, and the cancel tag last. Tags anywhere else are hidden ASCII.
func flagTags(rs []rune) bool {
	if len(rs) < 3 || rs[0] != 0x1F3F4 || rs[len(rs)-1] != 0xE007F {
		return false
	}
	for _, r := range rs[1:] {
		if !isTag(r) {
			return false
		}
	}
	return true
}

// Inline is s on one line: every control rune (C0, DEL, C1, tab included),
// line or paragraph separator, format character (Unicode Cf: bidi marks,
// overrides and isolates, zero-width characters) and invalid UTF-8 is
// written as a Go escape (\n, \r, \x1b, \u2028, \u200b), so text another
// member wrote can neither break a line, move the cursor, reorder what
// follows it nor hide in plain sight.
func Inline(s string) string { return escape(s, false) }

// WriteBlock writes multi-line text another member wrote, each line after
// indent (CRLF is one break). Inside a line a CR, ESC, separator or format
// character is escaped as Inline does, tabs kept, so the text can't start a
// line at column 0 or move the cursor; and the letters that could stand for
// an ASCII one beside ASCII letters escaped as in a subject (Guard: a
// line "labels: #rel\u0435ase" can't pass for the client's own). A
// line's padding is collapsed (collapseLine: its indentation kept up to 8
// runes, every later run of spaces one space).
func WriteBlock(w io.Writer, indent, s string) {
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		_, _ = fmt.Fprintf(w, "%s%s\n", indent, guard(escape(collapseLine(line), true)))
	}
}

// escape writes s's unsafe runes as Go escapes; keepTab leaves tabs as
// they are (inside a message's indented content).
func escape(s string, keepTab bool) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			_, _ = fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\t' && keepTab:
			b.WriteRune(r)
		case unsafeRune(r):
			q := strconv.QuoteRuneToASCII(r)
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

func unsafeRune(r rune) bool {
	switch {
	case unicode.IsControl(r): // C0, DEL and C1 (NEL among them)
		return true
	case r == '\u2028', r == '\u2029': // line and paragraph separators
		return true
	case unicode.Is(unicode.Cf, r):
		// Format characters: bidi marks, embeddings, overrides and isolates
		// (U+200E/F, U+061C, U+202A-E, U+2066-9), zero-width ones (U+200B-D,
		// U+2060, U+FEFF), the soft hyphen, tags.
		return true
	}
	return false
}
