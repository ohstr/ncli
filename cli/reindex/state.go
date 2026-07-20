package reindex

import (
	"fmt"
	"sync"
	"time"
)

type ReindexerStatus struct {
	mu             sync.Mutex
	IsRunning      bool      `json:"is_running"`
	TotalProcessed int       `json:"total_processed"`
	StartTime      time.Time `json:"start_time,omitempty"`
}

var (
	SearchState = &ReindexerStatus{}
	ZapsState   = &ReindexerStatus{}
)

// TryStart attempts to lock the reindexer. Returns true if it successfully started,
// or false if it is already running.
func (s *ReindexerStatus) TryStart() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.IsRunning {
		return false
	}

	s.IsRunning = true
	s.TotalProcessed = 0
	s.StartTime = time.Now()
	return true
}

// Complete marks the reindexer as finished.
func (s *ReindexerStatus) Complete() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.IsRunning = false
}

// AddProgress increments the processed count safely.
func (s *ReindexerStatus) AddProgress(count int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.IsRunning {
		s.TotalProcessed += count
	}
}

// GetStatus returns a snapshot of the current state.
func (s *ReindexerStatus) GetStatus() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	res := map[string]interface{}{
		"is_running":      s.IsRunning,
		"total_processed": s.TotalProcessed,
	}

	if s.IsRunning {
		uptime := time.Since(s.StartTime)
		res["duration"] = uptime.Round(time.Second).String()

		aps := 0.0
		if uptime.Seconds() > 0 {
			aps = float64(s.TotalProcessed) / uptime.Seconds()
		}
		res["items_per_sec"] = fmt.Sprintf("%.1f", aps)
	} else if !s.StartTime.IsZero() {
		res["duration"] = time.Since(s.StartTime).Round(time.Second).String()
	}

	return res
}
