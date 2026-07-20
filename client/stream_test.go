package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
	"github.com/rs/zerolog"
)

// TestRelayLoad stress tests the relay by opening 100 concurrent stream connections
func TestRelayLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in short mode")
	}

	// Enable info-level logging to see what happens (restored once this test finishes)
	withTestLogging(t, zerolog.InfoLevel)

	// 1. Start Local Test Relay
	port := 6688
	dbPath := t.TempDir() + "/load_test_relay.db"

	limitation := &nip11.Limitation{
		MaxLimit:        100000,
		MaxSubscriptions: 500, // Increase max subs for load test
	}
	store, err := relay.NewEventStore(dbPath, limitation)
	if err != nil {
		t.Fatalf("failed to create event store: %v", err)
	}
	defer store.Close()

	// Setup Handler
	// High buffer size for load test
	wsHandler := relay.NewSessionHandler(store, &nip11.Metadata{}, nil, relay.WithSessionBufferSize(10240), relay.WithSessionMaxConcurrentTasks(512))
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
	time.Sleep(200 * time.Millisecond)

	// 2. Load Production Relay List
	relaysFile := "tui/relays.txt"
	content, err := os.ReadFile(relaysFile)
	if err != nil {
		t.Fatalf("failed to read relays file: %v", err)
	}
	sources := strings.Split(string(content), "\n")

	// Filter empty lines
	var validSources []string
	for _, s := range sources {
		s = strings.TrimSpace(s)
		if s != "" {
			validSources = append(validSources, s)
		}
	}

	clientCount := len(validSources)
	t.Logf("Launching %d concurrent clients from relay list...", clientCount)

	var wg sync.WaitGroup
	wg.Add(clientCount)

	// Local Dest Relay
	localRelayURL := fmt.Sprintf("ws://localhost:%d", port)
	localURLParsed, _ := url.Parse(localRelayURL)

	for i, sourceRelay := range validSources {
		go func(id int, src string) {
			defer wg.Done()

			// Create Spec: From External Relay -> To Local Relay
			// This tests the flow: External -> Local

			uSrc, err := url.Parse(src)
			if err != nil {
				t.Logf("Skipping invalid url %s: %v", src, err)
				return // Don't fail whole test for one bad URL in list
			}

			fromFlow := &FlowSpec{
				Type:     FlOW_REMOTE,
				Relay:    src,
				Trusted:  true,
				relayURI: uSrc,
			}

			toFlow := &FlowSpec{
				Type:     FlOW_REMOTE,
				Relay:    localRelayURL,
				Trusted:  true,
				relayURI: localURLParsed,
			}

			// Empty filters = sync everything
			spec := &StreamSpec{
				Filters: []*FilterSpec{
					{
						SubscriptionFilter: nip01.SubscriptionFilter{}, // Empty filter
					},
				},
				From: []*FlowSpec{fromFlow},
				To:   []*FlowSpec{toFlow},
			}

			// Initialize private fields
			spec.filters = nip01.NewSubscriptionFilterGroup()
			spec.filters.Add(&spec.Filters[0].SubscriptionFilter)

			// Start Stream
			stream, err := NewStream(spec, false) // Quiet mode removed
			if err != nil {
				t.Logf("Client %d (%s) failed to create stream: %v", id, src, err)
				return
			}
			defer stream.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // 30s duration
			defer cancel()

			stream.Sync(ctx)

			<-ctx.Done()

		}(i, sourceRelay)
	}

	// Wait for all clients to finish (30s)
	wg.Wait()
	t.Log("Test finishing - context canceled errors after this point are normal.")
	t.Logf("Finished load test with %d clients", clientCount)
}
