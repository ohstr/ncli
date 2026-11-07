package form

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/nmilat/nippf"
)

// errEOF: the terminal ended before the prompts did.
var errEOF = errors.New("input ended before every question was answered: nothing sent")

// line reads one line, trimmed; io.EOF with nothing read is errEOF.
func line(w io.Writer, in *bufio.Reader, prompt string) (string, error) {
	_, _ = fmt.Fprint(w, prompt)
	s, err := in.ReadString('\n')
	if err != nil && s == "" {
		return "", errEOF
	}
	return strings.TrimSpace(s), nil
}

func yes(w io.Writer, in *bufio.Reader, prompt string, def bool) (bool, error) {
	for {
		s, err := line(w, in, prompt)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// confirm: anything but an explicit no sends (Enter is yes); input that
// ended is no.
func confirm(w io.Writer, in *bufio.Reader, prompt string) bool {
	ok, err := yes(w, in, prompt, true)
	return err == nil && ok
}

func typeHint(typ string) string {
	switch typ {
	case nippf.TypeNumber:
		return "a number"
	case nippf.TypeDate:
		return "a date, YYYY-MM-DD"
	case nippf.TypeTime:
		return "unix seconds"
	}
	return "text"
}

// promptResponse asks each question of f in turn, like a question dialog:
// its options with the recommended one marked and the author's why, a note
// where one is required (offered elsewhere), optional questions skippable.
// The answers are checked as a whole by nippf when the response is built.
// The form's labels come first: who answers sees them before any of the
// author's words.
func promptResponse(w io.Writer, in *bufio.Reader, f *nippf.Form, labels []string) ([]nippf.Choice, []nippf.Answer, error) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	writeLabels(w, labels, f.Author)
	p("title: %s\n", social.Title(f.Title))
	if f.Context != "" {
		p("\n")
		social.WriteBlock(w, "  ", f.Context)
	}
	var cs []nippf.Choice
	var as []nippf.Answer
	for _, q := range f.Questions {
		p("\n[%d] %s", q.Index, esc(q.Text))
		if q.Optional {
			p(" (optional: Enter skips)")
		}
		p("\n")
		if q.Choice() {
			for i, o := range q.Options {
				p("  %d) %s", i+1, esc(o.Label))
				if o.Rec {
					p("  — recommended")
				}
				if o.NoteRequired {
					p("  (say why: a note is required)")
				}
				p("\n")
				if o.Detail != "" {
					p("     %s\n", esc(o.Detail))
				}
			}
			if q.Why != "" {
				p("  why recommended: %s\n", esc(q.Why))
			}
			if len(q.Defaults) > 0 {
				p("  if nobody decides: %s\n", strings.Join(escAll(q.Defaults), ", "))
			}
			how := "choose one (number or id)"
			if q.Type == nippf.TypeMulti {
				how = "choose one or more (numbers or ids, comma-separated)"
			}
			var chosen []nippf.Option
		choose:
			for {
				s, err := line(w, in, how+": ")
				if err != nil {
					return nil, nil, err
				}
				if s == "" {
					if q.Optional {
						break
					}
					p("  this question needs an answer\n")
					continue
				}
				chosen = chosen[:0]
				bad := ""
				for _, tok := range strings.Split(s, ",") {
					tok = strings.TrimSpace(tok)
					if tok == "" {
						continue
					}
					o, ok := q.Option(tok)
					if n, err := strconv.Atoi(tok); err == nil && n >= 1 && n <= len(q.Options) {
						o, ok = q.Options[n-1], true
					}
					if !ok {
						bad = tok
						break
					}
					if !slices.ContainsFunc(chosen, func(c nippf.Option) bool { return c.ID == o.ID }) {
						chosen = append(chosen, o)
					}
				}
				switch {
				case bad != "":
					p("  %s is no option of this question\n", strconv.QuoteToASCII(bad))
				case q.Type == nippf.TypeSingle && len(chosen) != 1:
					p("  choose exactly one\n")
				case len(chosen) == 0:
					p("  choose at least one\n")
				default:
					break choose
				}
			}
			for _, o := range chosen {
				c := nippf.Choice{Option: o.ID}
				for {
					prompt := fmt.Sprintf("  a note on %s (Enter for none): ", strconv.QuoteToASCII(o.Label))
					if o.NoteRequired {
						prompt = fmt.Sprintf("  your note on %s (required): ", strconv.QuoteToASCII(o.Label))
					}
					s, err := line(w, in, prompt)
					if err != nil {
						return nil, nil, err
					}
					if s == "" && o.NoteRequired {
						continue
					}
					c.Note = s
					break
				}
				cs = append(cs, c)
			}
			continue
		}
		if q.Why != "" {
			p("  why: %s\n", esc(q.Why))
		}
		bounds := ""
		if q.Min != "" {
			bounds += " from " + esc(q.Min)
		}
		if q.Max != "" {
			bounds += " to " + esc(q.Max)
		}
		if len(q.Defaults) > 0 {
			p("  if nobody decides: %s\n", strings.Join(escAll(q.Defaults), ", "))
		}
		for {
			s, err := line(w, in, fmt.Sprintf("%s%s: ", typeHint(q.Type), bounds))
			if err != nil {
				return nil, nil, err
			}
			if s == "" {
				if q.Optional {
					break
				}
				p("  this question needs an answer\n")
				continue
			}
			if !nippf.ValidValue(q.Type, s) {
				p("  not %s\n", typeHint(q.Type))
				continue
			}
			note, err := line(w, in, "  a note (Enter for none): ")
			if err != nil {
				return nil, nil, err
			}
			as = append(as, nippf.Answer{Question: q.Index, Value: s, Note: note})
			break
		}
	}
	return cs, as, nil
}

// promptSpec asks for a form's parts, one by one.
func promptSpec(w io.Writer, in *bufio.Reader) (*Spec, error) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	ask := func(prompt string) (string, error) { return line(w, in, prompt) }
	s := &Spec{}
	var err error
	for s.Title == "" {
		if s.Title, err = ask("title (what is being decided): "); err != nil {
			return nil, err
		}
	}
	if s.Context, err = ask("context (what the responder needs to know; Enter for none): "); err != nil {
		return nil, err
	}
	for i := 0; ; i++ {
		p("\nquestion %d\n", i)
		text, err := ask("  text (Enter to finish): ")
		if err != nil {
			return nil, err
		}
		if text == "" {
			if i == 0 {
				p("  a form asks at least one question\n")
				i--
				continue
			}
			break
		}
		q := SpecQuestion{Text: text}
		for {
			t, err := ask("  type [singlechoice] (multiplechoice, text, number, date, time): ")
			if err != nil {
				return nil, err
			}
			if t == "" {
				t = nippf.TypeSingle
			}
			if nippf.KnownType(t) {
				q.Type = t
				break
			}
		}
		choice := q.Type == nippf.TypeSingle || q.Type == nippf.TypeMulti
		if choice {
			for {
				id, err := ask("  option id (letters, digits, - and _; Enter to finish): ")
				if err != nil {
					return nil, err
				}
				if id == "" {
					if len(q.Options) == 0 {
						continue
					}
					break
				}
				o := SpecOption{ID: id}
				if o.Label, err = ask("    label: "); err != nil {
					return nil, err
				}
				if o.Detail, err = ask("    detail (what choosing it does; Enter for none): "); err != nil {
					return nil, err
				}
				if o.Rec, err = yes(w, in, "    recommended? [y/N] ", false); err != nil {
					return nil, err
				}
				if o.NoteRequired, err = yes(w, in, "    a note required? [y/N] ", false); err != nil {
					return nil, err
				}
				q.Options = append(q.Options, o)
			}
			hasOther := slices.ContainsFunc(q.Options, func(o SpecOption) bool { return o.ID == "other" })
			if !hasOther {
				add, err := yes(w, in, "  add \"Other (say what)\" with a required note? [Y/n] ", true)
				if err != nil {
					return nil, err
				}
				if add {
					q.Options = append(q.Options, SpecOption{ID: "other", Label: "Other (say what)", NoteRequired: true})
				}
			}
		}
		if q.Why, err = ask("  why (your reason for what you recommend; Enter for none): "); err != nil {
			return nil, err
		}
		if q.Optional, err = yes(w, in, "  optional? [y/N] ", false); err != nil {
			return nil, err
		}
		if q.Type == nippf.TypeNumber || q.Type == nippf.TypeDate || q.Type == nippf.TypeTime {
			v, err := ask("  min (Enter for none): ")
			if err != nil {
				return nil, err
			}
			q.Min = scalar(v)
			if v, err = ask("  max (Enter for none): "); err != nil {
				return nil, err
			}
			q.Max = scalar(v)
		}
		d, err := ask("  default if no outcome is reached (an option id, several comma-separated, or a value; Enter for none): ")
		if err != nil {
			return nil, err
		}
		for _, v := range strings.Split(d, ",") {
			if v = strings.TrimSpace(v); v != "" {
				q.Default = append(q.Default, v)
			}
		}
		s.Questions = append(s.Questions, q)
	}
	list := func(prompt string) ([]string, error) {
		v, err := ask(prompt)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, k := range strings.Split(v, ",") {
			if k = strings.TrimSpace(k); k != "" {
				out = append(out, k)
			}
		}
		return out, nil
	}
	if s.Responders, err = list("\nresponders (npubs, comma-separated; Enter for --to only or anyone): "); err != nil {
		return nil, err
	}
	if s.Observers, err = list("observers (npubs, comma-separated; Enter for none): "); err != nil {
		return nil, err
	}
	q, err := ask("quorum (Enter for none): ")
	if err != nil {
		return nil, err
	}
	if q != "" {
		if s.Quorum, err = strconv.Atoi(q); err != nil {
			return nil, fmt.Errorf("quorum %q: a number", q)
		}
	}
	for _, t := range []struct {
		name string
		into *int64
	}{{"ends", &s.EndsAt}, {"expires (clients delete it then)", &s.Expiration}} {
		v, err := ask(t.name + " (a duration from now like 3d or 12h, or unix seconds; Enter for never): ")
		if err != nil {
			return nil, err
		}
		if *t.into, err = futureTime(v); err != nil {
			return nil, err
		}
	}
	high, err := yes(w, in, "priority high? [y/N] ", false)
	if err != nil {
		return nil, err
	}
	if high {
		s.Priority = "high"
	}
	return s, nil
}

// futureTime reads "" (none), unix seconds, or a duration from now (3d,
// 12h, 30m).
func futureTime(v string) (int64, error) {
	if v == "" {
		return 0, nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n, nil
	}
	d, err := time.ParseDuration(v)
	if strings.HasSuffix(v, "d") {
		var days int
		days, err = strconv.Atoi(strings.TrimSuffix(v, "d"))
		d = time.Duration(days) * 24 * time.Hour
	}
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q: a duration like 3d or 12h, or unix seconds", v)
	}
	return clock().Add(d).Unix(), nil
}
