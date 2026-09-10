package client

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
)

// See integration/inspect/README.md's "Stress stack" section. 15 target
// containers, not run by default CI -- `just test-integration-inspect-stress`.
const (
	inspectStressComposeFile = "../integration/inspect/stress-compose.yaml"
	inspectStressSpecFile    = "../integration/inspect/stress-inspect.yaml"
	inspectStressTargetCount = 15
)

func inspectStressTargetURLs() []string {
	urls := make([]string, inspectStressTargetCount)
	for i := 0; i < inspectStressTargetCount; i++ {
		urls[i] = fmt.Sprintf("ws://localhost:%d", 45590+i)
	}
	return urls
}

func TestInspectStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker-based inspect stress test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found on PATH, skipping inspect stress test")
	}

	runCompose(t, inspectStressComposeFile, "up", "-d", "--build")
	t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "-f", inspectStressComposeFile, "down", "-v")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("docker compose down failed: %v\n%s", err, out)
		}
	})

	waitForAllRelaysReady(t, inspectStressTargetURLs(), 90*time.Second)

	scenarios := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{"ManyTargetsHighVolumeAllLand", testInspectStressManyTargetsHighVolume},
		{"FilterCorrectnessUnderLoad", testInspectStressFilterCorrectnessUnderLoad},
		{"ConcurrentMultiTargetDisruption", testInspectStressConcurrentMultiTargetDisruption},
		{"ConcurrentMultiTargetStallDoesNotHangSession", testInspectStressConcurrentMultiTargetStall},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, sc.fn)
	}
}

// testInspectStressManyTargetsHighVolume: hundreds of events across all
// 15 targets at once.
func testInspectStressManyTargetsHighVolume(t *testing.T) {
	spec := loadTestInspectStressSpec(t)

	const perTarget = 50
	urls := inspectStressTargetURLs()
	idsByTarget := make([][]string, len(urls))
	errsByTarget := make([]error, len(urls))
	var wg sync.WaitGroup
	for i, target := range urls {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()
			idsByTarget[i], errsByTarget[i] = publishManyEventsErr(target, perTarget, fmt.Sprintf("stress-t%d", i))
		}(i, target)
	}
	wg.Wait()

	var published []string
	for i, ids := range idsByTarget {
		if errsByTarget[i] != nil {
			t.Fatal(errsByTarget[i])
		}
		published = append(published, ids...)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	insp := newTestInspector(t, ctx, spec)

	missing := waitForEventsInInspectStore(t, insp, published, 60*time.Second)
	if len(missing) > 0 {
		t.Errorf("published %d events across %d targets, but %d never landed in the inspect session's local store: %v",
			len(published), len(urls), len(missing), missing)
	}
}

// testInspectStressFilterCorrectnessUnderLoad mirrors stream's
// testStreamStressFilterCorrectnessUnderLoad, verified against the local
// session store instead of a destination relay.
func testInspectStressFilterCorrectnessUnderLoad(t *testing.T) {
	spec := loadTestInspectStressSpec(t)
	urls := inspectStressTargetURLs()

	type perTargetResult struct {
		includedIDs []string
		excludedIDs []string
		err         error
	}
	results := make([]perTargetResult, len(urls))
	var wg sync.WaitGroup
	for i, target := range urls {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()

			e1 := newIntegrationEventOfKindUnchecked(1, integrationPrivKey, fmt.Sprintf("filter-t%d-k1", i))
			e2 := newIntegrationEventOfKindUnchecked(7, integrationPrivKey, fmt.Sprintf("filter-t%d-k7-primary", i))
			e3 := newIntegrationEventOfKindUnchecked(7, integrationPrivKeyAlt, fmt.Sprintf("filter-t%d-k7-alt", i))
			e4 := newIntegrationEventOfKindUnchecked(3, integrationPrivKey, fmt.Sprintf("filter-t%d-k3", i))

			for _, ev := range []*nip01.Event{e1, e2, e3, e4} {
				if err := publishEventToRelayErr(target, ev); err != nil {
					results[i].err = err
					return
				}
			}
			results[i] = perTargetResult{
				includedIDs: []string{e1.ID, e2.ID},
				excludedIDs: []string{e3.ID, e4.ID},
			}
		}(i, target)
	}
	wg.Wait()

	var included, excluded []string
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("target %d: %v", i, r.err)
		}
		included = append(included, r.includedIDs...)
		excluded = append(excluded, r.excludedIDs...)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	insp := newTestInspector(t, ctx, spec)

	missing := waitForEventsInInspectStore(t, insp, included, 60*time.Second)
	if len(missing) > 0 {
		t.Errorf("filter-matching events from %d targets, but %d never landed in the inspect session's local store: %v", len(urls), len(missing), missing)
	}

	events, err := insp.store.store.FindEvents(context.Background(), &nip01.SubscriptionFilter{IDs: excluded})
	if err != nil {
		t.Fatalf("failed to query inspect session store: %v", err)
	}
	if len(events) > 0 {
		leaked := make([]string, len(events))
		for i, ev := range events {
			leaked[i] = ev.EventID
		}
		t.Errorf("%d event(s) that should have been excluded by the inspect session's filters landed in the local store anyway: %v", len(leaked), leaked)
	}
}

// testInspectStressConcurrentMultiTargetDisruption: several targets
// restarted simultaneously, not one at a time.
func testInspectStressConcurrentMultiTargetDisruption(t *testing.T) {
	spec := loadTestInspectStressSpec(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	insp := newTestInspector(t, ctx, spec)

	urls := inspectStressTargetURLs()

	before := make([]string, 0, len(urls))
	for i, u := range urls {
		ev := newIntegrationEvent(t, fmt.Sprintf("chaos-before-%d", i))
		publishEventToRelay(t, u, ev)
		before = append(before, ev.ID)
	}

	const disrupted = 4
	var restartWG sync.WaitGroup
	for i := 0; i < disrupted; i++ {
		restartWG.Add(1)
		service := fmt.Sprintf("target%d", i+1)
		go func(service string) {
			defer restartWG.Done()
			cmd := exec.Command("docker", "compose", "-f", inspectStressComposeFile, "restart", service)
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
		publishEventWithRetry(t, u, ev, 30*time.Second)
		after = append(after, ev.ID)
	}

	all := append(before, after...)
	missing := waitForEventsInInspectStore(t, insp, all, 30*time.Second)
	if len(missing) > 0 {
		t.Errorf("concurrent multi-target disruption: %d/%d events never landed in the inspect session's local store: %v", len(missing), len(all), missing)
	}
}

// testInspectStressConcurrentMultiTargetStall pauses several targets at
// once. Necessarily slow (~70s, InspectSpec has no timeouts: block), but
// pausing several costs no more time than pausing one -- all wait out the
// same default concurrently.
func testInspectStressConcurrentMultiTargetStall(t *testing.T) {
	spec := loadTestInspectStressSpec(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	insp := newTestInspector(t, ctx, spec)

	urls := inspectStressTargetURLs()
	const disrupted = 4
	services := make([]string, disrupted)
	for i := 0; i < disrupted; i++ {
		services[i] = fmt.Sprintf("target%d", i+1)
	}

	before := make([]string, 0, disrupted)
	for i := 0; i < disrupted; i++ {
		ev := newIntegrationEvent(t, fmt.Sprintf("stall-before-%d", i))
		publishEventToRelay(t, urls[i], ev)
		before = append(before, ev.ID)
	}

	for _, s := range services {
		runCompose(t, inspectStressComposeFile, "pause", s)
	}
	resolved := false
	t.Cleanup(func() {
		if !resolved {
			for _, s := range services {
				_ = exec.Command("docker", "compose", "-f", inspectStressComposeFile, "unpause", s).Run()
			}
		}
	})

	time.Sleep(70 * time.Second)

	for _, s := range services {
		runCompose(t, inspectStressComposeFile, "unpause", s)
	}
	resolved = true

	after := make([]string, 0, disrupted)
	for i := 0; i < disrupted; i++ {
		ev := newIntegrationEvent(t, fmt.Sprintf("stall-after-%d", i))
		publishEventWithRetry(t, urls[i], ev, 30*time.Second)
		after = append(after, ev.ID)
	}

	all := append(before, after...)
	missing := waitForEventsInInspectStore(t, insp, all, 30*time.Second)
	if len(missing) > 0 {
		t.Errorf("concurrent multi-target stall: %d/%d events never landed in the inspect session's local store: %v", len(missing), len(all), missing)
	}
}

// loadTestInspectStressSpec loads stress-inspect.yaml, overriding
// `targets` with all 15 URLs (the file only lists one).
func loadTestInspectStressSpec(t *testing.T) *InspectSpec {
	t.Helper()
	rs, err := loadSpecFromYaml(inspectStressSpecFile)
	if err != nil {
		t.Fatalf("failed to load %s: %v", inspectStressSpecFile, err)
	}
	spec, ok := rs.Spec.(*InspectSpec)
	if !ok {
		t.Fatalf("%s is not a `kind: inspect` spec", inspectStressSpecFile)
	}

	urls := inspectStressTargetURLs()
	spec.Targets = make([]*FlowSpec, len(urls))
	for i, u := range urls {
		spec.Targets[i] = newRemoteFlowSpec(t, u, true, 0)
	}
	return spec
}
