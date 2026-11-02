package climatrix

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nipOA"
)

// An attested key's DM: dm send --auth puts the owner's NIP-OA tag on the
// rumor; dm read checks it and shows it, trusted or not.
func TestDMAttested(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)
	owner, alice, bob := A(t, "carol"), A(t, "alice"), A(t, "bob") // carol attests alice's key
	ks, err := nipOA.NewKeySigner(owner.PrivHex)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := nipOA.Sign(context.Background(), ks, alice.PubHex, "kind=14")
	if err != nil {
		t.Fatal(err)
	}
	tj, _ := json.Marshal(tag)
	relay := []string{"-s", r.URL}
	with := func(args ...string) []string { return append(args, relay...) }

	e.MustOK(t, with("dm", "send", "--to", bob.Npub, "done", "--signer", alice.Nsec, "--auth", string(tj), "--json")...)

	type entry struct {
		AttestedBy    string `json:"attested_by"`
		AttestValid   *bool  `json:"attest_valid"`
		AttestTrusted bool   `json:"attest_trusted"`
	}
	read := func(extra ...string) []entry {
		var ms []entry
		e.MustOK(t, with(append([]string{"dm", "read", "--json", "--from", alice.Npub, "--signer", bob.Nsec}, extra...)...)...).JSON(t, &ms)
		if len(ms) != 1 {
			t.Fatalf("%d messages", len(ms))
		}
		return ms
	}
	if m := read("--trust-owners", owner.Npub)[0]; m.AttestedBy != owner.PubHex || m.AttestValid == nil || !*m.AttestValid || !m.AttestTrusted {
		t.Fatalf("trusted owner: %+v", m)
	}
	if m := read()[0]; m.AttestedBy != owner.PubHex || m.AttestValid == nil || !*m.AttestValid || m.AttestTrusted {
		t.Fatalf("no owner trusted: %+v", m)
	}

	text := e.MustOK(t, with("dm", "read", "--from", alice.Npub, "--signer", bob.Nsec, "--trust-owners", owner.Npub, "--attest-label", "bot")...).Stdout
	if !strings.Contains(text, alice.Npub+"  [bot of "+owner.Npub+"]  labels: -") {
		t.Fatalf("text: %s", text)
	}
	text = e.MustOK(t, with("dm", "read", "--from", alice.Npub, "--signer", bob.Nsec)...).Stdout
	if !strings.Contains(text, "[attested by "+owner.Npub+", owner not trusted]") {
		t.Fatalf("text, untrusted: %s", text)
	}

	t.Run("errors", func(t *testing.T) {
		// a tag for another key, a bad label word
		e.Run(t, with("dm", "send", "--to", bob.Npub, "x", "--signer", bob.Nsec, "--auth", string(tj), "--json")...).ExpectErr(t, "invalid_input")
		e.Run(t, with("dm", "read", "--json", "--from", alice.Npub, "--signer", bob.Nsec, "--attest-label", "Bot Of")...).ExpectErr(t, "invalid_input")
	})
}
