package climatrix

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nip19"
)

// corpusEvent is the shape of one testdata/events.json entry.
type corpusEvent struct {
	ID        string     `json:"id"`
	PubKey    string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Kind      int        `json:"kind"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	Sig       string     `json:"sig"`
}

func loadCorpus(t *testing.T) []corpusEvent {
	t.Helper()
	b, err := os.ReadFile(CorpusPath())
	if err != nil {
		t.Fatal(err)
	}
	var evs []corpusEvent
	if err := json.Unmarshal(b, &evs); err != nil {
		t.Fatal(err)
	}
	return evs
}

func firstOfKind(t *testing.T, evs []corpusEvent, kind int) corpusEvent {
	t.Helper()
	for _, e := range evs {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("corpus has no kind %d", kind)
	return corpusEvent{}
}

func findEvents(t *testing.T, e *Env, args ...string) []corpusEvent {
	t.Helper()
	r := e.MustOK(t, append([]string{"find", "--json"}, args...)...)
	var evs []corpusEvent
	r.JSON(t, &evs)
	if evs == nil {
		t.Fatalf("find printed null, want []\n%s", r)
	}
	r.ExpectCleanJSONStderr(t)
	return evs
}

// One seeded relay shared by the read-only query tests below.
func TestQuery(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	r.Seed(t)
	corpus := loadCorpus(t)
	e := NewEnv(t)
	note := firstOfKind(t, corpus, 1)

	t.Run("find by id forms", func(t *testing.T) {
		noteID, _ := nip19.EncodeNote(note.ID)
		nevent, _ := nip19.EncodeEvent(nip19.EventPointer{ID: note.ID, Relays: []string{r.URL}})
		for _, ident := range []string{note.ID, noteID, nevent} {
			evs := findEvents(t, e, ident, "-s", r.URL)
			if len(evs) != 1 || evs[0].ID != note.ID {
				t.Errorf("find %s: got %d events", ident[:12], len(evs))
			}
		}
	})

	t.Run("find by author forms", func(t *testing.T) {
		npub, _ := nip19.EncodePublicKey(note.PubKey)
		nprofile, _ := nip19.EncodeProfile(note.PubKey, nil)
		for _, ident := range []string{npub, nprofile} {
			for _, ev := range findEvents(t, e, ident, "-s", r.URL) {
				if ev.PubKey != note.PubKey {
					t.Errorf("find %s returned another author's event", ident[:12])
				}
			}
		}
	})

	t.Run("find by nip-05", func(t *testing.T) {
		srv := StartNip05(t, map[string]string{"bob": note.PubKey})
		e2 := NewEnv(t)
		e2.TrustNip05(srv)
		evs := findEvents(t, e2, srv.ID("bob"), "--kinds", "1", "-s", r.URL)
		if len(evs) == 0 {
			t.Fatalf("nip-05 author found nothing")
		}
		for _, ev := range evs {
			if ev.PubKey != note.PubKey || ev.Kind != 1 {
				t.Errorf("author AND kinds not both applied: %+v", ev)
			}
		}
	})

	t.Run("find filters", func(t *testing.T) {
		if evs := findEvents(t, e, "--kinds", "7", "--limit", "500", "-s", r.URL); len(evs) != 40 {
			t.Errorf("kind 7 = %d, want 40", len(evs))
		}
		if evs := findEvents(t, e, "--kinds", "1,7", "--limit", "3", "-s", r.URL); len(evs) != 3 {
			t.Errorf("--limit 3 returned %d", len(evs))
		}
		react := firstOfKind(t, corpus, 7)
		var target string
		for _, tag := range react.Tags {
			if tag[0] == "e" {
				target = tag[1]
			}
		}
		evs := findEvents(t, e, "--kinds", "7", "--tag", "e="+target, "-s", r.URL)
		if len(evs) == 0 {
			t.Errorf("--tag e= found nothing")
		}
		if evs := findEvents(t, e, "--kinds", "1", "--until", "1", "-s", r.URL); len(evs) != 0 {
			t.Errorf("--until 1 returned %d", len(evs))
		}
	})

	t.Run("no match is [] and exit 0", func(t *testing.T) {
		r2 := e.MustOK(t, "find", "--kinds", "4242", "-s", r.URL)
		if strings.TrimSpace(r2.Stdout) != "[]" {
			t.Errorf("stdout = %q, want []", r2.Stdout)
		}
	})

	t.Run("find -o also writes file", func(t *testing.T) {
		out := filepath.Join(e.Dir, "found.json")
		findEvents(t, e, note.ID, "-s", r.URL, "-o", out)
		b, err := os.ReadFile(out)
		if err != nil || !strings.Contains(string(b), note.ID) {
			t.Errorf("-o file: %v %s", err, b)
		}
	})

	t.Run("targets file", func(t *testing.T) {
		tf := e.WriteFile("targets.yaml", "kind: targets\nspec:\n  relays: ["+r.URL+"]\n  filters:\n    - kinds: [6]\n      limit: 100\n")
		if evs := findEvents(t, e, "-t", tf); len(evs) != 30 {
			t.Errorf("kind 6 via targets = %d, want 30", len(evs))
		}
		e.Run(t, "find", "-t", tf, "-s", r.URL, "--json").ExpectErr(t, "usage")
	})

	t.Run("partial failure tolerated", func(t *testing.T) {
		evs := findEvents(t, e, note.ID, "-s", DeadRelayURL(t)+","+r.URL)
		if len(evs) != 1 {
			t.Errorf("mixed targets: %d events", len(evs))
		}
	})

	t.Run("anonymous find on a gated relay", func(t *testing.T) {
		gated := authRelay(t)
		eve := A(t, "eve")
		DialAs(t, gated.URL, eve.PrivHex).Publish(Ev(t, eve.PrivHex, 1, "behind auth"))
		res := e.Run(t, "find", "--kinds", "1", "-s", gated.URL, "--json")
		// AGENTS.md: [] means "queried, nothing matched". Here the relay
		// refused the REQ, but the anonymous reader can't see that.
		known(t, "anon-restricted-read-looks-empty", res.Code == 0 && strings.TrimSpace(res.Stdout) == "[]",
			"anonymous find on an auth_required relay: exit 0, []")
		var evs []corpusEvent
		e.MustOK(t, "find", "--kinds", "1", "-s", gated.URL, "--auth-identity", eve.Nsec, "--json").JSON(t, &evs)
		if len(evs) != 1 {
			t.Errorf("--auth-identity find: %d events, want 1", len(evs))
		}
	})

	t.Run("every target down is network", func(t *testing.T) {
		e.Run(t, "find", "--kinds", "1", "-s", DeadRelayURL(t), "--json").ExpectErr(t, "network")
	})

	t.Run("bad values", func(t *testing.T) {
		e.Run(t, "find", "--kinds", "abc", "-s", r.URL, "--json").ExpectErr(t, "invalid_input")
		e.Run(t, "find", "--since", "nonsense", "-s", r.URL, "--json").ExpectErr(t, "invalid_input")
		e.Run(t, "find", "-s", "not a url", "--kinds", "1", "--json").ExpectErr(t, "invalid_input")
		e.Run(t, "find", "npub1notvalid", "-s", r.URL, "--json").ExpectErr(t, "invalid_input")
		e.Run(t, "find", "--json").ExpectErr(t, "usage")
	})

	t.Run("prefs relays are the default target", func(t *testing.T) {
		e2 := NewEnv(t)
		e2.MustOK(t, "prefs", "relays", "add", r.URL)
		if evs := findEvents(t, e2, note.ID); len(evs) != 1 {
			t.Errorf("find with prefs relay: %d events", len(evs))
		}
		e3 := NewEnv(t)
		e3.Run(t, "find", note.ID, "--json").ExpectErr(t, "not_found")
	})

	t.Run("dump", func(t *testing.T) {
		out := filepath.Join(e.Dir, "dump.json")
		res := e.MustOK(t, "dump", "-s", r.URL, "--kinds", "9735", "-o", out, "--json")
		res.ExpectCleanJSONStderr(t)
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		var evs []corpusEvent
		if err := json.Unmarshal(b, &evs); err != nil || len(evs) != 24 {
			t.Errorf("dump kind 9735: %d events, err %v", len(evs), err)
		}
		e.Run(t, "dump", "-s", r.URL, "--kinds", "1", "--json").ExpectErr(t, "usage")
		e.Run(t, "dump", "-s", DeadRelayURL(t), "--kinds", "1", "-o", out, "--json").ExpectErr(t, "network")
	})

	t.Run("dump merges across targets", func(t *testing.T) {
		r2 := StartRelay(t, "", nil)
		out := filepath.Join(e.Dir, "merged.json")
		// r has every kind-30023 event; r2 has none -- merge is still 20.
		e.MustOK(t, "dump", "-s", r.URL+","+r2.URL, "--kinds", "30023", "-o", out)
		b, _ := os.ReadFile(out)
		var evs []corpusEvent
		_ = json.Unmarshal(b, &evs)
		if len(evs) != 20 {
			t.Errorf("merged dump = %d, want 20", len(evs))
		}
	})

	t.Run("local db file as target", func(t *testing.T) {
		r2 := StartRelay(t, "", nil)
		NewEnv(t).MustOK(t, "publish", "-e", e.WriteFile("n.json", mustMarshal(t, []corpusEvent{note})), "-s", r2.URL)
		r2.Stop()
		db := filepath.Join(r2.Dir, "db", "notes.db")
		evs := findEvents(t, e, note.ID, "-s", db)
		if len(evs) != 1 {
			t.Errorf("find in .db file: %d events", len(evs))
		}
	})

	t.Run("profile", func(t *testing.T) {
		meta := firstOfKind(t, corpus, 0)
		var p map[string]any
		e.MustOK(t, "profile", meta.PubKey, "-s", r.URL, "--no-verify", "--json").JSON(t, &p)
		if p["pubkey"] != meta.PubKey {
			t.Errorf("profile pubkey: %v", p["pubkey"])
		}
		if _, ok := p["metadata"].(map[string]any); !ok {
			t.Errorf("profile has no metadata: %v", p)
		}
		var none map[string]any
		res := e.Run(t, "profile", A(t, "carol").Npub, "-s", r.URL, "--json")
		if res.Code == 0 {
			res.JSON(t, &none)
		} else {
			res.ExpectErr(t, "not_found")
		}
		e.Run(t, "profile", meta.PubKey, "-s", DeadRelayURL(t), "--json").ExpectErr(t, "network")
	})

	t.Run("profile aggregates across relays", func(t *testing.T) {
		meta := firstOfKind(t, corpus, 0)
		r2 := StartRelay(t, "", nil)
		var p struct {
			QueriedRelays int `json:"queried_relays"`
		}
		e.MustOK(t, "profile", meta.PubKey, "-s", r2.URL+","+r.URL, "--no-verify", "--json").JSON(t, &p)
		if p.QueriedRelays != 2 {
			t.Errorf("queried_relays = %d, want 2", p.QueriedRelays)
		}
	})

	t.Run("miner check live", func(t *testing.T) {
		alice := A(t, "alice")
		mined := filepath.Join(e.Dir, "mined.json")
		e.MustOK(t, "miner", "mine", "--content", "pow", "--identity", alice.Nsec, "-d", "8", "-o", mined)
		b, _ := os.ReadFile(mined)
		e.MustOK(t, "publish", "-e", e.WriteFile("mined-arr.json", "["+string(b)+"]"), "-s", r.URL)
		var c struct{ Checked, Valid int }
		e.MustOK(t, "miner", "check", "-s", r.URL, "--identity", alice.Npub, "--json").JSON(t, &c)
		if c.Checked != 1 || c.Valid != 1 {
			t.Errorf("live check: %+v", c)
		}
		e.Run(t, "miner", "check", "-s", DeadRelayURL(t), "--kinds", "1", "--json").ExpectErr(t, "network")
	})
}

func TestPublish(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)

	var rep struct {
		Attempted, Succeeded, Failed int
		Results                      []map[string]any
	}
	e.MustOK(t, "publish", "-e", CorpusPath(), "-s", r.URL, "--json").JSON(t, &rep)
	if rep.Attempted != 339 || rep.Succeeded != 339 || rep.Failed != 0 || len(rep.Results) != 339 {
		t.Fatalf("corpus publish: %d/%d ok, %d failed", rep.Succeeded, rep.Attempted, rep.Failed)
	}

	t.Run("republish is accepted (duplicate)", func(t *testing.T) {
		e.MustOK(t, "publish", "-e", CorpusPath(), "-s", r.URL, "--json").JSON(t, &rep)
		if rep.Failed != 0 {
			t.Errorf("republishing: %d failed", rep.Failed)
		}
	})

	t.Run("tampered event rejected per event", func(t *testing.T) {
		ev := firstOfKind(t, loadCorpus(t), 1)
		var raw map[string]any
		b, _ := json.Marshal(ev)
		_ = json.Unmarshal(b, &raw)
		raw["content"] = "tampered"
		raw["sig"] = strings.Repeat("0", 128)
		p := e.WriteFile("tampered.json", mustMarshal(t, []any{raw}))
		res := e.Run(t, "publish", "-e", p, "-s", r.URL, "--json")
		if res.Code == 0 {
			t.Fatalf("tampered event published with exit 0\n%s", res)
		}
		var rep struct{ Failed int }
		if json.Unmarshal([]byte(res.Stdout), &rep) == nil && rep.Failed != 1 {
			t.Errorf("failed = %d, want 1", rep.Failed)
		}
	})

	t.Run("errors", func(t *testing.T) {
		e.Run(t, "publish", "--json").ExpectErr(t, "usage")
		e.Run(t, "publish", "-e", filepath.Join(e.Dir, "none.json"), "-s", r.URL, "--json").ExpectErr(t, "invalid_input")
		e.Run(t, "publish", "-e", e.WriteFile("bad.json", "{x"), "-s", r.URL, "--json").ExpectErr(t, "invalid_input")
		e.Run(t, "publish", "-e", CorpusPath(), "-s", DeadRelayURL(t), "--json").ExpectErr(t, "network")
		NewEnv(t).Run(t, "publish", "-e", CorpusPath(), "--json").ExpectErr(t, "not_found")
	})
}

func TestPing(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)
	var res struct {
		Checked, Reachable, Unreachable int
	}
	e.MustOK(t, "ping", r.URL, "--json").JSON(t, &res)
	if res.Reachable != 1 {
		t.Errorf("ping up: %+v", res)
	}

	// Any unreachable target fails ping, unlike find/dump.
	// The per-relay results still go to stdout; the failure to stderr.
	mixed := e.Run(t, "ping", r.URL, DeadRelayURL(t), "--json")
	mixed.expectErr(t, "internal", true)
	mixed.JSON(t, &res)
	if res.Reachable != 1 || res.Unreachable != 1 {
		t.Errorf("ping mixed: %+v", res)
	}

	e.Run(t, "ping", "--json").ExpectErr(t, "not_found")
	tf := e.WriteFile("t.yaml", "kind: targets\nspec:\n  relays: ["+r.URL+"]\n")
	e.MustOK(t, "ping", "-t", tf, "--json").JSON(t, &res)
	if res.Reachable != 1 {
		t.Errorf("ping -t: %+v", res)
	}
	e.Run(t, "ping", "not a url", "--json").ExpectErr(t, "invalid_input")
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
