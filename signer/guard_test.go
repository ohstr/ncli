package signer

import (
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip49"
)

func TestGuard(t *testing.T) {
	priv, pub := testKey(t, "signer")
	otherPriv, _ := testKey(t, "other")
	nsec, err := nip19.EncodePrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	ncryptsec, err := nip49.Encrypt(priv, "pw")
	if err != nil {
		t.Fatal(err)
	}
	otherNsec, _ := nip19.EncodePrivateKey(otherPriv)
	g := NewGuard(priv, ncryptsec)

	for _, leak := range []string{priv, strings.ToUpper(priv), nsec, strings.ToUpper(nsec), ncryptsec} {
		if !g.ContainsEvent(target(t, pub, 1, t0, "here: "+leak+" ok")) {
			t.Errorf("content leak %.12s... not caught", leak)
		}
		if !g.ContainsEvent(target(t, pub, 1, t0, "", []string{"x", "y", "pre" + leak})) {
			t.Errorf("tag leak %.12s... not caught", leak)
		}
	}
	if g.ContainsEvent(target(t, pub, 1, t0, otherNsec+" "+otherPriv+" "+pub)) {
		t.Error("an unrelated key or the pubkey must pass the guard; that's policy's job")
	}
}
