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
