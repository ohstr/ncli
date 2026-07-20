package client

import (
	"context"
	"testing"

	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
)

// powTestPrivKey is an arbitrary, fixed test-only private key, only used to
// produce a validly signed event so handleEvent's event.Verify() call
// reaches its PoW check instead of failing earlier on signature format.
const powTestPrivKey = "0acd12cbf0fb87cd13b17bc9b57dffd11b3870b407984cec5a4ce2a69b90268c"

// newBadPowTestEvent signs an event that declares a NIP-13 nonce difficulty
// of 20 without actually having been mined for it -- essentially certain not
// to be met, so nip13.ValidatePow rejects it.
func newBadPowTestEvent(t *testing.T) *nip01.Event {
	t.Helper()
	ev := nip01.NewEvent(1, "pow test event", []string{"nonce", "1", "20"})
	if err := ev.Sign(powTestPrivKey); err != nil {
		t.Fatalf("failed to sign event: %v", err)
	}
	return ev
}

// newValidPowTestEvent mines a genuine, low-difficulty NIP-13 nonce so
// hasValidPow (used by neg_sync.go's pullEvents) has a positive case to
// check against, not just newBadPowTestEvent's negative one.
func newValidPowTestEvent(t *testing.T) *nip01.Event {
	t.Helper()
	pubKey, err := GetPublicKey(powTestPrivKey)
	if err != nil {
		t.Fatalf("failed to derive pubkey: %v", err)
	}
	ev := nip01.NewUnsignedEvent(1, pubKey, "pow test event")
	if err := ev.Mine(context.Background(), 4); err != nil {
		t.Fatalf("failed to mine pow: %v", err)
	}
	if err := ev.Sign(powTestPrivKey); err != nil {
		t.Fatalf("failed to sign event: %v", err)
	}
	return ev
}

// TestHasValidPow covers neg_sync.go's hasValidPow helper directly (no
// network involved, unlike TestNegSync_Integration): an event with no nonce
// tag has nothing to enforce, one with a genuinely mined nonce passes, and
// one with an unmined/mismatched nonce fails.
func TestHasValidPow(t *testing.T) {
	if !hasValidPow(nip01.NewEvent(1, "no nonce")) {
		t.Fatal("expected an event with no nonce tag to have nothing to enforce")
	}

	if !hasValidPow(newValidPowTestEvent(t)) {
		t.Fatal("expected a genuinely mined nonce to be accepted")
	}

	if hasValidPow(newBadPowTestEvent(t)) {
		t.Fatal("expected an unmined/mismatched nonce to be rejected")
	}
}

// TestHandleEventStrictPow covers the --strict-pow flag's three cases:
// lenient by default (a bad nonce is accepted), strict when requested (it's
// rejected), and Trusted always short-circuiting Verify() entirely
// regardless of strictPow. AddEvent (which bumps FlatRow's events column,
// index 1) only runs once handleEvent's Verify() call has actually
// succeeded, so it doubles as a reliable accept/reject signal here.
func TestHandleEventStrictPow(t *testing.T) {
	sc := NewStreamChannel(1, nil)
	ctx := context.Background()

	t.Run("default is lenient: bad nonce is accepted", func(t *testing.T) {
		stat := tui.NewInboundMetrics(1, "src", func() {})
		fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, false, nil)
		// fc.strictPow left at its zero value (false).

		sc.handleEvent(ctx, fc, newBadPowTestEvent(t))

		if events := stat.FlatRow()[1]; events != 1 {
			t.Fatalf("expected the event to be accepted (events=1), got events=%d", events)
		}
	})

	t.Run("strict-pow rejects a bad nonce", func(t *testing.T) {
		stat := tui.NewInboundMetrics(2, "src", func() {})
		fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, false, nil)
		fc.strictPow = true

		sc.handleEvent(ctx, fc, newBadPowTestEvent(t))

		if events := stat.FlatRow()[1]; events != 0 {
			t.Fatalf("expected the event to be rejected (events=0), got events=%d", events)
		}
	})

	t.Run("trusted flows skip verification regardless of strict-pow", func(t *testing.T) {
		stat := tui.NewInboundMetrics(3, "src", func() {})
		fc := NewFlowContext(nip01.NewSubscriptionFilterGroup(), stat, true, nil)
		fc.strictPow = true

		sc.handleEvent(ctx, fc, newBadPowTestEvent(t))

		if events := stat.FlatRow()[1]; events != 1 {
			t.Fatalf("expected a trusted flow to accept regardless of pow (events=1), got events=%d", events)
		}
	})
}
