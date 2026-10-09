package bunker

import "github.com/ohstr/nmilat/nip46/bunker"

// NewSecret generates a fresh random pairing secret, hex-encoded.
func NewSecret() (string, error) { return bunker.NewSecret() }

// BunkerURI builds the signer-generated bunker://<signer-pubkey>?relay=...&secret=...
// connection string a client pastes in to initiate pairing, one repeated
// "relay" param per relay.
func BunkerURI(signerPubkeyHex, secret string, relays []string) string {
	return (&bunker.URI{SignerPubKey: signerPubkeyHex, Relays: relays, Secret: secret}).String()
}
