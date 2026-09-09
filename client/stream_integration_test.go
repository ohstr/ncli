package client

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
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
