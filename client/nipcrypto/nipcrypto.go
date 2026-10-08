// Package nipcrypto runs the NIP-04/NIP-44 encrypt/decrypt half of the
// NIP-46 method set against a local private key. Shared by the relay
// bunker and the unix-socket signer.
package nipcrypto

import (
	"encoding/hex"
	"errors"
	"fmt"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/go-flokicoin/crypto/schnorr"
	"github.com/ohstr/nmilat/nip04"
	"github.com/ohstr/nmilat/nip44"
	"github.com/ohstr/nmilat/nip46"
)

// IsCryptoMethod reports whether method is one Do handles.
func IsCryptoMethod(method string) bool {
	switch method {
	case nip46.MethodNIP04Encrypt, nip46.MethodNIP04Decrypt, nip46.MethodNIP44Encrypt, nip46.MethodNIP44Decrypt:
		return true
	}
	return false
}

// Do runs one nip04_*/nip44_* method with NIP-46 params [peer-pubkey, text].
func Do(method, privKeyHex string, params []string) (string, error) {
	if len(params) < 2 {
		return "", errors.New("expected [pubkey, text] params")
	}
	peerPub, text := params[0], params[1]

	switch method {
	case nip46.MethodNIP04Encrypt:
		return nip04.Encrypt(text, privKeyHex, peerPub)
	case nip46.MethodNIP04Decrypt:
		return nip04.Decrypt(text, peerPub, privKeyHex)
	case nip46.MethodNIP44Encrypt:
		key, err := ConversationKey(privKeyHex, peerPub)
		if err != nil {
			return "", err
		}
		return nip44.Encrypt(text, key)
	case nip46.MethodNIP44Decrypt:
		key, err := ConversationKey(privKeyHex, peerPub)
		if err != nil {
			return "", err
		}
		return nip44.Decrypt(text, key)
	default:
		return "", fmt.Errorf("unsupported method %q", method)
	}
}

// ConversationKey derives the NIP-44 conversation key between privKeyHex
// and peerPubkeyHex. nip46 keeps its own copy unexported.
func ConversationKey(privKeyHex, peerPubkeyHex string) ([]byte, error) {
	privBytes, err := hex.DecodeString(privKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid identity private key: %w", err)
	}
	priv, _ := btcec.PrivKeyFromBytes(privBytes)

	pubBytes, err := hex.DecodeString(peerPubkeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid peer public key: %w", err)
	}
	pub, err := schnorr.ParsePubKey(pubBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid peer public key: %w", err)
	}

	return nip44.GenerateConversationKey(priv, pub)
}
