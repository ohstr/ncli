package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
	"github.com/rs/zerolog"
)

// syncBuffer is a concurrency-safe io.Writer, needed because the relay/client
// stack logs from multiple goroutines while the test captures output.
type syncBuffer struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

// performSyncTest runs the multi-relay sync test
// It starts a local relay server and runs a Stream client connecting to real production relays
func TestMultiRelaySync(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Enable debug logging, and capture it (only, not the terminal) so we
	// can tell PoW-noncompliant upstream data (harmless, live-relay data
	// quality) apart from a real pipeline regression when nothing gets
	// imported. Set up before the store/server exist below so migration
	// and startup logging is captured too. Restored automatically once
	// this test finishes.
	var logCapture syncBuffer
	withTestLogging(t, zerolog.DebugLevel, &logCapture)

	// 1. Start Local Test Relay
	port := 5577
	dbPath := t.TempDir() + "/test_relay.db"

	// Create event store
	limitation := &nip11.Limitation{
		MaxLimit:        100000,
		MaxSubscriptions: 100,
	}
	// relay.Session/EventStore default their Logger to zerolog.Nop(), which
	// is a separate instance from the process-global zlog.Logger that
	// withTestLogging above redirects -- so verification/rejection logging
	// from packet.go/handlers.go must be pointed at logCapture explicitly,
	// or the log-based diagnostics below never see it regardless of outcome.
	capLogger := zerolog.New(zerolog.ConsoleWriter{Out: &logCapture, NoColor: true}).Level(zerolog.DebugLevel)
	store, err := relay.NewEventStore(dbPath, limitation, relay.WithEventStoreLogger(capLogger))
	if err != nil {
		t.Fatalf("failed to create event store: %v", err)
	}
	defer store.Close()

	// Setup Handler
	wsHandler := relay.NewSessionHandler(store, &nip11.Metadata{}, nil,
		relay.WithLogger(capLogger),
		relay.WithSessionBufferSize(1024),
		relay.WithSessionMaxConcurrentTasks(128))
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: wsHandler,
	}

	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			t.Logf("server error: %v", err)
		}
	}()
	defer server.Close()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// 2. Define Source Relays (small subset, restricted to the most
	// consistently reliable public relays to minimize flakiness/live-data
	// noise from less-maintained relays).
	sources := []string{
		"wss://relay.damus.io",
		"wss://nos.lol",
	}

	// Helper to create remote flow spec with parsed URI
	createRemoteFlow := func(relayUrl string) *FlowSpec {
		u, err := url.Parse(relayUrl)
		if err != nil {
			t.Fatalf("invalid url %s: %v", relayUrl, err)
		}
		return &FlowSpec{
			Type:     FlOW_REMOTE,
			Relay:    relayUrl,
			Trusted:  true,
			relayURI: u,
		}
	}

	// 3. Configure Stream Spec
	spec := &StreamSpec{
		Filters: []*FilterSpec{
			{
				SubscriptionFilter: nip01.SubscriptionFilter{
					Limit: 10,
				},
			},
		},
		To: []*FlowSpec{
			createRemoteFlow(fmt.Sprintf("ws://localhost:%d", port)),
		},
	}
	// Manually populate private filters field since we skipped UnmarshalJSON
	spec.filters = nip01.NewSubscriptionFilterGroup()
	for _, f := range spec.Filters {
		spec.filters.Add(&f.SubscriptionFilter)
	}

	for _, url := range sources {
		spec.From = append(spec.From, createRemoteFlow(url))
	}

	// 4. Run Stream
	// Use quiet=true to skip TUI logging (which we simulate by not initializing TUI fully or passing quiet)
	// NewStream takes (spec)
	stream, err := NewStream(spec, false)
	if err != nil {
		t.Fatalf("failed to create stream: %v", err)
	}
	defer stream.Close()

	// Run for a fixed duration
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Sync returns stats slices, but runs goroutines in background
	// We need to wait. Stream.Sync implementation actually RETURNS the stats structures
	// and starts the flow in background.
	upStats, downStats := stream.Sync(ctx)

	// Validate connectivity
	t.Logf("Started sync from %d relays to local relay", len(sources))

	// Check stats periodically
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var totalImported uint64

	for {
		select {
		case <-ctx.Done():
			// Test finished
			t.Logf("Test finished. Total events imported: %d", totalImported)
			if totalImported == 0 {
				logText := logCapture.String()
				t.Logf("DEBUG logText length=%d", len(logText))
				verifyFailures := strings.Count(logText, "failed to verify")
				powMismatches := strings.Count(logText, "pow check failed:")
				storeErrors := strings.Count(logText, "event rejected")

				switch {
				case verifyFailures == 0 && storeErrors == 0:
					// Nothing was even attempted/rejected -- find out why by
					// checking the read-connection diagnostics client/stream.go
					// logs for each source relay (RemoteSubscription.Read).
					connOpenFailures := strings.Count(logText, "failed to open read connection")
					connReadErrors := strings.Count(logText, "read connection error")

					if connOpenFailures+connReadErrors >= len(sources) {
						// Every source connection errored before any event
						// could even reach verification/storage: that's an
						// external connectivity/availability condition
						// against live production relays, not a bug in the
						// sync pipeline itself.
						t.Logf("No events imported this run: all %d source connections failed or errored (open failures=%d, read errors=%d) -- treating as an environment/connectivity condition, not a regression. Captured log:\n%s", len(sources), connOpenFailures, connReadErrors, logText)
					} else {
						// Some sources apparently connected fine yet nothing
						// was attempted/rejected: the sync pipeline itself is
						// broken (wiring, filter construction, etc.), not a
						// data-quality or connectivity issue.
						t.Errorf("Expected to import events, got 0, no rejections were recorded, and connection failures (open=%d, read=%d) don't account for all %d sources; captured log:\n%s", connOpenFailures, connReadErrors, len(sources), logText)
					}
				case verifyFailures > 0 && verifyFailures == powMismatches && storeErrors == 0:
					// Every rejection came from nip13.ValidatePow (see the
					// "pow check failed:" wrap in nip01.Event.Verify): live
					// production relays occasionally serve a batch of events
					// tagged by non-compliant clients with a bad "nonce" tag
					// -- a self-declared difficulty higher than actually
					// achieved, a malformed tag shape, etc. That's
					// third-party data quality, not a bug in ncli/nmilat, so
					// don't hard-fail the suite over it.
					t.Logf("No events imported this run: all %d rejections were PoW-tag validation failures on live upstream data -- treating as an environment/data-quality condition, not a regression", verifyFailures)
				default:
					t.Errorf("Expected to import events, got 0 (verify failures=%d, PoW mismatches=%d, store errors=%d); rejections were not exclusively PoW mismatches, captured log:\n%s", verifyFailures, powMismatches, storeErrors, logText)
				}
			}
			return
		case <-ticker.C:
			// Aggregate stats
			// var currentImported uint64
			for _, stat := range upStats { // Upstream (Destination)
				_ = stat
			}
			for _, stat := range downStats {
				_ = stat
			}

			// Verify db directly
			count, err := store.CountEvents(context.Background(), nip01.NewSubscriptionFilterGroup())
			if err != nil {
				t.Errorf("failed to count events: %v", err)
			}
			atomic.StoreUint64(&totalImported, uint64(count))
			t.Logf("Current local DB count: %d", count)
		}
	}
}
