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

// See integration/stream/README.md's "Stress stack" section. 20 source
// containers, not run by default CI -- `just test-integration-stream-stress`.
const (
	streamStressComposeFile = "../integration/stream/stress-compose.yaml"
	streamStressSpecFile    = "../integration/stream/stress-stream.yaml"
	streamStressSourceCount = 20
	streamStressDestURL     = "ws://localhost:45560"
)

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

// testStreamStressManySourcesHighVolume: 20 real sources, each bursting
// concurrently, against one destination.
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

// testStreamStressFilterCorrectnessUnderLoad: every source publishes the
// same 4-event mix (see stress-stream.yaml's header for the matching
// matrix); asserts matching events arrive and non-matching ones never do.
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

			// included: kind 1 (any author), kind 7 (primary author).
			// excluded: kind 7 (alt author), kind 3.
			e1 := newIntegrationEventOfKindUnchecked(1, integrationPrivKey, fmt.Sprintf("filter-src%d-k1", i))
			e2 := newIntegrationEventOfKindUnchecked(7, integrationPrivKey, fmt.Sprintf("filter-src%d-k7-primary", i))
			e3 := newIntegrationEventOfKindUnchecked(7, integrationPrivKeyAlt, fmt.Sprintf("filter-src%d-k7-alt", i))
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

	leaked := fetchEventIDsFromRelay(t, streamStressDestURL, excluded)
	if len(leaked) > 0 {
		t.Errorf("%d event(s) that should have been excluded by the stream's filters reached the destination anyway: %v", len(leaked), leaked)
	}
}

// testStreamStressSustainedLoad: every source publishes continuously for
// a fixed duration instead of one burst.
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

// testStreamStressConcurrentMultiSourceDisruption: restarts several
// sources at once, mid-stream, then confirms every source's events land.
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

	const disrupted = 5
	var restartWG sync.WaitGroup
	for i := 0; i < disrupted; i++ {
		restartWG.Add(1)
		service := fmt.Sprintf("source%d", i+1)
		go func(service string) {
			defer restartWG.Done()
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
		publishEventWithRetry(t, u, ev, 30*time.Second) // first `disrupted` sources may still be mid-restart
		after = append(after, ev.ID)
	}

	all := append(before, after...)
	missing := waitForEventsAtRelay(t, streamStressDestURL, all, 30*time.Second)
	if len(missing) > 0 {
		t.Errorf("concurrent multi-source disruption: %d/%d events never reached the destination: %v", len(missing), len(all), missing)
	}
}

// testStreamStressDestinationStallUnderLoad: all sources publish
// concurrently while the destination is paused, not just one event.
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

	time.Sleep(15 * time.Second) // let the destination reconnect and recovery retry

	missing := waitForEventsAtRelay(t, streamStressDestURL, published, 60*time.Second)
	if len(missing) > 0 {
		t.Errorf("destination stall under load: %d/%d events never reached the destination: %v", len(missing), len(published), missing)
	}
	if lost := upStats[0].Lost(); lost != 0 {
		t.Errorf("expected Lost=0 (every event either delivered or handed to recovery), got %d", lost)
	}
}

// loadTestStreamStressSpec loads stress-stream.yaml, overriding `from`
// with all 20 source URLs (the file only lists one) and the recovery
// block (fresh temp dir, short retry interval).
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
