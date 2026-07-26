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

// TestHandleKeyCtrlS_NoCallbackRegistered_DoesNotPanic is a regression guard
// for a consumer of App (e.g. cli/bunker) that never calls RegisterCallback
// at all: a.saveFunc is nil in that case, and the pre-fix code called it
// unconditionally once neither activeSaver nor a focused EventSaver applied,
// panicking on the nil func value.
func TestHandleKeyCtrlS_NoCallbackRegistered_DoesNotPanic(t *testing.T) {
	app := NewApp()

	plain := tview.NewBox()
	app.SetFocus(plain)

	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlS, 0, tcell.ModNone))
}

// TestHandleKeyR_NoReloadFuncRegistered_DoesNotPanicAndFallsThrough mirrors
// the Ctrl+S guard above for 'r'/Restart: a consumer that never registers a
// reload callback (again, cli/bunker) must not panic, and -- since it has
// nothing to restart -- the key must fall through unswallowed so a focused
// widget's own 'r' binding (e.g. bunker's SessionsTable "revoke") still
// receives it.
func TestHandleKeyR_NoReloadFuncRegistered_DoesNotPanicAndFallsThrough(t *testing.T) {
	app := NewApp()

	plain := tview.NewBox()
	app.SetFocus(plain)

	event := tcell.NewEventKey(tcell.KeyRune, 'r', tcell.ModNone)
	got := app.handleKey(event)

	if got == nil {
		t.Fatal("expected 'r' to fall through to the focused widget when no reload callback is registered, got nil (swallowed)")
	}
}

// TestHandleKeyR_ReloadFuncRegistered_SwallowsEvent confirms the opposite
// side still works for apply's own boards: a registered reload callback
// makes 'r' trigger Restart's confirm dialog and swallow the key (so it
// isn't also dispatched to the focused widget in the same keystroke).
func TestHandleKeyR_ReloadFuncRegistered_SwallowsEvent(t *testing.T) {
	app := NewApp()
	app.RegisterCallback(func() {}, func() {})

	plain := tview.NewBox()
	app.SetFocus(plain)

	event := tcell.NewEventKey(tcell.KeyRune, 'r', tcell.ModNone)
	got := app.handleKey(event)

	if got != nil {
		t.Fatal("expected 'r' to be swallowed once a reload callback is registered")
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

// fakeFooterBoard is a minimal ChildProvider + FooterHintsProvider board,
// standing in for a real one (e.g. cli/bunker's BunkerBoard) to test
// App.refreshFooterHints' focus-tracking in isolation: its hints depend
// entirely on which of its two children is passed in.
type fakeFooterBoard struct {
	*tview.Flex
	a, b *tview.Box
}

func newFakeFooterBoard() *fakeFooterBoard {
	a, b := tview.NewBox(), tview.NewBox()
	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a, 1, 0, true).
		AddItem(b, 1, 0, false)
	return &fakeFooterBoard{Flex: flex, a: a, b: b}
}

func (f *fakeFooterBoard) Childs() []tview.Primitive { return []tview.Primitive{f.a, f.b} }

func (f *fakeFooterBoard) FooterHints(focused tview.Primitive) string {
	if focused == f.a {
		return "hints for A"
	}
	return "hints for B"
}

// TestRefreshFooterHintsTracksFocus is a regression guard for the
// contextual footer (see FooterHintsProvider's own doc comment,
// layout.go): the rendered footer text must track whichever child is
// currently focused, both right after Load and after every subsequent
// Focus call (Tab/Shift+Tab, or a dialog/overlay returning focus) --  not
// just whatever it happened to be at construction time.
func TestRefreshFooterHintsTracksFocus(t *testing.T) {
	app := NewApp().Init()
	board := newFakeFooterBoard()
	app.Load(board)

	if got := app.footer.status.GetText(true); got != "hints for A" {
		t.Fatalf("footer after Load = %q, want %q (childs[0] is the initial focus)", got, "hints for A")
	}

	app.Focus(1)
	if got := app.footer.status.GetText(true); got != "hints for B" {
		t.Fatalf("footer after Focus(1) = %q, want %q", got, "hints for B")
	}

	app.Focus(0)
	if got := app.footer.status.GetText(true); got != "hints for A" {
		t.Fatalf("footer after Focus(0) = %q, want %q", got, "hints for A")
	}
}

// TestRefreshFooterHintsNoopWithoutProvider guards the common case (every
// apply-style board): Load must not panic or set app.footer when the
// board doesn't implement FooterHintsProvider at all.
func TestRefreshFooterHintsNoopWithoutProvider(t *testing.T) {
	app := NewApp().Init()

	stream := FlowMetricsSlice{}
	events := NewEventTable(app, func(*nip01.Event) error { return nil })
	board := NewInspectBoard(app, t.Context(), &stream, &FlowLogger{}, events)

	app.Load(board)
	app.Focus(0) // must not panic with footerProvider == nil
}

// fakeCtrlCBoard is a minimal ChildProvider + CtrlCHandler board for
// testing App.handleKey's Ctrl+C dispatch in isolation.
type fakeCtrlCBoard struct {
	*tview.Box
	handled bool
	claim   bool
}

func (f *fakeCtrlCBoard) Childs() []tview.Primitive { return []tview.Primitive{f.Box} }

func (f *fakeCtrlCBoard) HandleCtrlC() bool {
	f.handled = true
	return f.claim
}

// TestHandleKeyCtrlC_HandlerClaims_SwallowsEvent guards the whole point of
// CtrlCHandler (see its own doc comment, cli/bunker's motivating case): a
// board that claims Ctrl+C (returns true) must have handleKey swallow the
// event so tview's own built-in "Ctrl-C closes the application" never
// also fires on the same keystroke.
func TestHandleKeyCtrlC_HandlerClaims_SwallowsEvent(t *testing.T) {
	app := NewApp().Init()
	board := &fakeCtrlCBoard{Box: tview.NewBox(), claim: true}
	app.Load(board)

	event := tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)
	got := app.handleKey(event)

	if !board.handled {
		t.Fatal("expected HandleCtrlC to be called")
	}
	if got != nil {
		t.Fatal("expected Ctrl+C to be swallowed (handleKey returns nil) once the board claims it")
	}
}

// TestHandleKeyCtrlC_HandlerDeclines_FallsThrough covers a handler that
// returns false: handleKey must NOT swallow the event, so tview's own
// default Stop()-the-application behavior still runs (via the unmodified
// returned event reaching the hardcoded check in tview's run loop).
func TestHandleKeyCtrlC_HandlerDeclines_FallsThrough(t *testing.T) {
	app := NewApp().Init()
	board := &fakeCtrlCBoard{Box: tview.NewBox(), claim: false}
	app.Load(board)

	event := tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)
	got := app.handleKey(event)

	if !board.handled {
		t.Fatal("expected HandleCtrlC to be called")
	}
	if got == nil {
		t.Fatal("expected Ctrl+C to fall through (handleKey returns the event) when the board declines it")
	}
}

// TestHandleKeyCtrlC_NoHandlerRegistered_FallsThrough is apply's own
// boards' case: no CtrlCHandler at all, so Ctrl+C must fall straight
// through to tview's own default without panicking on a nil handler.
func TestHandleKeyCtrlC_NoHandlerRegistered_FallsThrough(t *testing.T) {
	app := NewApp().Init()

	stream := FlowMetricsSlice{}
	events := NewEventTable(app, func(*nip01.Event) error { return nil })
	board := NewInspectBoard(app, t.Context(), &stream, &FlowLogger{}, events)
	app.Load(board)

	event := tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)
	got := app.handleKey(event)

	if got == nil {
		t.Fatal("expected Ctrl+C to fall through unswallowed when no CtrlCHandler is registered")
	}
}

// TestDismissOverlayDoesNotStealFocusWhenAnotherPageIsOnTop is a
// regression guard for a real bug found in cli/bunker: ShowOverlay's
// caller is often on the other end of an async completion (a network
// wait, a ticker poll) with no way to know what else happened on screen
// in the meantime. If a ShowDialog modal opened on top of the overlay
// while it was still up (e.g. cli/bunker's Auto-Prompt firing mid-
// pairing, or its openNostrconnectInput's own "Connecting..." overlay
// racing an unrelated approval), DismissOverlay used to unconditionally
// reclaim focus for the board regardless -- leaving that modal visible on
// screen but keyboard-dead: the operator's next keystroke landed on the
// board underneath, not the dialog they could actually see.
func TestDismissOverlayDoesNotStealFocusWhenAnotherPageIsOnTop(t *testing.T) {
	app := NewApp().Init()
	board := newFakeFooterBoard()
	app.Load(board)

	overlay := tview.NewBox()
	app.ShowOverlay("overlay1", overlay, overlay)
	if app.GetFocus() != overlay {
		t.Fatalf("GetFocus() after ShowOverlay = %v, want the overlay itself", app.GetFocus())
	}

	// Something else -- e.g. Auto-Prompt's own approval dialog -- opens on
	// top of the still-open overlay.
	app.ShowDialog("Approve request?", "text", tcell.ColorDefault, []string{"OK"}, func() {})
	dialogFocus := app.GetFocus()
	if _, ok := dialogFocus.(*tview.Button); !ok {
		t.Fatalf("GetFocus() after ShowDialog = %T, want *tview.Button", dialogFocus)
	}

	// The overlay's own async completion now fires and tries to dismiss
	// itself -- it must not steal focus from the dialog that's since taken
	// over the screen.
	app.DismissOverlay("overlay1")
	if got := app.GetFocus(); got != dialogFocus {
		t.Fatalf("GetFocus() after DismissOverlay = %v, want the still-open dialog's button (%v) untouched", got, dialogFocus)
	}
}

// TestDismissOverlayRestoresBoardFocusInTheNormalCase is the above test's
// counterpart: with nothing else layered on top, DismissOverlay must
// still hand focus back to the board -- its ordinary, overwhelmingly
// common case.
func TestDismissOverlayRestoresBoardFocusInTheNormalCase(t *testing.T) {
	app := NewApp().Init()
	board := newFakeFooterBoard()
	app.Load(board)

	overlay := tview.NewBox()
	app.ShowOverlay("overlay1", overlay, overlay)
	app.DismissOverlay("overlay1")

	if got := app.GetFocus(); got != board.a {
		t.Fatalf("GetFocus() after DismissOverlay = %v, want board.a (the board's initially-focused child)", got)
	}
}
