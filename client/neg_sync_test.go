package client

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip77"
	"github.com/ohstr/nmilat/relay"
	relayclient "github.com/ohstr/nmilat/relay/client"
)

// TestNegReconcileLocalOnly tests the full client-server reconciliation loop locally
// without any network calls — pure in-process Negentropy wire.
func TestNegReconcileLocalOnly(t *testing.T) {
	tests := []struct {
		name             string
		clientTimestamps []uint64
		serverTimestamps []uint64
		expectNeed       int
		expectHave       int
	}{
		{
			name:             "empty client, server has 10",
			clientTimestamps: []uint64{},
			serverTimestamps: makeTimestamps(10),
			expectNeed:       10,
			expectHave:       0,
		},
		{
			name:             "empty server, client has 10",
			clientTimestamps: makeTimestamps(10),
			serverTimestamps: []uint64{},
			expectNeed:       0,
			expectHave:       10,
		},
		{
			name:             "both have same 50",
			clientTimestamps: makeTimestamps(50),
			serverTimestamps: makeTimestamps(50),
			expectNeed:       0,
			expectHave:       0,
		},
		{
			name:             "50% overlap (client 1-100, server 51-150)",
			clientTimestamps: makeRange(1, 100),
			serverTimestamps: makeRange(51, 150),
			expectNeed:       50,
			expectHave:       50,
		},
		{
			name:             "large disjoint (client 200, server 200)",
			clientTimestamps: makeRange(1, 200),
			serverTimestamps: makeRange(201, 400),
			expectNeed:       200,
			expectHave:       200,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clientItems := timestampsToItems(tc.clientTimestamps)
			serverItems := timestampsToItems(tc.serverTimestamps)

			clientNeg := nip77.New(clientItems)
			serverNeg := nip77.New(serverItems)

			clientMsg := clientNeg.Initiate()

			var totalHave, totalNeed []string
			maxRounds := 100

			for round := 0; round < maxRounds; round++ {
				serverResp, _, _, err := serverNeg.Reconcile(clientMsg)
				if err != nil {
					t.Fatalf("round %d: server error: %v", round, err)
				}

				clientResp, roundHave, roundNeed, err := clientNeg.Reconcile(serverResp)
				if err != nil {
					t.Fatalf("round %d: client error: %v", round, err)
				}

				totalHave = append(totalHave, roundHave...)
				totalNeed = append(totalNeed, roundNeed...)

				if nip77.IsComplete(clientResp) {
					t.Logf("converged in %d rounds (need=%d, have=%d)", round+1, len(totalNeed), len(totalHave))
					break
				}

				clientMsg = clientResp

				if round == maxRounds-1 {
					t.Errorf("did not converge within %d rounds", maxRounds)
				}
			}

			if len(totalNeed) != tc.expectNeed {
				t.Errorf("need: got %d, want %d", len(totalNeed), tc.expectNeed)
			}
			if len(totalHave) != tc.expectHave {
				t.Errorf("have: got %d, want %d", len(totalHave), tc.expectHave)
			}
		})
	}
}

func makeTimestamps(n int) []uint64 {
	ts := make([]uint64, n)
	for i := 0; i < n; i++ {
		ts[i] = uint64(1000 + i*10)
	}
	return ts
}

func makeRange(from, to int) []uint64 {
	ts := make([]uint64, 0, to-from+1)
	for i := from; i <= to; i++ {
		ts = append(ts, uint64(i*10))
	}
	return ts
}

func timestampsToItems(timestamps []uint64) []nip77.Item {
	items := make([]nip77.Item, len(timestamps))
	for i, ts := range timestamps {
		items[i] = nip77.Item{Timestamp: ts}
		// Deterministic IDs from timestamp
		b := []byte(fmt.Sprintf("%016x", ts))
		copy(items[i].ID[:], b)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Compare(items[j]) < 0
	})
	return items
}

// newPushTestStore opens a fresh local store and seeds it with n events,
// returning the store, their IDs, and the store's path.
func newPushTestStore(t *testing.T, n int) (*relay.EventStore, []string) {
	t.Helper()
	store, err := relay.NewEventStore(filepath.Join(t.TempDir(), "push.db"), &nip11.Limitation{})
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(store.Close)

	ids := make([]string, n)
	events := make([]*nip01.Event, n)
	for i := 0; i < n; i++ {
		ev := newRegressionTestEvent(i)
		ids[i] = ev.ID
		events[i] = ev
	}
	if err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("failed to seed store: %v", err)
	}
	return store, ids
}

func connectToMockRelay(t *testing.T, ctx context.Context, server *httptest.Server) *relayclient.Connection {
	t.Helper()
	relayURI, err := url.Parse(mockRelayWSURL(server))
	if err != nil {
		t.Fatalf("invalid relay URL: %v", err)
	}
	conn, err := relayclient.NewConnection(ctx, relayURI, nil)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	t.Cleanup(conn.Close)
	return conn
}

// TestSyncPushEventsWaitsForRealAcks is a regression guard: pushEvents used
// to count an event as pushed the instant conn.Send returned true --  a
// purely local "did the write call succeed" signal, not "did the remote
// actually receive it". Against a relay that genuinely accepts every event,
// pushEvents must still converge and report every event delivered.
func TestSyncPushEventsWaitsForRealAcks(t *testing.T) {
	var received atomic.Int32
	server := newMockRelay(t, mockRelayAccept, &received)

	const n = 5
	store, haveIDs := newPushTestStore(t, n)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := connectToMockRelay(t, ctx, server)

	s := &SyncModule{spec: &SyncSpec{PullBatchSize: 10}, logger: &tui.FlowLogger{}}
	s.pushEvents(ctx, conn, store, haveIDs)

	if got := received.Load(); got != n {
		t.Errorf("mock relay saw %d/%d EVENT frames", got, n)
	}
	logs := flattenLoggerText(s.logger)
	if !strings.Contains(logs, fmt.Sprintf("Pushed %d events", n)) {
		t.Errorf("expected a summary reporting all %d events pushed, got logs: %v", n, logs)
	}
}

// TestSyncPushEventsDoesNotHangOnStalledAcks is the critical regression
// guard: pushEvents used to have zero awareness of whether the remote was
// even still there -- a plain `for _, ev := range events { conn.Send(ev) }`
// loop with no error/timeout handling at all. Against a relay that silently
// swallows every EVENT frame (a stall, or a relay that's simply gone quiet),
// pushEvents must still return within its bounded ack-wait window instead of
// hanging, and must say so rather than silently reporting success.
func TestSyncPushEventsDoesNotHangOnStalledAcks(t *testing.T) {
	orig := pushAckTimeout
	pushAckTimeout = 300 * time.Millisecond
	t.Cleanup(func() { pushAckTimeout = orig })

	server := newMockRelay(t, mockRelayHang, nil)

	const n = 3
	store, haveIDs := newPushTestStore(t, n)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := connectToMockRelay(t, ctx, server)

	s := &SyncModule{spec: &SyncSpec{PullBatchSize: 10}, logger: &tui.FlowLogger{}}

	done := make(chan struct{})
	go func() {
		s.pushEvents(ctx, conn, store, haveIDs)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pushEvents hung well past its configured ack-wait timeout instead of giving up on a stalled remote")
	}

	logs := flattenLoggerText(s.logger)
	if !strings.Contains(logs, "never got an ack") {
		t.Errorf("expected a warning that events never got acked, got logs: %v", logs)
	}
	if strings.Contains(logs, fmt.Sprintf("Pushed %d events", n)) {
		t.Errorf("a relay that never acked anything must not be reported as having received all %d events", n)
	}
}

// flattenLoggerText joins every log cell logger has recorded so far into one
// string, for a simple substring check instead of walking [][]string by hand.
func flattenLoggerText(logger *tui.FlowLogger) string {
	var sb strings.Builder
	for _, row := range logger.GetLastLogs() {
		for _, cell := range row {
			sb.WriteString(cell)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
