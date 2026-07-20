package client

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip77"
	"github.com/ohstr/nmilat/wire"
	"github.com/ohstr/nmilat/relay"
	relayclient "github.com/ohstr/nmilat/relay/client"
)

// TestNegSync_Integration tests NIP-77 negentropy sync against real public
// relays, table-driven per relay. It uses a temp local DB and verifies the
// protocol converges and pulls events.
//
// Run with: go test -run TestNegSync_Integration -v -count=1
// Skip in short mode: go test -short
func TestNegSync_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Unbounded kind-0 reconciliation asks a relay to fingerprint every
	// profile it has ever seen, which trips guards like damus's "blocked:
	// too many query results" even with a Since window, since that check
	// looks at the total match count up front. Scope to a single pubkey
	// (fiatjaf's, verified via his NIP-05 well-known JSON) whose kind-0
	// profile is confirmed identical (same event ID) on both relays below
	// and doesn't churn, unlike PoW-vanity-mining bots that republish a new
	// kind-0 every few seconds and would make the negotiated ID stale by
	// the time we try to pull it.
	stableProfile := "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"

	tests := []struct {
		name      string
		relay     string
		filter    *nip01.SubscriptionFilter
		direction string
		maxRounds int
		minEvents int // minimum events expected to be found
	}{
		{
			name:  "kind 0 profiles from damus",
			relay: "wss://relay.damus.io",
			filter: &nip01.SubscriptionFilter{
				Kinds:   []int{0},
				Authors: []string{stableProfile},
			},
			direction: SyncDirectionDown,
			maxRounds: 500,
			minEvents: 1,
		},
		{
			// relay.primal.net has negentropy disabled entirely (confirmed via
			// probe: NEG-OPEN gets `NOTICE ERROR: bad msg: negentropy disabled`),
			// so nos.lol stands in as the second negentropy-capable relay here.
			name:  "kind 0 profiles from nos.lol",
			relay: "wss://nos.lol",
			filter: &nip01.SubscriptionFilter{
				Kinds:   []int{0},
				Authors: []string{stableProfile},
			},
			direction: SyncDirectionDown,
			maxRounds: 500,
			minEvents: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			// 1. Create temp DB
			tmpFile, err := os.CreateTemp("", "neg_sync_test_*.db")
			if err != nil {
				t.Fatalf("failed to create temp file: %v", err)
			}
			tmpPath := tmpFile.Name()
			tmpFile.Close()
			defer os.Remove(tmpPath)

			t.Logf("temp db: %s", tmpPath)

			// 2. Open local store (empty)
			store, err := relay.NewEventStore(tmpPath, &nip11.Limitation{})
			if err != nil {
				t.Fatalf("failed to open store: %v", err)
			}
			defer store.Close()

			// 3. Build client negentropy with 0 items (first sync)
			allItems := []nip77.Item{}
			neg := nip77.New(allItems)
			initMsg := neg.Initiate()
			initHex, err := initMsg.ToHex()
			if err != nil {
				t.Fatalf("failed to encode initial message: %v", err)
			}

			t.Logf("initial message: %d bytes", len(initHex)/2)

			// 4. Connect to relay
			relayURI, err := url.Parse(tc.relay)
			if err != nil {
				t.Fatalf("failed to parse relay URL: %v", err)
			}

			conn, err := relayclient.NewConnection(ctx, relayURI, nil)
			if err != nil {
				t.Fatalf("failed to connect to %s: %v", tc.relay, err)
			}
			defer conn.Close()

			t.Log("connected to relay")

			// 5. Send NEG-OPEN
			subID := uuid.NewString()
			negOpen := &wire.NegOpenPacket{
				SubscriptionID: subID,
				Filter:         tc.filter,
				Message:        initHex,
			}

			select {
			case conn.Outgoing() <- negOpen:
			case <-ctx.Done():
				t.Fatal("timeout sending NEG-OPEN")
			}

			t.Log("NEG-OPEN sent")

			// 6. Reconciliation loop
			var haveIDs []string
			var needIDs []string
			converged := false

			for round := 0; round < tc.maxRounds; round++ {
				var resp wire.SubscriptionResponse
				select {
				case resp = <-conn.Read():
				case err := <-conn.Errors():
					t.Fatalf("round %d: connection error: %v", round, err)
				case <-ctx.Done():
					t.Fatalf("round %d: timeout waiting for response", round)
				}

				switch r := resp.(type) {
				case *wire.NegMsgResponse:
					theirMsg, err := nip77.FromHex(r.Message)
					if err != nil {
						t.Fatalf("round %d: failed to decode NEG-MSG: %v", round, err)
					}

					responseMsg, roundHave, roundNeed, err := neg.Reconcile(theirMsg)
					if err != nil {
						t.Fatalf("round %d: reconciliation failed: %v", round, err)
					}

					haveIDs = append(haveIDs, roundHave...)
					needIDs = append(needIDs, roundNeed...)

					t.Logf("round %d: have=%d, need=%d, total_need=%d, ranges=%d",
						round+1, len(roundHave), len(roundNeed), len(needIDs), len(responseMsg.Ranges))

					if nip77.IsComplete(responseMsg) {
						converged = true
						t.Logf("converged after %d rounds", round+1)
						break
					}

					// Send next round
					respHex, err := responseMsg.ToHex()
					if err != nil {
						t.Fatalf("round %d: failed to encode response: %v", round, err)
					}

					select {
					case conn.Outgoing() <- &wire.NegMsgPacket{SubscriptionID: subID, Message: respHex}:
					case <-ctx.Done():
						t.Fatalf("round %d: timeout sending NEG-MSG", round)
					}

				case *wire.NegErrResponse:
					t.Fatalf("relay returned NEG-ERR: %s", r.Code)

				case *wire.NoticeSubscriptionResponse:
					t.Logf("NOTICE: %s", r.Message)
					continue

				default:
					t.Logf("unexpected response type: %T (skipping)", resp)
					continue
				}

				if converged {
					break
				}
			}

			// 7. Send NEG-CLOSE
			select {
			case conn.Outgoing() <- &wire.NegClosePacket{SubscriptionID: subID}:
			case <-ctx.Done():
			}

			// 8. Assertions
			t.Logf("=== Summary ===")
			t.Logf("Converged: %v", converged)
			t.Logf("IDs to pull (need): %d", len(needIDs))
			t.Logf("IDs to push (have): %d", len(haveIDs))

			if !converged {
				t.Errorf("reconciliation did not converge within %d rounds", tc.maxRounds)
			}

			if len(needIDs) < tc.minEvents {
				t.Errorf("expected at least %d events to pull, got %d", tc.minEvents, len(needIDs))
			}

			// 9. Pull a sample batch (max 10) to verify the IDs are valid
			if len(needIDs) > 0 && converged {
				sampleSize := 10
				if len(needIDs) < sampleSize {
					sampleSize = len(needIDs)
				}
				sampleIDs := needIDs[:sampleSize]

				t.Logf("pulling %d sample events to verify...", sampleSize)

				fg := nip01.NewSubscriptionFilterGroup()
				fg.Add(&nip01.SubscriptionFilter{IDs: sampleIDs})

				reqSubID := uuid.NewString()
				conn.SubscribeWithID(reqSubID, fg)

				var pulledEvents []*nip01.Event
				pullTimeout := time.After(15 * time.Second)

			pullLoop:
				for {
					select {
					case resp := <-conn.Read():
						switch r := resp.(type) {
						case *wire.EventSubscriptionResponse:
							if r.Event != nil {
								pulledEvents = append(pulledEvents, r.Event)
							}
						case *wire.EOSESubscriptionResponse:
							break pullLoop
						}
					case <-pullTimeout:
						t.Log("pull sample timeout")
						break pullLoop
					case <-ctx.Done():
						break pullLoop
					}
				}

				conn.CloseSubscription(reqSubID)

				t.Logf("pulled %d/%d sample events", len(pulledEvents), sampleSize)

				if len(pulledEvents) == 0 {
					t.Error("failed to pull any sample events — IDs may be invalid")
				} else {
					// Verify pulled events are kind 0
					for _, ev := range pulledEvents {
						if ev.Kind != 0 {
							t.Errorf("expected kind 0 event, got kind %d (ID=%s)", ev.Kind, ev.ID[:8])
						}
					}
					t.Logf("✓ all %d pulled events are valid kind 0 profiles", len(pulledEvents))
				}
			}
		})
	}
}

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
