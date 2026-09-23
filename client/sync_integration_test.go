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
	// Second instance of the same relay config, empty at startup.
	syncIntegrationRemoteBURL = "ws://localhost:45521"
)

// TestSyncIntegration brings up compose.yaml's two real relay containers
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

	waitForAllRelaysReady(t, []string{syncIntegrationRemoteURL, syncIntegrationRemoteBURL}, 60*time.Second)

	t.Run("ReconcileCompleteness", testSyncReconcileCompleteness)
	t.Run("FilterCorrectness", testSyncFilterCorrectness)
	t.Run("MaxReconcileRoundsTooLowSurfacesCleanly", testSyncMaxReconcileRoundsTooLowSurfacesCleanly)
	t.Run("RemoteStallTriggersTimeoutNotHang", testSyncRemoteStallTriggersTimeoutNotHang)
	t.Run("NegentropyPropagatesBetweenRelayInstances", testSyncNegentropyPropagatesBetweenRelayInstances)
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

// testSyncRemoteStallTriggersTimeoutNotHang: the remote is paused *before*
// sync ever connects to it, with short configured Handshake/Ping/Pong.
// Pausing after a fixed sleep instead (racing a timer against however fast
// reconciliation+pull happen to finish) was tried first and never worked:
// on a fast local Docker network, sync can reconcile+pull+push everything
// well within any reasonable grace period, so the pause routinely landed
// on an already-finished, already-closed connection with nothing left to
// stall -- confirmed by the exact same ~16.3s failure appearing across
// three different config attempts. Pausing first removes the race
// entirely: the very first connection attempt is guaranteed to hit an
// already-frozen relay.
//
// Sync has no reconnect loop, so a stall anywhere in its lifecycle
// (connect, reconcile, pull, or push) must surface as a logged error and
// return, not hang. Which specific stage it fails at isn't the point --
// only that it fails visibly and promptly -- so this checks for any
// Error()-level log line (the same [ColorDanger]●[-] marker every error
// path in neg_sync.go already goes through) rather than one exact message.
func testSyncRemoteStallTriggersTimeoutNotHang(t *testing.T) {
	spec := loadTestSyncSpec(t)
	shortHandshake := "2s"
	shortPing := "2s"
	shortPong := "3s"
	spec.Timeouts = &TimeoutSpec{Handshake: &shortHandshake, Ping: &shortPing, Pong: &shortPong}

	localPath := filepath.Join(t.TempDir(), "sync.db")
	spec.GetLocal().Path = localPath

	runCompose(t, syncIntegrationComposeFile, "pause", "remote")
	t.Cleanup(func() {
		_ = exec.Command("docker", "compose", "-f", syncIntegrationComposeFile, "unpause", "remote").Run()
	})

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

	errorMarker := fmt.Sprintf("[%s]●[-]", tui.ColorDanger)
	deadline := time.Now().Add(15 * time.Second)
	for {
		for _, row := range logger.GetLastLogs() {
			for _, cell := range row {
				if strings.Contains(cell, errorMarker) {
					runCompose(t, syncIntegrationComposeFile, "unpause", "remote")
					return // stall was detected and surfaced cleanly -- test passes
				}
			}
		}
		if time.Now().After(deadline) {
			runCompose(t, syncIntegrationComposeFile, "unpause", "remote")
			t.Fatal("a remote paused before sync ever connected to it never surfaced any error within 15s -- sync appears to hang on a silent stall instead of timing out")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// testSyncNegentropyPropagatesBetweenRelayInstances runs the same relay
// config twice and moves a known event set from one instance to the other:
// seed A, reconcile it down into a local store, then push that store up
// into the empty B. Replaces the old live-relay negentropy test -- no
// third-party relay is involved, so the event set is exact rather than
// whatever a public relay happened to be serving.
func testSyncNegentropyPropagatesBetweenRelayInstances(t *testing.T) {
	const n = 25

	// The preceding subtest pauses A's container; don't race its unpause.
	waitForRelayReady(t, syncIntegrationRemoteURL, 30*time.Second)

	// The earlier subtests left several hundred events on A. Scope both
	// legs to what this one seeds, or sync.yaml's `since: 0` drags all of
	// them through the pull and the push -- and grows again every time a
	// subtest is added above.
	since := uint64(time.Now().Unix())
	seeded := publishManyEvents(t, syncIntegrationRemoteURL, n, "negsync")
	scope := &nip01.SubscriptionFilter{
		Kinds:   []int{1},
		Authors: []string{integrationPubKey},
		Since:   since,
	}

	// B must not already hold them, or the push assertion proves nothing.
	if found := fetchEventIDsFromRelay(t, syncIntegrationRemoteBURL, seeded); len(found) > 0 {
		t.Fatalf("relay B already holds %d/%d seeded event(s) before any sync ran", len(found), n)
	}

	localPath := filepath.Join(t.TempDir(), "negsync.db")

	runSyncLeg(t, syncIntegrationRemoteURL, localPath, SyncDirectionDown, scope)
	if missing := waitForEventsInLocalStore(t, localPath, seeded, 20*time.Second); len(missing) > 0 {
		t.Fatalf("pull from A: %d/%d event(s) never landed in the local store: %v", len(missing), n, missing)
	}

	runSyncLeg(t, syncIntegrationRemoteBURL, localPath, SyncDirectionUp, scope)
	if missing := waitForEventsAtRelay(t, syncIntegrationRemoteBURL, seeded, 20*time.Second); len(missing) > 0 {
		t.Errorf("push to B: %d/%d event(s) never reached the second relay instance: %v", len(missing), n, missing)
	}
}

// runSyncLeg runs one direction of a sync between the local store at
// localPath and relayURL, scoped to filter, then closes the module --
// bbolt is exclusive, so the handle must be released before the next leg
// opens the same file.
func runSyncLeg(t *testing.T, relayURL, localPath, direction string, filter *nip01.SubscriptionFilter) {
	t.Helper()

	spec := loadTestSyncSpec(t)
	spec.GetLocal().Path = localPath
	spec.Direction = direction
	spec.Filters = []*FilterSpec{NewFilterSpec(filter)}

	// Only the private remote pointer is read from here on -- From and To
	// are consumed by UnmarshalJSON to populate it. newRemoteFlowSpec
	// resolves relayURI/relayFallbackURI.
	spec.remote = newRemoteFlowSpec(t, relayURL, true, 0)

	sm, err := NewSyncModule(spec, nil, false)
	if err != nil {
		t.Fatalf("NewSyncModule(%s, %s) failed: %v", relayURL, direction, err)
	}
	// Closed explicitly below so the next leg can open the same file;
	// registered too so a t.Fatal mid-leg still releases it. Both Close
	// paths are sync.Once-guarded, so the double call is safe.
	t.Cleanup(sm.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	logger, err := sm.Run(ctx)
	if err != nil {
		t.Fatalf("sm.Run(%s, %s) failed: %v", relayURL, direction, err)
	}
	waitForSyncComplete(t, logger, 90*time.Second)
	// EventStore.Close waits on its workers and closes the db inline, so
	// the handle is gone by the time this returns -- no settling sleep.
	sm.Close()
}

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
