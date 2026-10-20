package tui

import (
	"fmt"
	"strings"

	"github.com/rivo/tview"
)

// HintTag renders one "<key> label" footer entry in the shared
// accent-key/muted-label colors.
func HintTag(key, label string) string {
	return fmt.Sprintf("[%s:-:b]%s [%s:-:-]%s", ColorAccent, key, ColorMuted, label)
}

// hintPanel is one of apply's panels: the primitive that holds focus while
// it's selected, and the keys it handles then.
type hintPanel interface {
	focusTarget() tview.Primitive
	hints() []string
}

// applyFooter puts the focused panel's keys on the first line and the
// board-wide ones on the second, each short enough for 80 columns.
func applyFooter(focused tview.Primitive, panels ...hintPanel) string {
	var own []string
	for _, p := range panels {
		if p.focusTarget() == focused {
			own = p.hints()
			break
		}
	}

	var global []string
	if len(panels) > 1 {
		global = append(global, HintTag("<Tab>/<Shift+Tab>", "Panels"))
	}
	global = append(global, HintTag("<r>", "Restart"))
	// A focused EventSaver takes Ctrl+S itself (see App.handleKey).
	if _, ok := focused.(EventSaver); !ok {
		global = append(global, HintTag("<Ctrl+S>", "Save"))
	}
	global = append(global, HintTag("<Ctrl+C>", "Quit"))

	return strings.Join(own, "   ") + "\n" + strings.Join(global, "   ")
}

func (t *Logger) focusTarget() tview.Primitive { return t.logs }

func (t *Logger) hints() []string {
	return []string{HintTag("<w>", "Toggle Wrap"), HintTag("<s>", "Toggle AutoScroll")}
}

func (t *Table) focusTarget() tview.Primitive { return t }

func (t *Table) hints() []string {
	hints := []string{HintTag("<d>", "Delete")}
	t.mu.Lock()
	defer t.mu.Unlock()
	var keys []string
	for _, c := range t.columns {
		if c.SortKey != 0 {
			keys = append(keys, string(c.SortKey))
		}
	}
	if len(keys) > 0 {
		hints = append(hints, HintTag("<Shift+"+strings.Join(keys, "/")+">", "Sort"))
	}
	return hints
}

func (t *EventTable) focusTarget() tview.Primitive { return t }

func (t *EventTable) hints() []string {
	return []string{
		HintTag("<Enter>", "View"),
		HintTag("<s>", "Toggle AutoScroll"),
		HintTag("<Ctrl+S>", "Save Event"),
	}
}

// FooterHints implements FooterHintsProvider.
func (sb *StreamBoard) FooterHints(focused tview.Primitive) string {
	return applyFooter(focused, sb.logger, sb.down, sb.up)
}

// FooterHints implements FooterHintsProvider.
func (b *InspectBoard) FooterHints(focused tview.Primitive) string {
	return applyFooter(focused, b.targets, b.logger, b.events)
}

// FooterHints implements FooterHintsProvider.
func (b *SyncBoard) FooterHints(focused tview.Primitive) string {
	return applyFooter(focused, b.logger)
}
