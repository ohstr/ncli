package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/ohstr/nmilat/nip01"
	"github.com/rivo/tview"
)

func TestColorizeJSONValue(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"string value", `"hello",`, fmt.Sprintf("[%s]%s[-:-:-]%s", tcell.ColorWhite, tview.Escape(`"hello"`), ",")},
		{"number value", "42,", fmt.Sprintf("[%s]%s[-:-:-]%s", tcell.ColorBlue, "42", ",")},
		{"bool value", "true,", fmt.Sprintf("[%s]%s[-:-:-]%s", tcell.ColorYellow, "true", ",")},
		{"null value", "null", fmt.Sprintf("[%s]%s[-:-:-]%s", tcell.ColorYellow, "null", "")},
		{"bare open brace", "{", tview.Escape("{")},
		{"bare open bracket", "[", tview.Escape("[")},
		{"bare close bracket with comma", "],", tview.Escape("],")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := colorizeJSONValue(tt.value); got != tt.want {
				t.Fatalf("colorizeJSONValue(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestColorizeJSONLineDetectsKeys(t *testing.T) {
	got := colorizeJSONLine(`  "kind": 1,`)
	if !strings.Contains(got, `"kind"`) {
		t.Fatalf("expected the key to survive in the output, got %q", got)
	}
	if !strings.HasPrefix(got, "  ") {
		t.Fatalf("expected leading indent to be preserved, got %q", got)
	}
}

// TestColorizeEventJSONRendersCleanlyThroughTextView is the important
// regression guard: content/tags can contain literal "[" and "]" (e.g. a
// note linking "[text](url)"), and tview's own dynamic-color parser treats
// unescaped brackets as the start of a tag. If colorizeEventJSON doesn't
// escape them (via tview.Escape), a TextView would silently mangle that
// content instead of displaying it -- so this round-trips real event data
// through an actual TextView and checks it survives intact.
func TestColorizeEventJSONRendersCleanlyThroughTextView(t *testing.T) {
	event := &nip01.Event{
		ID:        "abc123",
		PubKey:    "def456",
		CreatedAt: 1,
		Kind:      1,
		Tags:      [][]string{{"e", "someid"}, {"p", "otherid"}},
		Content:   "check out [this link](http://example.com) and [another] bracket",
		Sig:       "sig123",
	}

	colored := colorizeEventJSON(event)

	tv := tview.NewTextView().SetDynamicColors(true)
	fmt.Fprint(tv, colored)
	rendered := tv.GetText(true)

	for _, want := range []string{
		"abc123", "def456", "sig123", "someid", "otherid",
		"check out [this link](http://example.com) and [another] bracket",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected rendered output to contain %q, got:\n%s", want, rendered)
		}
	}
}
