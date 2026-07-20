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
		fmt.Fprintf(s, "[blue:-:b]<Tab> [gray:-:-]Next Panel   \t")
		fmt.Fprintf(s, "[blue:-:b]<Shift+Tab> [gray:-:-]Prev Panel   \t")
	}
	fmt.Fprintf(s, "[blue:-:b]<w> [gray:-:-]Toggle Wrap   \t")
	fmt.Fprintf(s, "[blue:-:b]<s> [gray:-:-]Toggle AutoScroll")
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
