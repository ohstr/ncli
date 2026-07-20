package tui

import "github.com/rivo/tview"

type Layout struct {
	*tview.Flex
	header *Header
	body   *Body
	footer *Footer
}

func NewLayout() *Layout {
	return &Layout{
		Flex: tview.NewFlex(),
	}
}

func (l *Layout) Init(app *App, board tview.Primitive) *Layout {
	l.header = NewHeader()
	l.body = NewBody(board)
	l.footer = NewFooter(len(l.body.Childs()) > 1)

	l.SetDirection(tview.FlexRow).
		AddItem(l.header, 4, 0, false).
		AddItem(l.body, 0, 1, true).
		AddItem(l.footer, 2, 0, false)

	return l
}
