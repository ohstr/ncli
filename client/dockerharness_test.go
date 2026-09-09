package client

// Shared helpers for this package's Docker-Compose-based hermetic e2e
// tests (client/stream_docker_test.go, client/inspect_docker_test.go,
// client/sync_docker_test.go, ...): each brings up real `ncli relay`
// containers (built from this repo's own build/relay/Dockerfile) and drives
// the client module under test in-process against their published ports,
// closing the "client says accepted, but did the relay actually get it?"
// gap by always checking a real relay's own wire protocol independently of
// whatever the client under test believes happened. See
// integration/<feature>/README.md for what each individual stack is.
//
// None of this is wired into `just test`/CI (needs Docker); see each
// feature's own `just test-integration-<feature>` recipe.

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	relayclient "github.com/ohstr/nmilat/relay/client"
)

// dockerHarnessPrivKey is an arbitrary, fixed test-only private key, shared
// by every docker-based e2e test in this package, used only to produce
// validly-signed synthetic events. Unlike the plain in-memory tests in
// *_regression_test.go, these events cross a real ncli relay server, which
// verifies every event's ID/signature unconditionally -- a flow's own
// `trusted` setting only ever governs what THIS client's read side skips
// checking, never what a relay server accepts on write.
const dockerHarnessPrivKey = "0acd12cbf0fb87cd13b17bc9b57dffd11b3870b407984cec5a4ce2a69b90268c"

// runDockerCompose runs `docker compose -f composeFile <args...>`, failing
// the test immediately (t.Fatalf) on error. Cleanup teardown steps that
// must not mask an earlier test failure run their own exec.Command instead
// (see TestStreamDocker's t.Cleanup) and log rather than fail.
func runDockerCompose(t *testing.T, composeFile string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"compose", "-f", composeFile}, args...)
	cmd := exec.Command("docker", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s failed: %v\n%s", strings.Join(cmdArgs, " "), err, out)
	}
}

// newDockerHarnessEvent signs a small, uniquely-content-tagged kind:1 event
// -- marker only needs to make this call's content distinct from every
// other call's, which is all that's needed for a distinct event ID.
func newDockerHarnessEvent(t *testing.T, marker string) *nip01.Event {
	t.Helper()
	ev := nip01.NewEvent(1, fmt.Sprintf("ncli itest %s", marker))
	if err := ev.Sign(dockerHarnessPrivKey); err != nil {
		t.Fatalf("failed to sign synthetic test event: %v", err)
	}
	return ev
}

// publishEventToRelay publishes ev to relayURL and fails the test if the
// relay doesn't accept it outright.
func publishEventToRelay(t *testing.T, relayURL string, ev *nip01.Event) {
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

// publishEventWithRetry is publishEventToRelay's tolerant sibling, for the
// one case where the target relay is expected to be briefly unreachable
// (right after a forced container restart).
func publishEventWithRetry(t *testing.T, relayURL string, ev *nip01.Event, timeout time.Duration) {
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
// given event IDs, independent of anything the client under test itself
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

// waitForEventsAtRelay polls relayURL until every ID in ids is retrievable
// or timeout elapses, returning whatever's still missing at that point
// (empty on full success).
func waitForEventsAtRelay(t *testing.T, relayURL string, ids []string, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var missing []string
	for {
		found := fetchEventIDsFromRelay(t, relayURL, ids)
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
