package metrics

import (
	"context"
	"os"
	"sync/atomic"
	"time"

	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/search"
)

// Sources are the running relay's parts read at scrape time. Any may be nil.
type Sources struct {
	Handler   *relay.SessionHandler
	Store     *relay.EventStore
	StorePath string
	Search    search.Service
	Rooms     *room.Manager
	// Jobs reports how many index jobs are running.
	Jobs func() int
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
			f.counterFunc("verify_dropped_total", "Profile verification jobs dropped on a full queue.", func() float64 { return float64(w.TotalDropped.Load()) })
		}
	}

	if s := src.Store; s != nil {
		f.gaugeFunc("store_task_queue_depth", "Store write tasks waiting for a worker.", func() float64 { return float64(s.TaskQueueDepth()) })
		f.gaugeFunc("store_task_queue_capacity", "Store write task queue size.", func() float64 { return float64(s.TaskQueueCapacity()) })
		f.gaugeFunc("scans_inflight", "Query scans holding a scan slot.", func() float64 { n, _ := s.InflightScans(); return float64(n) })
		f.gaugeFunc("scan_slots", "Scan slot limit (0 = unbounded).", func() float64 { _, l := s.InflightScans(); return float64(l) })
		f.gaugeFunc("store_read_txs_open", "Open bbolt read transactions.", func() float64 { return float64(s.Db().Stats().OpenTxN) })
	}

	if q, ok := src.Search.(interface {
		QueueDepth() int
		Dropped() uint64
	}); ok {
		f.gaugeFunc("search_index_queue_depth", "Profile updates waiting to be indexed.", func() float64 { return float64(q.QueueDepth()) })
		f.counterFunc("search_index_dropped_total", "Profile updates dropped on a full queue.", func() float64 { return float64(q.Dropped()) })
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
