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

// See integration/stream/README.md. Shared helpers live in
// client/integrationharness_test.go.
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

// streamIntegrationDest2URL is used only by MultipleDestinationsBothReceiveEvents.
const streamIntegrationDest2URL = "ws://localhost:45505"

// TestStreamIntegration brings up compose.yaml's real relay containers
// once, then runs each scenario as a subtest. Needs Docker.
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

	scenarios := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{"DestinationDisruptionDoesNotDropEvents", testDestinationDisruptionDoesNotDropEvents},
		{"SourceReconnectDoesNotHang", testSourceReconnectDoesNotHang},
		{"HighVolumeBurstAcrossAllSourcesIsNotLost", testHighVolumeBurstAcrossAllSourcesIsNotLost},
		{"MultipleDestinationsBothReceiveEvents", testMultipleDestinationsBothReceiveEvents},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, sc.fn)
	}
}

// testDestinationDisruptionDoesNotDropEvents covers two ways a destination
// can go down mid-stream: "Restart" (explicit disconnect) and "Stall"
// (`docker compose pause` -- process frozen, connection stays open,
// detected only via ping/pong timeout). Events published into the
// observed paused() window must arrive once recovered, or at least be
// counted in Lost -- never silently dropped.
func testDestinationDisruptionDoesNotDropEvents(t *testing.T) {
	cases := []struct {
		name                string
		cycles              int
		disrupt             func(t *testing.T)
		resolve             func(t *testing.T) // called once in the main flow
		cleanupIfUnresolved func()             // best-effort fallback if resolve never ran
	}{
		{
			name:    "Restart",
			cycles:  3,
			disrupt: func(t *testing.T) { runCompose(t, streamIntegrationComposeFile, "restart", "destination") },
			resolve: func(t *testing.T) {},
		},
		{
			name:    "Stall",
			cycles:  1,
			disrupt: func(t *testing.T) { runCompose(t, streamIntegrationComposeFile, "pause", "destination") },
			resolve: func(t *testing.T) { runCompose(t, streamIntegrationComposeFile, "unpause", "destination") },
			cleanupIfUnresolved: func() {
				_ = exec.Command("docker", "compose", "-f", streamIntegrationComposeFile, "unpause", "destination").Run()
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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

			// Avoid double-unpausing (fails on "not paused") if resolve
			// already ran in the main flow below.
			resolved := false
			if tc.cleanupIfUnresolved != nil {
				t.Cleanup(func() {
					if !resolved {
						tc.cleanupIfUnresolved()
					}
				})
			}

			time.Sleep(1 * time.Second) // let initial connections establish

			destFC := destinationFlowContext(t, stream)

			var published []string
			publishAndTrack := func(sourceURL, marker string) {
				ev := newIntegrationEvent(t, marker)
				publishEventToRelay(t, sourceURL, ev)
				published = append(published, ev.ID)
			}

			// Baseline: normal delivery works before any disruption.
			publishAndTrack(streamIntegrationSourceURLs[0], tc.name+"-baseline")

			for i := 0; i < tc.cycles; i++ {
				tc.disrupt(t)
				waitUntilDestinationPaused(t, destFC, 15*time.Second)
				if !isPaused(destFC) {
					t.Fatalf("cycle %d: destination un-paused before the race-window publish could happen -- window too short to test reliably", i)
				}
				sourceURL := streamIntegrationSourceURLs[i%len(streamIntegrationSourceURLs)]
				publishAndTrack(sourceURL, fmt.Sprintf("%s-window-%d", tc.name, i))

				tc.resolve(t)
				resolved = true
				time.Sleep(1 * time.Second)
			}

			time.Sleep(8 * time.Second) // let the destination reconnect and recovery retry

			missing := waitForEventsAtRelay(t, streamIntegrationDestURL, published, 10*time.Second)
			if len(missing) > 0 {
				t.Errorf("published %d events, but %d never reached the destination (permanently dropped): %v", len(published), len(missing), missing)
			}
			if lost := upStats[0].Lost(); lost != 0 {
				t.Errorf("expected Lost=0 (every event either delivered or handed to recovery), got %d", lost)
			}
		})
	}
}

// testSourceReconnectDoesNotHang: restarting a source must not hang or
// drop the stream, and an event published once it's back must still land.
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
	time.Sleep(2 * time.Second)

	after := newIntegrationEvent(t, "source-reconnect-after")
	publishEventWithRetry(t, streamIntegrationSourceURLs[0], after, 30*time.Second)

	missing := waitForEventsAtRelay(t, streamIntegrationDestURL, []string{before.ID, after.ID}, 20*time.Second)
	if len(missing) > 0 {
		t.Errorf("source reconnect: %d/2 events never reached the destination: %v", len(missing), missing)
	}
}

// testHighVolumeBurstAcrossAllSourcesIsNotLost: hundreds of events across
// all sources at once, against the destination's writeConcurrency cap
// (PR #45), against a real relay.
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

// testMultipleDestinationsBothReceiveEvents: fan-out to 2 destinations,
// not stream.yaml's usual 1.
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

// loadTestStreamSpec loads stream.yaml, overriding the recovery block
// with a fresh temp-dir store and short retry interval.
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
