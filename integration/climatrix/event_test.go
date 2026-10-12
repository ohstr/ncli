package climatrix

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip59"
)

// eventVerdict is the part of "event verify"/"event validate" --json the
// tests read.
type eventVerdict struct {
	Status string `json:"status"`
	ID     string `json:"id"`
	Source string `json:"source"`
}

// publishNote builds, signs and publishes a note through the CLI pipe an
// agent would use, returning the signed event.
func publishNote(t *testing.T, e *Env, relay string, priv Actor, args ...string) nip01.Event {
	t.Helper()
	draft := e.MustOK(t, append([]string{"event", "new", "--pubkey", priv.Npub}, args...)...)
	signed := e.RunStdin(t, draft.Stdout, "id", "sign", "-e", "-", "-o", "-", "--identity", priv.Nsec)
	if signed.Code != 0 {
		t.Fatalf("id sign -e - -o -\n%s", signed)
	}
	if r := e.RunStdin(t, signed.Stdout, "publish", "-e", "-", "-s", relay, "--json"); r.Code != 0 {
		t.Fatalf("publish -e -\n%s", r)
	}
	var ev nip01.Event
	signed.JSON(t, &ev)
	return ev
}

func neventFor(t *testing.T, ev nip01.Event, relays ...string) string {
	t.Helper()
	s, err := nip19.EncodeEvent(nip19.EventPointer{ID: ev.ID, Relays: relays, Author: ev.PubKey, Kind: ev.Kind})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Fetching and verifying by reference needs no identity and no prefs: an
// nevent's own relay hint is enough.
func TestEventVerifyAndGet(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)
	alice := A(t, "alice")
	ev := publishNote(t, e, r.URL, alice, "--content", "verify me")
	ref := neventFor(t, ev, r.URL)

	t.Run("verify nevent via its hint", func(t *testing.T) {
		var v eventVerdict
		e.MustOK(t, "event", "verify", ref, "--author", alice.Npub, "--json").JSON(t, &v)
		if v.Status != "valid" || v.ID != ev.ID || v.Source != r.URL {
			t.Errorf("got %+v", v)
		}
		if out := e.MustOK(t, "event", "verify", ref).Stdout; !strings.HasPrefix(out, "valid") {
			t.Errorf("text: %q", out)
		}
	})

	t.Run("verify a tampered event from stdin", func(t *testing.T) {
		bad := ev
		bad.Content = "forged"
		b, _ := json.Marshal(bad)
		res := e.RunStdin(t, string(b), "event", "verify", "--offline", "--json")
		res.expectErr(t, "internal", true)
		var v eventVerdict
		res.JSON(t, &v)
		if v.Status != "invalid" {
			t.Errorf("got %+v", v)
		}
	})

	t.Run("verify a wrong author", func(t *testing.T) {
		e.Run(t, "event", "verify", ref, "--author", A(t, "bob").Npub, "--json").expectErr(t, "internal", true)
	})

	t.Run("get prints the event as served", func(t *testing.T) {
		var got nip01.Event
		e.MustOK(t, "event", "get", ev.ID, "-s", r.URL).JSON(t, &got)
		if got.ID != ev.ID || got.Sig != ev.Sig {
			t.Errorf("got %+v", got)
		}
		e.MustOK(t, "event", "get", ev.ID, ev.ID, "-s", r.URL).ExpectJSONArray(t, "")
	})

	t.Run("missing and unreachable", func(t *testing.T) {
		missing := strings.Repeat("ab", 32)
		e.Run(t, "event", "get", missing, "-s", r.URL, "--json").ExpectErr(t, "not_found")
		e.Run(t, "event", "get", missing, "-s", DeadRelayURL(t), "--json").ExpectErr(t, "network")
		e.Run(t, "event", "get", missing, "--json").ExpectErr(t, "not_found")
		e.Run(t, "event", "get", "garbage", "--json").ExpectErr(t, "invalid_input")
	})

	t.Run("show", func(t *testing.T) {
		out := e.MustOK(t, "event", "show", ref).Stdout
		if !strings.Contains(out, "✓ valid") || !strings.Contains(out, "verify me") || !strings.Contains(out, alice.Npub) {
			t.Errorf("show:\n%s", out)
		}
		var v struct {
			Status string `json:"status"`
			Event  struct {
				ID string `json:"id"`
			} `json:"event"`
		}
		e.MustOK(t, "event", "show", ref, "--json").JSON(t, &v)
		if v.Status != "valid" || v.Event.ID != ev.ID {
			t.Errorf("show --json: %+v", v)
		}
	})

	t.Run("encode", func(t *testing.T) {
		var enc struct {
			Nevent string   `json:"nevent"`
			Hints  []string `json:"hints"`
		}
		e.MustOK(t, "event", "encode", ev.ID, "-s", r.URL, "--json").JSON(t, &enc)
		p, err := nip19.DecodeEvent(enc.Nevent)
		if err != nil || len(p.Relays) != 1 || p.Relays[0] != r.URL || p.Author != ev.PubKey {
			t.Errorf("nevent %+v %v", p, err)
		}
	})
}

// Where an event lives, and copying it somewhere else unchanged.
func TestEventSeenAndRebroadcast(t *testing.T) {
	needsRelay(t)
	src := StartRelay(t, "", nil)
	dst := StartRelay(t, "", nil)
	e := NewEnv(t)
	ev := publishNote(t, e, src.URL, A(t, "alice"), "--content", "spread me")
	both := src.URL + "," + dst.URL

	var seen struct {
		Found int `json:"found"`
		Asked int `json:"asked"`
	}
	e.MustOK(t, "event", "seen", ev.ID, "-s", both, "--json").JSON(t, &seen)
	if seen.Found != 1 || seen.Asked != 2 {
		t.Errorf("before: found on %d of %d", seen.Found, seen.Asked)
	}

	var rep struct {
		Succeeded int `json:"succeeded"`
	}
	e.MustOK(t, "event", "rebroadcast", ev.ID, "-s", src.URL, "--to", dst.URL, "--json").JSON(t, &rep)
	if rep.Succeeded != 1 {
		t.Errorf("rebroadcast: %+v", rep)
	}
	e.MustOK(t, "event", "seen", ev.ID, "-s", both, "--json").JSON(t, &seen)
	if seen.Found != 2 {
		t.Errorf("after: found on %d of %d", seen.Found, seen.Asked)
	}

	e.Run(t, "event", "seen", strings.Repeat("cd", 32), "-s", both, "--json").expectErr(t, "not_found", true)
	e.Run(t, "event", "rebroadcast", ev.ID, "-s", src.URL, "--json").ExpectErr(t, "usage")
}

// Building, id-ing, threading, diffing and validating events.
func TestEventAuthoring(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)
	alice, bob := A(t, "alice"), A(t, "bob")

	t.Run("id", func(t *testing.T) {
		draft := `{"kind":1,"content":"x","created_at":1790000000,"tags":[]}`
		res := e.RunStdin(t, draft, "event", "id", "--pubkey", alice.Npub, "--id-only")
		if res.Code != 0 || len(strings.TrimSpace(res.Stdout)) != 64 {
			t.Fatalf("event id\n%s", res)
		}
		e.RunStdin(t, `{"kind":1,"content":"x","tags":[]}`, "event", "id", "--json").ExpectErr(t, "invalid_input")
	})

	t.Run("thread", func(t *testing.T) {
		root := publishNote(t, e, r.URL, alice, "--content", "root")
		reply := publishNote(t, e, r.URL, bob, "--content", "reply", "--tag-json", `["e","`+root.ID+`","","root"]`)
		leaf := publishNote(t, e, r.URL, alice, "--content", "leaf",
			"--tag-json", `["e","`+root.ID+`","","root"]`, "--tag-json", `["e","`+reply.ID+`","","reply"]`)

		var v struct {
			Ancestors []nip01.Event `json:"ancestors"`
			Replies   []nip01.Event `json:"replies"`
		}
		e.MustOK(t, "event", "thread", reply.ID, "-s", r.URL, "--json").JSON(t, &v)
		if len(v.Ancestors) != 1 || v.Ancestors[0].ID != root.ID || len(v.Replies) != 1 || v.Replies[0].ID != leaf.ID {
			t.Errorf("thread: %+v", v)
		}
	})

	t.Run("diff", func(t *testing.T) {
		a := e.WriteFile("a.json", `{"kind":1,"content":"one","created_at":1,"tags":[["t","x"]],"pubkey":"`+alice.PubHex+`"}`)
		b := e.WriteFile("b.json", `{"kind":1,"content":"two","created_at":1,"tags":[["t","y"]],"pubkey":"`+alice.PubHex+`"}`)
		var v struct {
			Same    bool       `json:"same"`
			Added   [][]string `json:"tags_added"`
			Removed [][]string `json:"tags_removed"`
		}
		e.MustOK(t, "event", "diff", a, b, "--offline", "--json").JSON(t, &v)
		if v.Same || len(v.Added) != 1 || len(v.Removed) != 1 {
			t.Errorf("diff: %+v", v)
		}
	})

	t.Run("validate", func(t *testing.T) {
		build := func(tag string) string {
			draft := e.MustOK(t, "event", "new", "--kind", "10002", "--tag", "r="+tag, "--pubkey", alice.Npub)
			return e.RunStdin(t, draft.Stdout, "id", "sign", "-e", "-", "-o", "-", "--identity", alice.Nsec).Stdout
		}
		var v eventVerdict
		ok := e.RunStdin(t, build("wss://relay.example"), "event", "validate", "--offline", "--json")
		if ok.Code != 0 {
			t.Fatalf("validate good\n%s", ok)
		}
		ok.JSON(t, &v)
		if v.Status != "valid" {
			t.Errorf("good relay list: %+v", v)
		}
		e.RunStdin(t, build("https://not-a-relay.example"), "event", "validate", "--offline", "--json").expectErr(t, "internal", true)
	})

	t.Run("new rejects a bad tag", func(t *testing.T) {
		e.Run(t, "event", "new", "--tag-json", "not json", "--json").ExpectErr(t, "invalid_input")
	})
}

// A gift wrap opens with the recipient's key and not with anyone else's.
func TestEventUnwrap(t *testing.T) {
	e := NewEnv(t)
	alice, bob := A(t, "alice"), A(t, "bob")
	rumor := nip01.NewUnsignedEvent(14, alice.PubHex, "psst")
	wrap, err := nip59.Wrap(rumor, alice.PrivHex, bob.PubHex)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(wrap)

	var u struct {
		Sender string       `json:"sender"`
		Rumor  *nip01.Event `json:"rumor"`
	}
	res := e.RunStdin(t, string(b), "event", "unwrap", "--offline", "--identity", bob.Nsec, "--json")
	if res.Code != 0 {
		t.Fatalf("unwrap\n%s", res)
	}
	res.JSON(t, &u)
	if u.Sender != alice.PubHex || u.Rumor == nil || u.Rumor.Content != "psst" {
		t.Errorf("unwrap: %+v", u)
	}
	e.RunStdin(t, string(b), "event", "unwrap", "--offline", "--identity", A(t, "carol").Nsec, "--json").ExpectErr(t, "invalid_input")
}
