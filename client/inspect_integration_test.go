package client

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
)

// See integration/inspect/README.md. Shared helpers live in
// client/integrationharness_test.go.
const (
	inspectIntegrationComposeFile = "../integration/inspect/compose.yaml"
	inspectIntegrationSpecFile    = "../integration/inspect/inspect.yaml"
)

var inspectIntegrationTargetURLs = []string{
	"ws://localhost:45510",
	"ws://localhost:45511",
	"ws://localhost:45512",
}

// TestInspectIntegration brings up compose.yaml's three real relay
// containers once, then runs each scenario as a subtest. Needs Docker.
func TestInspectIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker-based inspect integration test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found on PATH, skipping inspect integration test")
	}

	runCompose(t, inspectIntegrationComposeFile, "up", "-d", "--build")
	t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "-f", inspectIntegrationComposeFile, "down", "-v")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("docker compose down failed: %v\n%s", err, out)
		}
	})

	for _, raw := range inspectIntegrationTargetURLs {
		waitForRelayReady(t, raw, 60*time.Second)
	}

	t.Run("CollectsFromAllTargets", testInspectCollectsFromAllTargets)
	t.Run("TargetDisruptionDoesNotMissEvents", testInspectTargetDisruptionDoesNotMissEvents)
	t.Run("DuplicateEventAcrossOverlappingTargetsIsNotDoubleStored", testInspectDuplicateEventAcrossOverlappingTargetsIsNotDoubleStored)
}

// testInspectCollectsFromAllTargets: every target holds events nothing
// else does; a session pointed at all of them must collect every one.
// "Large" is the high-volume case.
func testInspectCollectsFromAllTargets(t *testing.T) {
	cases := []struct {
		name      string
		perTarget int
	}{
		{"Small", 2},
		{"Large", 100},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := loadTestInspectSpec(t)

			idsByTarget := make([][]string, len(inspectIntegrationTargetURLs))
			errsByTarget := make([]error, len(inspectIntegrationTargetURLs))
			var wg sync.WaitGroup
			for i, target := range inspectIntegrationTargetURLs {
				wg.Add(1)
				go func(i int, target string) {
					defer wg.Done()
					idsByTarget[i], errsByTarget[i] = publishManyEventsErr(target, tc.perTarget, fmt.Sprintf("%s-t%d", tc.name, i))
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

			missing := waitForEventsInInspectStore(t, insp, published, 30*time.Second)
			if len(missing) > 0 {
				t.Errorf("published %d events across %d targets, but %d never landed in the inspect session's local store: %v",
					len(published), len(inspectIntegrationTargetURLs), len(missing), missing)
			}
		})
	}
}

// testInspectTargetDisruptionDoesNotMissEvents covers restart vs. a silent
// `docker compose pause` stall (see stream's
// testDestinationDisruptionDoesNotDropEvents). The disruption must not
// hang the session, and an event published once the target's back must
// still land.
func testInspectTargetDisruptionDoesNotMissEvents(t *testing.T) {
	cases := []struct {
		name                string
		targetURL           string
		settle              time.Duration
		disrupt             func(t *testing.T)
		resolve             func(t *testing.T) // called once in the main flow
		cleanupIfUnresolved func()             // best-effort fallback if resolve never ran
	}{
		{
			name:      "Restart",
			targetURL: inspectIntegrationTargetURLs[0],
			settle:    2 * time.Second,
			disrupt:   func(t *testing.T) { runCompose(t, inspectIntegrationComposeFile, "restart", "target1") },
			resolve:   func(t *testing.T) {},
		},
		{
			name:      "Stall",
			targetURL: inspectIntegrationTargetURLs[2],
			// InspectSpec has no `timeouts:` block, so this waits out
			// relayclient's default 60s PongTimeout.
			settle:  70 * time.Second,
			disrupt: func(t *testing.T) { runCompose(t, inspectIntegrationComposeFile, "pause", "target3") },
			resolve: func(t *testing.T) { runCompose(t, inspectIntegrationComposeFile, "unpause", "target3") },
			cleanupIfUnresolved: func() {
				_ = exec.Command("docker", "compose", "-f", inspectIntegrationComposeFile, "unpause", "target3").Run()
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := loadTestInspectSpec(t)

			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			insp := newTestInspector(t, ctx, spec)

			// Avoid double-unpausing if resolve already ran below.
			resolved := false
			if tc.cleanupIfUnresolved != nil {
				t.Cleanup(func() {
					if !resolved {
						tc.cleanupIfUnresolved()
					}
				})
			}

			before := newIntegrationEvent(t, tc.name+"-before")
			publishEventToRelay(t, tc.targetURL, before)

			tc.disrupt(t)
			time.Sleep(tc.settle)
			tc.resolve(t)
			resolved = true

			after := newIntegrationEvent(t, tc.name+"-after")
			publishEventWithRetry(t, tc.targetURL, after, 30*time.Second)

			missing := waitForEventsInInspectStore(t, insp, []string{before.ID, after.ID}, 20*time.Second)
			if len(missing) > 0 {
				t.Errorf("%s: %d/2 events never reached the inspect session's local store: %v", tc.name, len(missing), missing)
			}
		})
	}
}

// testInspectDuplicateEventAcrossOverlappingTargetsIsNotDoubleStored:
// publish one event to two targets, assert the store ends up with exactly
// one row for it, not two.
func testInspectDuplicateEventAcrossOverlappingTargetsIsNotDoubleStored(t *testing.T) {
	spec := loadTestInspectSpec(t)

	shared := newIntegrationEvent(t, "duplicate-across-targets")
	publishEventToRelay(t, inspectIntegrationTargetURLs[0], shared)
	publishEventToRelay(t, inspectIntegrationTargetURLs[1], shared)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	insp := newTestInspector(t, ctx, spec)

	if missing := waitForEventsInInspectStore(t, insp, []string{shared.ID}, 15*time.Second); len(missing) > 0 {
		t.Fatalf("event published to 2 overlapping targets never landed in the inspect session's local store: %v", missing)
	}

	events, err := insp.store.store.FindEvents(context.Background(), &nip01.SubscriptionFilter{IDs: []string{shared.ID}})
	if err != nil {
		t.Fatalf("failed to query inspect session store: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected exactly 1 stored row for an event delivered by 2 overlapping targets, got %d", len(events))
	}
}

func loadTestInspectSpec(t *testing.T) *InspectSpec {
	t.Helper()
	rs, err := loadSpecFromYaml(inspectIntegrationSpecFile)
	if err != nil {
		t.Fatalf("failed to load %s: %v", inspectIntegrationSpecFile, err)
	}
	spec, ok := rs.Spec.(*InspectSpec)
	if !ok {
		t.Fatalf("%s is not a `kind: inspect` spec", inspectIntegrationSpecFile)
	}
	return spec
}

// newTestInspector starts an Inspector headlessly (a plain tui.EventTable
// is enough to wire up its retain callback without rendering anything).
func newTestInspector(t *testing.T, ctx context.Context, spec *InspectSpec) *Inspector {
	t.Helper()
	events := tui.NewEventTable(nil, nil)
	insp, err := NewInspector(ctx, spec, events)
	if err != nil {
		t.Fatalf("NewInspector failed: %v", err)
	}
	t.Cleanup(insp.Close)
	return insp
}

// waitForEventsInInspectStore polls insp's local session store until every
// ID in ids is present or timeout elapses, returning whatever's still
// missing at that point (empty on full success).
func waitForEventsInInspectStore(t *testing.T, insp *Inspector, ids []string, timeout time.Duration) []string {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	var missing []string
	for {
		events, err := insp.store.store.FindEvents(ctx, &nip01.SubscriptionFilter{IDs: ids})
		if err != nil {
			t.Fatalf("failed to query inspect session store: %v", err)
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
