package climatrix

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/ohstr/nmilat/nip01"
)

type change struct {
	ID      string      `json:"id"`
	Added   []string    `json:"added"`
	Changed []string    `json:"changed"`
	Removed []string    `json:"removed"`
	Created bool        `json:"created"`
	Event   nip01.Event `json:"event"`
}

// latest is author's current kind on relay.
func latest(t *testing.T, e *Env, relay, author string, kind int) *nip01.Event {
	t.Helper()
	var evs []nip01.Event
	e.MustOK(t, "find", "--kinds", fmt.Sprint(kind), "--authors", author, "-s", relay, "--json").JSON(t, &evs)
	if len(evs) == 0 {
		return nil
	}
	newest := evs[0]
	for _, ev := range evs[1:] {
		if ev.CreatedAt > newest.CreatedAt {
			newest = ev
		}
	}
	return &newest
}

func pTags(ev *nip01.Event) []string {
	var out []string
	for _, t := range ev.Tags {
		if len(t) >= 2 && t[0] == "p" {
			out = append(out, t[1])
		}
	}
	return out
}

func TestProfileWrites(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)
	alice, bob, carol, mallory := A(t, "alice"), A(t, "bob"), A(t, "carol"), A(t, "mallory")
	as := func(args ...string) []string {
		return append(args, "--signer", alice.Nsec, "-s", r.URL, "--json")
	}

	t.Run("set refuses to start from nothing without --create", func(t *testing.T) {
		e.Run(t, as("profile", "set", "--name", "alice")...).ExpectErr(t, "not_found")
		if latest(t, e, r.URL, alice.PubHex, 0) != nil {
			t.Fatal("published anyway")
		}
	})

	t.Run("set merges and keeps every other field", func(t *testing.T) {
		publishNote(t, e, r.URL, alice, "--kind", "0", "--content", `{"name":"alice","lud16":"alice@pay.example","picture":"https://p.example/a.png","custom":{"x":1}}`)
		var c change
		e.MustOK(t, as("profile", "set", "--about", "hello", "--picture", "", "--field", "bot=true")...).JSON(t, &c)
		var meta map[string]any
		got := latest(t, e, r.URL, alice.PubHex, 0)
		if got.ID != c.ID || json.Unmarshal([]byte(got.Content), &meta) != nil {
			t.Fatalf("profile %+v", got)
		}
		if meta["name"] != "alice" || meta["lud16"] != "alice@pay.example" || meta["about"] != "hello" || meta["bot"] != true || meta["custom"] == nil {
			t.Fatalf("merged %v", meta)
		}
		if _, ok := meta["picture"]; ok {
			t.Fatalf("empty --picture should remove it: %v", meta)
		}
		if !slices.Equal(c.Removed, []string{"picture"}) || !slices.Contains(c.Added, "about") {
			t.Fatalf("change %+v", c)
		}
		// Nothing new: nothing published.
		var same change
		e.MustOK(t, as("profile", "set", "--about", "hello")...).JSON(t, &same)
		if same.ID != "" || latest(t, e, r.URL, alice.PubHex, 0).ID != c.ID {
			t.Fatalf("no-op published %+v", same)
		}
		// Editing a field that exists is a change, not an addition.
		var edit change
		e.MustOK(t, as("profile", "set", "--about", "hello again")...).JSON(t, &edit)
		if !slices.Equal(edit.Changed, []string{"about"}) || len(edit.Added) != 0 || len(edit.Removed) != 0 {
			t.Fatalf("edit %+v", edit)
		}
	})

	t.Run("follow keeps petnames, relays and content", func(t *testing.T) {
		e.Run(t, as("profile", "follow", bob.Npub)...).ExpectErr(t, "not_found")
		publishNote(t, e, r.URL, alice, "--kind", "3", "--content", `{"wss://old.example":{"read":true}}`,
			"--tag-json", `["p","`+carol.PubHex+`","wss://c.example","carol"]`, "--tag", "t=nostr")
		var c change
		e.MustOK(t, as("profile", "follow", bob.Npub, carol.Npub)...).JSON(t, &c)
		if !slices.Equal(c.Added, []string{bob.PubHex}) {
			t.Fatalf("added %v", c.Added)
		}
		got := latest(t, e, r.URL, alice.PubHex, 3)
		if !slices.Equal(pTags(got), []string{carol.PubHex, bob.PubHex}) || got.Content != `{"wss://old.example":{"read":true}}` {
			t.Fatalf("follow list %+v", got)
		}
		if !hasTag(*got, "p", carol.PubHex, "wss://c.example", "carol") || !hasTag(*got, "t", "nostr") {
			t.Fatalf("lost petname or tag: %v", got.Tags)
		}

		e.MustOK(t, as("profile", "unfollow", carol.Npub)...)
		if got = latest(t, e, r.URL, alice.PubHex, 3); !slices.Equal(pTags(got), []string{bob.PubHex}) {
			t.Fatalf("after unfollow %v", pTags(got))
		}
	})

	t.Run("never an empty list by accident", func(t *testing.T) {
		e.MustOK(t, as("profile", "mute", mallory.Npub, "--create")...)
		e.Run(t, as("profile", "unmute", mallory.Npub)...).ExpectErr(t, "usage")
		if pk := pTags(latest(t, e, r.URL, alice.PubHex, 10000)); !slices.Equal(pk, []string{mallory.PubHex}) {
			t.Fatalf("mute list changed by a refused unmute: %v", pk)
		}
		e.MustOK(t, as("profile", "unmute", mallory.Npub, "--allow-empty")...)
		if pk := pTags(latest(t, e, r.URL, alice.PubHex, 10000)); len(pk) != 0 {
			t.Fatalf("after --allow-empty %v", pk)
		}
	})

	t.Run("relays set", func(t *testing.T) {
		// No current list: nothing is replaced without --create.
		e.Run(t, as("profile", "relays", "set", "--both", "wss://both.example")...).ExpectErr(t, "not_found")
		var cs []change
		e.MustOK(t, as("profile", "relays", "set", "--both", "wss://both.example", "--read", "wss://in.example", "--dm", "wss://dm.example", "--create")...).JSON(t, &cs)
		if len(cs) != 2 || !cs[0].Created || len(cs[0].Added) != 2 {
			t.Fatalf("changes %+v", cs)
		}
		// Replacing the list reports what it drops.
		var again []change
		e.MustOK(t, as("profile", "relays", "set", "--both", "wss://both.example", "--read", "wss://in.example", "--write", "wss://out.example")...).JSON(t, &again)
		if len(again) != 1 || len(again[0].Added) != 1 || len(again[0].Removed) != 0 {
			t.Fatalf("second set %+v", again)
		}
		nip65 := latest(t, e, r.URL, alice.PubHex, 10002)
		if nip65 == nil || !hasTag(*nip65, "r", "wss://both.example") || !hasTag(*nip65, "r", "wss://in.example", "read") || hasTag(*nip65, "r", "wss://both.example", "read") {
			t.Fatalf("10002 %+v", nip65)
		}
		dm := latest(t, e, r.URL, alice.PubHex, 10050)
		if dm == nil || !hasTag(*dm, "relay", "wss://dm.example") {
			t.Fatalf("10050 %+v", dm)
		}
		// The read side shows it, unchanged.
		var view map[string]any
		e.MustOK(t, "profile", alice.Npub, "-s", r.URL, "--no-verify", "--json").JSON(t, &view)
		if view["relays"] == nil {
			t.Fatalf("profile view lost the relay list: %v", view)
		}
	})

	t.Run("an unreachable relay publishes nothing", func(t *testing.T) {
		dead := DeadRelayURL(t)
		e.Run(t, "profile", "follow", mallory.Npub, "--signer", alice.Nsec, "-s", dead, "--timeout", "2s", "--json").ExpectErr(t, "network")
		// Even when another relay answers: the silent one may hold a newer list.
		before := latest(t, e, r.URL, alice.PubHex, 3)
		e.Run(t, "profile", "follow", mallory.Npub, "--signer", alice.Nsec, "-s", r.URL+","+dead, "--timeout", "2s", "--json").ExpectErr(t, "network")
		if after := latest(t, e, r.URL, alice.PubHex, 3); after.ID != before.ID {
			t.Fatal("published although a relay didn't answer")
		}
	})

	t.Run("the last person needs --allow-empty, whatever else the list holds", func(t *testing.T) {
		// alice's follow list keeps its relay JSON and "t" tag; only bob is left.
		e.Run(t, as("profile", "unfollow", bob.Npub)...).ExpectErr(t, "usage")
		if pk := pTags(latest(t, e, r.URL, alice.PubHex, 3)); !slices.Equal(pk, []string{bob.PubHex}) {
			t.Fatalf("follows changed by a refused unfollow: %v", pk)
		}
	})

	t.Run("through a socket signer", func(t *testing.T) {
		sg := startSigner(t, e, e.WriteFile("profile-policy.yaml", "kind: signer-policy\nrules:\n  - {name: profile, allow: {kinds: [0]}}\n  - {name: rest, deny: {}}\n"))
		agent := A(t, "agent")
		e.MustOK(t, "profile", "set", "--name", "agent", "--create", "--signer", sg.URI, "-s", r.URL, "--json")
		if got := latest(t, e, r.URL, agent.PubHex, 0); got == nil {
			t.Fatal("agent profile missing")
		}
		e.Run(t, "profile", "follow", bob.Npub, "--create", "--signer", sg.URI, "-s", r.URL, "--json").ExpectErr(t, "auth")
	})

	t.Run("errors", func(t *testing.T) {
		e.Run(t, as("profile", "set")...).ExpectErr(t, "usage")
		e.Run(t, as("profile", "set", "--field", "nokey")...).ExpectErr(t, "invalid_input")
		e.Run(t, as("profile", "follow", "npub1nope")...).ExpectErr(t, "invalid_input")
		e.Run(t, "profile", "set", "--name", "x", "-s", r.URL, "--json").ExpectErr(t, "usage")
		e.Run(t, as("profile", "relays", "set")...).ExpectErr(t, "usage")
		e.Run(t, "profile", "relays", "--json").ExpectErr(t, "usage")
	})
}
