package relay

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
)

// countVerificationWorkerGoroutines counts currently-running
// (*ProfileVerificationWorker).worker goroutines via a full stack dump --
// more reliable than a bare runtime.NumGoroutine() delta, which is noisy in
// a shared test binary (other goroutines unrelated to this test start and
// exit around the same time).
func countVerificationWorkerGoroutines() int {
	buf := make([]byte, 4<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "ProfileVerificationWorker).worker(")
}

// TestServiceStop_StopsVerificationWorkers confirms followup issue (relay
// SIGTERM/shutdown inconsistency, integration/agent-eval/followup/issues.md):
// NewServer starts config.VerificationWorkers goroutines
// (wsHandler.VerificationWorker.Start), but Service.Stop() never calls
// wsHandler.VerificationWorker.Stop() -- only server.Shutdown() and
// store.Close() -- so every graceful shutdown abandons those goroutines
// (and whatever profile-verification job each was mid-processing) instead
// of actually stopping them.
func TestServiceStop_StopsVerificationWorkers(t *testing.T) {
	prevConfig := config
	t.Cleanup(func() { config = prevConfig })

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := relay.NewEventStore(dbPath, &nip11.Limitation{MaxLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}

	const workerCount = 5
	config = RelayConfig{
		Nip11:               nip11.Metadata{PubKey: strings.Repeat("a", 64), PrivKey: strings.Repeat("1", 64)},
		VerificationWorkers: workerCount,
	}

	s := NewServer(store, nil)

	// Give the worker goroutines a moment to actually start.
	deadline := time.Now().Add(2 * time.Second)
	for countVerificationWorkerGoroutines() < workerCount && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := countVerificationWorkerGoroutines(); got != workerCount {
		t.Fatalf("verification worker goroutines after start = %d, want %d", got, workerCount)
	}

	s.Stop()

	deadline = time.Now().Add(2 * time.Second)
	for {
		if got := countVerificationWorkerGoroutines(); got == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("verification worker goroutines after Stop() = %d, want 0 -- Service.Stop() never calls wsHandler.VerificationWorker.Stop(), leaking its worker goroutines on every graceful shutdown", countVerificationWorkerGoroutines())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
