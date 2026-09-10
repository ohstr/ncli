package client

// Shared helpers for this package's Docker Compose integration tests
// (stream/inspect/sync _integration_test.go and _stress_test.go). Each
// brings up real `ncli relay` containers and drives the client under test
// in-process, verifying results against the relay's own wire protocol
// rather than trusting the client's self-report.
//
// Needs Docker, not part of `just test`. See `just test-integrations`.

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	relayclient "github.com/ohstr/nmilat/relay/client"
)

// Fixed test-only keypair, shared by every event these tests sign.
const (
	integrationPrivKey = "0acd12cbf0fb87cd13b17bc9b57dffd11b3870b407984cec5a4ce2a69b90268c"
	integrationPubKey  = "3c1db3dd55e2ff09ba5317dd8eec2339797e9e2ddf74591172735c47f3a2ad6e"
)

// Second fixed keypair, used only to prove an `authors` filter excludes
// the wrong author, not just includes the right one.
const (
	integrationPrivKeyAlt = "ad8b71b0611f697ebd0b210ccc70ee3947b85fe59bad0c04f608217553b9c6d4"
	integrationPubKeyAlt  = "b94c6f8d038e6e9622a530991a189ae5f4a785efb234025e2a99fb3c5516c8b2"
)

// runCompose runs `docker compose -f composeFile <args...>`, failing the
// test on error. Teardown steps use their own exec.Command instead, so a
// cleanup failure doesn't mask an earlier test failure.
func runCompose(t *testing.T, composeFile string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"compose", "-f", composeFile}, args...)
	cmd := exec.Command("docker", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s failed: %v\n%s", strings.Join(cmdArgs, " "), err, out)
	}
}

// Error-returning core, safe to call from any goroutine.
func newIntegrationEventOfKindUnchecked(kind int, privKey, marker string, tags ...[]string) *nip01.Event {
	ev := nip01.NewEvent(kind, fmt.Sprintf("ncli itest %s", marker), tags...)
	if err := ev.Sign(privKey); err != nil {
		panic(fmt.Sprintf("failed to sign synthetic test event: %v", err))
	}
	return ev
}

func newIntegrationEventOfKind(t *testing.T, kind int, marker string, tags ...[]string) *nip01.Event {
	t.Helper()
	return newIntegrationEventOfKindUnchecked(kind, integrationPrivKey, marker, tags...)
}

// newIntegrationEventFromAltAuthor signs with the alt identity instead.
func newIntegrationEventFromAltAuthor(t *testing.T, kind int, marker string, tags ...[]string) *nip01.Event {
	t.Helper()
	return newIntegrationEventOfKindUnchecked(kind, integrationPrivKeyAlt, marker, tags...)
}

func newIntegrationEventUnchecked(marker string) *nip01.Event {
	return newIntegrationEventOfKindUnchecked(1, integrationPrivKey, marker)
}

// newIntegrationEvent defaults to kind:1, primary identity.
func newIntegrationEvent(t *testing.T, marker string) *nip01.Event {
	t.Helper()
	return newIntegrationEventUnchecked(marker)
}

// publishEventToRelayErr is publishEventToRelay's error-returning core,
// safe to call from any goroutine.
func publishEventToRelayErr(relayURL string, ev *nip01.Event) error {
	u, err := url.Parse(relayURL)
	if err != nil {
		return fmt.Errorf("invalid relay URL %q: %w", relayURL, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := relayclient.PublishEventToRelay(ctx, u, ev)
	if err != nil {
		return fmt.Errorf("failed to publish event %s to %s: %w", ev.ID, relayURL, err)
	}
	if !resp.Accepted {
		return fmt.Errorf("relay %s rejected event %s: %s", relayURL, ev.ID, resp.Message)
	}
	return nil
}

// publishEventToRelay fails the test if ev isn't accepted.
func publishEventToRelay(t *testing.T, relayURL string, ev *nip01.Event) {
	t.Helper()
	if err := publishEventToRelayErr(relayURL, ev); err != nil {
		t.Fatal(err)
	}
}

// newRemoteFlowSpec builds a valid remote *FlowSpec without going through
// YAML, for adding a flow to a spec already loaded from its fixture.
func newRemoteFlowSpec(t *testing.T, relayURL string, trusted bool, writeConcurrency int) *FlowSpec {
	t.Helper()
	u, fallback, err := ResolveRelayURL(relayURL)
	if err != nil {
		t.Fatalf("ResolveRelayURL(%q): %v", relayURL, err)
	}
	return &FlowSpec{
		Type:             FlOW_REMOTE,
		Relay:            relayURL,
		Trusted:          trusted,
		WriteConcurrency: writeConcurrency,
		relayURI:         u,
		relayFallbackURI: fallback,
	}
}

const publishManyEventsConcurrency = 16

// publishManyEventsErr is publishManyEvents' error-returning core, safe to
// call from any goroutine.
func publishManyEventsErr(relayURL string, n int, markerPrefix string) ([]string, error) {
	ids := make([]string, n)
	errs := make(chan error, n)

	sem := make(chan struct{}, publishManyEventsConcurrency)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		ev := newIntegrationEventUnchecked(fmt.Sprintf("%s-%d", markerPrefix, i))
		ids[i] = ev.ID

		wg.Add(1)
		sem <- struct{}{}
		go func(ev *nip01.Event) {
			defer wg.Done()
			defer func() { <-sem }()
			errs <- publishEventToRelayErr(relayURL, ev)
		}(ev)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			return nil, fmt.Errorf("publishManyEvents(%q, n=%d, %q): %w", relayURL, n, markerPrefix, err)
		}
	}
	return ids, nil
}

// publishManyEvents signs and publishes n distinct kind:1 events to
// relayURL, returning their IDs in order. Calls t.Fatalf on failure --
// call publishManyEventsErr instead from any non-test goroutine.
func publishManyEvents(t *testing.T, relayURL string, n int, markerPrefix string) []string {
	t.Helper()
	ids, err := publishManyEventsErr(relayURL, n, markerPrefix)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// publishEventWithRetry is publishEventToRelay's tolerant sibling, for
// when the target relay may be briefly unreachable (e.g. just restarted).
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

// fetchEventIDsFromRelayErr is fetchEventIDsFromRelay's error-returning
// core, safe to call from any goroutine and safe to retry on a transient
// failure (e.g. a single query outrunning its own 5s timeout under load).
func fetchEventIDsFromRelayErr(relayURL string, ids []string) (map[string]bool, error) {
	u, err := url.Parse(relayURL)
	if err != nil {
		return nil, fmt.Errorf("invalid relay URL %q: %w", relayURL, err)
	}
	filters := nip01.NewSubscriptionFilterGroup()
	filters.Add(&nip01.SubscriptionFilter{IDs: ids})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, err := relayclient.ReadEventsFromRelay(ctx, u, filters)
	if err != nil {
		return nil, fmt.Errorf("failed to query %s for %d event IDs: %w", relayURL, len(ids), err)
	}
	found := make(map[string]bool, len(events))
	for _, ev := range events {
		found[ev.ID] = true
	}
	return found, nil
}

// fetchEventIDsFromRelay queries relayURL directly for the given IDs,
// independent of what the client under test believes happened. Fails the
// test immediately on error -- see waitForEventsAtRelay for the tolerant,
// retrying counterpart.
func fetchEventIDsFromRelay(t *testing.T, relayURL string, ids []string) map[string]bool {
	t.Helper()
	found, err := fetchEventIDsFromRelayErr(relayURL, ids)
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// waitForEventsAtRelay polls relayURL until every ID is retrievable or
// timeout elapses, returning whatever's still missing. A transient query
// failure (e.g. one query outrunning its own 5s timeout under CI load) is
// treated as just another empty result to retry, not an immediate fatal --
// fetchEventIDsFromRelay's t.Fatal-on-error behavior used to short-circuit
// this loop on the very first hiccup, defeating the whole point of polling
// up to timeout.
func waitForEventsAtRelay(t *testing.T, relayURL string, ids []string, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var missing []string
	for {
		found, err := fetchEventIDsFromRelayErr(relayURL, ids)
		if err != nil {
			t.Logf("waitForEventsAtRelay: query failed, retrying: %v", err)
			found = nil
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

func waitForRelayReady(t *testing.T, relayURL string, timeout time.Duration) {
	t.Helper()
	if err := waitForRelayReadyErr(relayURL, timeout); err != nil {
		t.Fatal(err)
	}
}

// waitForRelayReadyErr is waitForRelayReady's error-returning core, safe
// to call from any goroutine.
func waitForRelayReadyErr(relayURL string, timeout time.Duration) error {
	u, err := url.Parse(relayURL)
	if err != nil {
		return fmt.Errorf("invalid relay URL %q: %w", relayURL, err)
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
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("relay at %s never became ready within %s: %w", relayURL, timeout, lastErr)
}

// waitForAllRelaysReady waits for every URL concurrently instead of
// sequentially, and reports every URL that failed, not just the first.
func waitForAllRelaysReady(t *testing.T, urls []string, timeout time.Duration) {
	t.Helper()
	errs := make([]error, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			errs[i] = waitForRelayReadyErr(u, timeout)
		}(i, u)
	}
	wg.Wait()

	var failed []error
	for _, err := range errs {
		if err != nil {
			failed = append(failed, err)
		}
	}
	if len(failed) > 0 {
		t.Fatalf("%d/%d relays never became ready: %v", len(failed), len(urls), failed)
	}
}
