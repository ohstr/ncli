package vault

import (
	"encoding/hex"
	"fmt"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/utils"
)

// Identity bundles every representation of a Nostr keypair.
type Identity struct {
	PrivKeyHex string
	PubKeyHex  string
	Nsec       string
	Npub       string
}

// GenerateIdentity mints a brand-new secp256k1 keypair and returns every
// display form of it. This is the only place a new keypair is ever created.
//
// It lives beside the vault rather than in client so that generating a key
// and saving it are reachable together without the streaming stack; client
// re-exports both names.
func GenerateIdentity() (*Identity, error) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}
	privHex := hex.EncodeToString(priv.Serialize())

	pubHex, err := utils.GetPublicKey(privHex)
	if err != nil {
		return nil, fmt.Errorf("failed to derive public key: %w", err)
	}

	nsec, err := nip19.EncodePrivateKey(privHex)
	if err != nil {
		return nil, err
	}
	npub, err := nip19.EncodePublicKey(pubHex)
	if err != nil {
		return nil, err
	}

	return &Identity{PrivKeyHex: privHex, PubKeyHex: pubHex, Nsec: nsec, Npub: npub}, nil
}
