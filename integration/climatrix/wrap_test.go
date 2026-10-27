package climatrix

import (
	"fmt"
	"testing"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip49"
)

// event wrap + publish, then unwrap with --signer and --from.
func TestEventWrapUnwrapFrom(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)
	alice, bob, mallory := A(t, "alice"), A(t, "bob"), A(t, "mallory")

	wrapFrom := func(sender Actor, content string) string {
		draft := e.MustOK(t, "event", "new", "--kind", "14", "--content", content, "--tag", "p="+bob.PubHex)
		wrap := e.RunStdin(t, draft.Stdout, "event", "wrap", "--to", bob.Npub, "--signer", sender.Nsec, "--json")
		if wrap.Code != 0 {
			t.Fatalf("wrap\n%s", wrap)
		}
		e.RunStdin(t, wrap.Stdout, "publish", "-e", "-", "-s", r.URL, "--json").ExpectOK(t)
		return wrap.Stdout
	}
	fromAlice := wrapFrom(alice, "for bob")
	fromMallory := wrapFrom(mallory, "MALLORY")

	var got []struct {
		Sender string      `json:"sender"`
		Rumor  nip01.Event `json:"rumor"`
	}
	res := e.RunStdin(t, "["+fromAlice+","+fromMallory+"]", "event", "unwrap", "--offline", "--identity", bob.Nsec, "--from", alice.Npub, "--json")
	if res.Code != 0 {
		t.Fatalf("unwrap\n%s", res)
	}
	res.JSON(t, &got)
	if len(got) != 1 || got[0].Sender != alice.PubHex || got[0].Rumor.Content != "for bob" || got[0].Rumor.PubKey != alice.PubHex {
		t.Fatalf("unwrap --from %+v", got)
	}

	// Through a socket signer holding bob's key.
	bobEnv := NewEnv(t)
	ncryptsec, err := nip49.Encrypt(bob.PrivHex, "bob-pw")
	if err != nil {
		t.Fatal(err)
	}
	keyFile := bobEnv.WriteFile("bob.ncryptsec", ncryptsec+"\n")
	pwFile := bobEnv.WriteFile("bob.pw", "bob-pw\n")
	policy := bobEnv.WriteFile("p.yaml", fmt.Sprintf("kind: signer-policy\nrules:\n  - {name: alice, allow: {methods: [nip44_decrypt], counterparts: [%s]}}\n  - {name: inbox, allow: {unwrap_from: [%s]}}\n", alice.Npub, alice.Npub))
	sg := startSignerWith(t, bobEnv, []string{"--identity", "file:" + keyFile, "--password-file", pwFile}, policy)
	res = bobEnv.RunStdin(t, "["+fromAlice+","+fromMallory+"]", "event", "unwrap", "--offline", "--signer", sg.URI, "--from", alice.Npub, "--json")
	if res.Code != 0 {
		t.Fatalf("unwrap --signer\n%s", res)
	}
	res.JSON(t, &got)
	if len(got) != 1 || got[0].Rumor.Content != "for bob" {
		t.Fatalf("unwrap --signer %+v", got)
	}

	e.RunStdin(t, fromAlice, "event", "unwrap", "--offline", "--identity", bob.Nsec, "--signer", sg.URI, "--json").ExpectErr(t, "usage")
	e.Run(t, "event", "wrap", "--signer", alice.Nsec, "--json").ExpectErr(t, "usage")
}
