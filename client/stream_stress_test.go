package client

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
)

// See integration/stream/README.md's "Stress stack" section for what this
// is and why it's a separate, heavier stack from stream_integration_test.go's
// (20 real source containers vs. 3): bug 2 (this branch's first commit)
// was found in production's ~55-source fan-in topology, which 3 sources
// can prove the *mechanism* generalizes across but can't stress at any
// real scale. Not run by `just test`/`test-integration`/the default CI
// `integrations` job (heavier and slower than the correctness suite) --
// run explicitly via `just test-integration-stream-stress`.
const (
	streamStressComposeFile = "../integration/stream/stress-compose.yaml"
	streamStressSpecFile    = "../integration/stream/stress-stream.yaml"
	streamStressSourceCount = 20
	streamStressDestURL     = "ws://localhost:45560"
)

// streamStressSourceURLs returns all streamStressSourceCount source URLs,
// ws://localhost:45561 through 45560+streamStressSourceCount -- see
// stress-compose.yaml.
func streamStressSourceURLs() []string {
	urls := make([]string, streamStressSourceCount)
	for i := 0; i < streamStressSourceCount; i++ {
		urls[i] = fmt.Sprintf("ws://localhost:%d", 45560+i+1)
	}
	return urls
}

func TestStreamStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker-based stream stress test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found on PATH, skipping stream stress test")
	}

	runCompose(t, streamStressComposeFile, "up", "-d", "--build")
	t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "-f", streamStressComposeFile, "down", "-v")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("docker compose down failed: %v\n%s", err, out)
		}
	})

	waitForAllRelaysReady(t, append([]string{streamStressDestURL}, streamStressSourceURLs()...), 90*time.Second)

	scenarios := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{"ManySourcesHighVolumeIsNotLost", testStreamStressManySourcesHighVolume},
		{"FilterCorrectnessUnderLoad", testStreamStressFilterCorrectnessUnderLoad},
		{"SustainedLoadOverTime", testStreamStressSustainedLoad},
		{"ConcurrentMultiSourceDisruption", testStreamStressConcurrentMultiSourceDisruption},
		{"DestinationStallUnderHighSustainedLoad", testStreamStressDestinationStallUnderLoad},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, sc.fn)
	}
}

// testStreamStressManySourcesHighVolume is the direct "bug 2 at real
// scale" scenario: streamStressSourceCount (20) real source relays instead
// of stream_integration_test.go's 3, each publishing a genuine burst
// concurrently, against one destination -- the shape production's ~55
// sources -> 1 destination actually has, just not yet at its full size.
func testStreamStressManySourcesHighVolume(t *testing.T) {
	spec := loadTestStreamStressSpec(t)
	stream, err := NewStream(spec, false)
	if err != nil {
		t.Fatalf("NewStream failed: %v", err)
	}
	t.Cleanup(stream.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	upStats, downStats := stream.Sync(ctx)
	if len(downStats) != streamStressSourceCount {
		t.Fatalf("expected %d sources, got %d", streamStressSourceCount, len(downStats))
	}
	time.Sleep(2 * time.Second)

	const perSource = 50
	urls := streamStressSourceURLs()
	idsBySource := make([][]string, len(urls))
	errsBySource := make([]error, len(urls))
	var wg sync.WaitGroup
	for i, sourceURL := range urls {
		wg.Add(1)
		go func(i int, sourceURL string) {
			defer wg.Done()
			idsBySource[i], errsBySource[i] = publishManyEventsErr(sourceURL, perSource, fmt.Sprintf("stress-src%d", i))
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

	missing := waitForEventsAtRelay(t, streamStressDestURL, published, 60*time.Second)
	if len(missing) > 0 {
		t.Errorf("published %d events across %d sources, but %d never reached the destination: %v",
			len(published), len(urls), len(missing), missing)
	}
	if lost := upStats[0].Lost(); lost != 0 {
		t.Errorf("expected Lost=0 across %d sources under load, got %d", len(urls), lost)
	}
}

// testStreamStressFilterCorrectnessUnderLoad proves stress-stream.yaml's
// two filter objects are enforced correctly under real multi-source load,
// not just that events arrive at all: every one of streamStressSourceCount
// sources publishes the same 4-event mix (see stress-stream.yaml's header
// for the exact matching matrix), and this asserts every "should match"
// event arrives while every "should not match" event never does --
// checking exclusion, which nothing else in this package does at this
// scale, is exactly as important as checking inclusion.
func testStreamStressFilterCorrectnessUnderLoad(t *testing.T) {
	spec := loadTestStreamStressSpec(t)
	stream, err := NewStream(spec, false)
	if err != nil {
		t.Fatalf("NewStream failed: %v", err)
	}
	t.Cleanup(stream.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	stream.Sync(ctx)
	time.Sleep(2 * time.Second)

	urls := streamStressSourceURLs()

	type perSourceResult struct {
		includedIDs []string
		excludedIDs []string
		err         error
	}
	results := make([]perSourceResult, len(urls))
	var wg sync.WaitGroup
	for i, sourceURL := range urls {
		wg.Add(1)
		go func(i int, sourceURL string) {
			defer wg.Done()

			// kind 1, any author -- matches filter 1 (kinds only) -> included.
			e1 := newIntegrationEventOfKindUnchecked(1, integrationPrivKey, fmt.Sprintf("filter-src%d-k1", i))
			// kind 7, primary author -- matches filter 2 (kind+author) -> included.
			e2 := newIntegrationEventOfKindUnchecked(7, integrationPrivKey, fmt.Sprintf("filter-src%d-k7-primary", i))
			// kind 7, alt author -- matches neither (wrong author for
			// filter 2, wrong kind for filter 1) -> excluded.
			e3 := newIntegrationEventOfKindUnchecked(7, integrationPrivKeyAlt, fmt.Sprintf("filter-src%d-k7-alt", i))
			// kind 3, any author -- matches neither -> excluded.
			e4 := newIntegrationEventOfKindUnchecked(3, integrationPrivKey, fmt.Sprintf("filter-src%d-k3", i))

			for _, ev := range []*nip01.Event{e1, e2, e3, e4} {
				if err := publishEventToRelayErr(sourceURL, ev); err != nil {
					results[i].err = err
					return
				}
			}
			results[i] = perSourceResult{
				includedIDs: []string{e1.ID, e2.ID},
				excludedIDs: []string{e3.ID, e4.ID},
			}
		}(i, sourceURL)
	}
	wg.Wait()

	var included, excluded []string
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("source %d: %v", i, r.err)
		}
		included = append(included, r.includedIDs...)
		excluded = append(excluded, r.excludedIDs...)
	}

	missing := waitForEventsAtRelay(t, streamStressDestURL, included, 60*time.Second)
	if len(missing) > 0 {
		t.Errorf("filter-matching events from %d sources, but %d never reached the destination: %v", len(urls), len(missing), missing)
	}

	// By the time every matching event above has settled (the wait just
	// above), enough time has passed that a wrongly-leaked excluded event
	// would have arrived too -- a single snapshot check is enough here,
	// not another poll loop.
	leaked := fetchEventIDsFromRelay(t, streamStressDestURL, excluded)
	if len(leaked) > 0 {
		t.Errorf("%d event(s) that should have been excluded by the stream's filters reached the destination anyway: %v", len(leaked), leaked)
	}
}

// testStreamStressSustainedLoad covers throughput over time rather than a
// single instantaneous burst: every source publishes continuously for a
// fixed duration, exercising sustained backpressure/pacing behavior none
// of this package's one-shot burst tests do.
func testStreamStressSustainedLoad(t *testing.T) {
	spec := loadTestStreamStressSpec(t)
	stream, err := NewStream(spec, false)
	if err != nil {
		t.Fatalf("NewStream failed: %v", err)
	}
	t.Cleanup(stream.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	upStats, _ := stream.Sync(ctx)
	time.Sleep(2 * time.Second)

	const duration = 10 * time.Second
	const perSourceInterval = 200 * time.Millisecond // ~5 events/sec/source

	urls := streamStressSourceURLs()
	idsBySource := make([][]string, len(urls))
	errsBySource := make([]error, len(urls))
	var wg sync.WaitGroup
	deadline := time.Now().Add(duration)
	for i, sourceURL := range urls {
		wg.Add(1)
		go func(i int, sourceURL string) {
			defer wg.Done()
			var ids []string
			for n := 0; time.Now().Before(deadline); n++ {
				ev := newIntegrationEventOfKindUnchecked(1, integrationPrivKey, fmt.Sprintf("sustained-src%d-%d", i, n))
				if err := publishEventToRelayErr(sourceURL, ev); err != nil {
					errsBySource[i] = err
					return
				}
				ids = append(ids, ev.ID)
				time.Sleep(perSourceInterval)
			}
			idsBySource[i] = ids
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
	t.Logf("published %d events over %s from %d sources", len(published), duration, len(urls))

	missing := waitForEventsAtRelay(t, streamStressDestURL, published, 60*time.Second)
	if len(missing) > 0 {
		t.Errorf("sustained load: %d/%d events never reached the destination: %v", len(missing), len(published), missing)
	}
	if lost := upStats[0].Lost(); lost != 0 {
		t.Errorf("expected Lost=0 under sustained load, got %d", lost)
	}
}

// testStreamStressConcurrentMultiSourceDisruption covers production's
// actual flakiness shape: with ~55 sources, several are realistically
// flapping at any given moment, not just one. Restarts a meaningful
// fraction of sources simultaneously (not sequentially) while the stream
// is live, then confirms every source -- disrupted or not -- still gets
// its events through.
func testStreamStressConcurrentMultiSourceDisruption(t *testing.T) {
	spec := loadTestStreamStressSpec(t)
	stream, err := NewStream(spec, false)
	if err != nil {
		t.Fatalf("NewStream failed: %v", err)
	}
	t.Cleanup(stream.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	stream.Sync(ctx)
	time.Sleep(2 * time.Second)

	urls := streamStressSourceURLs()

	before := make([]string, 0, len(urls))
	for i, u := range urls {
		ev := newIntegrationEvent(t, fmt.Sprintf("chaos-before-%d", i))
		publishEventToRelay(t, u, ev)
		before = append(before, ev.ID)
	}

	// Restart several sources at once, not one at a time.
	const disrupted = 5
	var restartWG sync.WaitGroup
	for i := 0; i < disrupted; i++ {
		restartWG.Add(1)
		service := fmt.Sprintf("source%d", i+1)
		go func(service string) {
			defer restartWG.Done()
			// t.Errorf (unlike t.Fatalf) is safe to call from any
			// goroutine, as long as it happens before the test function
			// returns -- restartWG.Wait() below ensures that.
			cmd := exec.Command("docker", "compose", "-f", streamStressComposeFile, "restart", service)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("docker compose restart %s failed: %v\n%s", service, err, out)
			}
		}(service)
	}
	restartWG.Wait()

	time.Sleep(3 * time.Second)

	after := make([]string, 0, len(urls))
	for i, u := range urls {
		ev := newIntegrationEvent(t, fmt.Sprintf("chaos-after-%d", i))
		// The first `disrupted` sources may still be mid-restart.
		publishEventWithRetry(t, u, ev, 30*time.Second)
		after = append(after, ev.ID)
	}

	all := append(before, after...)
	missing := waitForEventsAtRelay(t, streamStressDestURL, all, 30*time.Second)
	if len(missing) > 0 {
		t.Errorf("concurrent multi-source disruption: %d/%d events never reached the destination: %v", len(missing), len(all), missing)
	}
}

// testStreamStressDestinationStallUnderLoad is
// stream_integration_test.go's DestinationDisruptionDoesNotDropEvents'
// "Stall" case, stress-tested: every one of streamStressSourceCount
// sources publishes concurrently *while* the destination is paused,
// instead of one source publishing one event -- stresses the recovery
// store's capacity to absorb a genuinely large simultaneous burst of
// failed deliveries, not just prove the single-event mechanism works.
func testStreamStressDestinationStallUnderLoad(t *testing.T) {
	spec := loadTestStreamStressSpec(t)
	stream, err := NewStream(spec, false)
	if err != nil {
		t.Fatalf("NewStream failed: %v", err)
	}
	t.Cleanup(stream.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	upStats, _ := stream.Sync(ctx)
	time.Sleep(2 * time.Second)

	destFC := destinationFlowContext(t, stream)

	runCompose(t, streamStressComposeFile, "pause", "destination")
	resolved := false
	t.Cleanup(func() {
		if !resolved {
			_ = exec.Command("docker", "compose", "-f", streamStressComposeFile, "unpause", "destination").Run()
		}
	})

	waitUntilDestinationPaused(t, destFC, 15*time.Second)
	if !isPaused(destFC) {
		t.Fatal("destination un-paused before the load-under-stall publish could happen")
	}

	const perSource = 10
	urls := streamStressSourceURLs()
	idsBySource := make([][]string, len(urls))
	errsBySource := make([]error, len(urls))
	var wg sync.WaitGroup
	for i, sourceURL := range urls {
		wg.Add(1)
		go func(i int, sourceURL string) {
			defer wg.Done()
			idsBySource[i], errsBySource[i] = publishManyEventsErr(sourceURL, perSource, fmt.Sprintf("stall-load-src%d", i))
		}(i, sourceURL)
	}
	wg.Wait()

	runCompose(t, streamStressComposeFile, "unpause", "destination")
	resolved = true

	var published []string
	for i, ids := range idsBySource {
		if errsBySource[i] != nil {
			t.Fatal(errsBySource[i])
		}
		published = append(published, ids...)
	}

	// Give the destination time to fully reconnect and the recovery loop
	// several ticks to retry everything it queued during the stall.
	time.Sleep(15 * time.Second)

	missing := waitForEventsAtRelay(t, streamStressDestURL, published, 60*time.Second)
	if len(missing) > 0 {
		t.Errorf("destination stall under load: %d/%d events never reached the destination: %v", len(missing), len(published), missing)
	}
	if lost := upStats[0].Lost(); lost != 0 {
		t.Errorf("expected Lost=0 (every event either delivered or handed to recovery), got %d", lost)
	}
}

// loadTestStreamStressSpec loads integration/stream/stress-stream.yaml the
// same way `ncli apply` itself would (loadSpecFromYaml), then overrides
// `from` with all streamStressSourceCount source URLs (the checked-in file
// only lists one, to stay a valid, hand-runnable spec on its own -- see
// its header) and the recovery block (fresh temp dir, short retry
// interval, same rationale as loadTestStreamSpec).
func loadTestStreamStressSpec(t *testing.T) *StreamSpec {
	t.Helper()
	rs, err := loadSpecFromYaml(streamStressSpecFile)
	if err != nil {
		t.Fatalf("failed to load %s: %v", streamStressSpecFile, err)
	}
	spec, ok := rs.Spec.(*StreamSpec)
	if !ok {
		t.Fatalf("%s is not a `kind: stream` spec", streamStressSpecFile)
	}

	urls := streamStressSourceURLs()
	spec.From = make([]*FlowSpec, len(urls))
	for i, u := range urls {
		spec.From[i] = newRemoteFlowSpec(t, u, true, 0)
	}

	spec.Recovery = &RecoverySpec{
		StorePath:     filepath.Join(t.TempDir(), "recovery.db"),
		MaxRetries:    50,
		RetryInterval: "500ms",
	}
	return spec
}
