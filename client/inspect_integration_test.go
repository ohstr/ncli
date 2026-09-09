package client

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
)

// See integration/inspect/README.md for what this stack is. Shared
// docker-lifecycle/publish/fetch helpers used below (runCompose,
// newIntegrationEvent, publishEventToRelay, waitForRelayReady, etc.) live
// in client/integrationharness_test.go, alongside
// client/stream_integration_test.go.
const (
	inspectIntegrationComposeFile = "../integration/inspect/compose.yaml"
	inspectIntegrationSpecFile    = "../integration/inspect/inspect.yaml"
)

var inspectIntegrationTargetURLs = []string{
	"ws://localhost:45510",
	"ws://localhost:45511",
	"ws://localhost:45512",
}

// TestInspectIntegration brings up integration/inspect/compose.yaml's three real
// `ncli relay` containers once, then runs each scenario as a subtest
// against that shared stack -- needs Docker, hits no production relay.
// `apply -f inspect.yaml` has no `client` package coverage against a real
// relay at all before this (client/inspect_store_test.go only exercises
// the local store in isolation); this closes that gap. See
// `just test-integration-inspect`.
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
	t.Run("TargetReconnectDoesNotMissEvents", testInspectTargetReconnectDoesNotMissEvents)
}

// testInspectCollectsFromAllTargets is the direct analog, for inspect's
// read-only multi-target fan-in, of the scenario stream's docker test
// covers for its multi-source fan-in: every one of several relays holds
// events nothing else does, and a single inspect session pointed at all of
// them (the exact "all relays as source" shape) must come away with every
// one of them in its local session store, not just some.
func testInspectCollectsFromAllTargets(t *testing.T) {
	spec := loadTestInspectSpec(t)

	var published []string
	for i, target := range inspectIntegrationTargetURLs {
		for j := 0; j < 2; j++ {
			ev := newIntegrationEvent(t, fmt.Sprintf("collect-t%d-e%d", i, j))
			publishEventToRelay(t, target, ev)
			published = append(published, ev.ID)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	insp := newTestInspector(t, ctx, spec)

	missing := waitForEventsInInspectStore(t, insp, published, 15*time.Second)
	if len(missing) > 0 {
		t.Errorf("published %d events across %d targets, but %d never landed in the inspect session's local store: %v",
			len(published), len(inspectIntegrationTargetURLs), len(missing), missing)
	}
}

// testInspectTargetReconnectDoesNotMissEvents covers the general "flaky
// source relay" mechanism for inspect's read side (ClientSubscriptionContext.Run,
// shared with stream's sources -- see client/inspect.go's NewInspector):
// restarting a target mid-inspect must not hang the session, and an event
// published to that target once it's back up must still be picked up.
func testInspectTargetReconnectDoesNotMissEvents(t *testing.T) {
	spec := loadTestInspectSpec(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	insp := newTestInspector(t, ctx, spec)

	before := newIntegrationEvent(t, "inspect-reconnect-before")
	publishEventToRelay(t, inspectIntegrationTargetURLs[0], before)

	runCompose(t, inspectIntegrationComposeFile, "restart", "target1")

	// Not required for correctness, just makes sure the next publish
	// genuinely lands after the restart has taken effect rather than
	// racing one that hasn't started yet -- mirrors
	// testSourceReconnectDoesNotHang's identical use of a plain sleep here.
	time.Sleep(2 * time.Second)

	after := newIntegrationEvent(t, "inspect-reconnect-after")
	// target1 may still be mid-restart, so retry the publish itself --
	// what's under test is the inspect session's own reconnect, not this
	// helper publish's timing.
	publishEventWithRetry(t, inspectIntegrationTargetURLs[0], after, 30*time.Second)

	missing := waitForEventsInInspectStore(t, insp, []string{before.ID, after.ID}, 20*time.Second)
	if len(missing) > 0 {
		t.Errorf("target reconnect: %d/2 events never reached the inspect session's local store: %v", len(missing), missing)
	}
}

// loadTestInspectSpec loads integration/inspect/inspect.yaml the same way
// `ncli apply` itself would (loadSpecFromYaml) -- see this file's package
// doc comment and integration/inspect/README.md for why the test still
// constructs the Inspector directly rather than going through Client/apply
// itself.
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

// newTestInspector starts an Inspector headlessly: a plain, un-Init'd
// tui.EventTable is enough to make NewInspector wire up its retain callback
// (see client/inspect.go), which is what actually inserts every received
// event into the session's local InspectStore -- EventTable.Push itself
// only ever touches its own row slice, never the tui.App it's built with,
// so passing a nil *tui.App here is safe as long as nothing tries to render
// it (which this headless test never does).
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
