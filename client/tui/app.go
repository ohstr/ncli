// Package tui provides the terminal UI (built on tview) used to display
// live stream/inspect activity, logs, and flow metrics for ncli's client.
package tui

import (
	"fmt"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func init() {
	tview.Borders.HorizontalFocus = tview.BoxDrawingsHeavyHorizontal
	tview.Borders.VerticalFocus = tview.BoxDrawingsHeavyVertical
	tview.Borders.TopLeftFocus = tview.BoxDrawingsHeavyDownAndRight
	tview.Borders.TopRightFocus = tview.BoxDrawingsHeavyDownAndLeft
	tview.Borders.BottomLeftFocus = tview.BoxDrawingsHeavyUpAndRight
	tview.Borders.BottomRightFocus = tview.BoxDrawingsHeavyUpAndLeft
}

// EventSaver is implemented by a focusable widget that can save whatever
// event it currently has selected (e.g. EventTable). App.handleKey checks
// the focused primitive against this interface on Ctrl+S so the keystroke
// means "save the selected event" there and falls back to the default
// spec-save (saveFunc) everywhere else -- no widget-specific knowledge
// needed in App itself.
type EventSaver interface {
	SaveSelected() bool
}

// CtrlCHandler is implemented by a board that needs to override the
// default Ctrl+C behavior. Absent this, tview's own Application.run
// treats Ctrl+C as an unconditional, unconfirmed Application.Stop() the
// moment nothing else consumes the keystroke first -- fine for a board
// with nothing left to do once the TUI process itself exits, but wrong
// for one like cli/bunker's, where the TUI is just an attach client and
// letting the process exit is not the same as stopping what it's
// attached to. Return true to say "handled, don't fall through to
// tview's own default"; false to keep it.
type CtrlCHandler interface {
	HandleCtrlC() bool
}

type App struct {
	*tview.Application
	pages                *tview.Pages
	splashOnce           sync.Once
	dialog               *Dialog
	childs               []tview.Primitive
	reloadFunc, saveFunc func()

	// activeSaver, when set, takes priority over the GetFocus().(EventSaver)
	// check on Ctrl+S. Needed for overlays built on *tview.Form (like the
	// event detail view): Form.Focus delegates Application.focus down to
	// whichever button is currently selected, so GetFocus() never actually
	// returns the Form (or a wrapper around it) while such an overlay is
	// open -- only the raw *tview.Button, which can't implement EventSaver.
	// Cleared by whoever set it once the overlay closes.
	activeSaver EventSaver

	lastFocusedIndex int
	defaultLogger    *FlowLogger

	// footer/footerProvider back refreshFooterHints: footer is nil unless
	// Load's board implements FooterHintsProvider, in which case
	// footerProvider is that same board, re-invoked on every focus change
	// (Focus below) so its hints can depend on which of the board's own
	// panels is currently focused, not just be fixed once at Load time.
	footer         *Footer
	footerProvider FooterHintsProvider

	// ctrlCHandler is nil unless Load's board implements CtrlCHandler --
	// see that interface's own doc comment.
	ctrlCHandler CtrlCHandler
}

// SetActiveSaver overrides Ctrl+S's dispatch target regardless of focus --
// see the activeSaver field comment. Pass nil to clear it (e.g. when the
// overlay that set it closes).
func (a *App) SetActiveSaver(s EventSaver) {
	a.activeSaver = s
}

func (a *App) Logger() *FlowLogger {
	if a.defaultLogger == nil {
		a.defaultLogger = &FlowLogger{}
	}
	return a.defaultLogger
}

func NewApp() *App {
	return &App{
		Application: tview.NewApplication(),
		pages:       tview.NewPages(),
	}
}

func (a *App) Init() *App {

	a.pages.AddPage("splashscreen", SplashScreen(), true, true).
		AddPage("reloading", ReloadingScreen(), true, false)

	a.SetRoot(a.pages, true).SetFocus(a.pages)

	a.SetInputCapture(a.handleKey)

	return a
}

func (a *App) handleKey(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyRune:
		switch event.Rune() {
		case 'r':
			// Restart is opt-in: only a board that actually registered a
			// reload callback via RegisterCallback (apply's own boards) has
			// anything to restart. Without this guard, 'r' unconditionally
			// popped a "Restart?" dialog for *every* App -- including
			// bunker's, which never calls RegisterCallback and whose own
			// SessionsTable separately binds 'r' to "revoke" -- so the key
			// fired both actions at once (this handler never swallows the
			// event below), and actually clicking "Restart" there would
			// call the nil a.reloadFunc and panic. Returning nil here (only
			// when a reload callback exists) keeps 'r' fully available to
			// the focused widget on every other App, instead of hardcoding
			// a bunker-specific exception.
			if a.reloadFunc != nil {
				a.Restart()
				return nil
			}
		}

	case tcell.KeyCtrlC:
		// Checked here, in the Application-level input capture, not some
		// primitive's own SetInputCapture further down: tview's run loop
		// (application.go) applies this capture and its own hardcoded
		// "Ctrl-C closes the application" fallback *before* ever
		// dispatching to the root primitive's InputHandler chain, so a
		// primitive-level capture would never even see the keystroke --
		// tview would have already called Stop() and broken out of the
		// loop first. Returning nil here (only when a handler is
		// registered) both runs the handler's own response and prevents
		// tview's default from also firing on the same keystroke.
		if a.ctrlCHandler != nil && a.ctrlCHandler.HandleCtrlC() {
			return nil
		}

	case tcell.KeyCtrlS:
		// Same opt-in rule as Restart above: a.saveFunc is nil unless a
		// board registered one, and calling a nil func would panic.
		if a.activeSaver != nil {
			a.activeSaver.SaveSelected()
		} else if saver, ok := a.GetFocus().(EventSaver); ok {
			saver.SaveSelected()
		} else if a.saveFunc != nil {
			a.saveFunc()
		}

	case tcell.KeyTab:
		if a.childs == nil {
			break
		}
		for i, widget := range a.childs {
			if a.GetFocus() == widget {
				nextIndex := (i + 1) % len(a.childs)
				a.Focus(nextIndex)
				break
			}
		}

	case tcell.KeyBacktab:
		if a.childs == nil {
			break
		}
		for i, widget := range a.childs {
			if a.GetFocus() == widget {
				prevIndex := (i - 1 + len(a.childs)) % len(a.childs)
				a.Focus(prevIndex)
				break
			}
		}
	}

	return event
}

func (a *App) RegisterCallback(reloadFunc, saveFunc func()) {
	a.reloadFunc = func() {
		go reloadFunc()
	}
	a.saveFunc = saveFunc
}

func (a *App) Load(board tview.Primitive) {

	a.splashOnce.Do(func() {
		time.Sleep(time.Second * 1)
	})

	layout := NewLayout().Init(a, board)
	a.childs = layout.body.Childs()
	a.footer = layout.footer
	a.footerProvider, _ = board.(FooterHintsProvider)
	a.ctrlCHandler, _ = board.(CtrlCHandler)

	a.pages.HidePage("dialog").AddAndSwitchToPage("main", layout, true)

	// lastFocusedIndex defaults to 0, which is only correct if childs[0] is
	// also whichever primitive the board's own Flex(es) marked as initially
	// focused (the "true" bool in AddItem) -- not a given once a board's
	// Childs() order is chosen for Tab-cycling/reading order rather than to
	// match that. AddAndSwitchToPage above already resolved real focus down
	// to that primitive, so find its actual index here instead of assuming.
	for i, child := range a.childs {
		if child == a.GetFocus() {
			a.lastFocusedIndex = i
			break
		}
	}

	// layout.Init seeded the footer blank (real focus wasn't known yet at
	// that point) -- fill it in now that it is.
	a.refreshFooterHints()
}

func (a *App) Focus(index int) {
	if a.childs == nil || index >= len(a.childs) {
		return
	}
	a.SetFocus(a.childs[index])
	a.lastFocusedIndex = index
	a.refreshFooterHints()
}

// RefreshFooterHints re-renders the footer against whichever primitive
// currently has real focus -- the same recompute Focus already triggers
// on every Tab/Shift+Tab, exposed here for a board whose FooterHints
// output can also change without any focus change at all (e.g. cli/
// bunker's HistoryTable, whose hint depends on which row is currently
// selected within the still-focused table, not just that the table
// itself holds focus).
func (a *App) RefreshFooterHints() {
	a.refreshFooterHints()
}

// refreshFooterHints re-renders the footer against whichever primitive
// currently has focus -- a no-op unless Load's board implements
// FooterHintsProvider. Called from Focus (so it tracks every Tab/Shift+Tab
// and every dialog/overlay dismissal, which also route through Focus) --
// see FooterHintsProvider's own doc comment (layout.go) for why this
// exists instead of a fixed hint string chosen once.
func (a *App) refreshFooterHints() {
	if a.footer == nil || a.footerProvider == nil {
		return
	}
	a.footer.SetHints(a.footerProvider.FooterHints(a.GetFocus()))
}

// ShowOverlay displays prim as a full custom page over the board (e.g. a
// Form with a text input, which tview.Modal can't express) -- the general
// form behind ShowEvent's own "eventDetail" page, exposed directly for a
// caller (e.g. cli/bunker) that needs a bespoke overlay rather than a
// fixed N-button dialog. key names the page so a matching DismissOverlay
// call can remove exactly it. focus is whatever within prim should hold
// keyboard focus (e.g. the Form, not the outer Flex wrapping it).
func (a *App) ShowOverlay(key string, prim tview.Primitive, focus tview.Primitive) {
	a.pages.AddPage(key, prim, true, true)
	a.SetFocus(focus)
}

// ShowPositionedOverlay is like ShowOverlay, but prim keeps whatever rect
// the caller already gave it (resize=false) instead of being resized to
// the full screen on every redraw. Use this for a smaller dialog that
// should behave like a real modal -- the board still visible, still
// live, around it -- rather than a full-screen page. tview.Pages draws
// every visible page in order regardless of size, so a small,
// directly-positioned prim drawn after "main" simply overwrites its own
// handful of cells on top of main's own (already correct, already
// redrawing-live) content -- there's no margin/spacer region to keep
// correctly filled the way ShowOverlay's full-screen callers need (see
// cli/bunker's overlaySpacer, which exists specifically because that
// margin-filling approach was found not to hold up under a board's real
// concurrent redraw traffic). Callers compute and apply prim's own rect
// themselves first (see cli/bunker's positionedOverlayRect), since this
// package has no fixed opinion on a dialog's size/shape.
func (a *App) ShowPositionedOverlay(key string, prim tview.Primitive, focus tview.Primitive) {
	a.pages.AddPage(key, prim, false, true)
	a.SetFocus(focus)
}

// DismissOverlay removes key's page (see ShowOverlay) and returns focus to
// the main board -- unless some other page (e.g. a ShowDialog modal that
// opened on top of this overlay while it was still up, such as cli/
// bunker's Auto-Prompt firing mid-pairing) is now the frontmost visible
// page. This can happen because ShowOverlay/DismissOverlay callers are
// often driven by an async completion (a network wait, a ticker poll) that
// has no way to know what else happened on screen in the meantime; forcing
// focus back to the board here regardless would leave that other page
// visible but keyboard-dead -- the operator's next keystroke would land on
// the board underneath instead of whatever dialog they can actually see.
func (a *App) DismissOverlay(key string) {
	a.pages.RemovePage(key)
	if name, _ := a.pages.GetFrontPage(); name != "main" {
		return
	}
	a.Focus(a.lastFocusedIndex)
}

// DismissDialog closes whatever's currently on the "dialog" page (see
// showDialog/ShowDialog) without invoking any button's func -- for a
// caller that needs to programmatically close a dialog it opened itself
// once its wait resolves (e.g. a blocking "in progress" Alert, once the
// operation it was waiting on completes), rather than requiring the human
// to click a button first just to get it out of the way.
func (a *App) DismissDialog() {
	a.pages.SwitchToPage("main")
	a.Focus(a.lastFocusedIndex)
}

func (a *App) showDialog(title, text string, textColor tcell.Color, buttons []string, funcs ...func()) {
	a.dialog = NewDialog(title, text, buttons, funcs...)
	a.dialog.SetTextColor(textColor).SetText(text).SetTitle(title)

	a.pages.RemovePage("dialog").AddPage("dialog", a.dialog, true, true)

	// Explicit, not left to Pages.AddPage's own hasFocus-preserving
	// behavior: that only re-delegates focus if a.pages itself already
	// has it at the exact moment AddPage runs, which held for every
	// pre-existing caller here (always a direct response to a keypress,
	// i.e. already mid-focus-dispatch) but not for a dialog opened from a
	// ticker/poll callback (cli/bunker's Auto-Prompt: PendingTable.
	// maybeAutoPrompt/tryPromptNext) -- there the Modal came up with no
	// button focused/highlighted and arrow keys/Enter did nothing, since
	// nothing had explicitly told the Application focus belonged on it.
	a.SetFocus(a.dialog)
}

// ShowDialog displays a custom modal with an arbitrary button set,
// invoking funcs[i] when button i is chosen (or funcs[len(funcs)-1] on
// Esc, per Dialog's own convention), then always returning focus to the
// main view -- the general form behind Debug/Alert/Error/ConfirmDelete
// above, exposed directly for a caller (e.g. cli/bunker's approval
// dialog) that needs custom button labels those fixed helpers don't
// provide.
func (a *App) ShowDialog(title, text string, textColor tcell.Color, buttons []string, funcs ...func()) {
	wrapped := make([]func(), len(funcs))
	for i, fn := range funcs {
		fn := fn
		wrapped[i] = func() {
			fn()
			a.pages.SwitchToPage("main")
			a.Focus(a.lastFocusedIndex)
		}
	}
	a.showDialog(title, text, textColor, buttons, wrapped...)
}

func (a *App) Debug(text string) {
	a.showDialog("Debug", text, tcell.ColorDefault, []string{"OK"},
		func() {
			a.pages.SwitchToPage("main")
			a.Focus(a.lastFocusedIndex)
		},
	)
}

func (a *App) Alert(text string) {
	a.showDialog("Info", text, tcell.ColorDefault, []string{"OK"},
		func() {
			a.pages.SwitchToPage("main")
			a.Focus(a.lastFocusedIndex)
		},
	)
}

func (a *App) Error(text string) {
	a.showDialog("Error", text, tcell.ColorRed, []string{"OK"},
		func() {
			a.pages.SwitchToPage("main")
			a.Focus(a.lastFocusedIndex)
		},
	)
}

func (a *App) ConfirmDelete(text string, confirmFunc func()) {
	a.showDialog("Delete?", text, tcell.ColorDefault, []string{"OK", "Cancel"},
		func() {
			confirmFunc()
			a.pages.SwitchToPage("main")
			a.Focus(a.lastFocusedIndex)
		},
		func() {
			a.pages.SwitchToPage("main")
			a.Focus(a.lastFocusedIndex)
		},
	)
}

func (a *App) Reload() {

	a.showDialog(
		"Update Configuration?",
		"Settings have changed. Reload now?",
		tcell.ColorDefault,
		[]string{"Reload", "Ignore"},
		func() {
			a.pages.SendToBack("main").SwitchToPage("reloading")
			a.reloadFunc()
		}, func() {
			a.pages.SwitchToPage("main")
		})

}

func (a *App) Restart() {

	a.showDialog(
		"Restart?",
		"Do you want to restart?",
		tcell.ColorDefault,
		[]string{"Restart", "Cancel"},
		func() {
			a.pages.SendToBack("main").SwitchToPage("reloading")
			a.reloadFunc()
		}, func() {
			a.pages.SwitchToPage("main")
		})

}

func SplashScreen() *tview.Flex {
	text := tview.NewTextView().
		SetDynamicColors(true).
		SetText(fmt.Sprintf("[purple:-:b]%s[silver:-:-] %s", alignedLogo(), WELCOME_MESSAGE)).
		SetTextAlign(tview.AlignCenter)

	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(text, 7, 1, false).
		AddItem(nil, 0, 1, false)

	centeredFlex := tview.NewFlex().
		SetDirection(tview.FlexColumn).
		AddItem(nil, 0, 1, false).
		AddItem(flex, 0, 1, true).
		AddItem(nil, 0, 1, false)

	return centeredFlex
}

func ReloadingScreen() *tview.Flex {

	text := tview.NewTextView().
		SetDynamicColors(true).
		SetText(fmt.Sprintf("[purple:-:b]%s[purple:-:-] %s", alignedLogo(), "loading...")).
		SetTextAlign(tview.AlignCenter)

	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(text, 7, 1, false).
		AddItem(nil, 0, 1, false)

	centeredFlex := tview.NewFlex().
		SetDirection(tview.FlexColumn).
		AddItem(nil, 0, 1, false).
		AddItem(flex, 0, 1, true).
		AddItem(nil, 0, 1, false)

	return centeredFlex

}
