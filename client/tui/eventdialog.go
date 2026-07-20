package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/ohstr/nmilat/nip01"
	"github.com/rivo/tview"
)

// eventSaverFunc adapts a plain closure to EventSaver, for App.SetActiveSaver
// -- see that field's comment for why ShowEvent can't rely on the normal
// GetFocus().(EventSaver) check.
type eventSaverFunc struct {
	save func()
}

func (f eventSaverFunc) SaveSelected() bool {
	f.save()
	return true
}

// ShowEvent opens a large, scrollable view of event's full JSON, pretty
// printed and syntax-colored directly by this package -- no external editor
// or pager, so there's nothing to install and nothing to shell out to; it's
// read-only (the TextView never accepts edits). Close is the default
// selected button (index 0) so Enter never saves by accident; Escape and
// Ctrl+S both work regardless of which button is currently selected.
func (a *App) ShowEvent(event *nip01.Event, onSave func()) {
	dismiss := func() {
		a.SetActiveSaver(nil)
		a.pages.RemovePage("eventDetail")
		a.Focus(a.lastFocusedIndex)
	}

	// save closes this view *before* calling onSave, rather than after: onSave
	// pops the "event ... saved" confirmation dialog, and AddPage only hands
	// it focus if whatever's currently visible already has focus. Closing
	// first (via dismiss's plain page-removal, without dismiss's own
	// a.Focus(a.lastFocusedIndex)) leaves that in place for AddPage to build
	// on; calling dismiss() *after* onSave would instead immediately steal
	// focus back from the confirmation via that same a.Focus call. The
	// confirmation's own OK button already restores focus to the board when
	// the user dismisses it, so nothing else is needed here.
	save := func() {
		a.SetActiveSaver(nil)
		a.pages.RemovePage("eventDetail")
		onSave()
	}

	text := tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(true).
		SetScrollable(true)
	text.SetBorderPadding(1, 0, 2, 2)
	fmt.Fprint(text, colorizeEventJSON(event))
	text.ScrollToBeginning()

	form := tview.NewForm().
		SetButtonsAlign(tview.AlignCenter).
		SetButtonBackgroundColor(tcell.ColorDefault)
	form.AddButton("Close", dismiss)
	form.AddButton("Save", save)
	form.SetFocus(0) // "Close" -- default selected, so a stray Enter can't save
	form.SetCancelFunc(dismiss)
	form.SetBackgroundColor(tcell.ColorDefault)

	// Up/Down/PageUp/PageDown/Home/End scroll the (read-only) content
	// regardless of which button the Form has selected -- Form's own
	// left/right/Tab button navigation, Enter-to-activate, and
	// Escape-to-cancel are untouched.
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		row, col := text.GetScrollOffset()
		switch event.Key() {
		case tcell.KeyUp:
			text.ScrollTo(row-1, col)
		case tcell.KeyDown:
			text.ScrollTo(row+1, col)
		case tcell.KeyPgUp:
			text.ScrollTo(row-10, col)
		case tcell.KeyPgDn:
			text.ScrollTo(row+10, col)
		case tcell.KeyHome:
			text.ScrollToBeginning()
		case tcell.KeyEnd:
			text.ScrollToEnd()
		default:
			return event
		}
		return nil
	})

	view := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(text, 0, 1, false).
		AddItem(form, 3, 0, true)
	view.SetBorder(true).
		SetBorderColor(tcell.ColorPurple).
		SetTitle(" Event ").
		SetBackgroundColor(tcell.ColorDefault)

	a.SetActiveSaver(eventSaverFunc{save: save})
	a.pages.AddPage("eventDetail", centerBox(view, 90, 85), true, true)
	a.SetFocus(form)
}

// centerBox centers box within nil-item spacers sized to leave widthPercent/
// heightPercent of the screen for it -- the same technique SplashScreen uses
// for its own fixed-size content, generalized to a percentage of whatever
// the terminal's current size is.
func centerBox(box tview.Primitive, widthPercent, heightPercent int) tview.Primitive {
	widthRest := (100 - widthPercent) / 2
	heightRest := (100 - heightPercent) / 2

	row := tview.NewFlex().
		AddItem(nil, 0, widthRest, false).
		AddItem(box, 0, widthPercent, true).
		AddItem(nil, 0, widthRest, false)

	return tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, heightRest, false).
		AddItem(row, 0, heightPercent, true).
		AddItem(nil, 0, heightRest, false)
}

// jsonKeyLineRe matches one "key": value line from json.MarshalIndent's
// two-space-indented output (used by colorizeEventJSON below).
var jsonKeyLineRe = regexp.MustCompile(`^(\s*)"([^"]*)":\s*(.*)$`)

// colorizeEventJSON pretty-prints event as its canonical nostr JSON shape
// (json.MarshalIndent on *nip01.Event, the same encoding saved to file) and
// applies tview color tags line by line: keys in purple, string values in
// white, numbers in blue, true/false/null in yellow, structural punctuation
// left as-is. This is deliberately simple (regex over MarshalIndent's fixed,
// predictable layout) rather than a general JSON tokenizer -- nip01.Event
// only ever has string/int/nested-string-array fields, so a value never
// spans multiple lines.
func colorizeEventJSON(event *nip01.Event) string {
	raw, err := json.MarshalIndent(event, "", "  ")
	if err != nil {
		return tview.Escape(fmt.Sprintf("failed to render event: %s", err))
	}

	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		lines[i] = colorizeJSONLine(line)
	}
	return strings.Join(lines, "\n")
}

func colorizeJSONLine(line string) string {
	if m := jsonKeyLineRe.FindStringSubmatch(line); m != nil {
		indent, key, rest := m[1], m[2], m[3]
		return fmt.Sprintf("%s[%s]%s[-:-:-]: %s", indent, tcell.ColorPurple, tview.Escape(`"`+key+`"`), colorizeJSONValue(rest))
	}

	trimmed := strings.TrimLeft(line, " ")
	indent := line[:len(line)-len(trimmed)]
	return indent + colorizeJSONValue(trimmed)
}

// colorizeJSONValue colors a single value token (the part of a line after
// "key": , or a bare array element) -- everything from json.MarshalIndent
// arrives with any trailing comma already attached, which is preserved
// uncolored.
func colorizeJSONValue(value string) string {
	v, suffix := value, ""
	if strings.HasSuffix(value, ",") {
		v, suffix = value[:len(value)-1], ","
	}

	switch {
	case v == "" || v == "{" || v == "}" || v == "[" || v == "]" || v == "{}" || v == "[]":
		return tview.Escape(value)
	case strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`):
		return fmt.Sprintf("[%s]%s[-:-:-]%s", tcell.ColorWhite, tview.Escape(v), suffix)
	case v == "true" || v == "false" || v == "null":
		return fmt.Sprintf("[%s]%s[-:-:-]%s", tcell.ColorYellow, v, suffix)
	default:
		if _, err := strconv.ParseFloat(v, 64); err == nil {
			return fmt.Sprintf("[%s]%s[-:-:-]%s", tcell.ColorBlue, v, suffix)
		}
		return tview.Escape(value)
	}
}
