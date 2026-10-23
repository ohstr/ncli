package metrics

import (
	"context"
	"os"
	"sync/atomic"
	"time"

	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/relay"
	"github.com/prometheus/client_golang/prometheus"
)

// Sources are the running relay's parts read at scrape time. Any may be nil.
type Sources struct {
	Handler   *relay.SessionHandler
	Store     *relay.EventStore
	StorePath string
	Rooms     *room.Manager
	// Jobs reports how many index jobs are running.
	Jobs func() int
	// Indexes reports each index's state at scrape time.
	Indexes func() []IndexSample
}

// IndexSample is one index's state, as exported.
type IndexSample struct {
	Name    string
	State   string
	Entries int64
	Behind  int64
	Queued  int64
}

// IndexStates are the states index_state reports, each 0 or 1.
var IndexStates = []string{"disabled", "empty", "ready", "behind", "needs_rebuild", "unreachable", "building", "catching_up", "rescoring", "dropping"}

// indexCollector reads every index once per scrape.
type indexCollector struct {
	read                           func() []IndexSample
	state, entries, behind, queued *prometheus.Desc
}

func newIndexCollector(read func() []IndexSample) *indexCollector {
	d := func(name, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc(prometheus.BuildFQName(Namespace, "", name), help, labels, nil)
	}
	return &indexCollector{
		read:    read,
		state:   d("index_state", "Each index's state: 1 for the one it is in.", "index", "state"),
		entries: d("index_entries", "Entries an index holds.", "index"),
		behind:  d("index_behind", "Events stored since an index was last current.", "index"),
		queued:  d("index_queued", "Profiles waiting to be looked at again by an index.", "index"),
	}
}

func (c *indexCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.state
	ch <- c.entries
	ch <- c.behind
	ch <- c.queued
}

func (c *indexCollector) Collect(ch chan<- prometheus.Metric) {
	for _, ix := range c.read() {
		for _, st := range IndexStates {
			v := 0.0
			if st == ix.State {
				v = 1
			}
			ch <- prometheus.MustNewConstMetric(c.state, prometheus.GaugeValue, v, ix.Name, st)
		}
		ch <- prometheus.MustNewConstMetric(c.entries, prometheus.GaugeValue, float64(ix.Entries), ix.Name)
		ch <- prometheus.MustNewConstMetric(c.behind, prometheus.GaugeValue, float64(ix.Behind), ix.Name)
		ch <- prometheus.MustNewConstMetric(c.queued, prometheus.GaugeValue, float64(ix.Queued), ix.Name)
	}
}

// slowSampleInterval paces reads that touch the disk.
const slowSampleInterval = 30 * time.Second

// Attach registers gauges that read src when scraped, and starts sampling
// the disk-bound ones every 30s until ctx is done. Every read here is O(1):
// channel lengths, atomics and cached snapshots, never a store scan.
func (m *Metrics) Attach(ctx context.Context, src Sources) {
	f := factory{m.Registry}

	if h := src.Handler; h != nil {
		f.gaugeFunc("ws_sessions", "Open WebSocket sessions.", func() float64 { return float64(h.SessionCount()) })
		f.gaugeFunc("members", "NIP-43 members.", func() float64 { return float64(h.Membership().Count()) })
		f.gaugeFunc("groups", "NIP-29 groups hosted.", func() float64 { return float64(h.Groups().Count()) })
		if w := h.VerificationWorker; w != nil {
			f.gaugeFunc("verify_queue_depth", "Profile verification jobs waiting.", func() float64 { return float64(w.QueueDepth()) })
			f.counterFunc("verify_processed_total", "Profile verification jobs done.", func() float64 { return float64(w.TotalProcessed.Load()) })
			f.counterFunc("verify_errors_total", "Profile verification jobs that failed.", func() float64 { return float64(w.TotalErrors.Load()) })
		}
	}

	if s := src.Store; s != nil {
		f.gaugeFunc("store_task_queue_depth", "Store write tasks waiting for a worker.", func() float64 { return float64(s.TaskQueueDepth()) })
		f.gaugeFunc("store_task_queue_capacity", "Store write task queue size.", func() float64 { return float64(s.TaskQueueCapacity()) })
		f.gaugeFunc("scans_inflight", "Query scans holding a scan slot.", func() float64 { n, _ := s.InflightScans(); return float64(n) })
		f.gaugeFunc("scan_slots", "Scan slot limit (0 = unbounded).", func() float64 { _, l := s.InflightScans(); return float64(l) })
		f.gaugeFunc("store_read_txs_open", "Open bbolt read transactions.", func() float64 { return float64(s.Db().Stats().OpenTxN) })
	}

	if src.Indexes != nil {
		m.Registry.MustRegister(newIndexCollector(src.Indexes))
	}

	if r := src.Rooms; r != nil {
		f.gaugeFunc("huddle_rooms", "Huddle rooms with someone in them.", func() float64 { return float64(r.Len()) })
		f.gaugeFunc("huddle_peers", "Peers across huddle rooms.", func() float64 {
			n := 0
			for _, c := range r.Occupancy() {
				n += c
			}
			return float64(n)
		})
	}

	if jobs := src.Jobs; jobs != nil {
		f.gaugeFunc("jobs_running", "Index jobs running.", func() float64 { return float64(jobs()) })
	}

	if src.StorePath != "" {
		var size atomic.Int64
		sample := func() {
			if fi, err := os.Stat(src.StorePath); err == nil {
				size.Store(fi.Size())
			}
		}
		sample()
		f.gaugeFunc("store_size_bytes", "Event store file size, sampled every 30s.", func() float64 { return float64(size.Load()) })
		go func() {
			t := time.NewTicker(slowSampleInterval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					sample()
				}
			}
		}()
	}
}
