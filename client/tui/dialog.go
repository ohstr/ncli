package tui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type Dialog struct {
	*tview.Modal
}

func NewDialog(title, text string, buttons []string, funcs ...func()) *Dialog {

	d := &Dialog{}

	modal := tview.NewModal()
	modal.SetTitle(title)
	modal.SetText(text)
	modal.SetBackgroundColor(tcell.ColorDefault)
	modal.Box.SetBackgroundColor(tcell.ColorDefault)
	// NewModal's own constructor pairs SetButtonBackgroundColor with
	// SetButtonTextColor, so its focused-button style is already a clean
	// inversion of whatever those resolve to -- but left at tview's own
	// Styles defaults, that's black-on-white, not this app's own
	// purple-on-white "this is selected" convention every table's own
	// SetSelectedStyle already uses (table.go/eventtable.go/board.go) --
	// overridden explicitly here so a dialog's focused button matches
	// every other focus/selection indicator in the app, not tview's stock
	// look.
	modal.SetButtonActivatedStyle(tcell.Style{}.Background(tcell.ColorPurple).Foreground(tcell.ColorWhite))

	modal.AddButtons(buttons).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			if buttonIndex >= 0 && buttonIndex < len(funcs) {
				funcs[buttonIndex]()
			} else if len(funcs) >= 0 { // Esc key
				funcs[len(funcs)-1]()
			}
		})

	d.Modal = modal
	return d
}
