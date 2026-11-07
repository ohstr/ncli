package form

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nippf"
)

// rawControl fails if out holds a raw control, separator or format rune.
func rawControl(t *testing.T, what, out string) {
	t.Helper()
	for _, bad := range []string{"\x1b", "\r", "\u2028", "\u202e", "\u200b", "\u200e", "\ufeff", "\x85"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%s prints a raw %q: %q", what, bad, out)
		}
	}
}

// A form's title and context are its author's text. form list and
// form show print them escaped, so a form can't rewrite what the reader sees
// of another form (move the cursor, erase a line, clear the screen, forge a
// line).
func TestFormTextEscaped(t *testing.T) {
	author := strings.Repeat("a", 64)
	responder := strings.Repeat("b", 64)
	notice := &nippf.Form{ID: "c0ffee" + strings.Repeat("1", 58), Author: author, Title: "Notice: PR 1234567890ab at 0123456789ab", Private: true,
		Parties: []nippf.Party{{PubKey: responder, Role: nippf.RoleResponder}}, EndsAt: 1900000000}
	fake := "Notice: PR 1234567890ab at 0123456789ab  (cancelled, private; by " + short(author) + "; 0/1 answered)"
	forgedLine := shortID(strings.Repeat("2", 64)) + "  team, report"
	forged := &nippf.Form{ID: "d00d" + strings.Repeat("2", 60), Author: author, Private: true,
		Parties: []nippf.Party{{PubKey: responder, Role: nippf.RoleResponder}}, EndsAt: 1900000000,
		Title: "\x1b[1A\x1b[2K\r" + shortID(notice.ID) + "  " + fake + "\n" + forgedLine}
	var out bytes.Buffer
	for _, f := range []*nippf.Form{notice, forged} {
		v := &View{Form: f, State: nippf.StateOpen, Where: "private", Tally: f.Tally(nil)}
		fmt.Fprintln(&out, listLine(v))
	}
	rawControl(t, "form list", out.String())
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], `\x1b[1A\x1b[2K\r`) || !strings.Contains(lines[1], `\n`+strings.ReplaceAll(forgedLine, "  ", " ")) {
		t.Fatalf("form list, one line per form, escaped: %q", out.String())
	}

	forged.Context = "What changed: …\x1b[2J\x1b[Hform " + notice.ID + " by alice (cancelled, private)\n\nresult: cancelled\r\nlast\u202e"
	forged.Questions = []nippf.Question{{Index: 0, Type: nippf.TypeSingle, Text: "go?\x1b[2J", Why: "w\u200by",
		Options: []nippf.Option{{ID: "yes", Label: "yes\nresult: decided", Rec: true}, {ID: "no", Label: "no\u200e", Detail: "stops\nresult: decided\x1b[2J"}}, Defaults: []string{"no\x1b"}}}
	var show bytes.Buffer
	writeView(&show, &View{Form: forged, State: nippf.StateOpen, Where: "private", Tally: forged.Tally(nil),
		Result: &nippf.Result{Status: "decided", Content: "ok\nform x by y\x1b[2J"}})
	rawControl(t, "form show", show.String())
	for _, want := range []string{
		"\n  What changed: …\\x1b[2J\\x1b[Hform " + notice.ID + " by alice (cancelled, private)\n  \n  result: cancelled\n  last\\u202e\n",
		"[0] go?\\x1b[2J (singlechoice)", "why: w\\u200by", "- yes: yes\\nresult: decided", "- no: no\\u200e  — 0\n      stops\\nresult: decided\\x1b[2J\n", "default: no\\x1b",
		"    ok\n    form x by y\\x1b[2J\n",
	} {
		if !strings.Contains(show.String(), want) {
			t.Fatalf("form show lacks %q:\n%s", want, show.String())
		}
	}
	// No line the author wrote starts at column 0, the title neither (it follows "title: ").
	for _, l := range strings.Split(show.String(), "\n") {
		if strings.HasPrefix(l, "result: cancelled") || strings.HasPrefix(l, "form x") || strings.HasPrefix(l, `\x1b[1A`) {
			t.Fatalf("forged line at column 0: %q", l)
		}
	}

	// form respond's prompts too.
	var prompts bytes.Buffer
	if _, _, err := promptResponse(&prompts, bufio.NewReader(strings.NewReader("1\nfine\n")), forged, nil); err != nil {
		t.Fatal(err)
	}
	rawControl(t, "form respond", prompts.String())
	if !strings.Contains(prompts.String(), "\n  result: cancelled\n") || !strings.Contains(prompts.String(), `"yes\nresult: decided"`) {
		t.Fatalf("form respond's prompts: %q", prompts.String())
	}
}
