package tui

import (
	"context"

	"github.com/rivo/tview"
)

type StreamBoard struct {
	*tview.Flex
	app *App

	logger *Logger
	down   *Table
	up     *Table
}

func NewStreamBoard(app *App, ctx context.Context, upData *FlowMetricsSlice, downData *FlowMetricsSlice, logger *FlowLogger) *StreamBoard {
	sb := &StreamBoard{
		Flex:   tview.NewFlex(),
		app:    app,
		logger: NewLogger(app, logger).Init(ctx),
		down:   NewTable(app, "Sources", downData).Init(ctx),
		up:     NewTable(app, "Destinations", upData).Init(ctx),
	}

	flows := tview.NewFlex().SetDirection(tview.FlexRow)
	flows.AddItem(sb.down, 0, 1, false)
	flows.AddItem(sb.up, 0, 1, false)

	sb.AddItem(sb.logger, 0, 3, true)
	sb.AddItem(flows, 0, 4, false)

	return sb
}

////

type InspectBoard struct {
	*tview.Flex
	targets *Table
	logger  *Logger
	events  *EventTable
}

const (
	// minTargetsHeight/maxTargetsHeight bound Targets' fixed height (see
	// NewInspectBoard): sized to its actual target count (+4 for border,
	// header, and a little breathing room) rather than an arbitrary share
	// of the screen, since it only ever has a handful of short, numeric
	// rows -- Table scrolls internally if there are ever more targets than
	// maxTargetsHeight allows for.
	minTargetsHeight = 8
	maxTargetsHeight = 14
)

// NewInspectBoard takes events already constructed (not yet Init'd) by the
// caller, the same way stream/down/up data models are owned outside their
// boards -- Inspector needs the identical *EventTable wired into
// StreamChannel's retain hook before its target goroutines start, so it
// can't be created after the fact from inside this constructor.
func NewInspectBoard(app *App, ctx context.Context, stream *FlowMetricsSlice, logger *FlowLogger, events *EventTable) *InspectBoard {
	b := &InspectBoard{
		targets: NewTable(app, "Targets", stream).Init(ctx),
		logger:  NewLogger(app, logger).Init(ctx),
		events:  events.Init(ctx),
	}

	// Stacked full-width, not side-by-side: the old 50/50 split gave
	// Targets (a handful of rows, short numeric columns) as much width as
	// Events -- which actually needs it, since Content is the one column
	// worth reading and a half-width terminal truncates it hard. Targets
	// gets a height sized to its own row count instead of half the width;
	// Events gets the rest, full width; the ambient log stays a thin strip.
	// This is also what keeps a narrow/small terminal readable, since
	// splitting width (not height) is what hurts most there.
	//
	// Events is last (bottom), not the middle: it's the panel actually read
	// and interacted with, so it belongs where the eye and hands naturally
	// rest -- the same bottom-anchored convention as a shell prompt, `tail
	// -f`, or a chat client's message view. Targets/the log are glance-at
	// status, so they stack above it instead -- the log matches Targets'
	// height exactly rather than getting its own separate constant.
	targetsHeight := min(max(len(*stream)+4, minTargetsHeight), maxTargetsHeight)

	b.Flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(b.targets, targetsHeight, 0, false).
		AddItem(b.logger, targetsHeight, 0, false).
		AddItem(b.events, 0, 1, true)

	return b
}

////

type ConnectivityBoard struct {
	*tview.Flex
	logger *Logger
}

func NewConnectivityBoard(app *App, ctx context.Context) (*ConnectivityBoard, *FlowLogger) {
	logger := &FlowLogger{}
	b := &ConnectivityBoard{
		logger: NewLogger(app, logger).Init(ctx),
	}

	b.Flex = tview.NewFlex().
		AddItem(b.logger, 0, 3, true)

	return b, logger
}

////

type SyncBoard struct {
	*tview.Flex
	logger *Logger
}

func NewSyncBoard(app *App, ctx context.Context, logger *FlowLogger) *SyncBoard {
	b := &SyncBoard{
		logger: NewLogger(app, logger).Init(ctx),
	}

	b.Flex = tview.NewFlex().
		AddItem(b.logger, 0, 1, true)

	return b
}

////

type Body struct {
	*tview.Flex
	board tview.Primitive
}

func NewBody(board tview.Primitive) *Body {
	b := &Body{
		Flex:  tview.NewFlex(),
		board: board,
	}

	b.AddItem(board, 0, 1, true)

	return b
}

func (b *Body) Childs() []tview.Primitive {
	var childs []tview.Primitive
	switch c := b.board.(type) {
	case *InspectBoard:
		childs = append(childs, c.targets)
		childs = append(childs, c.logger.logs)
		childs = append(childs, c.events)

	case *StreamBoard:
		childs = append(childs, c.logger.logs)
		childs = append(childs, c.down)
		childs = append(childs, c.up)

	case *SyncBoard:
		childs = append(childs, c.logger.logs)
	}

	return childs
}
