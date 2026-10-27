package climatrix

import (
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip19"
)

// Every way of naming a person finds the same events; hex and hex
// prefixes, and values that name no one, behave as they always did.
func TestPersonForms(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)
	alice, bob := A(t, "alice"), A(t, "bob")
	note := publishNote(t, e, r.URL, alice, "--content", "for bob", "--tag", "p="+bob.PubHex)

	n := StartNip05(t, map[string]string{"alice": alice.PubHex, "bob": bob.PubHex})
	e.TrustNip05(n)
	nprofile, err := nip19.EncodeProfile(alice.PubHex, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.MustOK(t, "id", "import", "--label", "al", "--json", "--file", e.WriteFile("al.nsec", alice.Nsec+"\n"))

	find := func(args ...string) []nip01.Event {
		t.Helper()
		var evs []nip01.Event
		e.MustOK(t, append([]string{"find", "--kinds", "1", "-s", r.URL, "--json"}, args...)...).JSON(t, &evs)
		return evs
	}
	for _, who := range []string{alice.PubHex, alice.Npub, nprofile, n.ID("alice"), "al"} {
		if evs := find("--authors", who); len(evs) != 1 || evs[0].ID != note.ID {
			t.Errorf("--authors %s: %d events", who, len(evs))
		}
	}
	for _, who := range []string{bob.PubHex, bob.Npub, n.ID("bob")} {
		if evs := find("--tag", "p="+who); len(evs) != 1 {
			t.Errorf("--tag p=%s: %d events", who, len(evs))
		}
	}
	// A value that names no one is still sent as given: nothing matches.
	if evs := find("--tag", "p=pubkey1"); len(evs) != 0 {
		t.Errorf("literal p tag matched %d", len(evs))
	}
	if evs := find("--authors", "nobody"); len(evs) != 0 {
		t.Errorf("literal author matched %d", len(evs))
	}
	e.Run(t, "find", "--kinds", "1", "--authors", "npub1nope", "-s", r.URL, "--json").ExpectErr(t, "invalid_input")

	// dump takes the same forms.
	out := e.WriteFile("dump.json", "")
	e.MustOK(t, "dump", "--kinds", "1", "--authors", alice.Npub, "-s", r.URL, "-o", out, "--json")

	// signer check names its counterpart any way too.
	policy := e.WriteFile("p.yaml", "kind: signer-policy\nrules: [{name: dm, allow: {methods: [nip44_decrypt], counterparts: ["+alice.Npub+"]}}]")
	e.MustOK(t, "signer", "check", "--policy", policy, "--method", "nip44_decrypt", "--counterpart", n.ID("alice"), "--pubkey", "al", "--json")
	e.Run(t, "signer", "check", "--policy", policy, "--method", "nip44_decrypt", "--counterpart", "not-a-key", "--pubkey", bob.PubHex, "--json").ExpectErr(t, "invalid_input")

	if !strings.HasPrefix(alice.Npub, "npub1") {
		t.Fatal("fixture")
	}
}
