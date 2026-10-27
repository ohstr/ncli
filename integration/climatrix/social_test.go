package climatrix

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/ohstr/nmilat/nip01"
)

type publishedEvent struct {
	ID     string      `json:"id"`
	Kind   int         `json:"kind"`
	Event  nip01.Event `json:"event"`
	Report struct {
		Succeeded int `json:"succeeded"`
	} `json:"report"`
}

// act runs an event action with --json and checks the relay got it.
func act(t *testing.T, e *Env, args ...string) publishedEvent {
	t.Helper()
	var p publishedEvent
	e.MustOK(t, append(append([]string{"event"}, args...), "--json")...).JSON(t, &p)
	if p.Report.Succeeded < 1 || p.Event.ID != p.ID || p.Event.Verify() != nil {
		t.Fatalf("%v published %+v", args, p)
	}
	return p
}

// onRelay fetches id from relay, independently of the command's report.
func onRelay(t *testing.T, e *Env, relay, id string) nip01.Event {
	t.Helper()
	var evs []nip01.Event
	e.MustOK(t, "find", "--ids", id, "-s", relay, "--json").JSON(t, &evs)
	if len(evs) != 1 {
		t.Fatalf("relay has %d copies of %s", len(evs), id)
	}
	return evs[0]
}

func hasTag(ev nip01.Event, tag ...string) bool {
	for _, t := range ev.Tags {
		if len(t) >= len(tag) && slices.Equal(t[:len(tag)], tag) {
			return true
		}
	}
	return false
}

func TestEventSocial(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)
	alice, bob, carol := A(t, "alice"), A(t, "bob"), A(t, "carol")
	s := []string{"-s", r.URL}

	note := act(t, e, append([]string{"post", "hello nostr", "--hashtag", "#Nostr", "-m", carol.Npub, "--signer", alice.Nsec}, s...)...)
	t.Run("post", func(t *testing.T) {
		got := onRelay(t, e, r.URL, note.ID)
		if got.Kind != 1 || got.Content != "hello nostr" || !hasTag(got, "t", "nostr") || !hasTag(got, "p", carol.PubHex) {
			t.Fatalf("post %+v", got)
		}
	})

	t.Run("react", func(t *testing.T) {
		re := act(t, e, append([]string{"react", note.ID, "👀", "--signer", bob.Nsec}, s...)...)
		got := onRelay(t, e, r.URL, re.ID)
		if got.Kind != 7 || got.Content != "👀" || !hasTag(got, "e", note.ID) || !hasTag(got, "p", alice.PubHex) || !hasTag(got, "k", "1") || hasTag(got, "h") {
			t.Fatalf("reaction %+v", got)
		}
		plus := act(t, e, append([]string{"react", note.ID, "--signer", bob.Nsec}, s...)...)
		if plus.Event.Content != "+" {
			t.Fatalf("default reaction %q", plus.Event.Content)
		}
	})

	var reply publishedEvent
	t.Run("reply threads with NIP-10", func(t *testing.T) {
		reply = act(t, e, append([]string{"reply", note.ID, "agreed", "--signer", bob.Nsec}, s...)...)
		got := onRelay(t, e, r.URL, reply.ID)
		if got.Kind != 1 || !hasTag(got, "e", note.ID, r.URL, "root") || !hasTag(got, "p", alice.PubHex) || !hasTag(got, "p", carol.PubHex) {
			t.Fatalf("reply %+v", got)
		}
		deeper := act(t, e, append([]string{"reply", reply.ID, "me too", "--signer", alice.Nsec}, s...)...)
		got = onRelay(t, e, r.URL, deeper.ID)
		if !hasTag(got, "e", note.ID, r.URL, "root") || !hasTag(got, "e", reply.ID, r.URL, "reply") {
			t.Fatalf("nested reply %+v", got.Tags)
		}
	})

	t.Run("reply to anything else is a NIP-22 comment", func(t *testing.T) {
		article := publishNote(t, e, r.URL, alice, "--kind", "30023", "--content", "long form", "--tag", "d=essay")
		c := act(t, e, append([]string{"reply", article.ID, "nice essay", "--signer", bob.Nsec}, s...)...)
		got := onRelay(t, e, r.URL, c.ID)
		addr := fmt.Sprintf("30023:%s:essay", alice.PubHex)
		if got.Kind != 1111 || !hasTag(got, "A", addr) || !hasTag(got, "K", "30023") || !hasTag(got, "a", addr) || !hasTag(got, "k", "30023") {
			t.Fatalf("comment %+v", got.Tags)
		}
		// A reply to the comment keeps the essay as root.
		c2 := act(t, e, append([]string{"reply", c.ID, "thanks", "--signer", alice.Nsec}, s...)...)
		got = onRelay(t, e, r.URL, c2.ID)
		if !hasTag(got, "A", addr) || !hasTag(got, "e", c.ID) || !hasTag(got, "k", "1111") {
			t.Fatalf("comment reply %+v", got.Tags)
		}
	})

	t.Run("group message: reaction and reply carry h", func(t *testing.T) {
		msg := publishNote(t, e, r.URL, alice, "--kind", "9", "--content", "standup at 10", "--tag", "h=team")
		re := act(t, e, append([]string{"react", msg.ID, "👍", "--signer", bob.Nsec}, s...)...)
		if !hasTag(re.Event, "h", "team") || !hasTag(re.Event, "k", "9") || !hasTag(re.Event, "e", msg.ID) || !hasTag(re.Event, "p", alice.PubHex) {
			t.Fatalf("group reaction %+v", re.Event.Tags)
		}
		rep := act(t, e, append([]string{"reply", msg.ID, "ok", "--signer", bob.Nsec}, s...)...)
		if rep.Event.Kind != 9 || !hasTag(rep.Event, "h", "team") || !hasTag(rep.Event, "q", msg.ID) {
			t.Fatalf("group reply %+v", rep.Event)
		}
	})

	t.Run("repost", func(t *testing.T) {
		rp := act(t, e, append([]string{"repost", note.ID, "--signer", bob.Nsec}, s...)...)
		var inner nip01.Event
		if rp.Event.Kind != 6 || json.Unmarshal([]byte(rp.Event.Content), &inner) != nil || inner.ID != note.ID {
			t.Fatalf("repost %+v", rp.Event)
		}
	})

	t.Run("quote", func(t *testing.T) {
		q := act(t, e, append([]string{"post", "look", "--quote", note.ID, "--signer", bob.Nsec}, s...)...)
		if !hasTag(q.Event, "q", note.ID) || !hasTag(q.Event, "p", alice.PubHex) {
			t.Fatalf("quote %+v", q.Event)
		}
	})

	t.Run("delete only your own", func(t *testing.T) {
		mine := act(t, e, append([]string{"post", "oops", "--signer", bob.Nsec}, s...)...)
		del := act(t, e, append([]string{"delete", mine.ID, "--reason", "typo", "--signer", bob.Nsec}, s...)...)
		if del.Event.Kind != 5 || !hasTag(del.Event, "e", mine.ID) || !hasTag(del.Event, "k", "1") || del.Event.Content != "typo" {
			t.Fatalf("deletion %+v", del.Event)
		}
		e.Run(t, append([]string{"event", "delete", note.ID, "--signer", bob.Nsec, "--json"}, s...)...).ExpectErr(t, "invalid_input")
	})

	t.Run("through a socket signer", func(t *testing.T) {
		policy := e.WriteFile("social-policy.yaml", "kind: signer-policy\nrules:\n  - {name: reactions, allow: {kinds: [7]}}\n  - {name: rest, deny: {}}\n")
		sg := startSigner(t, e, policy)
		act(t, e, append([]string{"react", note.ID, "--signer", sg.URI}, s...)...)
		e.Run(t, append([]string{"event", "post", "not allowed", "--signer", sg.URI, "--json"}, s...)...).ExpectErr(t, "auth")
	})

	t.Run("errors", func(t *testing.T) {
		e.Run(t, append([]string{"event", "react", note.ID, "--json"}, s...)...).ExpectErr(t, "usage")
		e.Run(t, "event", "react", "--json").ExpectErr(t, "usage")
		e.Run(t, append([]string{"event", "react", note.ID, "a", "b", "--signer", bob.Nsec, "--json"}, s...)...).ExpectErr(t, "usage")
		e.Run(t, append([]string{"event", "reply", note.ID, " ", "--signer", bob.Nsec, "--json"}, s...)...).ExpectErr(t, "invalid_input")
		missing := "ab" + note.ID[2:]
		e.Run(t, append([]string{"event", "react", missing, "--signer", bob.Nsec, "--no-outbox", "--json"}, s...)...).expectErr(t, "not_found", true)
	})
}
