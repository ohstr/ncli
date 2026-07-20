package client

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
)

// TestPublishEventAccepted proves PublishEvent returns nil once the relay's
// matching OK(true) arrives.
func TestPublishEventAccepted(t *testing.T) {
	var received atomic.Int32
	server := newMockRelay(t, mockRelayAccept, &received)

	fs, err := flowSpecFromString(mockRelayWSURL(server))
	if err != nil {
		t.Fatalf("flowSpecFromString failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := connectRelayWithFallback(ctx, fs.relayURI, fs.relayFallbackURI, nil)
	if err != nil {
		t.Fatalf("failed to connect to mock relay: %v", err)
	}
	defer conn.Close()

	event := newRecoveryTestEvent(fmt.Sprintf("%064x", 1))
	if err := PublishEvent(ctx, conn, event); err != nil {
		t.Fatalf("PublishEvent failed against an accepting relay: %v", err)
	}
	if received.Load() != 1 {
		t.Fatalf("expected the mock relay to receive 1 event, got %d", received.Load())
	}
}

// TestPublishEventRejected proves PublishEvent surfaces the relay's OK(false)
// reason as an error, rather than treating any OK as success.
func TestPublishEventRejected(t *testing.T) {
	server := newMockRelay(t, mockRelayReject, nil)

	fs, err := flowSpecFromString(mockRelayWSURL(server))
	if err != nil {
		t.Fatalf("flowSpecFromString failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := connectRelayWithFallback(ctx, fs.relayURI, fs.relayFallbackURI, nil)
	if err != nil {
		t.Fatalf("failed to connect to mock relay: %v", err)
	}
	defer conn.Close()

	event := newRecoveryTestEvent(fmt.Sprintf("%064x", 2))
	if err := PublishEvent(ctx, conn, event); err == nil {
		t.Fatal("expected an error when the relay rejects the event, got nil")
	}
}

// TestPublishToTargetsCrossProduct proves PublishToTargets sends every event
// to every relay target (the full (event, relay) cross product), not just
// one event per relay or one relay per event.
func TestPublishToTargetsCrossProduct(t *testing.T) {
	var received1, received2 atomic.Int32
	server1 := newMockRelay(t, mockRelayAccept, &received1)
	server2 := newMockRelay(t, mockRelayAccept, &received2)

	targets := targetsSpecFromURLs(t, mockRelayWSURL(server1), mockRelayWSURL(server2))
	events := []*nip01.Event{
		newRecoveryTestEvent(fmt.Sprintf("%064x", 3)),
		newRecoveryTestEvent(fmt.Sprintf("%064x", 4)),
	}

	report, err := PublishToTargets(context.Background(), targets, events)
	if err != nil {
		t.Fatalf("PublishToTargets failed: %v", err)
	}
	if report.Attempted != 4 || report.Succeeded != 4 || report.Failed != 0 {
		t.Fatalf("expected 4 attempted/succeeded (2 events x 2 relays), got %+v", report)
	}
	if received1.Load() != 2 || received2.Load() != 2 {
		t.Fatalf("expected both relays to receive both events, got relay1=%d relay2=%d", received1.Load(), received2.Load())
	}
}

// TestPublishToTargetsToleratesUnreachableTarget proves an individual
// unreachable relay is logged and skipped -- matching
// mergeEventsFromTargets' tolerance for one bad target elsewhere (find/
// dump/miner check) -- rather than failing the whole publish attempt, as
// long as at least one target is reachable.
func TestPublishToTargetsToleratesUnreachableTarget(t *testing.T) {
	var received atomic.Int32
	good := newMockRelay(t, mockRelayAccept, &received)

	dead := httptest.NewServer(nil)
	deadURL := mockRelayWSURL(dead)
	dead.Close() // guaranteed connection failure: nothing is listening anymore

	targets := targetsSpecFromURLs(t, deadURL, mockRelayWSURL(good))
	events := []*nip01.Event{newRecoveryTestEvent(fmt.Sprintf("%064x", 5))}

	report, err := PublishToTargets(context.Background(), targets, events)
	if err != nil {
		t.Fatalf("expected the reachable target to make this succeed, got error: %v", err)
	}
	if report.Attempted != 1 || report.Succeeded != 1 {
		t.Fatalf("expected the one event to reach the one good relay, got %+v", report)
	}
	if received.Load() != 1 {
		t.Fatalf("expected the good relay to receive 1 event, got %d", received.Load())
	}
}

// TestPublishToTargetsAllUnreachable proves ErrNoReachableTargets is
// returned (not a false empty-but-successful report) when every target
// fails to connect.
func TestPublishToTargetsAllUnreachable(t *testing.T) {
	dead := httptest.NewServer(nil)
	deadURL := mockRelayWSURL(dead)
	dead.Close()

	targets := targetsSpecFromURLs(t, deadURL)
	events := []*nip01.Event{newRecoveryTestEvent(fmt.Sprintf("%064x", 6))}

	if _, err := PublishToTargets(context.Background(), targets, events); err != ErrNoReachableTargets {
		t.Fatalf("expected ErrNoReachableTargets, got %v", err)
	}
}

// TestPublishToTargetsRejectsLocalTarget proves a local-store target is
// rejected with a clear error instead of silently doing nothing -- local
// targets aren't supported by ncli publish yet.
func TestPublishToTargetsRejectsLocalTarget(t *testing.T) {
	targets := &TargetsSpec{Relays: []*FlowSpec{{Type: FlOW_LOCAL, Path: t.TempDir() + "/notes.db"}}}
	events := []*nip01.Event{newRecoveryTestEvent(fmt.Sprintf("%064x", 7))}

	if _, err := PublishToTargets(context.Background(), targets, events); err == nil {
		t.Fatal("expected an error for a local-store target, got nil")
	}
}

// targetsSpecFromURLs builds a *TargetsSpec directly from ws:// URLs (as
// produced by mockRelayWSURL), for tests that don't need a --relays
// comma-string round trip.
func targetsSpecFromURLs(t *testing.T, urls ...string) *TargetsSpec {
	t.Helper()
	spec := &TargetsSpec{}
	for _, u := range urls {
		fs, err := flowSpecFromString(u)
		if err != nil {
			t.Fatalf("flowSpecFromString(%q) failed: %v", u, err)
		}
		spec.Relays = append(spec.Relays, fs)
	}
	return spec
}
