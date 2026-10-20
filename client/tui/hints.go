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

// applyFooter lists the board-wide keys, then only the focused panel's own.
func applyFooter(focused tview.Primitive, panels ...hintPanel) string {
	var hints []string
	if len(panels) > 1 {
		hints = append(hints, HintTag("<Tab>/<Shift+Tab>", "Switch Panel"))
	}
	hints = append(hints, HintTag("<r>", "Restart"))
	// A focused EventSaver takes Ctrl+S itself (see App.handleKey).
	if _, ok := focused.(EventSaver); !ok {
		hints = append(hints, HintTag("<Ctrl+S>", "Save Snapshot"))
	}
	for _, p := range panels {
		if p.focusTarget() == focused {
			hints = append(hints, p.hints()...)
			break
		}
	}
	return strings.Join(hints, "   \t")
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
