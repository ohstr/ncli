package client

import (
	"context"
	"fmt"
	"testing"

	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
)

// newRetainTestFixture builds a StreamChannel with zero destinations
// registered and one source FlowContext -- the exact shape Inspector
// produces (it only ever registers source targets, never destinations), so
// handleEvent's no-subscriber branch is what's under test here.
func newRetainTestFixture() (sc *StreamChannel, srcFC *FlowContext) {
	sc = NewStreamChannel(0, nil)
	srcStat := tui.NewInboundMetrics(1, "src", func() {})
	srcFC = NewFlowContext(nip01.NewSubscriptionFilterGroup(), srcStat, true, nil)
	return sc, srcFC
}

func newRetainTestEvent(i int) *nip01.Event {
	return &nip01.Event{
		ID:        fmt.Sprintf("%064x", i),
		PubKey:    fmt.Sprintf("%064x", 1),
		CreatedAt: 1,
		Kind:      1,
		Tags:      [][]string{},
		Content:   "retain test event",
	}
}

// TestHandleEventAggregatesWhenRetainIsNil is a regression guard: Stream and
// Sync never set StreamChannel.retain, so handleEvent's no-subscriber branch
// must behave exactly as it did before that field existed -- logging via the
// aggregating FlowLogger.LogEvent, not routing anywhere else.
func TestHandleEventAggregatesWhenRetainIsNil(t *testing.T) {
	sc, srcFC := newRetainTestFixture()
	ctx := context.Background()

	sc.handleEvent(ctx, srcFC, newRetainTestEvent(1))
	sc.handleEvent(ctx, srcFC, newRetainTestEvent(2))

	logs := sc.logger.GetLastLogs()
	if len(logs) != 1 {
		t.Fatalf("expected the two events to coalesce into a single aggregated log line, got %d lines: %v", len(logs), logs)
	}
}

// TestHandleEventUsesRetainInsteadOfAggregatingWhenSet confirms Inspector's
// hook: when retain is set, handleEvent must hand it the full event and must
// not also feed the aggregating logger.
func TestHandleEventUsesRetainInsteadOfAggregatingWhenSet(t *testing.T) {
	sc, srcFC := newRetainTestFixture()
	ctx := context.Background()

	var received []*nip01.Event
	sc.retain = func(event *nip01.Event, attr tui.FlowAttr) {
		received = append(received, event)
	}

	ev1 := newRetainTestEvent(1)
	ev2 := newRetainTestEvent(2)
	sc.handleEvent(ctx, srcFC, ev1)
	sc.handleEvent(ctx, srcFC, ev2)

	if len(received) != 2 || received[0].ID != ev1.ID || received[1].ID != ev2.ID {
		t.Fatalf("expected retain to receive both events in order, got %+v", received)
	}

	if logs := sc.logger.GetLastLogs(); len(logs) != 0 {
		t.Fatalf("expected no lines in the aggregating logger when retain is set, got %v", logs)
	}
}
