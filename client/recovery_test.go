package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/nip01"
)

func TestRecoveryManager(t *testing.T) {
	// Setup temp dir for DB
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "recovery.db")

	// Create RecoveryManager
	rm, err := NewRecoveryManager(dbPath, 3, 100*time.Millisecond) // Fast retry for testing
	if err != nil {
		t.Fatalf("Failed to create recovery manager: %v", err)
	}
	defer rm.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rm.Start(ctx)

	// Mock Relay
	var receivedCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}

			// Simple relay protocol: ["EVENT", <event>]
			// Respond with ["OK", <id>, true, ""]
			// We assume we receive a valid event packet

			// We just parse enough to get ID or just always accept
			// But for "recovery", we expect it to send the event we stored.

			atomic.AddInt32(&receivedCount, 1)

			// Send OK (fake ID for simplicity if we don't parse)
			// But the client expects ID match.
			// Let's parse loosely
			// Actually, client/connection sends ["EVENT", event].
			// We can just find the ID string in the message for this test

			// Sending generic OK
			// We need to parse to get ID.
			// msg is JSON.
			// ["EVENT", { "id": "..." }]

			// Minimal hacky parsing
			// s := string(msg)
			// Extract ID...
			// or just respond with the ID we know we sent?
			// But RM sends different events?

			// Let's assume we test with known ID.

			// But wait, connection.Read expects ["OK", id, true, msg]

			// Let's try to pass the ID back if we can find it.
			// Or we just cheat and say we received it.

			// For a robust test, we want to confirm RM successfully sent it and got OK.
			// So we must reply OK.

			// find "id":"..."
			// Or use a proper parser?
			// ignoring for now, will fix if needed.
			// Actually connection.Send waits for OK matching ID.
			// If we don't send OK with correct ID, RM will timeout.

			// Just send a hardcoded OK for the event ID we are testing
			id := "4d6d9c5b65123456789012345678901234567890123456789012345678901234"
			response := fmt.Sprintf(`["OK", "%s", true, "saved"]`, id)
			conn.WriteMessage(websocket.TextMessage, []byte(response))
		}
	}))
	defer server.Close()

	// Convert http URL to ws
	u, _ := url.Parse(server.URL)
	u.Scheme = "ws"

	// Create a dummy event
	// Valid hex strings
	validID := "4d6d9c5b65123456789012345678901234567890123456789012345678901234"
	validPub := "0000000000000000000000000000000000000000000000000000000000000001"
	validSig := "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

	event := &nip01.Event{
		ID:        validID,
		PubKey:    validPub,
		CreatedAt: uint64(time.Now().Unix()),
		Kind:      1,
		Tags:      [][]string{},
		Content:   "test content",
		Sig:       validSig,
	}

	// 1. Save Failed Event
	err = rm.SaveFailedEvent(event, u.String(), fmt.Errorf("simulated failure"))
	if err != nil {
		t.Errorf("SaveFailedEvent failed: %v", err)
	}

	// 2. Wait for retry
	// We configured 100ms interval.
	time.Sleep(1 * time.Second)

	// 3. Check if received
	count := atomic.LoadInt32(&receivedCount)
	if count == 0 {
		t.Error("Relay did not receive retried event")
	} else {
		t.Logf("Relay received %d retry attempts", count)
	}

	// Check if metadata is gone (success)
	// We can't check private DB easily, but we can check if it stops retrying.
	// If it kept retrying, count would increase every 100ms.
	// Reset count
	atomic.StoreInt32(&receivedCount, 0)
	time.Sleep(500 * time.Millisecond)
	if atomic.LoadInt32(&receivedCount) > 0 {
		t.Error("RecoveryManager kept retrying after success!")
	}
}

// mockRelayBehavior controls how newMockRelay's upgrade handler responds to
// an incoming EVENT frame.
type mockRelayBehavior int

const (
	mockRelayAccept mockRelayBehavior = iota
	mockRelayReject
	mockRelayHang // never responds
)

// newMockRelay starts a minimal websocket relay for recovery tests: it
// replies to any EVENT frame with a naive OK response and, if received is
// non-nil, records how many events it saw.
func newMockRelay(t *testing.T, behavior mockRelayBehavior, received *atomic.Int32) *httptest.Server {
	t.Helper()

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

			if behavior == mockRelayHang {
				continue // never respond; let the caller's context time out
			}

			s := string(msg)
			target := "\"id\":\""
			idStart := -1
			for i := 0; i < len(s)-len(target); i++ {
				if s[i:i+len(target)] == target {
					idStart = i + len(target)
					break
				}
			}
			if idStart == -1 {
				continue
			}
			idEnd := idStart
			for idEnd < len(s) && s[idEnd] != '"' {
				idEnd++
			}
			id := s[idStart:idEnd]

			if received != nil {
				received.Add(1)
			}

			if behavior == mockRelayAccept {
				conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`["OK", "%s", true, ""]`, id)))
			} else {
				conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`["OK", "%s", false, "rejected"]`, id)))
			}
		}
	}))

	t.Cleanup(server.Close)
	return server
}

func mockRelayWSURL(server *httptest.Server) string {
	u, _ := url.Parse(server.URL)
	u.Scheme = "ws"
	return u.String()
}

func newRecoveryTestEvent(id string) *nip01.Event {
	return &nip01.Event{
		ID:        id,
		PubKey:    fmt.Sprintf("%064x", 1),
		CreatedAt: uint64(time.Now().Unix()),
		Kind:      1,
		Tags:      [][]string{},
		Content:   "recovery test event",
	}
}

func waitForPendingCount(t *testing.T, rm *RecoveryManager, want int, timeout time.Duration) []*RetryMeta {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		pending := rm.collectPending(true)
		if len(pending) == want {
			return pending
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d pending recovery entries, got %d", want, len(pending))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRecoveryFlushOnStopDeliversPending confirms Stop() attempts immediate
// delivery of everything still pending, rather than only draining the
// in-memory save queue and leaving persisted entries to wait for the next
// retryInterval tick (which, with a long interval as configured here, would
// never come before the process exits).
func TestRecoveryFlushOnStopDeliversPending(t *testing.T) {
	var received atomic.Int32
	server := newMockRelay(t, mockRelayAccept, &received)
	dest := mockRelayWSURL(server)

	rm, err := NewRecoveryManager(filepath.Join(t.TempDir(), "recovery.db"), 5, time.Hour)
	if err != nil {
		t.Fatalf("NewRecoveryManager failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rm.Start(ctx)

	ev := newRecoveryTestEvent(fmt.Sprintf("%064x", 42))
	if err := rm.SaveFailedEvent(ev, dest, fmt.Errorf("simulated failure")); err != nil {
		t.Fatalf("SaveFailedEvent failed: %v", err)
	}

	waitForPendingCount(t, rm, 1, 2*time.Second)

	// retryInterval is 1 hour, so if Stop() only relied on the periodic
	// ticker, nothing would be delivered here.
	rm.Stop()

	if got := received.Load(); got != 1 {
		t.Errorf("expected the mock relay to receive exactly 1 event via the exit-time flush, got %d", got)
	}

	if pending := rm.collectPending(true); len(pending) != 0 {
		t.Errorf("expected the recovered entry to be cleared after successful delivery, got %d still pending", len(pending))
	}
}

// TestRecoveryShutdownCancelDoesNotPenalizeAttempts confirms that when an
// outer context (e.g. the exit-time flush's bounded deadline) cuts off a
// publish attempt, the entry's Attempts counter is left untouched -- as
// opposed to a genuine rejection from the destination, which must still
// increment it.
func TestRecoveryShutdownCancelDoesNotPenalizeAttempts(t *testing.T) {
	hangServer := newMockRelay(t, mockRelayHang, nil)
	hangDest := mockRelayWSURL(hangServer)

	rejectServer := newMockRelay(t, mockRelayReject, nil)
	rejectDest := mockRelayWSURL(rejectServer)

	rm, err := NewRecoveryManager(filepath.Join(t.TempDir(), "recovery.db"), 5, time.Hour)
	if err != nil {
		t.Fatalf("NewRecoveryManager failed: %v", err)
	}
	defer rm.store.Close()

	hangEvent := newRecoveryTestEvent(fmt.Sprintf("%064x", 1))
	rejectEvent := newRecoveryTestEvent(fmt.Sprintf("%064x", 2))

	if err := rm.saveEventSync(context.Background(), hangEvent, hangDest, fmt.Errorf("simulated failure")); err != nil {
		t.Fatalf("saveEventSync (hang) failed: %v", err)
	}
	if err := rm.saveEventSync(context.Background(), rejectEvent, rejectDest, fmt.Errorf("simulated failure")); err != nil {
		t.Fatalf("saveEventSync (reject) failed: %v", err)
	}

	// Interrupted by an outer deadline -- the mock relay never responds, so
	// simplePublish blocks until shortCtx (not its own internal 10s timeout)
	// ends the attempt first.
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	rm.processBatch(shortCtx, hangDest, []*RetryMeta{{EventID: hangEvent.ID, Destination: hangDest}})
	shortCancel()

	// Genuine rejection -- plenty of time, no outer cancellation involved.
	longCtx, longCancel := context.WithTimeout(context.Background(), 5*time.Second)
	rm.processBatch(longCtx, rejectDest, []*RetryMeta{{EventID: rejectEvent.ID, Destination: rejectDest}})
	longCancel()

	pending := rm.collectPending(true)
	var hangMeta, rejectMeta *RetryMeta
	for _, m := range pending {
		switch m.EventID {
		case hangEvent.ID:
			hangMeta = m
		case rejectEvent.ID:
			rejectMeta = m
		}
	}

	if hangMeta == nil {
		t.Fatal("expected the shutdown-interrupted entry to still be pending")
	}
	if hangMeta.Attempts != 0 {
		t.Errorf("shutdown-interrupted attempt should not increment Attempts, got %d", hangMeta.Attempts)
	}

	if rejectMeta == nil {
		t.Fatal("expected the genuinely-rejected entry to still be pending (below maxRetries)")
	}
	if rejectMeta.Attempts != 1 {
		t.Errorf("genuine rejection should increment Attempts, got %d", rejectMeta.Attempts)
	}
}
