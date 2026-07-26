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

// FooterHintsProvider is implemented by a board built outside this
// package whose keybindings don't match apply's own fixed footer hint set
// (Tab/Shift+Tab/Wrap/AutoScroll) -- the same downstream-extension pattern
// as ChildProvider (body.go), applied to the footer instead of focus
// cycling. A board without this shows the default hints unchanged.
//
// focused is whatever App.GetFocus() currently returns -- FooterHints is
// re-invoked by App.refreshFooterHints on every focus change (Tab/
// Shift+Tab, or returning from a dialog/overlay), not just once at
// Layout.Init, so a board with several differently-controlled panels
// (e.g. cli/bunker's BunkerBoard) can show only the keys that actually do
// something in whichever one currently has focus instead of one fixed
// hint set covering all of them at once, most of which don't apply to
// whatever's actually focused right now. A board that ignores focused
// (e.g. always returns the same string) gets the old fixed-hint-set
// behavior for free.
type FooterHintsProvider interface {
	FooterHints(focused tview.Primitive) string // tview color-tag markup, e.g. "[blue:-:b]<Enter> [gray:-:-]Decide"
}

func (l *Layout) Init(app *App, board tview.Primitive) *Layout {
	l.header = NewHeader()
	l.body = NewBody(board)

	// Seeded blank/default here, not with a real board.(FooterHintsProvider)
	// call yet -- real focus within body isn't resolved until
	// App.Load's own loop runs (AddAndSwitchToPage below hasn't even
	// happened yet), so calling FooterHints now could only ever pass a
	// meaningless nil/wrong focused value. App.Load calls
	// refreshFooterHints itself immediately after resolving real focus,
	// before anything is ever drawn.
	if _, ok := board.(FooterHintsProvider); ok {
		l.footer = NewFooterWithHints("")
	} else {
		l.footer = NewFooter(len(l.body.Childs()) > 1)
	}

	l.SetDirection(tview.FlexRow).
		AddItem(l.header, 4, 0, false).
		AddItem(l.body, 0, 1, true).
		AddItem(l.footer, 2, 0, false)

	return l
}
