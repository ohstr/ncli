// Package zaptest builds zap receipts that pass nmilat's validation, for
// tests of the zap ranking and profile stats.
package zaptest

import (
	"testing"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip57/nip57test"
	"github.com/ohstr/nmilat/nipAZ"
)

// Key is a deterministic test keypair.
type Key = nip57test.Key

// NewKey derives key n.
func NewKey(t testing.TB, n int) Key {
	t.Helper()
	return nip57test.NewKey(0x4000 + n)
}

// Receipt is a NIP-57 zap of amount milli-sats from sender to recipient,
// signed by provider (the recipient's LNURL server) at createdAt.
func Receipt(t testing.TB, sender, provider Key, recipient string, amount int64, createdAt uint64) *nip01.Event {
	t.Helper()
	return receipt(t, nip57test.Spec{Sender: sender, Provider: provider, Recipient: recipient, Amount: amount, CreatedAt: createdAt})
}

// LokiReceipt is Receipt paid in loki: a NIP-AZ kind 9521 on Flokicoin.
func LokiReceipt(t testing.TB, sender, provider Key, recipient string, amount int64, createdAt uint64) *nip01.Event {
	t.Helper()
	return receipt(t, nip57test.Spec{Kind: nipAZ.KindAltZapReceipt, Sender: sender, Provider: provider, Recipient: recipient, Amount: amount, CreatedAt: createdAt})
}

func receipt(t testing.TB, s nip57test.Spec) *nip01.Event {
	t.Helper()
	ev, err := nip57test.Receipt(s)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}
