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
