package metrics

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// History samples the registry on a fixed interval into a ring of points, so
// the dashboard can draw charts without Prometheus, and fans each new point
// out to live viewers. An optional rollup keeps coarser points for longer.
// Sampling runs on its own goroutine: viewers only ever read what it already
// computed.
type History struct {
	m        *Metrics
	interval time.Duration

	mu     sync.RWMutex
	raw    *ring
	rollup *ring
	step   int64 // rollup step, ms
	bucket int64 // start of the rollup bucket being filled, ms
	acc    map[string]*accum
	latest Point
	prev   map[string]sample

	subMu sync.Mutex
	subs  map[chan Point]struct{}
}

// Point is one sample: per-second rates for counters, values for gauges,
// and p50/p95/p99 over the interval for histograms.
type Point struct {
	Time   int64              `json:"t"`
	Values map[string]float64 `json:"v"`
	// Totals are cumulative counter values, for "since start" figures.
	Totals map[string]float64 `json:"totals"`
}

type sample struct {
	at      time.Time
	value   float64
	buckets []float64 // cumulative counts, histograms only
	count   float64
}

// maxSubscribers caps live viewers, so a flood of dashboard tabs can't make
// the sampler's fan-out expensive.
const maxSubscribers = 8

// NewHistory keeps window worth of points sampled every interval.
func NewHistory(m *Metrics, interval, window time.Duration) *History {
	return &History{
		m:        m,
		interval: interval,
		raw:      newRing(int(window / interval)),
		prev:     map[string]sample{},
		subs:     map[chan Point]struct{}{},
	}
}

// WithRollup also keeps window worth of step-sized points: each the mean of
// its samples, or their max for quantiles so a latency spike survives.
func (h *History) WithRollup(step, window time.Duration) *History {
	h.rollup = newRing(int(window / step))
	h.step = step.Milliseconds()
	h.acc = map[string]*accum{}
	return h
}

// Run samples until ctx is done.
func (h *History) Run(ctx context.Context) {
	h.Sample(time.Now())
	t := time.NewTicker(h.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			h.Sample(now)
		}
	}
}

// Sample takes one point now.
func (h *History) Sample(now time.Time) {
	fams, err := h.m.Registry.Gather()
	if err != nil && len(fams) == 0 {
		return
	}
	p := Point{Time: now.UnixMilli(), Values: map[string]float64{}, Totals: map[string]float64{}}
	next := make(map[string]sample, len(h.prev))

	for _, fam := range fams {
		name, ok := seriesName(fam.GetName())
		if !ok {
			continue
		}
		for _, metric := range fam.GetMetric() {
			key := name + labelSuffix(metric)
			switch fam.GetType() {
			case dto.MetricType_COUNTER:
				v := metric.GetCounter().GetValue()
				p.Totals[key] = v
				if prev, ok := h.prev[key]; ok {
					p.Values[key] = rate(prev.value, v, now.Sub(prev.at))
				}
				next[key] = sample{at: now, value: v}
			case dto.MetricType_GAUGE:
				p.Values[key] = metric.GetGauge().GetValue()
			case dto.MetricType_HISTOGRAM:
				hist := metric.GetHistogram()
				cur := sample{at: now, count: float64(hist.GetSampleCount())}
				bounds := make([]float64, len(hist.GetBucket()))
				cur.buckets = make([]float64, len(hist.GetBucket()))
				for i, b := range hist.GetBucket() {
					bounds[i] = b.GetUpperBound()
					cur.buckets[i] = float64(b.GetCumulativeCount())
				}
				p.Totals[key+".count"] = cur.count
				if prev, ok := h.prev[key]; ok && len(prev.buckets) == len(cur.buckets) {
					delta := make([]float64, len(cur.buckets))
					for i := range delta {
						delta[i] = cur.buckets[i] - prev.buckets[i]
					}
					n := cur.count - prev.count
					p.Values[key+".rate"] = rate(prev.count, cur.count, now.Sub(prev.at))
					if n > 0 {
						p.Values[key+".p50"] = quantile(.50, bounds, delta, n)
						p.Values[key+".p95"] = quantile(.95, bounds, delta, n)
						p.Values[key+".p99"] = quantile(.99, bounds, delta, n)
					}
				}
				next[key] = cur
			}
		}
	}

	h.mu.Lock()
	h.prev = next
	h.latest = p
	h.raw.append(p.Time, p.Values)
	if h.rollup != nil {
		h.roll(p)
	}
	h.mu.Unlock()
	h.publish(p)
}

type accum struct{ sum, max, n float64 }

// roll adds p to the rollup bucket it falls in, closing the previous bucket
// when p starts a new one.
func (h *History) roll(p Point) {
	b := p.Time - p.Time%h.step
	if b != h.bucket && len(h.acc) > 0 {
		vals := make(map[string]float64, len(h.acc))
		for key, a := range h.acc {
			if isQuantile(key) {
				vals[key] = a.max
			} else {
				vals[key] = a.sum / a.n
			}
		}
		h.rollup.append(h.bucket, vals)
		h.acc = map[string]*accum{}
	}
	h.bucket = b
	for key, v := range p.Values {
		a := h.acc[key]
		if a == nil {
			a = &accum{max: v}
			h.acc[key] = a
		}
		a.sum += v
		a.n++
		a.max = math.Max(a.max, v)
	}
}

func isQuantile(key string) bool {
	return strings.HasSuffix(key, ".p50") || strings.HasSuffix(key, ".p95") || strings.HasSuffix(key, ".p99")
}

// ring holds up to capacity points as columns.
type ring struct {
	capacity int
	times    []int64              // unix ms, oldest first
	series   map[string][]float64 // aligned with times; NaN where absent
}

func newRing(capacity int) *ring {
	return &ring{capacity: max(capacity, 1), series: map[string][]float64{}}
}

func (r *ring) append(t int64, values map[string]float64) {
	full := len(r.times) == r.capacity
	if full {
		r.times = r.times[1:]
	}
	r.times = append(r.times, t)
	n := len(r.times)
	for key, col := range r.series {
		if full && len(col) > 0 {
			col = col[1:]
		}
		v, ok := values[key]
		if !ok {
			v = math.NaN()
		}
		r.series[key] = append(col, v)
	}
	for key, v := range values {
		if _, ok := r.series[key]; ok {
			continue
		}
		col := make([]float64, n)
		for i := range col {
			col[i] = math.NaN()
		}
		col[n-1] = v
		r.series[key] = col
	}
}

// Latest returns the most recent point.
func (h *History) Latest() Point {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.latest
}

// Columns is the history in uPlot's columnar shape: one time axis and one
// aligned column per series, null where a series had no value. Rollup
// points older than the first raw sample come first.
type Columns struct {
	Interval int64 `json:"interval_ms"`
	// Window is how far back the history reaches once full.
	Window int64                 `json:"window_ms"`
	Times  []int64               `json:"t"`
	Series map[string][]nullable `json:"series"`
}

type nullable float64

func (n nullable) MarshalJSON() ([]byte, error) {
	f := float64(n)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return []byte("null"), nil
	}
	return json.Marshal(f)
}

// Columns returns a copy of the history.
func (h *History) Columns() Columns {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := Columns{
		Interval: h.interval.Milliseconds(),
		Window:   int64(h.raw.capacity) * h.interval.Milliseconds(),
		Series:   map[string][]nullable{},
	}
	old := 0 // rollup points before the first raw sample
	if h.rollup != nil {
		out.Window = max(out.Window, int64(h.rollup.capacity)*h.step)
		for old < len(h.rollup.times) && (len(h.raw.times) == 0 || h.rollup.times[old] < h.raw.times[0]) {
			old++
		}
		out.Times = append(out.Times, h.rollup.times[:old]...)
	}
	out.Times = append(out.Times, h.raw.times...)
	for _, r := range []*ring{h.raw, h.rollup} {
		if r == nil {
			continue
		}
		for key := range r.series {
			if _, ok := out.Series[key]; ok {
				continue
			}
			c := make([]nullable, 0, len(out.Times))
			if h.rollup != nil {
				c = appendColumn(c, h.rollup.series[key], old)
			}
			out.Series[key] = appendColumn(c, h.raw.series[key], len(h.raw.times))
		}
	}
	return out
}

// appendColumn appends col's first n values, or n nulls when col is nil.
func appendColumn(dst []nullable, col []float64, n int) []nullable {
	for i := range n {
		if col == nil {
			dst = append(dst, nullable(math.NaN()))
		} else {
			dst = append(dst, nullable(col[i]))
		}
	}
	return dst
}

// Subscribe returns a channel of new points, or ok=false when maxSubscribers
// are already watching. Call cancel when done.
func (h *History) Subscribe() (points <-chan Point, cancel func(), ok bool) {
	h.subMu.Lock()
	defer h.subMu.Unlock()
	if len(h.subs) >= maxSubscribers {
		return nil, nil, false
	}
	ch := make(chan Point, 1)
	h.subs[ch] = struct{}{}
	return ch, func() {
		h.subMu.Lock()
		delete(h.subs, ch)
		h.subMu.Unlock()
	}, true
}

// publish hands p to every viewer without blocking: a viewer still holding
// the previous point simply skips this one.
func (h *History) publish(p Point) {
	h.subMu.Lock()
	defer h.subMu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

// keptRuntime are the Go/process series the dashboard shows; the rest of
// the runtime collector's output stays Prometheus-only.
var keptRuntime = map[string]string{
	"go_goroutines":                 "goroutines",
	"go_memstats_heap_alloc_bytes":  "heap_bytes",
	"go_gc_duration_seconds":        "gc_seconds",
	"process_resident_memory_bytes": "rss_bytes",
	"process_cpu_seconds_total":     "cpu_seconds_total",
	"process_open_fds":              "open_fds",
}

func seriesName(full string) (string, bool) {
	if short, ok := strings.CutPrefix(full, Namespace+"_"); ok {
		return short, true
	}
	short, ok := keptRuntime[full]
	return short, ok
}

// labelSuffix renders a series' label values as ":v1:v2", in label-name
// order, skipping build_info's version.
func labelSuffix(m *dto.Metric) string {
	labels := m.GetLabel()
	if len(labels) == 0 {
		return ""
	}
	sorted := append([]*dto.LabelPair(nil), labels...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].GetName() < sorted[j].GetName() })
	var b strings.Builder
	for _, l := range sorted {
		if l.GetName() == "version" {
			continue
		}
		b.WriteByte(':')
		b.WriteString(l.GetValue())
	}
	return b.String()
}

func rate(prev, cur float64, d time.Duration) float64 {
	if d <= 0 || cur < prev {
		return 0
	}
	return (cur - prev) / d.Seconds()
}

// quantile estimates the q-quantile from per-bucket counts (cumulative) the
// way Prometheus's histogram_quantile does: linear within the bucket.
func quantile(q float64, bounds, cumulative []float64, total float64) float64 {
	rank := q * total
	lower, below := 0.0, 0.0
	for i, upper := range bounds {
		if cumulative[i] >= rank {
			inBucket := cumulative[i] - below
			if inBucket <= 0 {
				return upper
			}
			return lower + (upper-lower)*(rank-below)/inBucket
		}
		lower, below = upper, cumulative[i]
	}
	// Past the last finite bound: report that bound, as Prometheus does.
	if len(bounds) > 0 {
		return bounds[len(bounds)-1]
	}
	return math.NaN()
}
