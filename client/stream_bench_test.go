package client

import (
	"context"
	"fmt"
	"testing"

	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
)

// BenchmarkStreamEventHistoryAddAtCapacity measures add() once the LRU is at
// its steady-state capacity (maxEventHistorySize), i.e. every call evicts
// the oldest entry -- the O(1) container/list-based eviction should show
// flat ns/op and allocs/op regardless of history size, unlike the old
// slice-based order with its O(n) removeFromOrder scan.
func BenchmarkStreamEventHistoryAddAtCapacity(b *testing.B) {
	seh := newStreamEventHistory()
	for i := 0; i < maxEventHistorySize; i++ {
		seh.add(newRegressionTestEvent(i))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seh.add(newRegressionTestEvent(maxEventHistorySize + i))
	}
}

// BenchmarkStreamEventHistoryReAddExisting measures re-adding an
// already-present entry (the MoveToFront path) with the history at full
// capacity -- this used to be the O(n) removeFromOrder scan in the old
// slice-based implementation.
func BenchmarkStreamEventHistoryReAddExisting(b *testing.B) {
	seh := newStreamEventHistory()
	events := make([]*nip01.Event, maxEventHistorySize)
	for i := range events {
		events[i] = newRegressionTestEvent(i)
		seh.add(events[i])
	}
	target := events[0]

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seh.add(target)
	}
}

// BenchmarkStreamEventHistoryGet measures lookups against a full history.
func BenchmarkStreamEventHistoryGet(b *testing.B) {
	seh := newStreamEventHistory()
	ids := make([]string, maxEventHistorySize)
	for i := range ids {
		ev := newRegressionTestEvent(i)
		ids[i] = ev.ID
		seh.add(ev)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seh.get(ids[i%len(ids)])
	}
}

// BenchmarkBroadcastEventsFanout compares delivery cost with 1 destination
// (no aliasing risk, so no event.Copy()) against multiple destinations
// (which each need their own copy) -- allocs/op should step up noticeably
// between the 1-subscriber and multi-subscriber cases.
func BenchmarkBroadcastEventsFanout(b *testing.B) {
	for _, n := range []int{1, 2, 5} {
		b.Run(fmt.Sprintf("%d_subscriber(s)", n), func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sc := NewStreamChannel(1, nil)
			go sc.broadcastEvents(ctx)

			for i := 0; i < n; i++ {
				stat := tui.NewOutboundMetrics(i+1, fmt.Sprintf("d-%d", i+1), func() {})
				fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, true, nil)
				sc.addSubscriber(fc)
				go sc.handleFlow(ctx, fc)
				go drainAndAck(ctx, fc)
			}

			srcStat := tui.NewInboundMetrics(1, "src", func() {})
			srcFC := NewFlowContext(nip01.NewSubscriptionFilterGroup(), srcStat, true, nil)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sc.handleEvent(ctx, srcFC, newRegressionTestEvent(i))
			}
		})
	}
}
