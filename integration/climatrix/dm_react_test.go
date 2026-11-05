package climatrix

import (
	"reflect"
	"strings"
	"testing"
)

type dmEntry struct {
	ID        string   `json:"id"`
	From      string   `json:"from"`
	Kind      int      `json:"kind"`
	Content   string   `json:"content"`
	ReplyTo   string   `json:"reply_to"`
	Mentions  []string `json:"mentions"`
	Type      string   `json:"type"`
	Emoji     string   `json:"emoji"`
	Target    string   `json:"target"`
	Targets   []string `json:"targets"`
	Reactions []struct {
		ID    string `json:"id"`
		From  string `json:"from"`
		Emoji string `json:"emoji"`
	} `json:"reactions"`
}

type dmReacted struct {
	ID      string   `json:"id"`
	To      string   `json:"to"`
	Target  string   `json:"target"`
	Emoji   string   `json:"emoji"`
	Targets []string `json:"targets"`
	Wraps   []string `json:"wraps"`
	Report  struct {
		Succeeded int `json:"succeeded"`
	} `json:"report"`
}

func dmEntries(t *testing.T, e *Env, args ...string) []dmEntry {
	t.Helper()
	var ms []dmEntry
	e.MustOK(t, append([]string{"dm", "read", "--json"}, args...)...).JSON(t, &ms)
	return ms
}

func TestDMReactions(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)
	agent, alice, mallory := A(t, "agent"), A(t, "alice"), A(t, "mallory")
	s := startSigner(t, e, dmPolicy(t, e, r.URL))
	relay := []string{"-s", r.URL}
	with := func(args ...string) []string { return append(args, relay...) }

	var msg dmSent
	e.MustOK(t, with("dm", "send", "--to", agent.Npub, "are you there?", "--signer", alice.Nsec, "--json")...).JSON(t, &msg)

	var eyes, talk dmReacted
	t.Run("react through the socket", func(t *testing.T) {
		e.MustOK(t, with("dm", "react", "--to", alice.Npub, msg.ID, "👀", "--kind", "14", "--signer", s.URI, "--json")...).JSON(t, &eyes)
		if eyes.ID == "" || eyes.To != alice.PubHex || eyes.Target != msg.ID || eyes.Emoji != "👀" || len(eyes.Wraps) != 2 || eyes.Report.Succeeded != 2 {
			t.Fatalf("react %+v", eyes)
		}
		e.MustOK(t, with("dm", "react", "--to", alice.Npub, msg.ID, "💬", "--signer", s.URI, "--json")...).JSON(t, &talk)
	})

	t.Run("alice reads the reactions", func(t *testing.T) {
		ms := dmEntries(t, e, with("--from", agent.Npub, "--signer", alice.Nsec)...)
		if len(ms) != 2 {
			t.Fatalf("read %+v", ms)
		}
		for _, m := range ms {
			if m.Type != "reaction" || m.Target != msg.ID || m.From != agent.PubHex || m.Kind != 7 || m.ReplyTo != "" {
				t.Fatalf("reaction %+v", m)
			}
		}
		if ms[0].ID != eyes.ID && ms[1].ID != eyes.ID {
			t.Fatalf("no 👀 %+v", ms)
		}
		folded := dmEntries(t, e, with("--from", agent.Npub, "--from", alice.Npub, "--signer", alice.Nsec, "--fold")...)
		if len(folded) != 1 || folded[0].ID != msg.ID || len(folded[0].Reactions) != 2 {
			t.Fatalf("folded %+v", folded)
		}
	})

	t.Run("unreact by target and emoji", func(t *testing.T) {
		var out dmReacted
		e.MustOK(t, with("dm", "unreact", "--to", alice.Npub, "--target", msg.ID, "--emoji", "👀", "--signer", s.URI, "--json")...).JSON(t, &out)
		if !reflect.DeepEqual(out.Targets, []string{eyes.ID}) || out.ID == "" || len(out.Wraps) != 2 {
			t.Fatalf("unreact %+v", out)
		}
		folded := dmEntries(t, e, with("--from", agent.Npub, "--from", alice.Npub, "--signer", alice.Nsec, "--fold")...)
		if len(folded) != 1 || len(folded[0].Reactions) != 1 || folded[0].Reactions[0].Emoji != "💬" {
			t.Fatalf("folded %+v", folded)
		}
		// Already removed: there is nothing left to find.
		e.Run(t, with("dm", "unreact", "--to", alice.Npub, "--target", msg.ID, "--emoji", "👀", "--signer", s.URI, "--json")...).ExpectErr(t, "not_found")
	})

	t.Run("a deletion by someone else counts for nothing", func(t *testing.T) {
		e.MustOK(t, with("dm", "unreact", "--to", alice.Npub, talk.ID, "--signer", mallory.Nsec, "--json")...)
		folded := dmEntries(t, e, with("--from", agent.Npub, "--from", alice.Npub, "--from", mallory.Npub, "--signer", alice.Nsec, "--fold")...)
		if len(folded) != 1 || len(folded[0].Reactions) != 1 || folded[0].Reactions[0].ID != talk.ID {
			t.Fatalf("folded %+v", folded)
		}
	})

	t.Run("unreact by id, text output", func(t *testing.T) {
		res := e.MustOK(t, with("dm", "unreact", "--to", alice.Npub, talk.ID, "--signer", s.URI)...)
		if !strings.HasPrefix(res.Stdout, "removed "+talk.ID) {
			t.Fatalf("unreact text\n%s", res)
		}
		folded := dmEntries(t, e, with("--from", agent.Npub, "--from", alice.Npub, "--signer", alice.Nsec, "--fold")...)
		if len(folded) != 1 || len(folded[0].Reactions) != 0 {
			t.Fatalf("folded %+v", folded)
		}
		read := e.MustOK(t, with("dm", "read", "--from", agent.Npub, "--signer", alice.Nsec)...)
		if !strings.Contains(read.Stdout, "👀 on "+msg.ID[:12]) || !strings.Contains(read.Stdout, "removed "+talk.ID[:12]) {
			t.Fatalf("dm read text\n%s", read)
		}
		react := e.MustOK(t, with("dm", "react", "--to", alice.Npub, msg.ID, "+", "--signer", s.URI)...)
		if !strings.HasPrefix(react.Stdout, "reacted + on "+msg.ID) || !strings.Contains(react.Stdout, "published ") {
			t.Fatalf("dm react text\n%s", react)
		}
		fold := e.MustOK(t, with("dm", "read", "--from", agent.Npub, "--from", alice.Npub, "--signer", alice.Nsec, "--fold")...)
		if !strings.Contains(fold.Stdout, "reactions: + 1") {
			t.Fatalf("dm read --fold text\n%s", fold)
		}
	})
}

func TestDMReactions_Errors(t *testing.T) {
	e := NewEnv(t)
	alice := A(t, "alice")
	dead := DeadRelayURL(t)
	id := strings.Repeat("a", 64)
	e.Run(t, "dm", "react", id, "👀", "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "usage")
	e.Run(t, "dm", "react", "--to", alice.Npub, id, "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "usage")
	e.Run(t, "dm", "react", "--to", alice.Npub, "nope", "👀", "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "invalid_input")
	e.Run(t, "dm", "react", "--to", alice.Npub, id, " ", "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "invalid_input")
	e.Run(t, "dm", "react", "--to", alice.Npub, id, "👀", "--signer", alice.Nsec, "--json").ExpectErr(t, "usage")
	e.Run(t, "dm", "react", "--to", alice.Npub, id, "👀", "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "network")
	e.Run(t, "dm", "unreact", "--to", alice.Npub, "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "usage")
	e.Run(t, "dm", "unreact", "--to", alice.Npub, id, "--target", id, "--emoji", "👀", "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "usage")
	e.Run(t, "dm", "unreact", "--to", alice.Npub, "--target", id, "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "usage")
	e.Run(t, "dm", "unreact", "--to", alice.Npub, "--target", id, "--emoji", "👀", "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "network")
	e.Run(t, "dm", "read", "--from", alice.Npub, "--fold", "-f", "-s", dead, "--signer", alice.Nsec, "--json").ExpectErr(t, "usage")
}
