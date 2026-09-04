package bunker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
)

// EventLogPath returns the OS-appropriate path to the durable event log,
// under ncli's shared bunker state directory -- same convention as
// SessionsPath/SocketPath.
func EventLogPath() string {
	return filepath.Join(common.AppConfigDir(), "bunker", "events.wal")
}

// walEntryType discriminates EventLog's three record shapes -- see
// EventLog's own doc comment for why there are three, not one.
type walEntryType string

const (
	walAdded    walEntryType = "added"
	walResolved walEntryType = "resolved"
	walSigned   walEntryType = "signed"
)

// walEntry is one line of the event log. Only the field(s) matching Type
// are ever set; the others stay nil/empty and are omitted from the
// marshaled JSON.
type walEntry struct {
	Type    walEntryType  `json:"type"`
	Pending *Pending      `json:"pending,omitempty"` // Type == walAdded
	History *HistoryEntry `json:"history,omitempty"` // Type == walResolved
	ID      string        `json:"id,omitempty"`      // Type == walSigned
	Event   *nip01.Event  `json:"event,omitempty"`   // Type == walSigned
}

// EventLog is an append-only, fsync'd-per-write JSONL log of Queue's own
// Added/Resolved lifecycle (plus the signed event that lands slightly
// later still, for an approved sign_event -- see Daemon.recordSignedEvent)
// -- the durable record Daemon.historyTail/Queue.byID don't otherwise
// have, so Request History survives a crash or restart instead of
// silently resetting to empty. Write pattern is bursty and paced by human
// approval clicks (never more than a handful of requests a second even
// under heavy use, since every one either already matched an existing
// Store grant with no write at all, or is waiting on a human), so an
// fsync per write is not a real latency concern here, unlike a genuine
// hot path.
type EventLog struct {
	path string
	f    *os.File
	mu   sync.Mutex // serializes appends, resolvedSinceCompact, and compact itself; fsync is inherently sequential anyway

	// resolvedSinceCompact counts AppendResolved calls since the log was
	// last compacted (at LoadEventLog, or by a runtime CompactDue/compact
	// pair -- see Daemon.recordHistory). Without a runtime trigger, compact
	// only ever ran once at startup, so a long-lived daemon's on-disk log
	// grows without bound between restarts even though the in-memory
	// historyTail it mirrors stays capped at maxHistoryTail -- this counter
	// is what lets recordHistory notice the file deserves trimming again
	// well before the next restart.
	resolvedSinceCompact int
}

// LoadEventLog opens path (creating it, and its parent directory, if
// missing -- an empty log, not an error, matching LoadStore's own
// "missing means empty" convention) and replays it into an in-memory
// history reconstruction:
//
//   - walAdded records seed a provisional pending-by-ID set.
//   - walResolved records move that ID into the reconstructed history
//     (removing it from the pending set) -- a HistoryEntry is written
//     here verbatim, not re-derived, so ResolvedAt/Verdict/Remembered/
//     Expired/Kind all survive exactly as recorded.
//   - walSigned records patch .Event onto the matching (already-resolved)
//     entry, mirroring recordSignedEvent's own later-arrival timing.
//
// Anything still in the pending set once replay finishes was genuinely
// in-flight when whatever wrote this log last stopped running (a crash,
// or just a restart with an undecided request sitting on screen) -- see
// this session's plan doc for why that does NOT mean "resume trying to
// get it approved": NIP-46 clients already resend an unanswered request,
// so by the time a human could act on a resurrected multi-minute-old
// prompt the live path has almost always already superseded it. Instead
// each leftover is folded into history as a terminal Expired entry (the
// same status a real Queue timeout already produces) and that resolution
// is durably appended right away, so a second restart doesn't rediscover
// the same leftover again.
//
// The reconstructed history is then compacted to the most recent
// maxHistoryTail entries, matching historyTail's own in-memory bound --
// if that actually trims anything, the file is rewritten (temp file,
// fsync, atomic rename) to hold just the survivors, so a long-lived
// daemon's log doesn't grow forever.
//
// Returns the open-for-append handle and the reconstructed history,
// oldest first (Daemon.History() reverses it for display, the same
// convention historyTail itself already uses).
func LoadEventLog(path string) (*EventLog, []HistoryEntry, error) {
	entries, err := readWALEntries(path)
	if err != nil {
		return nil, nil, err
	}

	pending := map[string]Pending{}
	history := map[string]*HistoryEntry{}
	var order []string

	for _, e := range entries {
		switch e.Type {
		case walAdded:
			if e.Pending != nil {
				pending[e.Pending.ID] = *e.Pending
			}
		case walResolved:
			if e.History != nil {
				h := *e.History
				delete(pending, h.ID)
				if _, exists := history[h.ID]; !exists {
					order = append(order, h.ID)
				}
				history[h.ID] = &h
			}
		case walSigned:
			if h, ok := history[e.ID]; ok {
				h.Event = e.Event
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil, err
	}
	l := &EventLog{path: path, f: f}

	leftoverIDs := make([]string, 0, len(pending))
	for id := range pending {
		leftoverIDs = append(leftoverIDs, id)
	}
	sort.Slice(leftoverIDs, func(i, j int) bool {
		return pending[leftoverIDs[i]].CreatedAt.Before(pending[leftoverIDs[j]].CreatedAt)
	})
	now := time.Now()
	for _, id := range leftoverIDs {
		p := pending[id]
		h := HistoryEntry{
			ID:         p.ID,
			ClientKey:  p.ClientKey,
			Method:     p.Method,
			Kind:       p.Kind,
			CreatedAt:  p.CreatedAt,
			ResolvedAt: now,
			Verdict:    Deny,
			Expired:    true,
		}
		if err := l.AppendResolved(h); err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		order = append(order, h.ID)
		history[h.ID] = &h
	}

	out := make([]HistoryEntry, 0, len(order))
	for _, id := range order {
		out = append(out, *history[id])
	}
	if len(out) > maxHistoryTail {
		out = out[len(out)-maxHistoryTail:]
		if err := l.compact(out); err != nil {
			_ = f.Close()
			return nil, nil, err
		}
	}

	return l, out, nil
}

// readWALEntries reads and parses every line of path. A parse failure on
// the very last line is treated as a torn write -- the process stopped
// mid-append, e.g. a crash between Write and the next line ever starting
// -- and is silently dropped rather than failing startup over an
// incomplete trailing record. A parse failure anywhere else is real
// corruption and is returned as a hard error rather than silently
// skipped, since silently ignoring it would be a worse violation of "no
// data loss" than refusing to start.
func readWALEntries(path string) ([]walEntry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil, nil
	}
	lines := strings.Split(trimmed, "\n")

	entries := make([]walEntry, 0, len(lines))
	for i, line := range lines {
		var e walEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			if i == len(lines)-1 {
				break
			}
			return nil, fmt.Errorf("%s: corrupt entry on line %d: %w", path, i+1, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// AppendAdded durably records p having started waiting on a human
// decision -- see Queue.OnAdded, wired to this in NewDaemon.
func (l *EventLog) AppendAdded(p Pending) error {
	return l.appendLocked(walEntry{Type: walAdded, Pending: &p})
}

// AppendResolved durably records h -- see Daemon.recordHistory, which
// calls this right after building the exact same HistoryEntry it also
// appends to historyTail.
func (l *EventLog) AppendResolved(h HistoryEntry) error {
	if err := l.appendLocked(walEntry{Type: walResolved, History: &h}); err != nil {
		return err
	}
	l.mu.Lock()
	l.resolvedSinceCompact++
	l.mu.Unlock()
	return nil
}

// CompactDue reports whether enough resolved requests have been appended
// since the last compaction (startup's, or a previous runtime one) to make
// another one worthwhile -- reusing maxHistoryTail itself as the interval,
// so a running daemon's log oscillates between roughly maxHistoryTail and
// 2*maxHistoryTail resolved lines instead of growing for as long as the
// process stays up. Cheap (a single counter compare) so Daemon.recordHistory
// can call it on every resolution without worrying about overhead.
func (l *EventLog) CompactDue() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.resolvedSinceCompact >= maxHistoryTail
}

// AppendSigned durably records the signed event for an already-resolved
// sign_event request -- see Daemon.recordSignedEvent, which calls this
// right after patching the same event onto its historyTail entry.
func (l *EventLog) AppendSigned(requestID string, event *nip01.Event) error {
	return l.appendLocked(walEntry{Type: walSigned, ID: requestID, Event: event})
}

func (l *EventLog) appendLocked(e walEntry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.f.Write(data); err != nil {
		return err
	}
	return l.f.Sync()
}

// compact rewrites the log to hold exactly one walResolved line per entry
// in entries (each already carries its own patched-in signed event, if
// any, so no separate walSigned lines are needed post-compaction) --
// temp file, fsync, atomic rename, so a crash mid-compact leaves the
// original file intact rather than a half-written replacement. Called both
// from LoadEventLog (a one-time startup trim) and from Daemon.recordHistory
// once CompactDue says so (a recurring runtime trim) -- either way,
// resolvedSinceCompact resets here so CompactDue's next answer is measured
// from this compaction, not the previous one.
func (l *EventLog) compact(entries []HistoryEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	tmpPath := l.path + ".tmp"
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	for _, h := range entries {
		data, err := json.Marshal(walEntry{Type: walResolved, History: &h})
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return err
		}
		if _, err := tmp.Write(append(data, '\n')); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	// The currently-open l.f keeps writing to the old inode even after
	// the rename below retargets the path -- close it first and reopen
	// against the now-compacted file so subsequent appends land in the
	// right place.
	if err := l.f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, l.path); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	l.f = f
	l.resolvedSinceCompact = 0
	return nil
}

// Close releases the log's underlying file handle.
func (l *EventLog) Close() error {
	return l.f.Close()
}
