package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/ohstr/nmilat/nip01"
	"github.com/rivo/tview"
)

// eventRow is one received event, kept in full (not just its ID) so
// selecting a row can show/save it without a store round-trip. See
// EventTable's rows field for the memory-bound rationale.
type eventRow struct {
	event     *nip01.Event
	createdAt time.Time
	attr      FlowAttr
}

// EventTable is Inspect's selectable, per-event pane -- the non-aggregated
// counterpart to Logger's coalesced "EVENTS n" lines. It mirrors Table's
// conventions (purple border, SetSelectable, ticker-driven QueueUpdateDraw
// render loop decoupled from ingestion) but keeps its own lightweight row
// model instead of the metrics-shaped FlowStat interface, which doesn't fit
// a single event.
type EventTable struct {
	*tview.Table
	app *App

	// onSave persists an event (append-to-session-file); owned by the
	// client package (which has the EventSessionWriter), injected here as a
	// plain func so this package doesn't need to import client.
	onSave func(*nip01.Event) error

	mu         sync.Mutex
	rows       []eventRow // live, appended by Push from event-delivery goroutines
	rendered   []eventRow // snapshot Update() last drew -- what row selection actually indexes into
	indexWidth int

	// autoscroll mirrors Logger's own toggle, applied here too since this is
	// the pane individual events actually live in: it keeps the view
	// following the newest row as more arrive. Shares the 'w'/'s' keys with
	// Logger's own toggle bar (both are global, not focus-scoped), so
	// pressing 's' follows both at once. There's no Wrap counterpart here
	// -- tview.Table cells are single-line regardless, so "full content"
	// just widens the column rather than wrapping, which isn't useful
	// enough to keep as a toggle; the Content column is always truncated.
	autoscroll bool
}

func NewEventTable(app *App, onSave func(*nip01.Event) error) *EventTable {
	t := &EventTable{
		Table:      tview.NewTable(),
		app:        app,
		onSave:     onSave,
		indexWidth: 1,
		autoscroll: true,
	}
	return t
}

func (t *EventTable) Init(ctx context.Context) *EventTable {
	t.SetFixed(1, 0).
		SetSelectable(true, false).
		SetBorder(true).
		SetBorderColor(tcell.ColorPurple).
		SetBorderPadding(0, 1, 1, 1)

	t.SetSelectedStyle(tcell.Style{}.
		Background(tcell.ColorPurple).
		Foreground(tcell.ColorWhite),
	)

	t.SetSelectedFunc(func(row, _ int) {
		if event := t.eventAt(row); event != nil {
			t.app.ShowEvent(event, func() {
				t.persist(event)
			})
		}
	})

	t.updateTitle(0)
	t.drawHeader()

	// Scoped to this table (via Box.SetInputCapture, not App.SetInputCapture)
	// so 's' only follows the tail here when this table is actually focused
	// -- the same per-widget mechanism Table.handleKeys already uses for
	// Console's 'd' key, rather than a global key every panel responds to
	// at once.
	t.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyHome, tcell.KeyEnd:
			// Manual navigation breaks autoscroll -- otherwise the next
			// tick would yank the selection back to the newest row right
			// out from under whatever the user just moved to. Let the
			// event fall through so Table's own selection movement still
			// happens.
			if t.autoscroll {
				t.autoscroll = false
				t.updateTitle(len(t.rendered))
			}

		case tcell.KeyRune:
			if event.Rune() == 's' {
				t.autoscroll = !t.autoscroll
				if t.autoscroll {
					t.followTail() // jump immediately, don't wait for the next tick
				}
				t.updateTitle(len(t.rendered))
				return nil
			}
		}
		return event
	})

	go t.render(ctx)

	return t
}

// SetIndexWidth sets the digit width used to pad each row's source flag, so
// it lines up with the Console table's flags -- see FlowMetrics.SetIndexWidth
// for why this is owned per-instance rather than as shared package state.
func (t *EventTable) SetIndexWidth(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.indexWidth = n
}

// Push records a newly received event. Safe to call concurrently from
// multiple sources' goroutines. Bounded at MaxLogEntries, oldest dropped --
// the same cap FlowLogger already applies to its own log slice; this is a
// bounded *view*, not the durable record (that's InspectStore, in the
// client package).
func (t *EventTable) Push(event *nip01.Event, attr FlowAttr) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.rows = append(t.rows, eventRow{event: event, createdAt: time.Now(), attr: attr})
	if len(t.rows) > MaxLogEntries {
		t.rows = t.rows[len(t.rows)-MaxLogEntries:]
	}
}

// SaveSelected implements EventSaver: Ctrl+S while this table is focused
// saves the currently selected row directly, without opening the modal.
func (t *EventTable) SaveSelected() bool {
	row, _ := t.GetSelection()
	event := t.eventAt(row)
	if event == nil {
		return false
	}
	return t.persist(event)
}

func (t *EventTable) persist(event *nip01.Event) bool {
	if err := t.onSave(event); err != nil {
		t.app.Error(fmt.Sprintf("failed to save event: %s", err))
		return false
	}
	t.app.Alert(fmt.Sprintf("event %s saved", shortHex(event.ID)))
	return true
}

// eventAt resolves a tview row index (1-based, row 0 is the header) against
// the last-rendered snapshot -- not the live rows slice, which may have been
// appended/trimmed since the frame the user is actually looking at was
// drawn.
func (t *EventTable) eventAt(row int) *nip01.Event {
	t.mu.Lock()
	defer t.mu.Unlock()

	idx := row - 1
	if idx < 0 || idx >= len(t.rendered) {
		return nil
	}
	return t.rendered[idx].event
}

func (t *EventTable) updateTitle(count int) {
	autoColor, autoText := statusText(t.autoscroll)
	t.SetTitle(fmt.Sprintf(" [::b][%s]EVENTS [[%s]%d[%s]] [%s]Autoscroll:[%s]%s[-:-:-] ",
		tcell.ColorPurple, tcell.ColorWhiteSmoke, count, tcell.Color53,
		tcell.ColorPurple, autoColor, autoText))
}

var eventTableHeaders = []string{"Time", "Src", "Kind", "Pubkey", "Id", "Content"}

func (t *EventTable) drawHeader() {
	for c, name := range eventTableHeaders {
		t.SetCell(0, c, tview.NewTableCell(fmt.Sprintf("[-:-:b]%s", strings.ToUpper(name))).
			SetExpansion(1).
			SetTextColor(tcell.ColorBlack).
			SetAlign(tview.AlignLeft).
			SetSelectable(false))
	}
}

func (t *EventTable) Update() {
	t.mu.Lock()
	rows := make([]eventRow, len(t.rows))
	copy(rows, t.rows)
	indexWidth := t.indexWidth
	t.rendered = rows
	t.mu.Unlock()

	t.Clear()
	t.drawHeader()
	t.updateTitle(len(rows))

	for i, r := range rows {
		cells := []string{
			fmt.Sprintf("[gray:-:-]%s", formatTimestamp(r.createdAt)),
			renderFlag(r.attr.FlagColor, r.attr.Index, indexWidth),
			strconv.Itoa(r.event.Kind),
			shortHex(r.event.PubKey),
			shortHex(r.event.ID),
			previewContent(r.event.Content),
		}
		for c, val := range cells {
			t.SetCell(i+1, c, tview.NewTableCell(val).
				SetExpansion(1).
				SetAlign(tview.AlignLeft))
		}
	}

	if t.autoscroll {
		t.followTail()
	}
}

// followTail selects and scrolls to the newest row, keeping the highlighted
// selection and the visible viewport in sync -- without this, autoscroll
// would move the viewport to the bottom while the selection (what Enter/
// Ctrl+S actually act on) silently stayed wherever it was left, possibly
// scrolled out of view entirely. Any manual Up/Down/PageUp/PageDown/Home/End
// (see Init's input capture) turns autoscroll off first, so this only ever
// runs while the user isn't actively browsing an older row.
func (t *EventTable) followTail() {
	t.mu.Lock()
	n := len(t.rendered)
	t.mu.Unlock()

	if n == 0 {
		return
	}
	t.Select(n, 0)
	t.SetOffset(n, 0)
}

func (t *EventTable) render(ctx context.Context) {
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

// shortHex mirrors the ev.ID[:8]-style truncation already used elsewhere
// (e.g. neg_sync.go) for displaying a hex id/pubkey compactly.
func shortHex(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}

// previewContent collapses an event's content to a single, truncated
// display line so one long note can't blow out the table's row width.
func previewContent(content string) string {
	content = strings.ReplaceAll(content, "\n", " ")
	const maxLen = 60
	if len(content) <= maxLen {
		return content
	}
	return content[:maxLen] + "…"
}
