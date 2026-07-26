package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type Logger struct {
	*tview.Flex
	app        *App
	logs       *tview.TextView
	actions    *tview.TextView
	logger     *FlowLogger
	wrap       bool
	autoscroll bool
}

func NewLogger(app *App, logger *FlowLogger) *Logger {
	t := &Logger{
		app:        app,
		Flex:       tview.NewFlex(),
		logs:       tview.NewTextView(),
		actions:    tview.NewTextView(),
		logger:     logger,
		autoscroll: true,
		wrap:       false,
	}

	return t
}

func (t *Logger) Init(ctx context.Context) *Logger {

	t.SetBorder(true).
		SetBorderPadding(0, 0, 1, 1)

	WireFocusBorder(t.logs, t.Flex.Box)

	t.actions.SetDynamicColors(true).
		SetWrap(false).
		SetWordWrap(false).
		SetTextAlign(tview.AlignCenter)

	t.logs.SetDynamicColors(true).
		SetWordWrap(false)

	t.Flex.SetDirection(tview.FlexRow).
		AddItem(t.actions, 1, 1, false).
		AddItem(t.logs, 0, 1, true)

	t.UpdateActions()

	// Scoped to this Logger (via Box.SetInputCapture, not App.SetInputCapture)
	// so 'w'/'s' only affect it while it's actually focused -- Flex.HasFocus/
	// InputHandler check their children recursively, so this still fires
	// correctly even though the focus target Body.Childs() registers is
	// t.logs (the inner TextView), not this outer Flex.
	t.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Rune() {
		case 'w':
			t.wrap = !t.wrap
			t.UpdateActions()
			return nil
		case 's':
			t.autoscroll = !t.autoscroll
			t.UpdateActions()
			return nil
		}
		return event
	})

	go t.render(ctx)

	return t
}

// FocusTarget returns the actual focusable primitive within this Logger --
// its inner log TextView, not the outer Flex wrapper (which has no visual
// "selected" state of its own and doesn't forward key input the same way,
// per the comment on Init's SetInputCapture above). This package's own
// boards (InspectBoard/StreamBoard/SyncBoard) already register logs
// directly for exactly this reason; exposed here for a board built outside
// this package (e.g. cli/bunker's BunkerBoard) that needs the same
// Tab-cycling correctness.
func (t *Logger) FocusTarget() tview.Primitive {
	return t.logs
}

func (t *Logger) UpdateActions() {
	t.logs.SetWrap(t.wrap)
	t.actions.Clear()

	autoColor, autoText := StatusText(t.autoscroll)
	wrapColor, wrapText := StatusText(t.wrap)
	fmt.Fprintf(t.actions, "[%s::b]Autoscroll:[%s]%s \t [%s]Wrap:[%s]%s", ColorPrimary, autoColor, autoText, ColorPrimary, wrapColor, wrapText)
}

func (t *Logger) Update() {

	// Periodically clear TextView to prevent unbounded memory growth
	t.clearOldLogs()

	logs := t.logger.GetLastLogs()

	for _, columns := range logs {
		fmt.Fprintln(t.logs, strings.Join(columns, " "))
	}

	if len(logs) > 0 && t.autoscroll {
		t.logs.ScrollToEnd()
	}

}

// clearOldLogs clears the TextView if it has accumulated too many lines
func (t *Logger) clearOldLogs() {
	// tview doesn't expose line count, so we estimate based on buffer length
	// Average line ~100 chars, so 5MB of text ~= 50k lines
	bufLen := len(t.logs.GetText(false))
	if bufLen > 5*1024*1024 { // 5MB threshold
		t.logs.Clear()
		// TextView is now empty, new logs will be appended in Update()
	}
}

func (t *Logger) render(ctx context.Context) {

	ticker := time.NewTicker(time.Second * 2)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.app.QueueUpdateDraw(func() {
				t.Update()
			})

		case <-ctx.Done():
			return
		}
	}
}

// StatusText renders an On/Off toggle's current state as a color/label
// pair, the shared convention every On/Off toggle in this package (and
// cli/bunker's own board, e.g. its Auto-Prompt toggle) renders itself
// with -- green "On", gray "Off".
func StatusText(status bool) (tcell.Color, string) {
	if status {
		return ColorSuccess, "On"
	}
	return ColorMuted, "Off"
}
