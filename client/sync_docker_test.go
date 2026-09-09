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
// docker-lifecycle/publish/fetch helpers used below (runDockerCompose,
// newDockerHarnessEvent, publishEventToRelay, waitForRelayReady, etc.) live
// in client/dockerharness_test.go, alongside client/stream_docker_test.go
// and client/inspect_docker_test.go.
const (
	syncDockerComposeFile = "../integration/sync/compose.yaml"
	syncDockerSpecFile    = "../integration/sync/sync.yaml"
	syncDockerRemoteURL   = "ws://localhost:45520"
)

// TestSyncDocker brings up integration/sync/compose.yaml's single real
// `ncli relay` container once, then runs each scenario as a subtest
// against it -- needs Docker, hits no production relay. Replaces this
// package's only prior sync coverage, TestNegSync_Integration
// (client/neg_sync_test.go), which depends on live wss://relay.ohstr.com
// negentropy support and one specific pubkey -- fragile and
// non-deterministic (kept as-is, not deleted: it's still a useful "does
// this actually interop with a real-world deployed relay" smoke test, just
// not one this package can gate a regression on with any determinism). See
// `just test-integration-sync`.
func TestSyncDocker(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker-based sync integration test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found on PATH, skipping sync integration test")
	}

	runDockerCompose(t, syncDockerComposeFile, "up", "-d", "--build")
	t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "-f", syncDockerComposeFile, "down", "-v")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("docker compose down failed: %v\n%s", err, out)
		}
	})

	waitForRelayReady(t, syncDockerRemoteURL, 60*time.Second)

	t.Run("BothDirectionsReconcile", testSyncBothDirectionsReconcile)
}

// testSyncBothDirectionsReconcile seeds each side of a `direction: both`
// sync with events the other side doesn't have, then asserts negentropy
// reconciliation against a real relay carries every one of them the right
// way: local-only events get pushed up, remote-only events get pulled
// down. This is the real-relay analog of the two-endpoint shape
// `examples/apply/sync.yaml` documents -- unlike stream/inspect, sync is
// exactly one local store and one remote relay, never a multi-relay fan-in
// (see SyncSpec.UnmarshalJSON's "only one local/remote flow allowed").
func testSyncBothDirectionsReconcile(t *testing.T) {
	spec := loadTestSyncSpec(t)

	localPath := filepath.Join(t.TempDir(), "sync.db")
	spec.GetLocal().Path = localPath

	var remoteOnly []string
	for i := 0; i < 3; i++ {
		ev := newDockerHarnessEvent(t, fmt.Sprintf("sync-remote-only-%d", i))
		publishEventToRelay(t, syncDockerRemoteURL, ev)
		remoteOnly = append(remoteOnly, ev.ID)
	}

	var localOnly []string
	localEvents := make([]*nip01.Event, 0, 3)
	for i := 0; i < 3; i++ {
		ev := newDockerHarnessEvent(t, fmt.Sprintf("sync-local-only-%d", i))
		localOnly = append(localOnly, ev.ID)
		localEvents = append(localEvents, ev)
	}
	seedLocalSyncStore(t, localPath, localEvents)

	sm, err := NewSyncModule(spec, nil, false)
	if err != nil {
		t.Fatalf("NewSyncModule failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger, err := sm.Run(ctx)
	if err != nil {
		t.Fatalf("sm.Run failed: %v", err)
	}
	waitForSyncComplete(t, logger, 30*time.Second)

	// Give the deferred store.Close() inside SyncModule.execute() a moment
	// to actually run -- "Sync complete" is logged just before execute()
	// returns, not after, so there's a brief window where the local store
	// file may still be held.
	time.Sleep(200 * time.Millisecond)

	missingRemote := waitForEventsAtRelay(t, syncDockerRemoteURL, localOnly, 10*time.Second)
	if len(missingRemote) > 0 {
		t.Errorf("push: %d local-only event(s) never reached the remote relay: %v", len(missingRemote), missingRemote)
	}

	missingLocal := waitForEventsInLocalStore(t, localPath, remoteOnly, 10*time.Second)
	if len(missingLocal) > 0 {
		t.Errorf("pull: %d remote-only event(s) never landed in the local store: %v", len(missingLocal), missingLocal)
	}
}

// loadTestSyncSpec loads integration/sync/sync.yaml the same way `ncli
// apply` itself would (loadSpecFromYaml).
func loadTestSyncSpec(t *testing.T) *SyncSpec {
	t.Helper()
	rs, err := loadSpecFromYaml(syncDockerSpecFile)
	if err != nil {
		t.Fatalf("failed to load %s: %v", syncDockerSpecFile, err)
	}
	spec, ok := rs.Spec.(*SyncSpec)
	if !ok {
		t.Fatalf("%s is not a `kind: sync` spec", syncDockerSpecFile)
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
