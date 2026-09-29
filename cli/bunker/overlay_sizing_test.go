package bunker

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/ohstr/ncli/client/tui"
)

// TestPositionedOverlayRectCapped covers the sizing rule directly, since
// the interesting cases are terminal sizes a rendered test would have to
// enumerate one screen at a time.
func TestPositionedOverlayRectCapped(t *testing.T) {
	tests := []struct {
		name                   string
		screenW, screenH       int
		widthPercent, maxWidth int
		heightRows             int
		wantW, wantH           int
	}{
		{
			// The case from the bug report: 90% of 250 is 225 columns of
			// dialog around a single input line.
			name:    "a wide terminal is capped, not filled",
			screenW: 250, screenH: 60,
			widthPercent: 90, maxWidth: 100, heightRows: 5,
			wantW: 100, wantH: 5,
		},
		{
			name:    "an ordinary terminal follows the percentage",
			screenW: 80, screenH: 25,
			widthPercent: 90, maxWidth: 100, heightRows: 5,
			wantW: 72, wantH: 5,
		},
		{
			name:    "a narrow terminal keeps a usable minimum",
			screenW: 50, screenH: 20,
			widthPercent: 50, maxWidth: 100, heightRows: 5,
			wantW: overlayMinWidth, wantH: 5,
		},
		{
			name:    "never wider than the screen itself",
			screenW: 30, screenH: 20,
			widthPercent: 90, maxWidth: 100, heightRows: 5,
			wantW: 30, wantH: 5,
		},
		{
			name:    "never taller than the screen itself",
			screenW: 80, screenH: 4,
			widthPercent: 90, maxWidth: 100, heightRows: 5,
			wantW: 72, wantH: 4,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			x, y, w, h := cappedOverlayRect(tc.screenW, tc.screenH, tc.widthPercent, tc.maxWidth, tc.heightRows)

			if w != tc.wantW {
				t.Errorf("width = %d, want %d", w, tc.wantW)
			}
			if h != tc.wantH {
				t.Errorf("height = %d, want %d", h, tc.wantH)
			}
			if x < 0 || y < 0 {
				t.Errorf("origin = (%d,%d), want it on screen", x, y)
			}
			if x+w > tc.screenW || y+h > tc.screenH {
				t.Errorf("rect %dx%d at (%d,%d) runs off a %dx%d screen", w, h, x, y, tc.screenW, tc.screenH)
			}
			// Centered, within the rounding an odd-width screen forces.
			if gap := tc.screenW - w - 2*x; gap < 0 || gap > 1 {
				t.Errorf("horizontal margins are uneven by %d, want the box centered", gap)
			}
		})
	}
}

// The paste dialog used to be 100% of the screen's width and 20% of its
// height: a full-width box around one input line, with most of its height
// empty below the buttons. Sizing it to its content only helps if the
// content still fits, so this asserts against what actually renders --
// both that the box no longer spans the terminal and that nothing the
// operator needs got clipped off the bottom.
func TestNostrconnectInputOverlay_IsSizedToItsContent(t *testing.T) {
	client := &fullBoardTestClient{status: StatusInfo{IdentityPub: testNpubPub}}

	app := tui.NewApp().Init()
	flowLogger := &tui.FlowLogger{}
	screen := tcell.NewSimulationScreen("")
	app.SetScreen(screen)

	board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)
	app.Load(board)

	go func() { _ = app.Run() }()
	defer app.Stop()

	// Widened only once the Application is running: Run calls the screen's
	// own Init, which resets a SimulationScreen to its default 80x25 and
	// would silently undo a size set before this point -- leaving a test
	// that reads as "on a wide terminal" while actually proving nothing,
	// since 100% of 80 columns is already under the cap.
	const screenW, screenH = 250, 60
	waitForCond(t, func() bool {
		w, h := screen.Size()
		if w != screenW || h != screenH {
			screen.SetSize(screenW, screenH)
			return false
		}
		return true
	}, "the simulation screen never resized")

	app.QueueUpdateDraw(func() { board.pending.openNostrconnectInput() })

	// Both buttons have to be on screen, not clipped off the bottom by a
	// height that no longer fits tview.Form's own layout.
	waitForScreen(t, app, screen, "Connect")
	waitForScreen(t, app, screen, "Cancel")
	waitForScreen(t, app, screen, "nostrconnect:// URI:")

	// The input field is painted with the primary colour as its
	// background -- the purple bar that used to run the full width of the
	// terminal. Measuring that run is both the most direct read of the
	// reported problem and immune to guessing which runes tview drew the
	// border with.
	field := widestRunWithBackground(t, app, screen, tui.ColorPrimary)
	if field == 0 {
		t.Fatal("no input field rendered")
	}
	if field > 100 {
		t.Errorf("input field spans %d columns on a %d-column screen, want the dialog capped at 100", field, screenW)
	}
	if field < 20 {
		t.Errorf("input field is only %d columns wide, too narrow to paste into", field)
	}
}

// waitForCond polls cond until it holds or the deadline passes. Distinct
// from queue_test.go's own waitFor, which is shorter-deadlined and has no
// message of its own -- a TUI draw loop needs both.
func waitForCond(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// waitForScreen polls until substr renders, the same technique the other
// overlay tests in this file use for tview's asynchronous draw loop.
func waitForScreen(t *testing.T, app *tui.App, screen tcell.SimulationScreen, substr string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		var found bool
		app.QueueUpdate(func() { found = screenContains(screen, substr) })
		if found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%q never rendered -- the dialog may be clipped by nostrconnectInputRows", substr)
}

// widestRunWithBackground returns the longest horizontal run of cells
// painted with bg. Reads what was actually rendered rather than a rect the
// production code hands back, so a sizing regression cannot hide behind an
// accessor.
func widestRunWithBackground(t *testing.T, app *tui.App, screen tcell.SimulationScreen, bg tcell.Color) int {
	t.Helper()

	var widest int
	app.QueueUpdate(func() {
		cells, w, h := screen.GetContents()
		for y := range h {
			run := 0
			for x := range w {
				idx := y*w + x
				if idx < 0 || idx >= len(cells) {
					continue
				}
				_, cellBG, _ := cells[idx].Style.Decompose()
				if cellBG == bg {
					run++
					widest = max(widest, run)
					continue
				}
				run = 0
			}
		}
	})
	return widest
}
