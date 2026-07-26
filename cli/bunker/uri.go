package bunker

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
)

// secretByteLen matches the entropy other NIP-46 signer implementations
// (nsec.app, Amber) use for a bunker://Nostrconnect pairing secret.
const secretByteLen = 16

// NewSecret generates a fresh random pairing secret, hex-encoded.
func NewSecret() (string, error) {
	b := make([]byte, secretByteLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate pairing secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// BunkerURI builds the signer-generated bunker://<signer-pubkey>?relay=...&secret=...
// connection string a client pastes in to initiate pairing -- the
// counterpart flow to nip46.ParseNostrconnect's client-generated
// nostrconnect://, which this package consumes directly (no wrapper
// needed: it's already the exact shape daemon.go wants). Unlike
// nostrconnect://, which this SDK's ParseNostrconnect only reads a single
// relay query param from, bunker:// here supports one or more relays (the
// wider real-world convention), each as its own repeated "relay" param.
func BunkerURI(signerPubkeyHex, secret string, relays []string) string {
	q := url.Values{}
	for _, r := range relays {
		q.Add("relay", r)
	}
	if secret != "" {
		q.Set("secret", secret)
	}

	u := url.URL{
		Scheme:   "bunker",
		Host:     signerPubkeyHex,
		RawQuery: q.Encode(),
	}
	return u.String()
}
