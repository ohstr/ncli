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
			a.Restart()
		}

	case tcell.KeyCtrlS:
		if a.activeSaver != nil {
			a.activeSaver.SaveSelected()
		} else if saver, ok := a.GetFocus().(EventSaver); ok {
			saver.SaveSelected()
		} else {
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
}

func (a *App) Focus(index int) {
	if a.childs == nil || index >= len(a.childs) {
		return
	}
	a.SetFocus(a.childs[index])
	a.lastFocusedIndex = index
}

func (a *App) showDialog(title, text string, textColor tcell.Color, buttons []string, funcs ...func()) {
	a.dialog = NewDialog(title, text, buttons, funcs...)
	a.dialog.SetTextColor(textColor).SetText(text).SetTitle(title)

	a.pages.RemovePage("dialog").AddPage("dialog", a.dialog, true, true)
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
