package climatrix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRelayMetricsToken(t *testing.T) {
	e := NewEnv(t)
	tokens := filepath.Join(e.Dir, "metrics.tokens")
	cfg := e.WriteFile("relay.yaml", "admin:\n  metrics:\n    tokensFile: "+tokens+"\n")

	var added struct{ Name, Token, File string }
	e.MustOK(t, "relay", "metrics", "token", "add", "prometheus", "--config", cfg, "--json").JSON(t, &added)
	if added.Name != "prometheus" || added.File != tokens || !strings.HasPrefix(added.Token, "ncli_mt_") {
		t.Fatalf("add: %+v", added)
	}
	raw, err := os.ReadFile(tokens)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), added.Token) {
		t.Fatal("plaintext token written to the tokens file")
	}
	if fi, _ := os.Stat(tokens); fi.Mode().Perm() != 0o600 {
		t.Errorf("tokens file mode %v, want 0600", fi.Mode().Perm())
	}

	e.Run(t, "relay", "metrics", "token", "add", "prometheus", "--file", tokens, "--json").ExpectErr(t, "conflict")
	e.Run(t, "relay", "metrics", "token", "add", "bad name", "--file", tokens, "--json").ExpectErr(t, "invalid_input")

	var list struct {
		File   string
		Tokens []struct{ Name, Created string }
	}
	e.MustOK(t, "relay", "metrics", "token", "list", "--file", tokens, "--json").JSON(t, &list)
	if len(list.Tokens) != 1 || list.Tokens[0].Name != "prometheus" || list.Tokens[0].Created == "" {
		t.Fatalf("list: %+v", list)
	}

	e.MustOK(t, "relay", "metrics", "token", "remove", "prometheus", "--file", tokens, "--json")
	e.Run(t, "relay", "metrics", "token", "remove", "prometheus", "--file", tokens, "--json").ExpectErr(t, "not_found")
	e.MustOK(t, "relay", "metrics", "token", "list", "--file", tokens, "--json").JSON(t, &list)
	if list.Tokens == nil || len(list.Tokens) != 0 {
		t.Errorf("list after remove: %+v, want []", list.Tokens)
	}

	// A loosened file is refused rather than trusted.
	if err := os.Chmod(tokens, 0o644); err != nil {
		t.Fatal(err)
	}
	e.Run(t, "relay", "metrics", "token", "list", "--file", tokens, "--json").ExpectErr(t, "invalid_input")

	// No --file and no config naming one is a usage error.
	NewEnv(t).Run(t, "relay", "metrics", "token", "list", "--json").ExpectErr(t, "usage")
}
