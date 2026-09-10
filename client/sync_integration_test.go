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

// See integration/sync/README.md. Shared helpers live in
// client/integrationharness_test.go.
const (
	syncIntegrationComposeFile = "../integration/sync/compose.yaml"
	syncIntegrationSpecFile    = "../integration/sync/sync.yaml"
	syncIntegrationRemoteURL   = "ws://localhost:45520"
)

// TestSyncIntegration brings up compose.yaml's one real relay container
// once, then runs each scenario as a subtest. Needs Docker.
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
	t.Run("FilterCorrectness", testSyncFilterCorrectness)
	t.Run("MaxReconcileRoundsTooLowSurfacesCleanly", testSyncMaxReconcileRoundsTooLowSurfacesCleanly)
	t.Run("RemoteStallTriggersTimeoutNotHang", testSyncRemoteStallTriggersTimeoutNotHang)
}

// testSyncReconcileCompleteness: seeds each side of a `direction: both`
// sync with events the other side lacks, asserts both directions land.
// "Large" forces pullBatchSize into multiple batches.
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

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			logger, err := sm.Run(ctx)
			if err != nil {
				t.Fatalf("sm.Run failed: %v", err)
			}
			waitForSyncComplete(t, logger, 90*time.Second)
			time.Sleep(200 * time.Millisecond) // let deferred store.Close() run

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

// testSyncFilterCorrectness proves sync only reconciles events matching
// its filter, both directions. A single filter with 2 kinds + 1 author --
// not multiple filter objects, since only Filters[0] reaches NEG-OPEN
// (see integration/sync/README.md's "Confirmed gap").
func testSyncFilterCorrectness(t *testing.T) {
	spec := loadTestSyncSpec(t)
	spec.Filters = []*FilterSpec{
		NewFilterSpec(&nip01.SubscriptionFilter{
			Kinds:   []int{1, 7},
			Authors: []string{integrationPubKey},
		}),
	}

	localPath := filepath.Join(t.TempDir(), "sync.db")
	spec.GetLocal().Path = localPath

	matchRemote1 := newIntegrationEventOfKind(t, 1, "filter-remote-match-k1")
	matchRemote2 := newIntegrationEventOfKind(t, 7, "filter-remote-match-k7")
	noMatchRemoteAuthor := newIntegrationEventFromAltAuthor(t, 7, "filter-remote-nomatch-author")
	noMatchRemoteKind := newIntegrationEventOfKind(t, 3, "filter-remote-nomatch-kind")
	for _, ev := range []*nip01.Event{matchRemote1, matchRemote2, noMatchRemoteAuthor, noMatchRemoteKind} {
		publishEventToRelay(t, syncIntegrationRemoteURL, ev)
	}

	matchLocal1 := newIntegrationEventOfKind(t, 1, "filter-local-match-k1")
	matchLocal2 := newIntegrationEventOfKind(t, 7, "filter-local-match-k7")
	noMatchLocalAuthor := newIntegrationEventFromAltAuthor(t, 7, "filter-local-nomatch-author")
	noMatchLocalKind := newIntegrationEventOfKind(t, 3, "filter-local-nomatch-kind")
	seedLocalSyncStore(t, localPath, []*nip01.Event{matchLocal1, matchLocal2, noMatchLocalAuthor, noMatchLocalKind})

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
	waitForSyncComplete(t, logger, 30*time.Second)
	time.Sleep(200 * time.Millisecond) // let deferred store.Close() run

	missingRemote := waitForEventsAtRelay(t, syncIntegrationRemoteURL, []string{matchLocal1.ID, matchLocal2.ID}, 10*time.Second)
	if len(missingRemote) > 0 {
		t.Errorf("push: %d filter-matching local-only event(s) never reached the remote relay: %v", len(missingRemote), missingRemote)
	}
	missingLocal := waitForEventsInLocalStore(t, localPath, []string{matchRemote1.ID, matchRemote2.ID}, 10*time.Second)
	if len(missingLocal) > 0 {
		t.Errorf("pull: %d filter-matching remote-only event(s) never landed in the local store: %v", len(missingLocal), missingLocal)
	}

	leakedToRemote := fetchEventIDsFromRelay(t, syncIntegrationRemoteURL, []string{noMatchLocalAuthor.ID, noMatchLocalKind.ID})
	if len(leakedToRemote) > 0 {
		t.Errorf("%d non-matching local-only event(s) reached the remote relay anyway: %v", len(leakedToRemote), leakedToRemote)
	}

	// Timeout 0: single snapshot check (sync already completed above).
	// stillAbsent's length should equal the input length -- anything less
	// means something leaked in.
	stillAbsent := waitForEventsInLocalStore(t, localPath, []string{noMatchRemoteAuthor.ID, noMatchRemoteKind.ID}, 0)
	if len(stillAbsent) != 2 {
		t.Errorf("expected both non-matching remote-only events to stay out of the local store, but %d leaked in", 2-len(stillAbsent))
	}
}

// testSyncMaxReconcileRoundsTooLowSurfacesCleanly: forces MaxReconcileRounds
// to 1 against a large divergent set. Must degrade gracefully (partial
// sync, no hang), not assert an exact round count.
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
		t.Logf("note: %d items converged within maxReconcileRounds=1, faster than expected -- round-cap path not actually exercised this run, but no-hang invariant held", n)
	}
}

// testSyncRemoteStallTriggersTimeoutNotHang: `docker compose pause` on the
// remote, with a short configured Pong. Sync has no reconnect loop, so a
// stalled connection must surface a "connection error" and return, not
// hang.
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

	time.Sleep(1 * time.Second) // let the connection establish before stalling it

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

// loadTestSyncSpec loads sync.yaml.
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

// seedLocalSyncStore opens a fresh local store at path, inserts events,
// and closes it -- must be closed before SyncModule opens its own handle.
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

// waitForSyncComplete polls logger until "Sync complete" appears or
// timeout elapses. GetLastLogs drains as it reads, so this must be the
// only caller polling this logger.
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

// waitForEventsInLocalStore polls the local store at path until every ID
// is present or timeout elapses, returning what's still missing. Only
// safe once the sync module owning path has closed its handle.
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
