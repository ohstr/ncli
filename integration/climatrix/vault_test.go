package climatrix

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Commands that write prefs.yaml must keep the vault identity in it:
// losing it makes the next save create a new vault key, and every entry
// saved before becomes undecryptable ("invalid MAC").
func TestVaultSurvivesPrefsWrites(t *testing.T) {
	e := NewEnv(t)
	e.MustOK(t, "id", "--save", "--label", "keep", "--json")

	cfg := e.WriteFile("relay.yaml", "port: 21698\n")
	for _, args := range [][]string{
		{"prefs", "relays", "add", "ws://localhost:21697"},
		{"prefs", "relays", "remove", "ws://localhost:21697"},
		{"prefs", "relays", "clear"},
		{"relay", "context", "add", "c1", cfg},
		{"relay", "context", "use", "c1"},
		{"relay", "context", "remove", "c1"},
		{"blossom", "servers", "add", "http://localhost:21696"},
		{"blossom", "servers", "remove", "http://localhost:21696"},
		{"id", "--save", "--label", "second", "--json"},
	} {
		e.MustOK(t, append(args, "--json")...)
		if res := e.Run(t, "id", "list", "--reveal", "--json"); res.Code != 0 {
			t.Fatalf("after %s, saved identities no longer decrypt\n%s", strings.Join(args, " "), res)
		}
	}
}

// With the vault key gone (a lost or hand-edited prefs.yaml) but entries
// still saved, nothing silently starts a new vault -- that would orphan
// every saved identity -- and the error names the real problem.
func TestVaultKeyMissing(t *testing.T) {
	e := NewEnv(t)
	e.MustOK(t, "id", "--save", "--label", "old", "--json")
	var path struct{ Path string }
	e.MustOK(t, "prefs", "path", "--json").JSON(t, &path)
	raw, err := os.ReadFile(path.Path)
	if err != nil {
		t.Fatal(err)
	}
	prefs := map[string]any{}
	if err := yaml.Unmarshal(raw, &prefs); err != nil {
		t.Fatal(err)
	}
	delete(prefs, "vault_identity")
	out, _ := yaml.Marshal(prefs)
	if err := os.WriteFile(path.Path, out, 0o600); err != nil {
		t.Fatal(err)
	}

	er := e.Run(t, "id", "--save", "--label", "new", "--json").ExpectErr(t, "not_found")
	if !strings.Contains(er.Error, "vault key is missing") {
		t.Errorf("error doesn't name the missing vault key: %q", er.Error)
	}
	e.Run(t, "id", "list", "--reveal", "--json").ExpectErr(t, "not_found")
	var list struct {
		Identities []map[string]any `json:"identities"`
	}
	e.MustOK(t, "id", "list", "--json").JSON(t, &list)
	if len(list.Identities) != 1 {
		t.Errorf("saved entries changed: %v", list.Identities)
	}
}

// A different vault password is refused as auth, never "internal".
func TestVaultWrongPassword(t *testing.T) {
	e := NewEnv(t)
	e.MustOK(t, "id", "--save", "--label", "k", "--json")
	e.Setenv("NCLI_VAULT_PASSWORD", "something-else")
	e.Run(t, "id", "list", "--reveal", "--json").ExpectErr(t, "auth")
	e.Run(t, "id", "--save", "--label", "k2", "--json").ExpectErr(t, "auth")
	e.Run(t, "id", "sign", "-e", e.WriteFile("u.json", `{"kind":1,"content":"x","created_at":1,"tags":[]}`), "-o", e.Dir+"/s.json", "--identity", "k", "--json").ExpectErr(t, "auth")
}

// id relabel renames by label or npub without the key; the key still
// decrypts afterwards.
func TestIDRelabel(t *testing.T) {
	e := NewEnv(t)
	var a, b struct{ Npub, Label string }
	e.MustOK(t, "id", "--save", "--label", "alice", "--json").JSON(t, &a)
	e.MustOK(t, "id", "--save", "--label", "bob", "--json").JSON(t, &b)

	type out struct {
		Npub, Label, Status string
		PreviousLabel       string `json:"previous_label"`
	}
	var o out
	e.MustOK(t, "id", "relabel", "alice", "carol", "--json").JSON(t, &o)
	if o.Status != "relabeled" || o.Label != "carol" || o.PreviousLabel != "alice" || o.Npub != a.Npub {
		t.Fatalf("relabel by label = %+v", o)
	}
	e.MustOK(t, "id", "relabel", a.Npub, "carol", "--json").JSON(t, &o)
	if o.Status != "unchanged" {
		t.Fatalf("relabel to same label = %+v", o)
	}
	if er := e.Run(t, "id", "relabel", "carol", "BOB", "--json").ExpectErr(t, "conflict"); er.Input != "BOB" {
		t.Fatalf("conflict input = %q", er.Input)
	}
	e.Run(t, "id", "relabel", "alice", "x", "--json").ExpectErr(t, "not_found")
	e.Run(t, "id", "relabel", "carol", "--json").ExpectErr(t, "usage")

	// Case-only rename of its own label is allowed.
	e.MustOK(t, "id", "relabel", "carol", "Carol", "--json").JSON(t, &o)
	if o.Status != "relabeled" || o.Label != "Carol" {
		t.Fatalf("case-only relabel = %+v", o)
	}
	e.MustOK(t, "id", "carol", "--reveal", "--json")
}

// relabel and rm find an entry by hex pubkey or nip-05 too, and never
// prompt when piped; a pubkey that isn't saved is not_found.
func TestIDRelabelRmByIdentifier(t *testing.T) {
	e := NewEnv(t)
	var a, b struct {
		Npub   string
		PubHex string `json:"pub_hex"`
	}
	e.MustOK(t, "id", "--save", "--label", "alice", "--json").JSON(t, &a)
	e.MustOK(t, "id", "--save", "--label", "bob", "--json").JSON(t, &b)
	nip05 := StartNip05(t, map[string]string{"alice": a.PubHex, "stranger": strings.Repeat("ab", 32)})
	e.TrustNip05(nip05)

	var o struct{ Label, Status string }
	e.MustOK(t, "id", "relabel", a.PubHex, "a2", "--json").JSON(t, &o)
	if o.Status != "relabeled" || o.Label != "a2" {
		t.Fatalf("relabel by hex = %+v", o)
	}
	e.MustOK(t, "id", "relabel", nip05.ID("alice"), "a3", "--json").JSON(t, &o)
	if o.Status != "relabeled" || o.Label != "a3" {
		t.Fatalf("relabel by nip-05 = %+v", o)
	}
	e.Run(t, "id", "rm", nip05.ID("stranger"), "--yes", "--json").ExpectErr(t, "not_found")
	e.MustOK(t, "id", "rm", nip05.ID("alice"), "--yes", "--json").JSON(t, &o)
	if o.Status != "removed" || o.Label != "a3" {
		t.Fatalf("rm by nip-05 = %+v", o)
	}
	e.MustOK(t, "id", "rm", b.PubHex, "--yes", "--json")

	// Piped, no --json, no password: a clean usage error, not a prompt.
	e.MustOK(t, "id", "--save", "--label", "c", "--json")
	e.Setenv("NCLI_VAULT_PASSWORD", "")
	if r := e.Run(t, "id", "relabel", "c", "d"); r.Code != 2 {
		t.Fatalf("piped relabel without password: want exit 2\n%s", r)
	}
}

// id rm needs --yes without a terminal, removes only the named entry, and
// is not_found once gone.
func TestIDRm(t *testing.T) {
	e := NewEnv(t)
	var a struct{ Npub string }
	e.MustOK(t, "id", "--save", "--label", "alice", "--json").JSON(t, &a)
	e.MustOK(t, "id", "--save", "--label", "bob", "--json")

	e.Run(t, "id", "rm", "alice", "--json").ExpectErr(t, "usage")
	e.Run(t, "id", "rm", "nobody", "--yes", "--json").ExpectErr(t, "not_found")

	var o struct{ Npub, Label, Status string }
	e.MustOK(t, "id", "rm", a.Npub, "--yes", "--json").JSON(t, &o)
	if o.Status != "removed" || o.Label != "alice" || o.Npub != a.Npub {
		t.Fatalf("rm = %+v", o)
	}
	e.Run(t, "id", "remove", "alice", "--yes", "--json").ExpectErr(t, "not_found")

	var list struct {
		Identities []struct{ Label string } `json:"identities"`
	}
	e.MustOK(t, "id", "list", "--reveal", "--json").JSON(t, &list)
	if len(list.Identities) != 1 || list.Identities[0].Label != "bob" {
		t.Fatalf("after rm, vault = %+v, want only bob", list.Identities)
	}
}

// id import: key from stdin or --file only, first valid line wins,
// re-running is a no-op, and a key is never saved twice.
func TestIDImport(t *testing.T) {
	e := NewEnv(t)
	var key struct {
		Nsec, Npub string
		PrivHex    string `json:"priv_hex"`
	}
	e.MustOK(t, "id", "--json").JSON(t, &key)

	type out struct {
		Npub, Label, Status string
		PreviousLabel       string `json:"previous_label"`
		SkippedLines        int    `json:"skipped_lines"`
	}
	imp := func(stdin string, args ...string) Result {
		return e.RunStdin(t, stdin, append([]string{"id", "import", "--json"}, args...)...)
	}
	ok := func(r Result) out {
		t.Helper()
		if r.Code != 0 {
			t.Fatalf("want exit 0\n%s", r)
		}
		var o out
		r.JSON(t, &o)
		return o
	}

	if o := ok(imp("# backup\n\n  "+key.Nsec+"  \nnsec1ignored\n", "--label", "alice")); o.Status != "imported" || o.Npub != key.Npub || o.SkippedLines != 1 {
		t.Fatalf("import = %+v", o)
	}
	if o := ok(imp(key.PrivHex)); o.Status != "unchanged" || o.Label != "alice" {
		t.Fatalf("re-import = %+v", o)
	}
	if er := imp(key.Nsec, "--label", "bob").ExpectErr(t, "conflict"); er.Input != "alice" {
		t.Fatalf("conflict input = %q, want alice", er.Input)
	}
	if o := ok(imp(key.Nsec, "--label", "bob", "--force")); o.Status != "relabeled" || o.PreviousLabel != "alice" {
		t.Fatalf("--force = %+v", o)
	}
	if o := ok(e.Run(t, "id", "import", "--json", "--file", e.WriteFile("k.txt", key.Nsec+"\n"))); o.Status != "unchanged" || o.Label != "bob" {
		t.Fatalf("--file re-import = %+v", o)
	}

	r := imp("", key.Nsec)
	r.ExpectErr(t, "usage")
	if strings.Contains(r.Stdout+r.Stderr, key.Nsec) {
		t.Fatalf("positional key echoed back\n%s", r)
	}
	imp("npub1notakey\n").ExpectErr(t, "invalid_input")

	var list struct {
		Identities []map[string]any `json:"identities"`
	}
	e.MustOK(t, "id", "list", "--json").JSON(t, &list)
	if len(list.Identities) != 1 {
		t.Fatalf("vault holds %d entries, want 1", len(list.Identities))
	}

	// The imported key signs under its label.
	e.MustOK(t, "id", "sign", "-e", e.WriteFile("u.json", `{"kind":1,"content":"x","created_at":1,"tags":[]}`), "-o", e.Dir+"/s.json", "--identity", "bob", "--json")
}
