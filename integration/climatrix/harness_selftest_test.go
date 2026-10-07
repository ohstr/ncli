package climatrix

import (
	"testing"
)

// The fixed actors decode to the keys testdata/actors.json claims.
func TestHarness_ActorsAreConsistent(t *testing.T) {
	e := NewEnv(t)
	for _, name := range []string{"operator", "admin", "alice", "bob", "carol", "eve", "mallory", "agent"} {
		a := A(t, name)
		var dec struct {
			PrivHex string `json:"priv_hex"`
		}
		e.MustOK("decode", a.Nsec, "--json").JSON(t, &dec)
		if dec.PrivHex != a.PrivHex {
			t.Errorf("%s: nsec decodes to %s, want %s", name, dec.PrivHex, a.PrivHex)
		}
		var id struct {
			PubHex string `json:"pub_hex"`
		}
		e.MustOK("id", a.Npub, "--json").JSON(t, &id)
		if id.PubHex != a.PubHex {
			t.Errorf("%s: npub resolves to %s, want %s", name, id.PubHex, a.PubHex)
		}
	}
}

// A local relay starts, takes the whole corpus, and serves it back.
func TestHarness_RelaySeedsCorpus(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	r.Seed(t)
	var events []map[string]any
	NewEnv(t).MustOK("find", "--kinds", "1", "--limit", "500", "-s", r.URL).JSON(t, &events)
	if len(events) != 100 {
		t.Fatalf("kind 1 events = %d, want 100 (testdata/README.md)", len(events))
	}
}

// nip-05 resolves against the local TLS fixture, not real DNS.
func TestHarness_Nip05Fixture(t *testing.T) {
	alice := A(t, "alice")
	srv := StartNip05(t, map[string]string{"alice": alice.PubHex})
	e := NewEnv(t)
	e.TrustNip05(srv)
	var id struct {
		PubHex string `json:"pub_hex"`
	}
	e.MustOK("id", srv.ID("alice"), "--json").JSON(t, &id)
	if id.PubHex != alice.PubHex {
		t.Fatalf("nip-05 resolved to %s, want %s", id.PubHex, alice.PubHex)
	}
}
