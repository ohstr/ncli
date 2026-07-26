package tui

import (
	"fmt"
	"time"

	"github.com/rivo/tview"
)

type Status struct {
	*tview.TextView
	text         string
	showPanelNav bool
}

func NewStatus(initText string, showPanelNav bool) *Status {
	s := &Status{
		TextView:     tview.NewTextView(),
		showPanelNav: showPanelNav,
	}
	s.SetDynamicColors(true)
	s.SetTextAlign(tview.AlignLeft)
	s.SetBorderPadding(0, 0, 1, 1)
	s.Update(initText)

	return s
}

func (s *Status) Update(text string) {
	s.Clear()
	if s.showPanelNav {
		fmt.Fprintf(s, "[%s:-:b]<Tab> [%s:-:-]Next Panel   \t", ColorAccent, ColorMuted)
		fmt.Fprintf(s, "[%s:-:b]<Shift+Tab> [%s:-:-]Prev Panel   \t", ColorAccent, ColorMuted)
	}
	fmt.Fprintf(s, "[%s:-:b]<w> [%s:-:-]Toggle Wrap   \t", ColorAccent, ColorMuted)
	fmt.Fprintf(s, "[%s:-:b]<s> [%s:-:-]Toggle AutoScroll", ColorAccent, ColorMuted)
}

type Footer struct {
	*tview.Flex
	status *Status
}

func (s *Status) Mock() {
	ss := []string{"Running", "Pause", "Started"}
	for i := 0; ; {
		s.Update(ss[i])
		<-time.After(time.Second * 3)
		if i == len(ss)-1 {
			i = 0
		} else {
			i++
		}
	}
}

func NewFooter(showPanelNav bool) *Footer {
	f := &Footer{
		Flex:   tview.NewFlex(),
		status: NewStatus("Running", showPanelNav),
	}

	f.AddItem(f.status, 0, 1, false)

	return f
}

// NewFooterWithHints builds a Footer showing exactly hints (tview
// color-tag markup) instead of Status.Update's hardcoded Tab/Shift+Tab/
// Wrap/AutoScroll set -- for a board (see FooterHintsProvider) whose own
// keybindings don't match apply's fixed hint bar.
func NewFooterWithHints(hints string) *Footer {
	status := &Status{TextView: tview.NewTextView()}
	status.SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft).
		SetBorderPadding(0, 0, 1, 1)
	fmt.Fprint(status, hints)

	f := &Footer{Flex: tview.NewFlex(), status: status}
	f.AddItem(f.status, 0, 1, false)
	return f
}

// SetHints replaces a NewFooterWithHints footer's text -- for a
// FooterHintsProvider board (see layout.go) whose hints change as focus
// moves between its own panels, refreshed by App.refreshFooterHints on
// every focus change rather than fixed once at Layout.Init.
func (f *Footer) SetHints(text string) {
	f.status.Clear()
	fmt.Fprint(f.status, text)
}
