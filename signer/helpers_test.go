package signer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/utils"
)

// testKey derives a deterministic keypair from name.
func testKey(t *testing.T, name string) (priv, pub string) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	priv = hex.EncodeToString(sum[:])
	pub, err := utils.GetPublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func npub(t *testing.T, pub string) string {
	t.Helper()
	n, err := nip19.EncodePublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

var t0 = time.Unix(1_800_000_000, 0)

// target builds an unsigned sign_event target owned by signerPub, with its
// id computed the way the server does.
func target(t *testing.T, signerPub string, kind int, created time.Time, content string, tags ...[]string) *nip01.Event {
	t.Helper()
	if tags == nil {
		tags = [][]string{}
	}
	ev := &nip01.Event{PubKey: signerPub, CreatedAt: uint64(created.Unix()), Kind: kind, Tags: tags, Content: content}
	id, err := ev.HashID()
	if err != nil {
		t.Fatal(err)
	}
	ev.ID = hex.EncodeToString(id)
	return ev
}

// signed builds and signs an event with priv.
func signed(t *testing.T, priv string, kind int, created time.Time, content string, tags ...[]string) *nip01.Event {
	t.Helper()
	if tags == nil {
		tags = [][]string{}
	}
	ev := &nip01.Event{CreatedAt: uint64(created.Unix()), Kind: kind, Tags: tags, Content: content}
	if err := ev.Sign(priv); err != nil {
		t.Fatal(err)
	}
	return ev
}

// writePolicy writes body (a format string taking the given args) as
// policy.yaml in a temp dir and returns its path.
func writePolicy(t *testing.T, body string, args ...any) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(body, args...)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustParse(t *testing.T, body string, args ...any) *Policy {
	t.Helper()
	p, err := LoadPolicy(writePolicy(t, body, args...), LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func noneUsed(string) bool { return false }
