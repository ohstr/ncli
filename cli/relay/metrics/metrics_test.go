package metrics

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/relay"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestEnumSizesMatchNmilat(t *testing.T) {
	if n := len(relay.MessageTypes()); n != relayMessageTypes {
		t.Errorf("relay has %d message types, metrics sized for %d", n, relayMessageTypes)
	}
	if n := len(relay.EventResults()); n != relayEventResults {
		t.Errorf("relay has %d event results, metrics sized for %d", n, relayEventResults)
	}
	if n := len(relay.RejectReasons()); n != relayRejectReasons {
		t.Errorf("relay has %d reject reasons, metrics sized for %d", n, relayRejectReasons)
	}
	if n := len(relay.KindClasses()); n != relayKindClasses {
		t.Errorf("relay has %d kind classes, metrics sized for %d", n, relayKindClasses)
	}
}

// The relay calls these on every message; none may allocate.
func TestObserverDoesNotAllocate(t *testing.T) {
	m := New("test")
	allocs := testing.AllocsPerRun(1000, func() {
		m.SessionOpened()
		m.Message(relay.MsgEvent)
		m.EventHandled(relay.EventRejected, relay.ReasonBlocked, relay.KindNote, 3*time.Millisecond)
		m.EventHandled(relay.EventAccepted, relay.ReasonNone, relay.KindZap, time.Millisecond)
		m.ReqServed(5*time.Millisecond, 20)
		m.ReadRefused(relay.ReasonAuthRequired)
		m.CountServed(time.Millisecond)
		m.SubscriptionsChanged(1)
		m.SubscriptionsChanged(-1)
		m.Auth(false, relay.ReasonRestricted)
		m.Auth(true, relay.ReasonNone)
		m.Sent(100*time.Microsecond, false)
		m.BatchCommitted(10, 2*time.Millisecond, false)
		m.ScanBusy()
		m.SessionClosed()
	})
	if allocs != 0 {
		t.Fatalf("observer allocated %.1f times per run", allocs)
	}
}

func BenchmarkEventHandled(b *testing.B) {
	m := New("bench")
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			m.Message(relay.MsgEvent)
			m.EventHandled(relay.EventAccepted, relay.ReasonNone, relay.KindNote, time.Millisecond)
			m.Sent(50*time.Microsecond, false)
		}
	})
}

func TestObserverCountsByLabel(t *testing.T) {
	m := New("test")
	m.EventHandled(relay.EventRejected, relay.ReasonPow, relay.KindNote, time.Millisecond)
	m.EventHandled(relay.EventRejected, relay.ReasonPow, relay.KindReaction, time.Millisecond)
	m.EventHandled(relay.EventAccepted, relay.ReasonNone, relay.KindNote, time.Millisecond)
	m.Auth(false, relay.ReasonNone)
	m.SubscriptionsChanged(3)
	m.SubscriptionsChanged(-1)

	if got := testutil.ToFloat64(m.rejected[relay.ReasonPow]); got != 2 {
		t.Errorf("pow rejections = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.byClass[relay.KindNote]); got != 2 {
		t.Errorf("note events = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.authRejected[relay.ReasonOther]); got != 1 {
		t.Errorf("auth rejections with no reason = %v, want 1 counted as other", got)
	}
	if got := testutil.ToFloat64(m.subscriptions); got != 2 {
		t.Errorf("subscriptions = %v, want 2", got)
	}
}

func TestHistoryRatesAndQuantiles(t *testing.T) {
	m := New("test")
	h := NewHistory(m, time.Second, 10*time.Second)
	t0 := time.Unix(1000, 0)
	h.Sample(t0)

	for i := 0; i < 100; i++ {
		m.Message(relay.MsgEvent)
		m.EventHandled(relay.EventAccepted, relay.ReasonNone, relay.KindNote, 3*time.Millisecond)
	}
	h.Sample(t0.Add(2 * time.Second))

	p := h.Latest()
	if got := p.Values["messages_total:EVENT"]; got != 50 {
		t.Errorf("EVENT rate = %v/s, want 50", got)
	}
	if got := p.Totals["messages_total:EVENT"]; got != 100 {
		t.Errorf("EVENT total = %v, want 100", got)
	}
	p50 := p.Values["event_ok_seconds.p50"]
	if p50 <= .0025 || p50 > .005 {
		t.Errorf("EVENT OK p50 = %v, want within the 2.5-5ms bucket", p50)
	}
	if got := p.Values["event_ok_seconds.rate"]; got != 50 {
		t.Errorf("EVENT OK rate = %v/s, want 50", got)
	}
	if _, ok := p.Values["build_info"]; !ok {
		t.Error("build_info missing from snapshot")
	}
}

func TestHistoryRingKeepsWindowAndNullsGaps(t *testing.T) {
	m := New("test")
	h := NewHistory(m, time.Second, 3*time.Second)
	t0 := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		h.Sample(t0.Add(time.Duration(i) * time.Second))
	}
	cols := h.Columns()
	if len(cols.Times) != 3 || cols.Times[0] != t0.Add(2*time.Second).UnixMilli() {
		t.Fatalf("times = %v, want the last 3 samples", cols.Times)
	}
	rateCol := cols.Series["messages_total:EVENT"]
	if len(rateCol) != 3 {
		t.Fatalf("rate column has %d points, want 3", len(rateCol))
	}

	out, err := json.Marshal(cols)
	if err != nil {
		t.Fatal(err)
	}
	// p50 has no samples at all, so it never appears; a rate seen only
	// from the second point on starts with a null.
	if !strings.Contains(string(out), `"uptime_seconds":[`) {
		t.Errorf("gauge series missing from columns: %s", out)
	}
	if strings.Contains(string(out), "NaN") {
		t.Errorf("NaN leaked into JSON: %s", out)
	}
}

func TestHistoryRollupPrecedesRawSamples(t *testing.T) {
	m := New("test")
	// 3 raw samples kept; 10s rollups.
	h := NewHistory(m, time.Second, 3*time.Second).WithRollup(10*time.Second, time.Minute)
	t0 := time.Unix(1000, 0)
	for i := range 25 {
		if i < 10 {
			m.Message(relay.MsgEvent)
		}
		h.Sample(t0.Add(time.Duration(i) * time.Second))
	}
	cols := h.Columns()
	// Buckets 1000s and 1010s are closed; 1020s is still filling. Raw starts at 1022s.
	want := []int64{1000_000, 1010_000, 1022_000, 1023_000, 1024_000}
	if !slices.Equal(cols.Times, want) {
		t.Fatalf("times = %v, want %v", cols.Times, want)
	}
	if cols.Window != 60_000 {
		t.Errorf("window = %d, want the rollup's 60000", cols.Window)
	}
	ev := cols.Series["messages_total:EVENT"]
	if len(ev) != len(want) {
		t.Fatalf("EVENT column has %d points, want %d", len(ev), len(want))
	}
	// One EVENT a second for the first 10s: rates 1,1,...(9 of them, the first
	// sample has none), then 0 from 1010s.
	if ev[0] != 1 || ev[1] != 0 {
		t.Errorf("rolled-up EVENT rates = %v, %v; want 1, 0", ev[0], ev[1])
	}
}

func TestHistorySubscribeIsCapped(t *testing.T) {
	h := NewHistory(New("test"), time.Second, time.Minute)
	var cancels []func()
	for i := 0; i < maxSubscribers; i++ {
		_, cancel, ok := h.Subscribe()
		if !ok {
			t.Fatalf("subscriber %d refused", i)
		}
		cancels = append(cancels, cancel)
	}
	if _, _, ok := h.Subscribe(); ok {
		t.Fatal("subscriber past the cap accepted")
	}
	cancels[0]()
	ch, _, ok := h.Subscribe()
	if !ok {
		t.Fatal("subscriber refused after one left")
	}
	h.Sample(time.Now())
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("no point published")
	}
}

func TestQuantile(t *testing.T) {
	bounds := []float64{1, 2, 4}
	cum := []float64{10, 30, 40}
	if got := quantile(.5, bounds, cum, 40); got != 1.5 {
		t.Errorf("p50 = %v, want 1.5", got)
	}
	if got := quantile(.25, bounds, cum, 40); got != 1 {
		t.Errorf("p25 = %v, want 1", got)
	}
	if got := quantile(.99, []float64{1}, []float64{5}, 10); got != 1 {
		t.Errorf("past last bound = %v, want 1", got)
	}
	if !math.IsNaN(quantile(.5, nil, nil, 1)) {
		t.Error("no buckets should be NaN")
	}
}
