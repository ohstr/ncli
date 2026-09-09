package client

// Shared helpers for this package's Docker-Compose-based hermetic
// integration tests (client/stream_integration_test.go,
// client/inspect_integration_test.go, client/sync_integration_test.go,
// ...): each brings up real `ncli relay` containers (built from this
// repo's own build/relay/Dockerfile) and drives the client module under
// test in-process against their published ports, closing the "client says
// accepted, but did the relay actually get it?" gap by always checking a
// real relay's own wire protocol independently of whatever the client
// under test believes happened. See integration/<feature>/README.md for
// what each individual stack is.
//
// Not part of `just test` (needs Docker), but runs automatically in CI as
// its own job -- see `just test-integrations` and each feature's own
// `just test-integration-<feature>` recipe.

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

// integrationPrivKey is an arbitrary, fixed test-only private key, shared
// by every docker-based e2e test in this package, used only to produce
// validly-signed synthetic events. Unlike the plain in-memory tests in
// *_regression_test.go, these events cross a real ncli relay server, which
// verifies every event's ID/signature unconditionally -- a flow's own
// `trusted` setting only ever governs what THIS client's read side skips
// checking, never what a relay server accepts on write.
const (
	integrationPrivKey = "0acd12cbf0fb87cd13b17bc9b57dffd11b3870b407984cec5a4ce2a69b90268c"
	integrationPubKey  = "3c1db3dd55e2ff09ba5317dd8eec2339797e9e2ddf74591172735c47f3a2ad6e" // derives from integrationPrivKey
)

// integrationPrivKeyAlt/integrationPubKeyAlt are a second arbitrary, fixed
// test-only keypair -- distinct from integrationPrivKey/integrationPubKey
// -- used only by filter-correctness scenarios that need to prove an
// `authors` filter actually excludes events from the "wrong" author, not
// just include events from the right one. Everything else in this package
// shares the one identity; nothing relies on these two ever being
// confused with each other.
const (
	integrationPrivKeyAlt = "ad8b71b0611f697ebd0b210ccc70ee3947b85fe59bad0c04f608217553b9c6d4"
	integrationPubKeyAlt  = "b94c6f8d038e6e9622a530991a189ae5f4a785efb234025e2a99fb3c5516c8b2" // derives from integrationPrivKeyAlt
)

// runCompose runs `docker compose -f composeFile <args...>`, failing
// the test immediately (t.Fatalf) on error. Cleanup teardown steps that
// must not mask an earlier test failure run their own exec.Command instead
// (see TestStreamIntegration's t.Cleanup) and log rather than fail.
func runCompose(t *testing.T, composeFile string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"compose", "-f", composeFile}, args...)
	cmd := exec.Command("docker", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s failed: %v\n%s", strings.Join(cmdArgs, " "), err, out)
	}
}

// newIntegrationEventOfKindUnchecked is newIntegrationEventOfKind's
// error-returning core -- signing a fixed, valid hex private key cannot
// actually fail, so this only exists to give publishManyEvents an error to
// check without calling t.Fatalf from a worker goroutine (see its own doc
// comment for why that matters). Generalizes newIntegrationEventUnchecked
// to an arbitrary kind/author/tag set, for filter-correctness scenarios
// that need to prove NIP-01 matching (kind/author/tag AND-within-a-filter,
// OR-across-filters) actually holds under real load, not just that events
// arrive at all.
func newIntegrationEventOfKindUnchecked(kind int, privKey, marker string, tags ...[]string) *nip01.Event {
	ev := nip01.NewEvent(kind, fmt.Sprintf("ncli itest %s", marker), tags...)
	if err := ev.Sign(privKey); err != nil {
		// Unreachable in practice (privKey is always one of this file's
		// fixed, valid keys), but panic rather than silently return an
		// unsigned event if it ever somehow did fail.
		panic(fmt.Sprintf("failed to sign synthetic test event: %v", err))
	}
	return ev
}

// newIntegrationEventOfKind signs a small, uniquely-content-tagged event of
// the given kind (and, optionally, tags) from the primary test identity
// (integrationPrivKey/integrationPubKey) -- marker only needs to make this
// call's content distinct from every other call's, which is all that's
// needed for a distinct event ID.
func newIntegrationEventOfKind(t *testing.T, kind int, marker string, tags ...[]string) *nip01.Event {
	t.Helper()
	return newIntegrationEventOfKindUnchecked(kind, integrationPrivKey, marker, tags...)
}

// newIntegrationEventFromAltAuthor is newIntegrationEventOfKind's sibling,
// signed by integrationPrivKeyAlt instead -- for scenarios proving an
// `authors` filter excludes the "wrong" author, not just includes the
// right one.
func newIntegrationEventFromAltAuthor(t *testing.T, kind int, marker string, tags ...[]string) *nip01.Event {
	t.Helper()
	return newIntegrationEventOfKindUnchecked(kind, integrationPrivKeyAlt, marker, tags...)
}

// newIntegrationEventUnchecked is newIntegrationEvent's error-returning
// core; see newIntegrationEventOfKindUnchecked.
func newIntegrationEventUnchecked(marker string) *nip01.Event {
	return newIntegrationEventOfKindUnchecked(1, integrationPrivKey, marker)
}

// newIntegrationEvent signs a small, uniquely-content-tagged kind:1 event
// -- marker only needs to make this call's content distinct from every
// other call's, which is all that's needed for a distinct event ID.
func newIntegrationEvent(t *testing.T, marker string) *nip01.Event {
	t.Helper()
	return newIntegrationEventUnchecked(marker)
}

// publishEventToRelayErr is publishEventToRelay's error-returning core,
// used directly by publishManyEvents' worker goroutines (which must not
// call t.Fatalf themselves -- see that function's doc comment).
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

// publishEventToRelay publishes ev to relayURL and fails the test if the
// relay doesn't accept it outright.
func publishEventToRelay(t *testing.T, relayURL string, ev *nip01.Event) {
	t.Helper()
	if err := publishEventToRelayErr(relayURL, ev); err != nil {
		t.Fatal(err)
	}
}

// newRemoteFlowSpec builds a valid remote *FlowSpec without going through
// YAML unmarshalling -- for tests that need to add a flow to a spec
// already loaded from its fixture (e.g. a second stream destination).
// Mirrors exactly what FlowSpec.UnmarshalJSON's FlOW_REMOTE case does:
// relayURI/relayFallbackURI must be resolved via ResolveRelayURL and set
// directly, or code that reads them (connectRelayWithFallback) would
// silently treat the flow as having no address at all.
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

// publishManyEventsConcurrency bounds how many simultaneous publish
// connections publishManyEvents opens against one relay -- high enough to
// make a few hundred events fast, low enough not to itself become the kind
// of unpaced burst client/stream.go's own write-concurrency cap (PR #45,
// see client/stream_regression_test.go and this file's high-volume tests)
// exists to guard against.
const publishManyEventsConcurrency = 16

// publishManyEventsErr is publishManyEvents' error-returning core --
// exported (within the package) specifically so callers that need to run
// several of these concurrently across different relays (e.g. one per
// source/target, for a burst that actually overlaps in time) can do so
// safely: spawn goroutines calling *this*, collect `(ids, err)` back
// through an ordinary channel or slice, and only call t.Fatalf once back
// on the main test goroutine. Calling the t-based publishManyEvents
// directly from inside such a goroutine would violate the same rule its
// own internal workers have to follow -- see publishManyEvents' doc
// comment.
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
// relayURL, using markerPrefix plus each event's index to keep every one
// content-distinct (see newIntegrationEvent), and returns their IDs in
// index order. Used by this package's high-volume-input scenarios to
// stress a relay/flow with real traffic instead of a handful of events.
//
// Only ever call this from the goroutine actually running the test (or a
// t.Run subtest closure) -- it calls t.Fatalf on failure, which must not
// happen from another goroutine (see testing.T's docs). If you need
// several of these running concurrently against different relays at once,
// use publishManyEventsErr directly instead (see its own doc comment).
func publishManyEvents(t *testing.T, relayURL string, n int, markerPrefix string) []string {
	t.Helper()
	ids, err := publishManyEventsErr(relayURL, n, markerPrefix)
	if err != nil {
		t.Fatal(err)
	}
	return ids
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
	if err := waitForRelayReadyErr(relayURL, timeout); err != nil {
		t.Fatal(err)
	}
}

// waitForRelayReadyErr is waitForRelayReady's error-returning core, safe
// to call from any goroutine (see waitForAllRelaysReady).
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

// waitForAllRelaysReady waits for every URL in urls concurrently, instead
// of waitForRelayReady's implied sequential cost -- with a couple dozen
// containers in a stress stack, waiting up to `timeout` *each* in sequence
// would dominate the whole test's runtime for no reason, since they're all
// starting up in parallel anyway. Fails with every URL that timed out, not
// just the first, so a genuinely broken stack is diagnosable in one shot.
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
