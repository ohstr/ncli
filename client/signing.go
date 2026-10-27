package client

import (
	"context"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip44"
	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/utils"
)

// Signer signs events for one identity: a key held in this process
// (LocalSigner) or a signer reached over a socket.
type Signer interface {
	PubKey() string
	Sign(ctx context.Context, ev *nip01.Event) error
}

// Crypto is a Signer that also encrypts and decrypts, as a DM needs.
// LocalSigner, a socket signer and a bunker all are.
type Crypto interface {
	Signer
	Encrypt(ctx context.Context, scheme, peerPubKey, plaintext string) (string, error)
	Decrypt(ctx context.Context, scheme, peerPubKey, ciphertext string) (string, error)
}

// MessageKeyer derives one NIP-44 payload's message keys, for a proof.
type MessageKeyer interface {
	MessageKeys(ctx context.Context, peerPubKey, payload string) (nip44.Keys, error)
}

var (
	_ Crypto       = (*LocalSigner)(nil)
	_ MessageKeyer = (*LocalSigner)(nil)
)

// LocalSigner signs with a private key held in memory.
type LocalSigner struct {
	priv, pub string
	key       *nip46.LocalKey
}

// NewLocalSigner wraps privKeyHex.
func NewLocalSigner(privKeyHex string) (*LocalSigner, error) {
	pub, err := utils.GetPublicKey(privKeyHex)
	if err != nil {
		return nil, err
	}
	key, err := nip46.NewLocalKey(privKeyHex)
	if err != nil {
		return nil, err
	}
	return &LocalSigner{priv: privKeyHex, pub: pub, key: key}, nil
}

func (s *LocalSigner) PubKey() string { return s.pub }

func (s *LocalSigner) Sign(_ context.Context, ev *nip01.Event) error {
	return ev.Sign(s.priv)
}

func (s *LocalSigner) Encrypt(ctx context.Context, scheme, peerPubKey, plaintext string) (string, error) {
	return s.key.Encrypt(ctx, scheme, peerPubKey, plaintext)
}

func (s *LocalSigner) Decrypt(ctx context.Context, scheme, peerPubKey, ciphertext string) (string, error) {
	return s.key.Decrypt(ctx, scheme, peerPubKey, ciphertext)
}

func (s *LocalSigner) MessageKeys(ctx context.Context, peerPubKey, payload string) (nip44.Keys, error) {
	return s.key.MessageKeys(ctx, peerPubKey, payload)
}

// KeySigner wraps privKeyHex as a Signer, or returns nil for "" (anonymous).
func KeySigner(privKeyHex string) Signer {
	if privKeyHex == "" {
		return nil
	}
	s, err := NewLocalSigner(privKeyHex)
	if err != nil {
		return nil
	}
	return s
}
