// Package zaptest builds zap receipts that pass nmilat's NIP-57
// validation, for tests of top-zapped and profile stats.
package zaptest

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flokiorg/go-flokicoin/chainutil/bech32"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip57"
	"github.com/ohstr/nmilat/utils"
)

// Key is a deterministic test keypair.
type Key struct{ Priv, Pub string }

// NewKey derives key n.
func NewKey(t testing.TB, n int) Key {
	t.Helper()
	priv := fmt.Sprintf("%064x", 0x5000+n)
	pub, err := utils.GetPublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return Key{priv, pub}
}

// Receipt is a NIP-57 zap of amount milli-sats from sender to recipient,
// signed by provider (the recipient's LNURL server) at createdAt.
func Receipt(t testing.TB, sender, provider Key, recipient string, amount int64, createdAt uint64) *nip01.Event {
	t.Helper()
	req := nip57.NewZapRequest(nip57.ZapRequestParams{
		Recipient: recipient, Relays: []string{"wss://relay.example.com"}, AmountMsat: amount,
	})
	req.CreatedAt = createdAt
	if err := req.Sign(sender.Priv); err != nil {
		t.Fatal(err)
	}
	desc, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	descHash := sha256.Sum256(desc)
	payment := sha256.Sum256([]byte(fmt.Sprintf("%s-%d-%d", recipient, amount, createdAt)))

	receipt := nip57.NewZapReceipt(nip57.ZapReceiptParams{
		ProviderPubkey: provider.Pub, Recipient: recipient, Description: string(desc),
		Bolt11: bolt11(t, amount, payment[:], descHash[:]),
	})
	receipt.CreatedAt = createdAt
	if err := receipt.Sign(provider.Priv); err != nil {
		t.Fatal(err)
	}
	return receipt
}

// bolt11 builds an unsigned bitcoin invoice with the hashes receipt
// validation checks; nothing verifies its node signature.
func bolt11(t testing.TB, amountMsat int64, paymentHash, descriptionHash []byte) string {
	t.Helper()
	words := make([]byte, 7) // timestamp
	field := func(typ byte, payload []byte) {
		data, err := bech32.ConvertBits(payload, 8, 5, true)
		if err != nil {
			t.Fatal(err)
		}
		words = append(words, typ, byte(len(data)/32), byte(len(data)%32))
		words = append(words, data...)
	}
	field(1, paymentHash)      // p
	field(23, descriptionHash) // h
	words = append(words, make([]byte, 104)...)
	// One pico-bitcoin is a tenth of a milli-sat.
	s, err := bech32.Encode(fmt.Sprintf("lnbc%dp", amountMsat*10), words)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
