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
// out to live viewers. Sampling runs on its own goroutine: viewers only ever
// read what it already computed.
type History struct {
	m        *Metrics
	interval time.Duration
	capacity int

	mu     sync.RWMutex
	times  []int64              // unix ms, oldest first
	series map[string][]float64 // aligned with times; NaN where absent
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
		capacity: int(window / interval),
		series:   map[string][]float64{},
		prev:     map[string]sample{},
		subs:     map[chan Point]struct{}{},
	}
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
	h.append(p)
	h.mu.Unlock()
	h.publish(p)
}

func (h *History) append(p Point) {
	full := len(h.times) == h.capacity
	if full {
		h.times = h.times[1:]
	}
	h.times = append(h.times, p.Time)
	n := len(h.times)
	for key, col := range h.series {
		if full && len(col) > 0 {
			col = col[1:]
		}
		v, ok := p.Values[key]
		if !ok {
			v = math.NaN()
		}
		h.series[key] = append(col, v)
	}
	for key, v := range p.Values {
		if _, ok := h.series[key]; ok {
			continue
		}
		col := make([]float64, n)
		for i := range col {
			col[i] = math.NaN()
		}
		col[n-1] = v
		h.series[key] = col
	}
}

// Latest returns the most recent point.
func (h *History) Latest() Point {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.latest
}

// Columns is the history in uPlot's columnar shape: one time axis and one
// aligned column per series, null where a series had no value.
type Columns struct {
	Interval int64                 `json:"interval_ms"`
	Times    []int64               `json:"t"`
	Series   map[string][]nullable `json:"series"`
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
		Times:    append([]int64(nil), h.times...),
		Series:   make(map[string][]nullable, len(h.series)),
	}
	for key, col := range h.series {
		c := make([]nullable, len(col))
		for i, v := range col {
			c[i] = nullable(v)
		}
		out.Series[key] = c
	}
	return out
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
