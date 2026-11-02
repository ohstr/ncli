package form

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/nmilat/nippf"
	"github.com/spf13/cobra"
)

// printJSON writes v as indented JSON to the command's output.
func printJSON(cmd *cobra.Command, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), `{"error":"failed to encode JSON output"}`)
		return
	}
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
}

func jsonMode(cmd *cobra.Command) bool {
	j, _ := cmd.Flags().GetBool("json")
	return j
}

func when(unix int64) string {
	return time.Unix(unix, 0).UTC().Format("2006-01-02 15:04Z")
}

// Every field of a form, a response or a result is its author's text, ids
// and pubkeys included: text output prints them through social.Inline (one
// line, control and format runes escaped) or social.WriteBlock (several
// lines, each indented), never raw.

// esc is a field on one line, escaped as a subject is (social.Subject), so
// a question, an option, a note or a why can't spell a client line with a
// look-alike letter.
func esc(s string) string { return social.Subject(s) }

// npub is a pubkey as an npub, escaped (one that isn't hex prints as is).
func npub(hex string) string { return esc(social.Npub(hex)) }

func short(hex string) string {
	n := social.Npub(hex)
	if r := []rune(n); len(r) > 16 {
		n = string(r[:12]) + "…" + string(r[len(r)-4:])
	}
	return esc(n)
}

func shortID(id string) string {
	if r := []rune(id); len(r) > 12 {
		id = string(r[:12])
	}
	return esc(id)
}

func escAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, v := range ss {
		out[i] = esc(v)
	}
	return out
}

// stateLine: the state, with the end and where the form lives.
func stateLine(v *View) string {
	s := string(v.State)
	f := v.Form
	if f.EndsAt > 0 && v.State == nippf.StateOpen {
		s += ", ends " + when(f.EndsAt)
	}
	switch v.Where {
	case "group":
		s += ", in group " + esc(f.Group)
	default:
		s += ", " + esc(v.Where)
	}
	if f.Priority {
		s += ", priority high"
	}
	if f.ReadOnly {
		s += ", read-only (a question type this client doesn't answer)"
	}
	return s
}

// listLine is one form in form list: its id, its labels, its state, author and tally, and its title last — the
// author's text after every field the client writes, so a title can't stand
// where the labels print.
func listLine(v *View) string {
	f := v.Form
	you := ""
	switch {
	case v.Role == "author":
		you = "yours"
	case v.Mine != nil:
		you = "you answered"
	case v.CanRespond:
		you = "to answer"
	case v.Role == "observer":
		you = "observer"
	}
	answered := fmt.Sprintf("%d answered", v.Tally.Responses)
	if n := len(f.Responders()); n > 0 {
		answered = fmt.Sprintf("%d/%d answered", v.Tally.Responses, n)
	}
	parts := []string{stateLine(v), "by " + short(f.Author), answered}
	if you != "" {
		parts = append(parts, you)
	}
	return fmt.Sprintf("%s  %s%s(%s)  %s", shortID(f.ID), attestField(v), labelFields(v.Labels), strings.Join(parts, "; "), social.Title(f.Title))
}

// attestField is the form's attestation (social.AttestColumn) and two
// spaces, or "" for none: the client's, before any of the author's text.
func attestField(v *View) string {
	if a := social.AttestColumn(v.AttestedBy, v.AttestValid, v.AttestTrusted, v.attestWord); a != "" {
		return a + "  "
	}
	return ""
}

// labelFields: "labels: #a #b  " ("labels: -  " for none), followed by two
// spaces, for a header that names them before any of the author's text.
func labelFields(labels []string) string {
	if l := social.Labels(labels); l != "" {
		return "labels: " + l + "  "
	}
	return "labels: -  "
}

// writeLabels prints the labels line (when there are labels), before
// anything the author wrote.
func writeLabels(w io.Writer, labels []string, author string) {
	if l := social.Labels(labels); l != "" {
		_, _ = fmt.Fprintf(w, "labels: %s (its author's: %s)\n", l, short(author))
	}
}

// writeView prints one form in full: what it asks, the tally and notes, the
// answers, who hasn't answered, your response and the result. Its labels
// come first, then the client's header; the title follows
// "title: ", so no text of the author's starts a line.
func writeView(w io.Writer, v *View) {
	f := v.Form
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	writeLabels(w, v.Labels, f.Author)
	p("form %s by %s, %s%s (%s)\n", esc(f.ID), npub(f.Author), attestField(v), when(f.CreatedAt), stateLine(v))
	p("\ntitle: %s\n", social.Title(f.Title))
	if f.Context != "" {
		p("\n")
		social.WriteBlock(w, "  ", f.Context)
	}
	for _, e := range f.Events {
		p("about: event %s\n", esc(e))
	}
	for _, a := range f.Addrs {
		p("about: %s\n", esc(a))
	}
	if rs := f.Responders(); len(rs) > 0 {
		names := make([]string, 0, len(rs))
		for _, r := range rs {
			names = append(names, npub(r))
		}
		p("responders: %s\n", strings.Join(names, ", "))
	}
	if os := f.Observers(); len(os) > 0 {
		names := make([]string, 0, len(os))
		for _, o := range os {
			names = append(names, npub(o))
		}
		p("observers: %s\n", strings.Join(names, ", "))
	}
	if f.Quorum > 0 {
		met := "not met"
		if v.Tally.QuorumMet {
			met = "met"
		}
		p("quorum: %d (%s)\n", f.Quorum, met)
	}
	if f.Expiration > 0 {
		p("expires: %s\n", when(f.Expiration))
	}
	for _, q := range f.Questions {
		qt := tallyOf(v.Tally, q.Index)
		p("\n[%d] %s (%s", q.Index, esc(q.Text), esc(q.Type))
		if q.Optional {
			p(", optional")
		}
		if q.Min != "" {
			p(", min %s", esc(q.Min))
		}
		if q.Max != "" {
			p(", max %s", esc(q.Max))
		}
		p(")\n")
		if q.Why != "" {
			p("    why: %s\n", esc(q.Why))
		}
		for _, o := range q.Options {
			p("    - %s: %s", esc(o.ID), esc(o.Label))
			if o.Rec {
				p("  [recommended]")
			}
			if o.NoteRequired {
				p("  [note required]")
			}
			if qt != nil && qt.Counts != nil {
				p("  — %d", qt.Counts[o.ID])
			}
			p("\n")
			if qt != nil {
				for _, n := range qt.Notes[o.ID] {
					p("        %s: %s\n", short(n.PubKey), esc(n.Text))
				}
			}
		}
		if len(q.Defaults) > 0 {
			p("    default: %s\n", strings.Join(escAll(q.Defaults), ", "))
		}
		if qt != nil {
			for _, a := range qt.Answers {
				p("    %s: %s", short(a.PubKey), esc(a.Value))
				if a.Note != "" {
					p(" (%s)", esc(a.Note))
				}
				p("\n")
			}
		}
	}
	p("\n%d response(s)", v.Tally.Responses)
	if len(v.Waiting) > 0 {
		names := make([]string, 0, len(v.Waiting))
		for _, k := range v.Waiting {
			names = append(names, npub(k))
		}
		p("; not answered: %s", strings.Join(names, ", "))
	}
	p("\n")
	if v.Mine != nil {
		p("your response (%s): %s\n", when(v.Mine.CreatedAt), describe(v.Mine.Choices, v.Mine.Answers))
	}
	if r := v.Result; r != nil {
		p("result: %s at %s", esc(string(r.Status)), when(r.CreatedAt))
		if d := describe(r.Choices, r.Answers); d != "" {
			p(" — %s", d)
		}
		p("\n")
		if r.Content != "" {
			social.WriteBlock(w, "    ", r.Content)
		}
	}
	switch {
	case v.CanRespond && v.Role != "author":
		p("answer: ncli form respond %s …\n", shortID(f.ID))
	case v.Role == "author" && v.Result == nil:
		p("close: ncli form result %s --status decided|default|cancelled …\n", shortID(f.ID))
	}
}

func tallyOf(t *nippf.Tally, index int) *nippf.QuestionTally {
	if t == nil {
		return nil
	}
	for i := range t.Questions {
		if t.Questions[i].Index == index {
			return &t.Questions[i]
		}
	}
	return nil
}

// describe: choices and answers on one line.
func describe(cs []nippf.Choice, as []nippf.Answer) string {
	var parts []string
	for _, c := range cs {
		s := esc(c.Option)
		if c.Note != "" {
			s += fmt.Sprintf(" (%s)", esc(c.Note))
		}
		parts = append(parts, s)
	}
	for _, a := range as {
		s := fmt.Sprintf("[%d]=%s", a.Question, esc(a.Value))
		if a.Note != "" {
			s += fmt.Sprintf(" (%s)", esc(a.Note))
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}
