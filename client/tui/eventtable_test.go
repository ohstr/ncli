package tui

import (
	"fmt"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/ohstr/nmilat/nip01"
)

func newEventTableTestEvent(i int) *nip01.Event {
	return &nip01.Event{
		ID:        fmt.Sprintf("%064x", i),
		PubKey:    fmt.Sprintf("%064x", 1),
		CreatedAt: 1,
		Kind:      1,
		Tags:      [][]string{},
		Content:   "event table test event",
	}
}

func TestEventTablePushCapsAtMaxLogEntries(t *testing.T) {
	et := NewEventTable(nil, func(*nip01.Event) error { return nil })

	const extra = 100
	for i := range MaxLogEntries + extra {
		et.Push(newEventTableTestEvent(i), FlowAttr{})
	}

	if got := len(et.rows); got != MaxLogEntries {
		t.Fatalf("expected rows capped at %d, got %d", MaxLogEntries, got)
	}

	// The oldest `extra` events should have been dropped, so the first
	// retained row is event #extra, not #0.
	if got := et.rows[0].event.ID; got != newEventTableTestEvent(extra).ID {
		t.Fatalf("expected oldest surviving row to be event %d, got id %s", extra, got)
	}
}

func TestEventTableSaveSelectedReturnsFalseWhenEmpty(t *testing.T) {
	et := NewEventTable(nil, func(*nip01.Event) error { return nil })

	if et.SaveSelected() {
		t.Fatal("expected SaveSelected to return false on a table with no rows")
	}
}

func TestEventTableSaveSelectedSavesTheSelectedRow(t *testing.T) {
	app := NewApp()

	var saved *nip01.Event
	et := NewEventTable(app, func(e *nip01.Event) error {
		saved = e
		return nil
	})

	ev1 := newEventTableTestEvent(1)
	ev2 := newEventTableTestEvent(2)
	et.Push(ev1, FlowAttr{})
	et.Push(ev2, FlowAttr{})

	// Update() populates the rendered snapshot that row selection indexes
	// into -- normally driven by render()'s ticker, called directly here so
	// the test doesn't need a live context/goroutine.
	et.Update()

	// Row 0 is the header; row 2 is the second pushed event.
	et.Select(2, 0)

	if !et.SaveSelected() {
		t.Fatal("expected SaveSelected to return true with a row selected")
	}
	if saved == nil || saved.ID != ev2.ID {
		t.Fatalf("expected event %s to be saved, got %+v", ev2.ID, saved)
	}
}

func TestEventTableSaveSelectedFailurePropagatesFromOnSave(t *testing.T) {
	app := NewApp()

	et := NewEventTable(app, func(*nip01.Event) error {
		return fmt.Errorf("boom")
	})

	et.Push(newEventTableTestEvent(1), FlowAttr{})
	et.Update()
	et.Select(1, 0)

	if et.SaveSelected() {
		t.Fatal("expected SaveSelected to return false when onSave errors")
	}
}

func TestPreviewContent(t *testing.T) {
	long := "this line is definitely going to be longer than the sixty character cap"

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"short content untouched", "hello", "hello"},
		{"long content truncated", long, long[:60] + "…"},
		{"newlines collapsed to spaces", "line one\nline two", "line one line two"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := previewContent(tt.content); got != tt.want {
				t.Fatalf("previewContent(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

// TestEventTableUpdateRespectsAutoscrollToggle is a light regression guard
// for the 's' key's effect: with autoscroll on, Update() should push the
// viewport offset to the bottom (SetOffset) rather than leaving it wherever
// the user last scrolled to.
func TestEventTableUpdateRespectsAutoscrollToggle(t *testing.T) {
	et := NewEventTable(nil, func(*nip01.Event) error { return nil })

	for i := range 50 {
		et.Push(newEventTableTestEvent(i), FlowAttr{})
	}

	et.autoscroll = false
	et.SetOffset(3, 0)
	et.Update()
	if row, _ := et.GetOffset(); row != 3 {
		t.Fatalf("expected autoscroll off to leave the manual offset alone, got row offset %d", row)
	}

	et.autoscroll = true
	et.Update()
	if row, _ := et.GetOffset(); row <= 3 {
		t.Fatalf("expected autoscroll on to move the offset toward the bottom, got row offset %d", row)
	}
}

// TestEventTableInputCaptureIsScopedToThisWidget guards against a regression
// back to the old design, where 'w'/'s' were registered globally on the App
// (firing for every keypress regardless of focus, so toggling one panel's
// autoscroll also toggled every other panel's). Init must install the
// capture on the table itself (Box.SetInputCapture), not on app.
func TestEventTableInputCaptureIsScopedToThisWidget(t *testing.T) {
	app := NewApp()
	et := NewEventTable(app, func(*nip01.Event) error { return nil })
	et.Init(t.Context())

	if et.GetInputCapture() == nil {
		t.Fatal("expected Init to install an input capture on the table itself")
	}
	if app.GetInputCapture() != nil {
		t.Fatal("expected Init not to install a global App-level input capture")
	}
}

func TestEventTableSKeyTogglesAutoscroll(t *testing.T) {
	app := NewApp()
	et := NewEventTable(app, func(*nip01.Event) error { return nil })
	et.Init(t.Context())

	if !et.autoscroll {
		t.Fatal("expected autoscroll to default to true")
	}

	capture := et.GetInputCapture()
	sKey := tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone)

	if got := capture(sKey); got != nil {
		t.Fatal("expected 's' to be swallowed (capture returns nil)")
	}
	if et.autoscroll {
		t.Fatal("expected 's' to toggle autoscroll off")
	}

	if got := capture(sKey); got != nil {
		t.Fatal("expected 's' to be swallowed (capture returns nil)")
	}
	if !et.autoscroll {
		t.Fatal("expected a second 's' to toggle autoscroll back on")
	}
}

// TestEventTableManualNavigationBreaksAutoscroll guards the UX fix: without
// this, autoscroll would yank the selection/viewport back to the newest row
// on the next tick, right out from under whatever row the user just
// manually navigated to.
func TestEventTableManualNavigationBreaksAutoscroll(t *testing.T) {
	app := NewApp()
	et := NewEventTable(app, func(*nip01.Event) error { return nil })
	et.Init(t.Context())

	if !et.autoscroll {
		t.Fatal("expected autoscroll to default to true")
	}

	capture := et.GetInputCapture()
	downKey := tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)

	if got := capture(downKey); got != downKey {
		t.Fatal("expected a navigation key to fall through unmodified so Table's own selection movement still happens")
	}
	if et.autoscroll {
		t.Fatal("expected manual navigation to turn autoscroll off")
	}
}
