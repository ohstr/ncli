package jobs

import (
	"fmt"
	"time"
)

// queueWindow is how long search's queue must keep growing before the
// dashboard warns that search is falling behind.
const queueWindow = 30 * time.Minute

// queueSample is search's queue and applied total at one moment.
type queueSample struct {
	at      time.Time
	queued  int64
	applied int64
}

// queueWatch keeps samples of search's queue, about a minute apart, over
// the last queueWindow and a little more.
type queueWatch struct{ samples []queueSample }

func (w *queueWatch) add(s queueSample) {
	if n := len(w.samples); n > 0 {
		last := w.samples[n-1]
		if s.applied < last.applied {
			w.samples = nil // the relay restarted its counters
		} else if s.at.Sub(last.at) < time.Minute {
			return
		}
	}
	w.samples = append(w.samples, s)
	cut := 0
	for cut+1 < len(w.samples) && s.at.Sub(w.samples[cut+1].at) >= queueWindow {
		cut++
	}
	w.samples = w.samples[cut:]
}

// warning says when, for the whole window, the queue grew faster than
// profiles were applied: it never fell below where the window began and
// ends higher. "" otherwise, or before a window of samples exists.
func (w *queueWatch) warning() string {
	if len(w.samples) < 2 {
		return ""
	}
	first, last := w.samples[0], w.samples[len(w.samples)-1]
	if last.at.Sub(first.at) < queueWindow || last.queued <= first.queued {
		return ""
	}
	for _, s := range w.samples[1:] {
		if s.queued < first.queued {
			return ""
		}
	}
	return fmt.Sprintf("search is falling behind: %s profiles joined the queue in the last %s while %s were applied",
		Count(last.queued-first.queued+last.applied-first.applied), queueWindow, Count(last.applied-first.applied))
}
