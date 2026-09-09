package client

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// See integration/stream/README.md for what this stack is and why the
// client under test runs in-process here rather than as a compose service.
// Shared docker-lifecycle/publish/fetch helpers used below (runCompose,
// newIntegrationEvent, publishEventToRelay, waitForEventsAtRelay, etc.)
// live in client/integrationharness_test.go, alongside this package's
// other hermetic integration tests.
const (
	streamIntegrationComposeFile = "../integration/stream/compose.yaml"
	streamIntegrationSpecFile    = "../integration/stream/stream.yaml"
)

var streamIntegrationSourceURLs = []string{
	"ws://localhost:45501",
	"ws://localhost:45502",
	"ws://localhost:45503",
}

const streamIntegrationDestURL = "ws://localhost:45500"

// streamIntegrationDest2URL is only used by MultipleDestinationsBothReceiveEvents
// -- see compose.yaml's destination2 service.
const streamIntegrationDest2URL = "ws://localhost:45505"

// TestStreamIntegration brings up integration/stream/compose.yaml's real
// destination + source `ncli relay` containers once, then runs each
// scenario as a subtest against that shared stack -- needs Docker, hits no
// production relay. See `just test-integration-stream`.
func TestStreamIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker-based stream integration test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found on PATH, skipping stream integration test")
	}

	runCompose(t, streamIntegrationComposeFile, "up", "-d", "--build")
	t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "-f", streamIntegrationComposeFile, "down", "-v")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("docker compose down failed: %v\n%s", err, out)
		}
	})

	for _, raw := range append([]string{streamIntegrationDestURL}, streamIntegrationSourceURLs...) {
		waitForRelayReady(t, raw, 60*time.Second)
	}

	t.Run("DestinationReconnectDoesNotDropEvents", testDestinationReconnectDoesNotDropEvents)
	t.Run("SourceReconnectDoesNotHang", testSourceReconnectDoesNotHang)
	t.Run("DestinationStallTriggersTimeoutNotHang", testDestinationStallTriggersTimeoutNotHang)
	t.Run("HighVolumeBurstAcrossAllSourcesIsNotLost", testHighVolumeBurstAcrossAllSourcesIsNotLost)
	t.Run("MultipleDestinationsBothReceiveEvents", testMultipleDestinationsBothReceiveEvents)
}

// testDestinationReconnectDoesNotDropEvents is the regression test for the
// bug this harness was built for: a destination silently dropping events
// received during its own reconnect window (client/stream.go's
// deliverToSubscriber, paused() case). It forces several real destination
// reconnects, publishes known events to a source while the destination is
// *observed* to be in that window (not just assumed from timing), and
// asserts every one of them eventually shows up at the destination -- or,
// short of that, is at least accounted for by the Lost stat, never
// unaccounted-for.
func testDestinationReconnectDoesNotDropEvents(t *testing.T) {
	spec := loadTestStreamSpec(t)
	stream, err := NewStream(spec, false)
	if err != nil {
		t.Fatalf("NewStream failed: %v", err)
	}
	t.Cleanup(stream.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	upStats, downStats := stream.Sync(ctx)
	if len(upStats) != 1 {
		t.Fatalf("expected exactly 1 destination, got %d", len(upStats))
	}
	if len(downStats) != len(streamIntegrationSourceURLs) {
		t.Fatalf("expected %d sources, got %d", len(streamIntegrationSourceURLs), len(downStats))
	}

	// Let the initial connections establish before doing anything else.
	time.Sleep(1 * time.Second)

	destFC := destinationFlowContext(t, stream)

	var published []string
	publishAndTrack := func(sourceURL, marker string) {
		ev := newIntegrationEvent(t, marker)
		publishEventToRelay(t, sourceURL, ev)
		published = append(published, ev.ID)
	}

	// Baseline: normal delivery works before any forced reconnect.
	publishAndTrack(streamIntegrationSourceURLs[0], "baseline")

	const cycles = 3
	for i := 0; i < cycles; i++ {
		runCompose(t, streamIntegrationComposeFile, "restart", "destination")
		waitUntilDestinationPaused(t, destFC, 15*time.Second)

		// Still observed paused right now -- this is the exact window
		// deliverToSubscriber's paused() branch handles, not a guess based
		// on elapsed time.
		if !isPaused(destFC) {
			t.Fatalf("cycle %d: destination un-paused before the race-window publish could happen -- window too short to test reliably", i)
		}
		sourceURL := streamIntegrationSourceURLs[i%len(streamIntegrationSourceURLs)]
		publishAndTrack(sourceURL, fmt.Sprintf("race-window-%d", i))

		time.Sleep(1 * time.Second)
	}

	// Give the destination time to fully reconnect and the recovery loop a
	// few ticks to retry anything it queued during the windows above.
	time.Sleep(8 * time.Second)

	missing := waitForEventsAtRelay(t, streamIntegrationDestURL, published, 10*time.Second)
	if len(missing) > 0 {
		t.Errorf("published %d events, but %d never reached the destination (permanently dropped): %v", len(published), len(missing), missing)
	}

	if lost := upStats[0].Lost(); lost != 0 {
		t.Errorf("expected Lost=0 (every event either delivered or handed to recovery), got %d", lost)
	}
}

// testSourceReconnectDoesNotHang covers the general "flaky source relay"
// mechanism cheaply, against a real relay rather than 55 real public ones:
// restarting a source mid-stream must not hang or drop the stream, and an
// event published to that source once it's back up must still be picked up.
func testSourceReconnectDoesNotHang(t *testing.T) {
	spec := loadTestStreamSpec(t)
	stream, err := NewStream(spec, false)
	if err != nil {
		t.Fatalf("NewStream failed: %v", err)
	}
	t.Cleanup(stream.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	stream.Sync(ctx)
	time.Sleep(1 * time.Second)

	before := newIntegrationEvent(t, "source-reconnect-before")
	publishEventToRelay(t, streamIntegrationSourceURLs[0], before)

	runCompose(t, streamIntegrationComposeFile, "restart", "source1")

	// Not required for correctness, just makes sure the next publish
	// genuinely lands after the restart has taken effect rather than
	// racing one that hasn't started yet.
	time.Sleep(2 * time.Second)

	after := newIntegrationEvent(t, "source-reconnect-after")
	// source1 may still be mid-restart, so retry the publish itself --
	// what's under test is the stream client's own Read-side reconnect,
	// not this helper publish's timing.
	publishEventWithRetry(t, streamIntegrationSourceURLs[0], after, 30*time.Second)

	missing := waitForEventsAtRelay(t, streamIntegrationDestURL, []string{before.ID, after.ID}, 20*time.Second)
	if len(missing) > 0 {
		t.Errorf("source reconnect: %d/2 events never reached the destination: %v", len(missing), missing)
	}
}

// testDestinationStallTriggersTimeoutNotHang covers a failure mode
// DestinationReconnectDoesNotDropEvents can't: a *silent* stall (the
// destination process frozen, TCP connection still technically open) with
// no close/reset at all, as opposed to `docker compose restart`'s abrupt
// connection teardown. Detecting this relies entirely on
// stream.yaml's configured ping/pong timeouts (getConnectionConfig,
// client/stream.go) actually firing -- a real, previously-untested code
// path distinct from the explicit-disconnect path the restart-based tests
// exercise. `docker compose pause` freezes the container's processes via
// the kernel's cgroup freezer without touching the network stack, so the
// TCP connection itself stays established while the frozen relay can't
// read, process, or answer a websocket ping -- exactly the "silent stall"
// this needs.
func testDestinationStallTriggersTimeoutNotHang(t *testing.T) {
	spec := loadTestStreamSpec(t)
	stream, err := NewStream(spec, false)
	if err != nil {
		t.Fatalf("NewStream failed: %v", err)
	}
	t.Cleanup(stream.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	upStats, _ := stream.Sync(ctx)
	time.Sleep(1 * time.Second)

	destFC := destinationFlowContext(t, stream)

	runCompose(t, streamIntegrationComposeFile, "pause", "destination")
	t.Cleanup(func() {
		// Best-effort: later cleanup steps (stream.Close, then `docker
		// compose down`) must not themselves get stuck waiting on a
		// destination this subtest left paused, if it fails before
		// reaching the unpause below.
		_ = exec.Command("docker", "compose", "-f", streamIntegrationComposeFile, "unpause", "destination").Run()
	})

	// stream.yaml configures pong: "4s" -- this must observe paused()
	// actually flip, not just sleep ~4s and hope, since that's the whole
	// point of the test (proving the timeout path fires at all).
	waitUntilDestinationPaused(t, destFC, 15*time.Second)

	stalled := newIntegrationEvent(t, "destination-stall")
	publishEventToRelay(t, streamIntegrationSourceURLs[0], stalled)

	runCompose(t, streamIntegrationComposeFile, "unpause", "destination")

	// Give the now-unfrozen destination time to finish reconnecting and the
	// recovery loop a few ticks to retry anything queued during the stall.
	time.Sleep(8 * time.Second)

	missing := waitForEventsAtRelay(t, streamIntegrationDestURL, []string{stalled.ID}, 10*time.Second)
	if len(missing) > 0 {
		t.Errorf("event published during the stall never reached the destination: %v", missing)
	}
	if lost := upStats[0].Lost(); lost != 0 {
		t.Errorf("expected Lost=0 (event either delivered or handed to recovery), got %d", lost)
	}
}

// testHighVolumeBurstAcrossAllSourcesIsNotLost is the e2e regression test
// PR #45 (fix/apply-stream-publish-concurrency-cap) never got: an unpaced
// burst from a large `from` pool overwhelming a destination's own
// concurrency guard, previously fixed only at the unit level (see
// client/stream_regression_test.go). stream.yaml's destination already
// sets `writeConcurrency: 8`; this drives real traffic well past that cap
// (hundreds of events across all 3 sources at once) against a real relay
// enforcing its own real limits, rather than a mock that can't reject
// anything.
func testHighVolumeBurstAcrossAllSourcesIsNotLost(t *testing.T) {
	spec := loadTestStreamSpec(t)
	stream, err := NewStream(spec, false)
	if err != nil {
		t.Fatalf("NewStream failed: %v", err)
	}
	t.Cleanup(stream.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	upStats, _ := stream.Sync(ctx)
	time.Sleep(1 * time.Second)

	const perSource = 100
	idsBySource := make([][]string, len(streamIntegrationSourceURLs))
	errsBySource := make([]error, len(streamIntegrationSourceURLs))
	var wg sync.WaitGroup
	for i, sourceURL := range streamIntegrationSourceURLs {
		wg.Add(1)
		go func(i int, sourceURL string) {
			defer wg.Done()
			// publishManyEventsErr, not publishManyEvents: this runs in a
			// goroutine that isn't the one running the test, and t.Fatalf
			// must never be called from anywhere else -- see its doc
			// comment. Fatal on the aggregated results below instead, back
			// on this function's own goroutine.
			idsBySource[i], errsBySource[i] = publishManyEventsErr(sourceURL, perSource, fmt.Sprintf("burst-src%d", i))
		}(i, sourceURL)
	}
	wg.Wait()

	var published []string
	for i, ids := range idsBySource {
		if errsBySource[i] != nil {
			t.Fatal(errsBySource[i])
		}
		published = append(published, ids...)
	}

	missing := waitForEventsAtRelay(t, streamIntegrationDestURL, published, 30*time.Second)
	if len(missing) > 0 {
		t.Errorf("published %d events across %d sources, but %d never reached the destination: %v",
			len(published), len(streamIntegrationSourceURLs), len(missing), missing)
	}
	if lost := upStats[0].Lost(); lost != 0 {
		t.Errorf("expected Lost=0 under a large burst (writeConcurrency should pace delivery, never drop it), got %d", lost)
	}
}

// testMultipleDestinationsBothReceiveEvents covers real fan-out to more
// than one destination -- every other scenario in this file sticks to
// stream.yaml's single checked-in destination, so broadcastEvents'
// multi-subscriber path (client/stream.go) has otherwise never run against
// a real relay on each end.
func testMultipleDestinationsBothReceiveEvents(t *testing.T) {
	waitForRelayReady(t, streamIntegrationDest2URL, 60*time.Second)

	spec := loadTestStreamSpec(t)
	spec.To = append(spec.To, newRemoteFlowSpec(t, streamIntegrationDest2URL, true, 8))

	stream, err := NewStream(spec, false)
	if err != nil {
		t.Fatalf("NewStream failed: %v", err)
	}
	t.Cleanup(stream.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	upStats, _ := stream.Sync(ctx)
	if len(upStats) != 2 {
		t.Fatalf("expected exactly 2 destinations, got %d", len(upStats))
	}
	time.Sleep(1 * time.Second)

	published := publishManyEvents(t, streamIntegrationSourceURLs[0], 10, "fanout")

	for _, destURL := range []string{streamIntegrationDestURL, streamIntegrationDest2URL} {
		missing := waitForEventsAtRelay(t, destURL, published, 15*time.Second)
		if len(missing) > 0 {
			t.Errorf("destination %s: %d/%d events never arrived: %v", destURL, len(missing), len(published), missing)
		}
	}
	for i, stat := range upStats {
		if lost := stat.Lost(); lost != 0 {
			t.Errorf("destination %d: expected Lost=0, got %d", i, lost)
		}
	}
}

// loadTestStreamSpec loads integration/stream/stream.yaml the same way
// `ncli apply` itself does (loadSpecFromYaml), then overrides only the
// recovery block: a fresh temp-dir store per test and a short retry
// interval, so this test's own short lifetime can't itself cause
// RecoveryManager.handleRetryFailure to give up on an event before the
// test gets a chance to observe it recovered.
func loadTestStreamSpec(t *testing.T) *StreamSpec {
	t.Helper()
	rs, err := loadSpecFromYaml(streamIntegrationSpecFile)
	if err != nil {
		t.Fatalf("failed to load %s: %v", streamIntegrationSpecFile, err)
	}
	spec, ok := rs.Spec.(*StreamSpec)
	if !ok {
		t.Fatalf("%s is not a `kind: stream` spec", streamIntegrationSpecFile)
	}
	spec.Recovery = &RecoverySpec{
		StorePath:     filepath.Join(t.TempDir(), "recovery.db"),
		MaxRetries:    50,
		RetryInterval: "500ms",
	}
	return spec
}

// destinationFlowContext returns the (sole) destination's FlowContext.
// sc.subscribers is populated synchronously within Sync() itself before it
// returns, and only ever read/written elsewhere under subscribersMu -- see
// the identical pattern in client/integration_test.go.
func destinationFlowContext(t *testing.T, stream *Stream) *FlowContext {
	t.Helper()
	stream.sc.subscribersMu.RLock()
	defer stream.sc.subscribersMu.RUnlock()
	for _, fc := range stream.sc.subscribers {
		return fc
	}
	t.Fatal("stream has no destination flow context")
	return nil
}

// isPaused reports whether fc is, right now, in the reconnect window
// deliverToSubscriber's paused() branch handles.
func isPaused(fc *FlowContext) bool {
	select {
	case <-fc.paused():
		return true
	default:
		return false
	}
}

func waitUntilDestinationPaused(t *testing.T, fc *FlowContext, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if isPaused(fc) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("destination never entered its paused (reconnecting) state within the timeout")
}
