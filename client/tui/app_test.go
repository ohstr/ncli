package tui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/ohstr/nmilat/nip01"
	"github.com/rivo/tview"
)

// fakeEventSaver is a minimal EventSaver-implementing primitive, standing in
// for EventTable so these tests exercise App.handleKey's dispatch logic in
// isolation.
type fakeEventSaver struct {
	*tview.Box
	saved bool
}

func (f *fakeEventSaver) SaveSelected() bool {
	f.saved = true
	return true
}

// TestHandleKeyCtrlSDispatchesToFocusedEventSaver is a regression guard for
// the contextual Ctrl+S: when the focused widget can save a selection
// itself, Ctrl+S must go to it instead of the default spec-save (saveFunc).
func TestHandleKeyCtrlSDispatchesToFocusedEventSaver(t *testing.T) {
	app := NewApp()

	saveFuncCalled := false
	app.RegisterCallback(func() {}, func() { saveFuncCalled = true })

	saver := &fakeEventSaver{Box: tview.NewBox()}
	app.SetFocus(saver)

	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlS, 0, tcell.ModNone))

	if !saver.saved {
		t.Fatal("expected the focused EventSaver to receive SaveSelected")
	}
	if saveFuncCalled {
		t.Fatal("expected saveFunc not to be called when an EventSaver is focused")
	}
}

// TestHandleKeyCtrlSFallsBackToSaveFuncWhenNotEventSaver guards the other
// side: every existing focus target (Console table, ambient log, Stream's
// Sources/Destinations tables) doesn't implement EventSaver, so Ctrl+S must
// keep behaving exactly as it did before this feature -- saving the run's
// spec YAML.
func TestHandleKeyCtrlSFallsBackToSaveFuncWhenNotEventSaver(t *testing.T) {
	app := NewApp()

	saveFuncCalled := false
	app.RegisterCallback(func() {}, func() { saveFuncCalled = true })

	plain := tview.NewBox()
	app.SetFocus(plain)

	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlS, 0, tcell.ModNone))

	if !saveFuncCalled {
		t.Fatal("expected saveFunc to be called when the focused widget is not an EventSaver")
	}
}

// TestLoadSetsLastFocusedIndexToTheActualFocusedChild is a regression guard:
// lastFocusedIndex defaults to 0, which is only correct if childs[0] happens
// to be whichever primitive the board's own Flex(es) marked as initially
// focused. InspectBoard's Childs() order (Targets, log, Events) is chosen to
// match reading/Tab order, not focus order -- Events (index 2) is the one
// actually focused on load, via NewInspectBoard's own AddItem(..., true).
// Without Load correcting lastFocusedIndex, closing any dialog shown before
// the user's first Tab press would restore focus to Targets (index 0)
// instead of Events.
func TestLoadSetsLastFocusedIndexToTheActualFocusedChild(t *testing.T) {
	app := NewApp().Init()

	stream := FlowMetricsSlice{}
	events := NewEventTable(app, func(*nip01.Event) error { return nil })

	board := NewInspectBoard(app, t.Context(), &stream, &FlowLogger{}, events)
	app.Load(board)

	wantIndex := -1
	for i, child := range app.childs {
		if child == app.GetFocus() {
			wantIndex = i
		}
	}
	if wantIndex == -1 {
		t.Fatal("expected one of app.childs to match the actually-focused primitive")
	}
	if app.lastFocusedIndex != wantIndex {
		t.Fatalf("expected lastFocusedIndex to be %d (the actually-focused child), got %d", wantIndex, app.lastFocusedIndex)
	}
	if app.childs[wantIndex] != board.events {
		t.Fatalf("expected the events table to be the initially-focused child, focus landed on childs[%d] instead", wantIndex)
	}
}
