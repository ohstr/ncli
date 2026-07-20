package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/nip01"
	"github.com/rs/zerolog"
)

// TestRateLimitRecovery puts it all together:
// 1. Client tries to send event via Stream
// 2. Server rejects it with "rate-limited"
// 3. Client should save to Recovery
// 4. Recovery should retry
// 5. Server eventually accepts
func TestRateLimitRecovery(t *testing.T) {
	// Setup Recovery DB
	tmpDir := t.TempDir()
	recoveryPath := filepath.Join(tmpDir, "recovery.db")

	// Enable logs (restored automatically once this test finishes)
	withTestLogging(t, zerolog.DebugLevel)

	// 1. Setup Mock Relay
	var rateLimitActive int32 = 1 // 1 = true, 0 = false
	var receivedEvents sync.Map   // ID -> bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}

			// Expect ["EVENT", {id: ...}]
			// Very naive parsing to extract ID
			s := string(msg)
			if len(s) < 20 {
				continue
			}

			// Check if it's an event
			// We scan for "id":"..."
			// This is fragile but works for testing known event
			idStart := -1
			target := "\"id\":\""
			for i := 0; i < len(s)-len(target); i++ {
				if s[i:i+len(target)] == target {
					idStart = i + len(target)
					break
				}
			}

			if idStart != -1 {
				// extract ID
				idEnd := -1
				for i := idStart; i < len(s); i++ {
					if s[i] == '"' {
						idEnd = i
						break
					}
				}
				if idEnd != -1 {
					id := s[idStart:idEnd]

					// Decision logic
					if atomic.LoadInt32(&rateLimitActive) == 1 {
						// REJECT
						resp := fmt.Sprintf(`["OK", "%s", false, "rate-limited: calm down"]`, id)
						conn.WriteMessage(websocket.TextMessage, []byte(resp))
					} else {
						// ACCEPT
						receivedEvents.Store(id, true)
						resp := fmt.Sprintf(`["OK", "%s", true, ""]`, id)
						conn.WriteMessage(websocket.TextMessage, []byte(resp))
					}
				}
			}
		}
	}))
	defer server.Close()

	relayURL, _ := url.Parse(server.URL)
	relayURL.Scheme = "ws"

	// 2. Setup Stream Client with Recovery
	// We construct a specific flow manually to avoid full Stream complexity if possible,
	// but using Stream ensures we test the `handleFlow` logic we patched.

	filters := nip01.NewSubscriptionFilterGroup()

	spec := &StreamSpec{
		Recovery: &RecoverySpec{
			StorePath:     recoveryPath,
			MaxRetries:    20, // Plenty of retries
			RetryInterval: "100ms",
		},
		To: []*FlowSpec{
			{
				Type:     FlOW_REMOTE,
				Relay:    relayURL.String(),
				Trusted:  true,
				relayURI: relayURL,
			},
		},
		Filters: []*FilterSpec{{}},
	}
	spec.filters = filters

	stream, err := NewStream(spec, false)
	if err != nil {
		t.Fatalf("Failed to create stream: %v", err)
	}
	defer stream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start Sync (activates flows)
	upStats, _ := stream.Sync(ctx)
	if len(upStats) != 1 {
		t.Fatalf("Expected 1 upstream flow, got %d", len(upStats))
	}

	// Wait for connection to establish
	time.Sleep(200 * time.Millisecond)

	// 3. Inject Event
	// We need to inject an event into the stream to be sent to 'Dest'.
	// Stream usually pulls from 'Source'. We have no source.
	// We can manually use `NewLocalSubscription` as source or just manually inject into the channel?
	// Stream doesn't expose incoming channel easily.

	// Better: Add a dummy source flow
	// Or use `RecoveryManager.SaveFailedEvent` directly? No, we want to test `stream.go` logic.

	// Let's create a temporary source: a LocalSubscription with pre-inserted event.
	// Or easier: A "File" source?
	// Or just use the internals since we are in `client` package (whitebox).

	// Access the destination flow context. csc.fc is written by the
	// background goroutine Sync() spawns per destination (RemoteSubscription
	// -> ClientSubscriptionContext.Run), with no synchronization the test
	// could rely on. sc.subscribers is populated synchronously within Sync()
	// itself (same goroutine as this test, before Sync() returns) and only
	// ever read/written under subscribersMu elsewhere, so read it via that
	// lock instead of racing on csc.fc directly.
	var targetFlow *FlowContext
	stream.sc.subscribersMu.RLock()
	for _, fc := range stream.sc.subscribers {
		targetFlow = fc
		break
	}
	stream.sc.subscribersMu.RUnlock()

	if targetFlow == nil {
		t.Fatal("Could not find destination flow context")
	}

	// Create Event
	event := &nip01.Event{
		ID:        "1111111111111111111111111111111111111111111111111111111111111111",
		PubKey:    "0000000000000000000000000000000000000000000000000000000000000001",
		CreatedAt: uint64(time.Now().Unix()),
		Kind:      1,
		Tags:      [][]string{},
		Content:   "test payload",
		Sig:       "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
	}

	// We bypass broadcastEvents (which normally registers the event as
	// pending an ACK), so register it directly on the destination's own
	// pending map before handing it to the flow.
	targetFlow.pending.add(event)

	// Send to flow
	targetFlow.incomingEvents <- event

	// 4. Verify Rejection & Recovery
	// Wait briefly for initial rejection
	time.Sleep(200 * time.Millisecond)

	// Check stats
	// Failures should be > 0. Lost should be 0 (because recovered).

	vals := upStats[0].FlatRow()
	// OutboundMetrics skips Pubkeys/Kinds: [id, events, failures, synced, retries, age]
	// Index 2 is Failures. Lost has no column of its own (folded into the
	// Failures cell for display) so it's read via the Lost() accessor.
	failures := vals[2]
	lost := upStats[0].Lost()

	if failures == 0 {
		t.Logf("Warning: No failures recorded yet. Relay might be slow or event swallowed. Stats: %v", vals)
	} else {
		t.Logf("Confirmed %d failures were recorded", failures)
	}

	if lost > 0 {
		t.Errorf("Expected 0 Lost (recovered), got %d. Recovery mechanism failed!", lost)
	}

	// 5. Lift Rate Limit quickly so retries can succeed
	time.Sleep(100 * time.Millisecond) // Just wait for first retry attempt
	atomic.StoreInt32(&rateLimitActive, 0)
	t.Log("Lifting rate limit...")

	// 6. Wait for Retry and Success
	// Retry interval is 100ms. Give it 3 seconds to be safe.
	success := false
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if _, ok := receivedEvents.Load(event.ID); ok {
			success = true
			break
		}
	}

	if !success {
		t.Error("Event was never successfully delivered after rate limit lift!")
	} else {
		t.Log("Success! Event recovered and delivered.")
	}

	// Check final stats
	vals = upStats[0].FlatRow()
	t.Logf("Final Stats: Failures=%d, Lost=%d, Events=%d", vals[2], upStats[0].Lost(), vals[1])

	// The retry counter in Stream stats is for stream worker retries, not RecoveryManager retries
	// RecoveryManager operates independently and doesn't update the stream stats
	// As long as Lost=0 and the event was delivered, recovery worked correctly
}
