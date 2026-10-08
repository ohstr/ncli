package client

import (
	"context"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/utils"
)

// Signer signs events for one identity: a key held in this process
// (LocalSigner) or a signer reached over a socket.
type Signer interface {
	PubKey() string
	Sign(ctx context.Context, ev *nip01.Event) error
}

// LocalSigner signs with a private key held in memory.
type LocalSigner struct {
	priv, pub string
}

// NewLocalSigner wraps privKeyHex.
func NewLocalSigner(privKeyHex string) (*LocalSigner, error) {
	pub, err := utils.GetPublicKey(privKeyHex)
	if err != nil {
		return nil, err
	}
	return &LocalSigner{priv: privKeyHex, pub: pub}, nil
}

func (s *LocalSigner) PubKey() string { return s.pub }

func (s *LocalSigner) Sign(_ context.Context, ev *nip01.Event) error {
	return ev.Sign(s.priv)
}
