package climatrix

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/ohstr/nmilat/nip19"
)

// Commands that need no relay. Fast enough for `just check`.

func TestVersion(t *testing.T) {
	e := NewEnv(t)
	var v map[string]string
	e.MustOK(t, "version", "--json").JSON(t, &v)
	for _, k := range []string{"ncli_version", "nmilat_version", "app_data_dir", "prefs_path", "vault_path", "log_dir"} {
		if v[k] == "" {
			t.Errorf("version --json missing %q: %v", k, v)
		}
	}
	if !strings.HasPrefix(v["app_data_dir"], e.Dir) {
		t.Errorf("app_data_dir %q not under the isolated config dir %q", v["app_data_dir"], e.Dir)
	}
	if out := e.MustOK(t, "version").Stdout; !strings.Contains(out, "ncli:") {
		t.Errorf("text version output: %q", out)
	}
}

func TestDecode(t *testing.T) {
	e := NewEnv(t)
	alice := A(t, "alice")
	id := strings.Repeat("ab", 32)
	note, _ := nip19.EncodeNote(id)
	nevent, _ := nip19.EncodeEvent(nip19.EventPointer{ID: id, Relays: []string{"ws://localhost:1"}, Author: alice.PubHex, Kind: 1})
	nprofile, _ := nip19.EncodeProfile(alice.PubHex, []string{"ws://localhost:1"})
	naddr, _ := nip19.EncodeAddr(nip19.EntityPointer{Identifier: "standup", PublicKey: alice.PubHex, Kind: 30312})

	cases := []struct {
		entity, typ, want string
	}{
		{alice.Npub, "npub", alice.PubHex},
		{alice.Nsec, "nsec", alice.PrivHex},
		{note, "note", id},
		{nevent, "nevent", id},
		{nprofile, "nprofile", alice.PubHex},
		{naddr, "naddr", "standup"},
	}
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			r := e.MustOK(t, "decode", c.entity, "--json")
			var m map[string]any
			r.JSON(t, &m)
			if m["type"] != c.typ {
				t.Errorf("type = %v, want %s", m["type"], c.typ)
			}
			if !strings.Contains(r.Stdout, c.want) {
				t.Errorf("decoded output lacks %s\n%s", c.want, r)
			}
			if text := e.MustOK(t, "decode", c.entity); !strings.Contains(text.Stdout, c.want) {
				t.Errorf("text output lacks %s\n%s", c.want, text)
			}
		})
	}

	t.Run("bad checksum", func(t *testing.T) {
		bad := note[:len(note)-1] + "q"
		if bad == note {
			bad = note[:len(note)-1] + "p"
		}
		er := e.Run(t, "decode", bad, "--json").ExpectErr(t, "invalid_input")
		if er.Input != bad {
			t.Errorf("input = %q, want the entity", er.Input)
		}
	})
	t.Run("garbage", func(t *testing.T) {
		e.Run(t, "decode", "hello", "--json").ExpectErr(t, "invalid_input")
	})
	t.Run("malformed nsec never echoed", func(t *testing.T) {
		bad := alice.Nsec[:len(alice.Nsec)-2] + "zz"
		er := e.Run(t, "decode", bad, "--json").ExpectErr(t, "invalid_input")
		if er.Input != "" {
			t.Errorf("nsec echoed in input: %q", er.Input)
		}
	})
	t.Run("too many args", func(t *testing.T) {
		e.Run(t, "decode", alice.Npub, alice.Npub, "--json").ExpectErr(t, "usage")
	})
}

func TestID(t *testing.T) {
	e := NewEnv(t)

	t.Run("generate", func(t *testing.T) {
		var g struct {
			Npub, Nsec, PrivHex, PubHex string
			Saved                       bool
		}
		r := e.MustOK(t, "id", "--json")
		r.JSON(t, &g)
		raw := map[string]any{}
		r.JSON(t, &raw)
		for _, k := range []string{"npub", "nsec", "priv_hex", "pub_hex"} {
			if s, _ := raw[k].(string); s == "" {
				t.Errorf("id --json missing %s", k)
			}
		}
		if raw["saved"] != false {
			t.Errorf("unsaved identity reported saved")
		}
	})

	t.Run("save, conflict, list, reveal", func(t *testing.T) {
		var saved map[string]any
		e.MustOK(t, "id", "--save", "--label", "seed", "--json").JSON(t, &saved)
		if saved["saved"] != true || saved["label"] != "seed" {
			t.Fatalf("save result: %v", saved)
		}
		er := e.Run(t, "id", "--save", "--label", "seed", "--json").ExpectErr(t, "conflict")
		if er.Input != "seed" {
			t.Errorf("conflict input = %q", er.Input)
		}

		var list struct {
			Identities []map[string]any `json:"identities"`
		}
		e.MustOK(t, "id", "list", "--json").JSON(t, &list)
		if len(list.Identities) != 1 || list.Identities[0]["label"] != "seed" {
			t.Fatalf("id list: %v", list)
		}
		if _, ok := list.Identities[0]["nsec"]; ok {
			t.Errorf("id list leaks nsec without --reveal")
		}
		e.MustOK(t, "id", "list", "--reveal", "--json").JSON(t, &list)
		if list.Identities[0]["nsec"] != saved["nsec"] {
			t.Errorf("--reveal nsec mismatch")
		}

		var inspect map[string]any
		e.MustOK(t, "id", "seed", "--json").JSON(t, &inspect)
		if inspect["npub"] != saved["npub"] {
			t.Errorf("inspect by label: %v", inspect)
		}
	})

	t.Run("wrong vault password", func(t *testing.T) {
		e2 := NewEnv(t)
		e2.MustOK(t, "id", "--save", "--label", "k", "--json")
		e2.Setenv("NCLI_VAULT_PASSWORD", "wrong")
		e2.Run(t, "id", "list", "--reveal", "--json").ExpectErr(t, "auth")
	})

	// AGENTS.md: a missing vault entry is not_found; a malformed key is
	// invalid_input.
	t.Run("missing vault label", func(t *testing.T) {
		er := e.Run(t, "id", "nosuchlabel", "--json").ExpectErr(t, "not_found")
		if er.Input != "nosuchlabel" {
			t.Errorf("input = %q", er.Input)
		}
		e.Run(t, "id", "sign", "-e", e.WriteFile("u1.json", `{"kind":1,"content":"x","created_at":1,"tags":[]}`), "-o", e.Dir+"/s1.json", "--identity", "nosuchlabel", "--json").ExpectErr(t, "not_found")
		e.Run(t, "id", "npub1notvalid", "--json").ExpectErr(t, "invalid_input")
	})

	t.Run("inspect nprofile and hex", func(t *testing.T) {
		alice := A(t, "alice")
		nprofile, _ := nip19.EncodeProfile(alice.PubHex, []string{"ws://localhost:1"})
		for _, ident := range []string{alice.PubHex, nprofile, alice.Npub} {
			var m map[string]any
			e.MustOK(t, "id", ident, "--json").JSON(t, &m)
			if m["pub_hex"] != alice.PubHex {
				t.Errorf("id %s: pub_hex = %v", ident, m["pub_hex"])
			}
		}
	})
}

func TestIDSign(t *testing.T) {
	e := NewEnv(t)
	alice := A(t, "alice")
	unsigned := e.WriteFile("unsigned.json", `[{"kind":1,"content":"one","created_at":1790000000,"tags":[]},{"kind":1,"content":"two","created_at":1790000001,"tags":[["t","x"]]}]`)
	out := filepath.Join(e.Dir, "signed.json")

	var res map[string]any
	e.MustOK(t, "id", "sign", "-e", unsigned, "-o", out, "--identity", alice.Nsec, "--json").JSON(t, &res)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var signed []map[string]any
	if err := json.Unmarshal(b, &signed); err != nil || len(signed) != 2 {
		t.Fatalf("signed file: %v %s", err, b)
	}
	for _, ev := range signed {
		if ev["pubkey"] != alice.PubHex || ev["sig"] == "" || ev["id"] == "" {
			t.Errorf("bad signed event: %v", ev)
		}
	}
	// The signatures verify: miner check re-verifies ids, and a relay
	// would reject a bad sig; decode-level check is enough here.
	// No private key behind the identity is an auth failure everywhere
	// (cli/keyresolve), not a parse failure.
	t.Run("pubkey-only identity rejected", func(t *testing.T) {
		e.Run(t, "id", "sign", "-e", unsigned, "-o", out, "--identity", alice.Npub, "--json").ExpectErr(t, "auth")
	})
	t.Run("missing events file", func(t *testing.T) {
		e.Run(t, "id", "sign", "-e", filepath.Join(e.Dir, "nope.json"), "-o", out, "--identity", alice.Nsec, "--json").ExpectErr(t, "invalid_input")
	})
	t.Run("missing flags", func(t *testing.T) {
		e.Run(t, "id", "sign", "--json").ExpectErr(t, "usage")
	})
}

func TestIDDelegate(t *testing.T) {
	e := NewEnv(t)
	alice, agent := A(t, "alice"), A(t, "agent")
	var d map[string]any
	e.MustOK(t, "id", "delegate", "--issuer", alice.Nsec, "--delegatee", agent.Nsec, "--kinds", "1,7", "--duration", "30", "--json").JSON(t, &d)
	if d["issuer_pubkey"] != alice.PubHex || d["delegatee_pubkey"] != agent.PubHex {
		t.Errorf("delegation keys: %v", d)
	}
	if c, _ := d["conditions"].(string); !strings.HasPrefix(c, "kind=1,7&created_at<") {
		t.Errorf("conditions = %q", c)
	}
	if tok, _ := d["token"].(string); len(tok) != 128 {
		t.Errorf("token = %q, want a 64-byte schnorr sig", tok)
	}
	// Checked independently of nmilat: NIP-26 signs
	// sha256("nostr:delegation:<delegatee>:<conditions>").
	t.Run("token verifies under NIP-26", func(t *testing.T) {
		cond, _ := d["conditions"].(string)
		tok, _ := d["token"].(string)
		sigBytes, err := hex.DecodeString(tok)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := schnorr.ParseSignature(sigBytes)
		if err != nil {
			t.Fatal(err)
		}
		pubBytes, _ := hex.DecodeString(alice.PubHex)
		pub, err := schnorr.ParsePubKey(pubBytes)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte("nostr:delegation:" + agent.PubHex + ":" + cond))
		if !sig.Verify(digest[:], pub) {
			t.Errorf("delegation token doesn't verify against NIP-26's nostr:delegation:... string")
		}
	})
	t.Run("no issuer non-interactive", func(t *testing.T) {
		e.Run(t, "id", "delegate", "--json").ExpectErr(t, "usage")
	})
	t.Run("bad issuer", func(t *testing.T) {
		e.Run(t, "id", "delegate", "--issuer", "nsec1bogus", "--delegatee", agent.Nsec, "--json").ExpectErr(t, "invalid_input")
	})
	t.Run("bad kinds", func(t *testing.T) {
		e.Run(t, "id", "delegate", "--issuer", alice.Nsec, "--delegatee", agent.Nsec, "--kinds", "x", "--json").ExpectErr(t, "invalid_input")
	})
}

func TestMinerFileMode(t *testing.T) {
	e := NewEnv(t)
	alice := A(t, "alice")
	mined := filepath.Join(e.Dir, "mined.json")

	var m map[string]any
	e.MustOK(t, "miner", "mine", "--content", "hi", "--identity", alice.Nsec, "-d", "8", "-o", mined, "--json").JSON(t, &m)
	if m["signed"] != true || m["difficulty"] != float64(8) {
		t.Fatalf("mine result: %v", m)
	}
	if id, _ := m["id"].(string); !strings.HasPrefix(id, "00") {
		t.Errorf("mined id %q lacks 8 leading zero bits", id)
	}

	var c struct {
		Checked, Valid, Invalid int
	}
	e.MustOK(t, "miner", "check", "-e", mined, "--json").JSON(t, &c)
	if c.Checked != 1 || c.Valid != 1 {
		t.Errorf("check: %+v", c)
	}

	t.Run("unmined event fails check", func(t *testing.T) {
		b, _ := os.ReadFile(CorpusPath())
		var corpus []map[string]any
		_ = json.Unmarshal(b, &corpus)
		one, _ := json.Marshal(corpus[:1])
		p := e.WriteFile("plain.json", string(one))
		r := e.Run(t, "miner", "check", "-e", p, "--json")
		if r.Code == 0 {
			var c struct{ Valid int }
			r.JSON(t, &c)
			if c.Valid != 0 {
				t.Errorf("a corpus event with no nonce counted as valid PoW\n%s", r)
			}
		}
	})
	t.Run("mine needs an output mode", func(t *testing.T) {
		e.Run(t, "miner", "mine", "--content", "hi", "--json").ExpectErr(t, "usage")
	})
	t.Run("bad difficulty", func(t *testing.T) {
		e.Run(t, "miner", "mine", "--content", "hi", "--identity", alice.Nsec, "-d", "-1", "-o", mined, "--json").ExpectErr(t, "invalid_input")
	})
	t.Run("check bad file", func(t *testing.T) {
		p := e.WriteFile("bad.json", "{not json")
		e.Run(t, "miner", "check", "-e", p, "--json").ExpectErr(t, "invalid_input")
	})
}

func TestPrefs(t *testing.T) {
	e := NewEnv(t)
	var path struct{ Path string }
	e.MustOK(t, "prefs", "path", "--json").JSON(t, &path)
	if !strings.HasPrefix(path.Path, e.Dir) {
		t.Errorf("prefs path %q outside isolated dir", path.Path)
	}

	add := func(u string) (added bool, relay string) {
		var r struct {
			Added bool
			Relay string
		}
		e.MustOK(t, "prefs", "relays", "add", u, "--json").JSON(t, &r)
		return r.Added, r.Relay
	}
	if added, _ := add("ws://localhost:21601"); !added {
		t.Errorf("first add not added")
	}
	if added, _ := add("ws://localhost:21601"); added {
		t.Errorf("duplicate add reported added")
	}
	if _, relay := add("relay.local.test"); relay != "relay.local.test" && relay != "wss://relay.local.test" {
		t.Errorf("bare host stored as %q", relay)
	}

	var list struct{ Relays []string }
	e.MustOK(t, "prefs", "relays", "list", "--json").JSON(t, &list)
	if len(list.Relays) != 2 {
		t.Fatalf("list: %v", list.Relays)
	}

	var rm struct{ Removed bool }
	e.MustOK(t, "prefs", "relays", "remove", "ws://localhost:21601", "--json").JSON(t, &rm)
	if !rm.Removed {
		t.Errorf("remove of a listed relay: removed=false")
	}
	e.MustOK(t, "prefs", "relays", "remove", "ws://localhost:21601", "--json").JSON(t, &rm)
	if rm.Removed {
		t.Errorf("remove of an absent relay: removed=true")
	}
	e.MustOK(t, "prefs", "relays", "clear", "--json")
	e.MustOK(t, "prefs", "relays", "list", "--json").JSON(t, &list)
	if len(list.Relays) != 0 {
		t.Errorf("after clear: %v", list.Relays)
	}

	t.Run("invalid url", func(t *testing.T) {
		e.Run(t, "prefs", "relays", "add", "not a url", "--json").ExpectErr(t, "invalid_input")
		e.Run(t, "prefs", "relays", "add", "http://localhost:1", "--json").ExpectErr(t, "invalid_input")
	})
}

func TestRelayContext(t *testing.T) {
	e := NewEnv(t)
	cfg := e.WriteFile("relay.yaml", "port: 21699\n")

	var added struct{ Name, Path string }
	e.MustOK(t, "relay", "context", "add", "one", cfg, "--json").JSON(t, &added)
	if added.Name != "one" || added.Path != cfg {
		t.Errorf("add: %+v", added)
	}
	var list struct {
		Contexts map[string]string
		Current  string
	}
	e.MustOK(t, "relay", "context", "list", "--json").JSON(t, &list)
	if list.Contexts["one"] != cfg || list.Current != "" {
		t.Errorf("list: %+v", list)
	}
	e.MustOK(t, "relay", "context", "use", "one", "--json")
	e.MustOK(t, "relay", "context", "list", "--json").JSON(t, &list)
	if list.Current != "one" {
		t.Errorf("current = %q", list.Current)
	}

	e.Run(t, "relay", "context", "use", "nope", "--json").ExpectErr(t, "not_found")
	var rmc struct{ Removed bool }
	e.MustOK(t, "relay", "context", "remove", "nope", "--json").JSON(t, &rmc)
	if rmc.Removed {
		t.Errorf("removing an absent context reported removed")
	}
	e.Run(t, "relay", "context", "add", "two", filepath.Join(e.Dir, "missing.yaml"), "--json").ExpectErr(t, "not_found")
	e.MustOK(t, "relay", "context", "remove", "one", "--json")
	var after struct {
		Contexts map[string]string
		Current  string
	}
	e.MustOK(t, "relay", "context", "list", "--json").JSON(t, &after)
	if len(after.Contexts) != 0 || after.Current != "" {
		t.Errorf("after removing the current context: %+v", after)
	}
}

// Group commands invoked without a subcommand are usage errors (exit 2),
// never a silent success.
func TestBareGroupCommands(t *testing.T) {
	e := NewEnv(t)
	for _, c := range []string{
		"blossom", "blossom servers", "bunker sessions", "groups", "groups members",
		"groups pins", "huddle", "miner", "prefs", "prefs relays", "relay clear",
		"relay invites", "relay members", "relay reindex", "relay roles", "space",
	} {
		t.Run(c, func(t *testing.T) {
			r := e.Run(t, append(strings.Fields(c), "--json")...)
			r.ExpectErr(t, "usage")
			if strings.Contains(r.Stderr, "Usage:") {
				t.Errorf("--json printed help")
			}
		})
	}
	t.Run("bare root", func(t *testing.T) {
		r := e.Run(t, "--json")
		r.ExpectErr(t, "usage")
		if strings.Contains(r.Stdout+r.Stderr, "Usage:") {
			t.Errorf("--json printed help")
		}
		if h := e.Run(t, "--help"); h.Code != 0 || !strings.Contains(h.Stdout, "Usage:") {
			t.Errorf("--help: exit %d", h.Code)
		}
		e.Run(t, "nosuchcmd", "--json").ExpectErr(t, "usage")
	})
	t.Run("unknown subcommand", func(t *testing.T) {
		e.Run(t, "prefs", "nope", "--json").ExpectErr(t, "usage")
	})
	t.Run("unknown flag", func(t *testing.T) {
		e.Run(t, "version", "--nope", "--json").ExpectErr(t, "usage")
	})
}

// Without a running daemon the bunker's scriptable surface reports it
// cleanly instead of hanging or succeeding.
func TestBunkerWithoutDaemon(t *testing.T) {
	e := NewEnv(t)
	var st struct{ Running bool }
	e.MustOK(t, "bunker", "status", "--json").JSON(t, &st)
	if st.Running {
		t.Fatalf("status reports a bunker running in a fresh env")
	}
	pk := A(t, "alice").PubHex
	for _, args := range [][]string{
		{"bunker", "sessions", "list"},
		{"bunker", "sessions", "grants", pk},
		{"bunker", "sessions", "rename", pk, "x"},
		{"bunker", "sessions", "revoke", pk},
		{"bunker", "sessions", "revoke-grant", pk, "--method", "ping"},
		{"bunker", "history"},
		{"bunker", "stop"},
		{"bunker", "connect"},
	} {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			e.Run(t, append(args, "--json")...).ExpectErr(t, "not_found")
		})
	}
	t.Run("TUI commands need a TTY", func(t *testing.T) {
		e.Run(t, "bunker", "--json").ExpectErr(t, "usage")
		e.Run(t, "bunker", "attach", "--json").ExpectErr(t, "usage")
	})
}
