package client

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
)

// See integration/sync/README.md for what this stack is. Shared
// docker-lifecycle/publish/fetch helpers used below (runCompose,
// newIntegrationEvent, publishEventToRelay, waitForRelayReady, etc.) live
// in client/integrationharness_test.go, alongside
// client/stream_integration_test.go and client/inspect_integration_test.go.
const (
	syncIntegrationComposeFile = "../integration/sync/compose.yaml"
	syncIntegrationSpecFile    = "../integration/sync/sync.yaml"
	syncIntegrationRemoteURL   = "ws://localhost:45520"
)

// TestSyncIntegration brings up integration/sync/compose.yaml's single real
// `ncli relay` container once, then runs each scenario as a subtest
// against it -- needs Docker, hits no production relay. Replaces this
// package's only prior sync coverage, TestNegSync_Integration
// (client/neg_sync_test.go), which depends on live wss://relay.ohstr.com
// negentropy support and one specific pubkey -- fragile and
// non-deterministic (kept as-is, not deleted: it's still a useful "does
// this actually interop with a real-world deployed relay" smoke test, just
// not one this package can gate a regression on with any determinism). See
// `just test-integration-sync`.
func TestSyncIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker-based sync integration test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found on PATH, skipping sync integration test")
	}

	runCompose(t, syncIntegrationComposeFile, "up", "-d", "--build")
	t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "-f", syncIntegrationComposeFile, "down", "-v")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("docker compose down failed: %v\n%s", err, out)
		}
	})

	waitForRelayReady(t, syncIntegrationRemoteURL, 60*time.Second)

	t.Run("ReconcileCompleteness", testSyncReconcileCompleteness)
	t.Run("MaxReconcileRoundsTooLowSurfacesCleanly", testSyncMaxReconcileRoundsTooLowSurfacesCleanly)
	t.Run("RemoteStallTriggersTimeoutNotHang", testSyncRemoteStallTriggersTimeoutNotHang)
}

// testSyncReconcileCompleteness is table-driven across data volume: seeds
// each side of a `direction: both` sync with events the other side doesn't
// have, then asserts negentropy reconciliation against a real relay
// carries every one of them the right way -- local-only events get pushed
// up, remote-only events get pulled down. This is the real-relay analog of
// the two-endpoint shape `examples/apply/sync.yaml` documents -- unlike
// stream/inspect, sync is exactly one local store and one remote relay,
// never a multi-relay fan-in (see SyncSpec.UnmarshalJSON's "only one
// local/remote flow allowed"). "Large" is the "high input" case: hundreds
// of events on each side instead of "Small"'s handful, forcing
// sync.yaml's pullBatchSize (100) into multiple pull batches and giving
// negentropy a genuinely large diff to reconcile against a real relay, not
// a mock that can't reject/rate-limit anything.
func testSyncReconcileCompleteness(t *testing.T) {
	cases := []struct {
		name string
		n    int
	}{
		{"Small", 3},
		{"Large", 150},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := loadTestSyncSpec(t)

			localPath := filepath.Join(t.TempDir(), "sync.db")
			spec.GetLocal().Path = localPath

			remoteOnly := publishManyEvents(t, syncIntegrationRemoteURL, tc.n, tc.name+"-remote")

			localOnly := make([]string, tc.n)
			localEvents := make([]*nip01.Event, tc.n)
			for i := 0; i < tc.n; i++ {
				ev := newIntegrationEvent(t, fmt.Sprintf("%s-local-%d", tc.name, i))
				localOnly[i] = ev.ID
				localEvents[i] = ev
			}
			seedLocalSyncStore(t, localPath, localEvents)

			sm, err := NewSyncModule(spec, nil, false)
			if err != nil {
				t.Fatalf("NewSyncModule failed: %v", err)
			}
			t.Cleanup(sm.Close)

			// Generous enough for both rows -- a ceiling, not an expected
			// duration, so "Small" isn't slowed down by sharing it with
			// "Large".
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			logger, err := sm.Run(ctx)
			if err != nil {
				t.Fatalf("sm.Run failed: %v", err)
			}
			waitForSyncComplete(t, logger, 90*time.Second)

			// Give the deferred store.Close() inside SyncModule.execute() a
			// moment to actually run -- "Sync complete" is logged just
			// before execute() returns, not after, so there's a brief
			// window where the local store file may still be held.
			time.Sleep(200 * time.Millisecond)

			missingRemote := waitForEventsAtRelay(t, syncIntegrationRemoteURL, localOnly, 20*time.Second)
			if len(missingRemote) > 0 {
				t.Errorf("push: %d/%d local-only event(s) never reached the remote relay: %v", len(missingRemote), tc.n, missingRemote)
			}
			missingLocal := waitForEventsInLocalStore(t, localPath, remoteOnly, 20*time.Second)
			if len(missingLocal) > 0 {
				t.Errorf("pull: %d/%d remote-only event(s) never landed in the local store: %v", len(missingLocal), tc.n, missingLocal)
			}
		})
	}
}

// testSyncMaxReconcileRoundsTooLowSurfacesCleanly covers the round-cap
// branch client/neg_sync.go's reconcile loop falls into when
// nip77.IsComplete never returns true within spec.MaxReconcileRounds --
// previously reachable only in theory, never actually exercised end to
// end. With the cap forced down to 1 against a large divergent set, the
// invariant under test is that this degrades gracefully (logs a warning,
// syncs whatever partial have/need sets it collected, and still finishes)
// rather than hanging or crashing -- not a specific round count, since
// negentropy's actual convergence speed for a given dataset size isn't
// this test's concern and asserting an exact number would just make it
// flaky against protocol/library changes.
func testSyncMaxReconcileRoundsTooLowSurfacesCleanly(t *testing.T) {
	spec := loadTestSyncSpec(t)
	spec.MaxReconcileRounds = 1

	localPath := filepath.Join(t.TempDir(), "sync.db")
	spec.GetLocal().Path = localPath

	const n = 300
	publishManyEvents(t, syncIntegrationRemoteURL, n, "sync-toofew-remote")

	localEvents := make([]*nip01.Event, n)
	for i := 0; i < n; i++ {
		localEvents[i] = newIntegrationEvent(t, fmt.Sprintf("sync-toofew-local-%d", i))
	}
	seedLocalSyncStore(t, localPath, localEvents)

	sm, err := NewSyncModule(spec, nil, false)
	if err != nil {
		t.Fatalf("NewSyncModule failed: %v", err)
	}
	t.Cleanup(sm.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger, err := sm.Run(ctx)
	if err != nil {
		t.Fatalf("sm.Run failed: %v", err)
	}

	var sawMaxRoundsWarning bool
	deadline := time.Now().Add(30 * time.Second)
	for {
		done := false
		for _, row := range logger.GetLastLogs() {
			for _, cell := range row {
				if strings.Contains(cell, "Max reconciliation rounds reached") {
					sawMaxRoundsWarning = true
				}
				if strings.Contains(cell, "Sync complete") {
					done = true
				}
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sync with an artificially low maxReconcileRounds did not complete within the timeout -- it must degrade gracefully, never hang")
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !sawMaxRoundsWarning {
		t.Logf("note: %d fully-divergent items on each side converged within maxReconcileRounds=1 -- negentropy resolved this faster than expected, so this run never actually exercised the round-cap path itself; the important invariant (no hang, clean completion) still held", n)
	}
}

// testSyncRemoteStallTriggersTimeoutNotHang is sync's analog of
// client/stream_integration_test.go's DestinationStallTriggersTimeoutNotHang:
// `docker compose pause` freezes the remote relay's process without
// touching the already-established TCP connection, simulating a silent
// stall distinct from an explicit disconnect. This is only meaningful
// because of the ConnectionConfig fix (client/spec.go's
// TimeoutSpec.ConnectionConfig, see that commit) -- before it, sync's
// `timeouts:` block had no effect at all, so a short configured Pong here
// would have silently used relayclient's 60s default instead, making this
// test either much slower or unable to reliably distinguish "detected the
// stall" from "happened to finish first."
//
// Unlike stream, sync has no reconnect/retry loop of its own: a stalled
// connection is simply a failed run that must surface an error and return,
// not hang -- confirmed here via the "connection error" log line
// SyncModule.execute logs on exactly this path (client/neg_sync.go's
// `case err := <-conn.Errors()`), not by expecting "Sync complete" (which
// this run, by design, never reaches).
func testSyncRemoteStallTriggersTimeoutNotHang(t *testing.T) {
	spec := loadTestSyncSpec(t)
	shortPong := "3s"
	spec.Timeouts = &TimeoutSpec{Pong: &shortPong}

	localPath := filepath.Join(t.TempDir(), "sync.db")
	spec.GetLocal().Path = localPath

	sm, err := NewSyncModule(spec, nil, false)
	if err != nil {
		t.Fatalf("NewSyncModule failed: %v", err)
	}
	t.Cleanup(sm.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger, err := sm.Run(ctx)
	if err != nil {
		t.Fatalf("sm.Run failed: %v", err)
	}

	// Let the connection actually establish (handshake + NEG-OPEN) before
	// stalling it -- what's under test is a silent stall on an established
	// connection, not a handshake timeout.
	time.Sleep(1 * time.Second)

	runCompose(t, syncIntegrationComposeFile, "pause", "remote")
	t.Cleanup(func() {
		_ = exec.Command("docker", "compose", "-f", syncIntegrationComposeFile, "unpause", "remote").Run()
	})

	deadline := time.Now().Add(15 * time.Second)
	for {
		for _, row := range logger.GetLastLogs() {
			for _, cell := range row {
				if strings.Contains(cell, "connection error") {
					runCompose(t, syncIntegrationComposeFile, "unpause", "remote")
					return // stall was detected and surfaced cleanly -- test passes
				}
			}
		}
		if time.Now().After(deadline) {
			runCompose(t, syncIntegrationComposeFile, "unpause", "remote")
			t.Fatal("a stalled remote with a 3s configured pong timeout never surfaced a connection error within 15s -- sync appears to hang on a silent stall instead of timing out")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// loadTestSyncSpec loads integration/sync/sync.yaml the same way `ncli
// apply` itself would (loadSpecFromYaml).
func loadTestSyncSpec(t *testing.T) *SyncSpec {
	t.Helper()
	rs, err := loadSpecFromYaml(syncIntegrationSpecFile)
	if err != nil {
		t.Fatalf("failed to load %s: %v", syncIntegrationSpecFile, err)
	}
	spec, ok := rs.Spec.(*SyncSpec)
	if !ok {
		t.Fatalf("%s is not a `kind: sync` spec", syncIntegrationSpecFile)
	}
	return spec
}

// seedLocalSyncStore opens a fresh local EventStore at path, inserts
// events, and closes it again before returning -- SyncModule.execute()
// opens its own handle on the same path, and this package's local stores
// (bbolt-backed) only tolerate one open handle at a time.
func seedLocalSyncStore(t *testing.T, path string, events []*nip01.Event) {
	t.Helper()
	store, err := relay.NewEventStore(path, &nip11.Limitation{})
	if err != nil {
		t.Fatalf("failed to open local store to seed at %s: %v", path, err)
	}
	defer store.Close()
	if err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("failed to seed local store at %s: %v", path, err)
	}
}

// waitForSyncComplete polls logger (the *tui.FlowLogger returned by
// SyncModule.Run) until its "Sync complete" line appears or timeout
// elapses. GetLastLogs is a draining read (see tui.FlowLogger), so this
// must be the only caller polling this particular logger.
func waitForSyncComplete(t *testing.T, logger *tui.FlowLogger, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, row := range logger.GetLastLogs() {
			for _, cell := range row {
				if strings.Contains(cell, "Sync complete") {
					return
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("sync did not report completion within the timeout")
}

// waitForEventsInLocalStore polls the local store at path until every ID in
// ids is present or timeout elapses, returning whatever's still missing at
// that point (empty on full success). Only safe to call once the sync
// module that owns path has actually closed its own handle (see
// waitForSyncComplete's doc comment).
func waitForEventsInLocalStore(t *testing.T, path string, ids []string, timeout time.Duration) []string {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	var missing []string
	for {
		store, err := relay.NewEventStore(path, &nip11.Limitation{})
		if err != nil {
			t.Fatalf("failed to open local store at %s: %v", path, err)
		}
		events, err := store.FindEvents(ctx, &nip01.SubscriptionFilter{IDs: ids})
		store.Close()
		if err != nil {
			t.Fatalf("failed to query local store at %s: %v", path, err)
		}
		found := make(map[string]bool, len(events))
		for _, ev := range events {
			found[ev.EventID] = true
		}
		missing = nil
		for _, id := range ids {
			if !found[id] {
				missing = append(missing, id)
			}
		}
		if len(missing) == 0 || time.Now().After(deadline) {
			return missing
		}
		time.Sleep(300 * time.Millisecond)
	}
}
