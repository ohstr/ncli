package bunker

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
)

func TestLoadEventLog_MissingFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")

	log, history, err := LoadEventLog(path)
	if err != nil {
		t.Fatalf("LoadEventLog() error = %v, want nil for a missing file", err)
	}
	defer log.Close()

	if len(history) != 0 {
		t.Errorf("history = %+v, want empty", history)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected LoadEventLog to create %s, stat error = %v", path, err)
	}
}

// TestEventLog_AppendAndReplayRoundTrip is the core guarantee this whole
// file exists for: everything appended across all three record types is
// exactly what a fresh LoadEventLog reconstructs, including a signed
// event arriving (AppendSigned) after its own resolution (AppendResolved)
// -- the same two-hook timing recordHistory/recordSignedEvent already
// have in daemon.go, not a coincidence (see EventLog's own doc comment).
func TestEventLog_AppendAndReplayRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")

	log, _, err := LoadEventLog(path)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().Truncate(time.Second)
	event := &nip01.Event{ID: "abcd", PubKey: "ef01", Kind: 1, Content: "hello", CreatedAt: uint64(now.Unix()), Sig: "deadbeef"}

	if err := log.AppendAdded(Pending{ID: "req-1", ClientKey: "app1", Method: "sign_event", Kind: 1, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := log.AppendResolved(HistoryEntry{ID: "req-1", ClientKey: "app1", Method: "sign_event", Kind: 1, CreatedAt: now, ResolvedAt: now, Verdict: Allow, Remembered: true}); err != nil {
		t.Fatal(err)
	}
	if err := log.AppendSigned("req-1", event); err != nil {
		t.Fatal(err)
	}
	// A second, unrelated request that never gets a signed event (e.g.
	// method == ping) -- guards against AppendSigned's patch leaking onto
	// the wrong entry.
	if err := log.AppendAdded(Pending{ID: "req-2", ClientKey: "app2", Method: "ping", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := log.AppendResolved(HistoryEntry{ID: "req-2", ClientKey: "app2", Method: "ping", CreatedAt: now, ResolvedAt: now, Verdict: Deny}); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, history, err := LoadEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()

	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2: %+v", len(history), history)
	}
	if history[0].ID != "req-1" || history[0].Verdict != Allow || !history[0].Remembered {
		t.Errorf("history[0] = %+v, want req-1/Allow/Remembered=true", history[0])
	}
	if history[0].Event == nil || history[0].Event.ID != "abcd" {
		t.Errorf("history[0].Event = %+v, want the signed event patched in", history[0].Event)
	}
	if history[1].ID != "req-2" || history[1].Verdict != Deny {
		t.Errorf("history[1] = %+v, want req-2/Deny", history[1])
	}
	if history[1].Event != nil {
		t.Errorf("history[1].Event = %+v, want nil -- AppendSigned for req-1 must not leak onto req-2", history[1].Event)
	}
}

// TestLoadEventLog_TornLastLineIsDropped guards crash-mid-write recovery:
// a truncated final line (the process died between Write and the next
// append ever starting) must not fail startup -- it's dropped, and
// everything before it still loads normally.
func TestLoadEventLog_TornLastLineIsDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")

	h := HistoryEntry{ID: "req-1", Method: "ping", Verdict: Allow}
	line1, err := json.Marshal(walEntry{Type: walResolved, History: &h})
	if err != nil {
		t.Fatal(err)
	}
	torn := `{"type":"resolved","history":{"id":"req-2"` // deliberately truncated, no closing braces

	content := string(line1) + "\n" + torn
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	log, history, err := LoadEventLog(path)
	if err != nil {
		t.Fatalf("LoadEventLog() error = %v, want the torn last line silently dropped, not a hard failure", err)
	}
	defer log.Close()

	if len(history) != 1 || history[0].ID != "req-1" {
		t.Errorf("history = %+v, want exactly the one entry before the torn line", history)
	}
}

// TestLoadEventLog_CorruptMiddleLineIsHardError guards the other half of
// the same policy: corruption anywhere but the very last line is real
// data loss, and silently skipping it would violate the whole point of a
// durable log -- refusing to start is the honest failure mode.
func TestLoadEventLog_CorruptMiddleLineIsHardError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")

	h1 := HistoryEntry{ID: "req-1", Method: "ping", Verdict: Allow}
	h3 := HistoryEntry{ID: "req-3", Method: "ping", Verdict: Allow}
	line1, _ := json.Marshal(walEntry{Type: walResolved, History: &h1})
	line3, _ := json.Marshal(walEntry{Type: walResolved, History: &h3})

	content := string(line1) + "\n" + "not json at all, and not the last line either" + "\n" + string(line3) + "\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := LoadEventLog(path); err == nil {
		t.Fatal("LoadEventLog() error = nil, want a hard error for mid-file corruption")
	}
}

// TestLoadEventLog_SelfHealsInterruptedPending guards this plan's central
// design decision: a request left "added" with no matching "resolved" --
// genuinely in-flight when whatever wrote the log last stopped running --
// must surface as a terminal Expired HistoryEntry (the same status a real
// Queue timeout produces), not silently vanish and not stay "pending"
// forever waiting for a decision nothing will ever deliver on (see the
// plan's own reasoning: NIP-46's resend-on-timeout already makes that
// unnecessary). The self-heal write must also be durable immediately, so
// a second load doesn't rediscover the same leftover again.
func TestLoadEventLog_SelfHealsInterruptedPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")

	createdAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	p := Pending{ID: "req-1", ClientKey: "app1", Method: "sign_event", Kind: 1, CreatedAt: createdAt}
	line, err := json.Marshal(walEntry{Type: walAdded, Pending: &p})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	log, history, err := LoadEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1: %+v", len(history), history)
	}
	h := history[0]
	if h.ID != "req-1" || h.Verdict != Deny || !h.Expired {
		t.Errorf("history[0] = %+v, want req-1/Deny/Expired=true", h)
	}
	if !h.CreatedAt.Equal(createdAt) {
		t.Errorf("history[0].CreatedAt = %v, want the original Pending.CreatedAt %v", h.CreatedAt, createdAt)
	}

	// Second load: the self-heal from the first load must already be
	// durable, so this must not produce a second synthesized entry.
	log2, history2, err := LoadEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log2.Close()
	if len(history2) != 1 || history2[0].ID != "req-1" {
		t.Errorf("second load history = %+v, want the same single already-healed entry, not rediscovered again", history2)
	}
}

// TestLoadEventLog_CompactsToMaxHistoryTail guards the bound: a log with
// more resolved entries than maxHistoryTail is trimmed to the most recent
// maxHistoryTail on load, and the file itself is rewritten to match (not
// just the in-memory slice) -- otherwise a long-lived daemon's log grows
// forever even though historyTail itself never does.
func TestLoadEventLog_CompactsToMaxHistoryTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")

	const total = maxHistoryTail + 50
	base := time.Now().Add(-time.Hour)

	var buf strings.Builder
	for i := 0; i < total; i++ {
		h := HistoryEntry{
			ID:         fmt.Sprintf("req-%d", i),
			Method:     "ping",
			Verdict:    Allow,
			CreatedAt:  base.Add(time.Duration(i) * time.Second),
			ResolvedAt: base.Add(time.Duration(i) * time.Second),
		}
		line, err := json.Marshal(walEntry{Type: walResolved, History: &h})
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0600); err != nil {
		t.Fatal(err)
	}

	log, history, err := LoadEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	if len(history) != maxHistoryTail {
		t.Fatalf("history len = %d, want maxHistoryTail (%d)", len(history), maxHistoryTail)
	}
	if want := fmt.Sprintf("req-%d", total-maxHistoryTail); history[0].ID != want {
		t.Errorf("history[0].ID = %q, want %q (the oldest surviving entry)", history[0].ID, want)
	}
	if want := fmt.Sprintf("req-%d", total-1); history[len(history)-1].ID != want {
		t.Errorf("history[last].ID = %q, want %q (the newest entry)", history[len(history)-1].ID, want)
	}

	lineCount := countLines(t, path)
	if lineCount != maxHistoryTail {
		t.Errorf("file line count after compaction = %d, want maxHistoryTail (%d)", lineCount, maxHistoryTail)
	}

	// Idempotency: reloading the already-compacted file must return the
	// same entries and not trim/rewrite again.
	log2, history2, err := LoadEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log2.Close()
	if len(history2) != maxHistoryTail {
		t.Errorf("second load history len = %d, want maxHistoryTail (%d)", len(history2), maxHistoryTail)
	}
}

// TestEventLog_CompactDue guards the runtime-compaction trigger
// (Daemon.recordHistory's counterpart to LoadEventLog's own startup-only
// compact): CompactDue must stay false under the threshold, flip true once
// maxHistoryTail resolved entries have been appended since the last
// compaction, and compact must both shrink the file and reset the counter
// so CompactDue goes back to false immediately after.
func TestEventLog_CompactDue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")
	log, _, err := LoadEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	var tail []HistoryEntry
	for i := 0; i < maxHistoryTail-1; i++ {
		h := HistoryEntry{ID: fmt.Sprintf("req-%d", i), Method: "ping", Verdict: Allow}
		if err := log.AppendResolved(h); err != nil {
			t.Fatal(err)
		}
		tail = append(tail, h)
		if log.CompactDue() {
			t.Fatalf("CompactDue() = true after %d appends, want false (below maxHistoryTail)", i+1)
		}
	}

	last := HistoryEntry{ID: "req-last", Method: "ping", Verdict: Allow}
	if err := log.AppendResolved(last); err != nil {
		t.Fatal(err)
	}
	tail = append(tail, last)
	if !log.CompactDue() {
		t.Fatalf("CompactDue() = false after %d appends, want true (reached maxHistoryTail)", maxHistoryTail)
	}

	if err := log.compact(tail); err != nil {
		t.Fatal(err)
	}
	if log.CompactDue() {
		t.Error("CompactDue() = true right after compact, want false (counter should reset)")
	}
	if lineCount := countLines(t, path); lineCount != len(tail) {
		t.Errorf("file line count after runtime compact = %d, want %d", lineCount, len(tail))
	}

	reloaded, history, err := LoadEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if len(history) != len(tail) {
		t.Fatalf("reloaded history len = %d, want %d", len(history), len(tail))
	}
}

// TestEventLog_CompactBeforeSignedEvent_StillPreservesIt guards a timing
// window the runtime trigger (CompactDue/compact called from
// Daemon.recordHistory) newly makes reachable in normal operation, not
// just a crash: compact can fire for one resolution while an earlier
// sign_event resolution is still in flight -- resolved (recordHistory has
// already run) but not yet signed (Handler.execute/recordSignedEvent
// hasn't reached OnSigned yet) -- so the historyTail snapshot compact
// rewrites the file from has that entry's Event still nil. This proves
// that's fine: AppendSigned lands as a fresh line appended after the
// rewritten file, and a later LoadEventLog replay (walResolved then
// walSigned, in file order) still reconstructs the signed event correctly
// -- exactly like a signed event arriving after an ordinary,
// non-compacting resolution already does in
// TestEventLog_AppendAndReplayRoundTrip.
func TestEventLog_CompactBeforeSignedEvent_StillPreservesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")
	log, _, err := LoadEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	pending := HistoryEntry{ID: "sign-1", Method: "sign_event", Kind: 1, Verdict: Allow}
	if err := log.AppendResolved(pending); err != nil {
		t.Fatal(err)
	}

	// compact fires (as Daemon.recordHistory would once CompactDue says
	// so) while pending is still unsigned -- the exact snapshot
	// recordHistory would have taken of historyTail at this instant.
	if err := log.compact([]HistoryEntry{pending}); err != nil {
		t.Fatal(err)
	}

	// The signature only completes after the compaction -- Handler.execute
	// finishing and firing OnSigned/recordSignedEvent.
	event := &nip01.Event{ID: "deadbeef", Content: "signed after compaction"}
	if err := log.AppendSigned(pending.ID, event); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, history, err := LoadEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()

	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1: %+v", len(history), history)
	}
	if history[0].Event == nil || history[0].Event.Content != "signed after compaction" {
		t.Fatalf("history[0].Event = %+v, want the signed event patched in after the compaction", history[0].Event)
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	n := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if scanner.Text() != "" {
			n++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestEventLog_ConcurrentAppendsAreRaceClean guards appendLocked's own
// mutex: Add and Resolve fire from different goroutines in the real
// daemon (the request-handling goroutine vs. the IPC command goroutine),
// so concurrent Append* calls are the normal case, not an edge case.
func TestEventLog_ConcurrentAppendsAreRaceClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")
	log, _, err := LoadEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("req-%d", i)
			_ = log.AppendAdded(Pending{ID: id, Method: "sign_event", Kind: 1})
		}(i)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("req-%d", i)
			_ = log.AppendResolved(HistoryEntry{ID: id, Method: "sign_event", Kind: 1, Verdict: Allow})
		}(i)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("req-%d", i)
			_ = log.AppendSigned(id, &nip01.Event{ID: id})
		}(i)
	}
	wg.Wait()

	if got := countLines(t, path); got != n*3 {
		t.Errorf("line count = %d, want %d (%d requests x 3 records each)", got, n*3, n)
	}
}
