package tui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/ohstr/nmilat/nip01"
)

func footerText(app *App) string {
	return app.footer.status.GetText(true)
}

func assertHints(t *testing.T, got string, want, notWant []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("footer %q: missing %q", got, w)
		}
	}
	for _, n := range notWant {
		if strings.Contains(got, n) {
			t.Errorf("footer %q: unexpected %q", got, n)
		}
	}
}

// The footer lists only the focused panel's keys, plus the board-wide ones.
func TestStreamBoardFooterFollowsFocus(t *testing.T) {
	app := NewApp().Init()
	down := FlowMetricsSlice{NewFlowMetrics(0, "wss://a", tcell.ColorDefault, nil, false, nil)}
	up := FlowMetricsSlice{}
	board := NewStreamBoard(app, t.Context(), &up, &down, &FlowLogger{})
	board.down.Update()
	app.Load(board)

	global := []string{"<Tab>/<Shift+Tab> Panels", "<r> Restart", "<Ctrl+S> Save", "<Ctrl+C> Quit"}

	// Initial focus: the log. Its keys lead; board-wide keys follow on line 2.
	assertHints(t, footerText(app), append(global, "Toggle Wrap", "Toggle AutoScroll"), []string{"Delete", "Sort"})
	if !strings.HasPrefix(footerText(app), "<w> Toggle Wrap") {
		t.Errorf("footer %q: want the focused panel's keys first", footerText(app))
	}

	// Sources: delete and its column sort keys, no log toggles.
	app.Focus(1)
	assertHints(t, footerText(app), append(global, "<d> Delete", "<Shift+N/E/"), []string{"Wrap", "AutoScroll"})

	// Back to the log.
	app.Focus(0)
	assertHints(t, footerText(app), []string{"Toggle Wrap"}, []string{"Delete"})
}

// Events takes Ctrl+S itself, so the snapshot hint gives way to its own.
func TestInspectBoardFooterEvents(t *testing.T) {
	app := NewApp().Init()
	stream := FlowMetricsSlice{}
	events := NewEventTable(app, func(*nip01.Event) error { return nil })
	board := NewInspectBoard(app, t.Context(), &stream, &FlowLogger{}, events)
	app.Load(board)

	lines := strings.Split(footerText(app), "\n")
	if len(lines) != 2 {
		t.Fatalf("footer %q: want 2 lines", footerText(app))
	}
	assertHints(t, lines[0], []string{"<Enter> View", "Toggle AutoScroll", "<Ctrl+S> Save Event"}, []string{"Wrap", "Delete"})
	assertHints(t, lines[1], []string{"<r> Restart", "<Ctrl+C> Quit"}, []string{"Ctrl+S"})
}

// A single-panel board has nothing to switch to.
func TestSyncBoardFooterHasNoPanelSwitch(t *testing.T) {
	app := NewApp().Init()
	app.Load(NewSyncBoard(app, t.Context(), &FlowLogger{}))

	assertHints(t, footerText(app), []string{"Toggle Wrap", "<Ctrl+S> Save"}, []string{"Panels"})
}
