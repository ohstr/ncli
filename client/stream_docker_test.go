package client

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	relayclient "github.com/ohstr/nmilat/relay/client"
)

// See integration/stream/README.md for what this stack is and why the
// client under test runs in-process here rather than as a compose service.
const (
	streamDockerComposeFile = "../integration/stream/compose.yaml"
	streamDockerSpecFile    = "../integration/stream/stream.yaml"

	// streamTestPrivKey is an arbitrary, fixed test-only private key, used
	// only to produce validly-signed synthetic events. Unlike the plain
	// in-memory tests in stream_regression_test.go, the events here cross a
	// real ncli relay server (this stack's containers), which verifies
	// every event's ID/signature unconditionally -- a flow's own `trusted`
	// setting only ever governs what THIS client's read side skips
	// checking, never what a relay server accepts on write.
	streamTestPrivKey = "0acd12cbf0fb87cd13b17bc9b57dffd11b3870b407984cec5a4ce2a69b90268c"
)

var streamDockerSourceURLs = []string{
	"ws://localhost:45501",
	"ws://localhost:45502",
	"ws://localhost:45503",
}

const streamDockerDestURL = "ws://localhost:45500"

// TestStreamDocker brings up integration/stream/compose.yaml's real
// destination + source `ncli relay` containers once, then runs each
// scenario as a subtest against that shared stack -- needs Docker, hits no
// production relay. See `just test-integration-stream`.
func TestStreamDocker(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker-based stream integration test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found on PATH, skipping stream integration test")
	}

	runDockerCompose(t, "up", "-d", "--build")
	t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "-f", streamDockerComposeFile, "down", "-v")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("docker compose down failed: %v\n%s", err, out)
		}
	})

	for _, raw := range append([]string{streamDockerDestURL}, streamDockerSourceURLs...) {
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
	if len(downStats) != len(streamDockerSourceURLs) {
		t.Fatalf("expected %d sources, got %d", len(streamDockerSourceURLs), len(downStats))
	}

	// Let the initial connections establish before doing anything else.
	time.Sleep(1 * time.Second)

	destFC := destinationFlowContext(t, stream)

	var published []string
	publishAndTrack := func(sourceURL, marker string) {
		ev := newStreamDockerEvent(t, marker)
		publishStreamDockerEvent(t, sourceURL, ev)
		published = append(published, ev.ID)
	}

	// Baseline: normal delivery works before any forced reconnect.
	publishAndTrack(streamDockerSourceURLs[0], "baseline")

	const cycles = 3
	for i := 0; i < cycles; i++ {
		runDockerCompose(t, "restart", "destination")
		waitUntilDestinationPaused(t, destFC, 15*time.Second)

		// Still observed paused right now -- this is the exact window
		// deliverToSubscriber's paused() branch handles, not a guess based
		// on elapsed time.
		if !isPaused(destFC) {
			t.Fatalf("cycle %d: destination un-paused before the race-window publish could happen -- window too short to test reliably", i)
		}
		sourceURL := streamDockerSourceURLs[i%len(streamDockerSourceURLs)]
		publishAndTrack(sourceURL, fmt.Sprintf("race-window-%d", i))

		time.Sleep(1 * time.Second)
	}

	// Give the destination time to fully reconnect and the recovery loop a
	// few ticks to retry anything it queued during the windows above.
	time.Sleep(8 * time.Second)

	missing := waitForEventsAtDestination(t, published, 10*time.Second)
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

	before := newStreamDockerEvent(t, "source-reconnect-before")
	publishStreamDockerEvent(t, streamDockerSourceURLs[0], before)

	runDockerCompose(t, "restart", "source1")

	// Not required for correctness, just makes sure the next publish
	// genuinely lands after the restart has taken effect rather than
	// racing one that hasn't started yet.
	time.Sleep(2 * time.Second)

	after := newStreamDockerEvent(t, "source-reconnect-after")
	// source1 may still be mid-restart, so retry the publish itself --
	// what's under test is the stream client's own Read-side reconnect,
	// not this helper publish's timing.
	publishWithRetry(t, streamDockerSourceURLs[0], after, 30*time.Second)

	missing := waitForEventsAtDestination(t, []string{before.ID, after.ID}, 20*time.Second)
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
	rs, err := loadSpecFromYaml(streamDockerSpecFile)
	if err != nil {
		t.Fatalf("failed to load %s: %v", streamDockerSpecFile, err)
	}
	spec, ok := rs.Spec.(*StreamSpec)
	if !ok {
		t.Fatalf("%s is not a `kind: stream` spec", streamDockerSpecFile)
	}
	spec.Recovery = &RecoverySpec{
		StorePath:     filepath.Join(t.TempDir(), "recovery.db"),
		MaxRetries:    50,
		RetryInterval: "500ms",
	}
	return spec
}

// newStreamDockerEvent signs a small, uniquely-content-tagged kind:1 event
// -- marker only needs to make this call's content distinct from every
// other call's, which is all that's needed for a distinct event ID.
func newStreamDockerEvent(t *testing.T, marker string) *nip01.Event {
	t.Helper()
	ev := nip01.NewEvent(1, fmt.Sprintf("ncli stream-itest %s", marker))
	if err := ev.Sign(streamTestPrivKey); err != nil {
		t.Fatalf("failed to sign synthetic test event: %v", err)
	}
	return ev
}

func publishStreamDockerEvent(t *testing.T, relayURL string, ev *nip01.Event) {
	t.Helper()
	u, err := url.Parse(relayURL)
	if err != nil {
		t.Fatalf("invalid relay URL %q: %v", relayURL, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := relayclient.PublishEventToRelay(ctx, u, ev)
	if err != nil {
		t.Fatalf("failed to publish event %s to %s: %v", ev.ID, relayURL, err)
	}
	if !resp.Accepted {
		t.Fatalf("relay %s rejected event %s: %s", relayURL, ev.ID, resp.Message)
	}
}

// publishWithRetry is publishStreamDockerEvent's tolerant sibling, for the
// one case where the target relay is expected to be briefly unreachable
// (right after a forced container restart).
func publishWithRetry(t *testing.T, relayURL string, ev *nip01.Event, timeout time.Duration) {
	t.Helper()
	u, err := url.Parse(relayURL)
	if err != nil {
		t.Fatalf("invalid relay URL %q: %v", relayURL, err)
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		resp, err := relayclient.PublishEventToRelay(ctx, u, ev)
		cancel()
		if err == nil && resp.Accepted {
			return
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("rejected: %s", resp.Message)
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("failed to publish event %s to %s within %s: %v", ev.ID, relayURL, timeout, lastErr)
}

// fetchEventIDsFromRelay queries relayURL directly over the wire for the
// given event IDs, independent of anything the stream client itself
// believes -- this is what closes the gap between "client says accepted"
// and "relay actually has it."
func fetchEventIDsFromRelay(t *testing.T, relayURL string, ids []string) map[string]bool {
	t.Helper()
	u, err := url.Parse(relayURL)
	if err != nil {
		t.Fatalf("invalid relay URL %q: %v", relayURL, err)
	}
	filters := nip01.NewSubscriptionFilterGroup()
	filters.Add(&nip01.SubscriptionFilter{IDs: ids})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, err := relayclient.ReadEventsFromRelay(ctx, u, filters)
	if err != nil {
		t.Fatalf("failed to query %s for %d event IDs: %v", relayURL, len(ids), err)
	}
	found := make(map[string]bool, len(events))
	for _, ev := range events {
		found[ev.ID] = true
	}
	return found
}

// waitForEventsAtDestination polls the destination relay until every ID in
// ids is retrievable or timeout elapses, returning whatever's still
// missing at that point (empty on full success).
func waitForEventsAtDestination(t *testing.T, ids []string, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var missing []string
	for {
		found := fetchEventIDsFromRelay(t, streamDockerDestURL, ids)
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

// waitForRelayReady retries a no-op query against relayURL until it
// succeeds (proving the relay is up and speaking the protocol) or timeout
// elapses -- covers both the container's own startup time and the initial
// image build.
func waitForRelayReady(t *testing.T, relayURL string, timeout time.Duration) {
	t.Helper()
	u, err := url.Parse(relayURL)
	if err != nil {
		t.Fatalf("invalid relay URL %q: %v", relayURL, err)
	}
	filters := nip01.NewSubscriptionFilterGroup()
	filters.Add(&nip01.SubscriptionFilter{})

	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, lastErr = relayclient.ReadEventsFromRelay(ctx, u, filters)
		cancel()
		if lastErr == nil {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("relay at %s never became ready within %s: %v", relayURL, timeout, lastErr)
}

func runDockerCompose(t *testing.T, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"compose", "-f", streamDockerComposeFile}, args...)
	cmd := exec.Command("docker", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s failed: %v\n%s", strings.Join(cmdArgs, " "), err, out)
	}
}
