package client

import (
	"context"
	"testing"

	"github.com/ohstr/nmilat/nip01"
)

func TestLocalSigner(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewLocalSigner(id.PrivKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	if s.PubKey() != id.PubKeyHex {
		t.Fatalf("PubKey = %s, want %s", s.PubKey(), id.PubKeyHex)
	}
	ev := nip01.NewEvent(1, "hi")
	if err := s.Sign(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if err := ev.Verify(); err != nil || ev.PubKey != id.PubKeyHex {
		t.Fatalf("signed event: %v", err)
	}
	if _, err := NewLocalSigner("not-hex"); err == nil {
		t.Fatal("NewLocalSigner accepted a bad key")
	}
}
