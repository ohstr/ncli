package climatrix

import (
	"strings"
	"testing"
)

// TestRelayCheckLimits: --check validates limits: and warns, without
// failing, about a public relay that limits nothing.
func TestRelayCheckLimits(t *testing.T) {
	e := NewEnv(t)
	priv := A(t, "operator").PrivHex
	base := func() string { return "store: " + t.TempDir() + "/notes.db\nnip11:\n  privkey: " + priv + "\n" }

	type checked struct {
		OK       bool     `json:"ok"`
		Warnings []string `json:"warnings"`
	}
	const warning = "no rate limits configured (limits:)"

	t.Run("a public relay without limits warns", func(t *testing.T) {
		path := e.WriteFile("public.yaml", base())
		res := e.MustOK(t, "relay", "--config", path, "--check", "--json")
		var got checked
		res.JSON(t, &got)
		if !got.OK || len(got.Warnings) != 1 || got.Warnings[0] != warning {
			t.Errorf("--check = %s", res.Stdout)
		}
		if !strings.Contains(res.Stderr, warning) {
			t.Errorf("no warning on stderr:\n%s", res)
		}
		text := e.MustOK(t, "relay", "--config", path, "--check")
		if !strings.Contains(text.Stderr, warning) || !strings.Contains(text.Stdout, "is valid") {
			t.Errorf("text --check:\n%s", text)
		}
	})

	t.Run("an auth_required relay does not", func(t *testing.T) {
		path := e.WriteFile("auth.yaml", base()+"  url: wss://relay.example.com\n  limitation:\n    auth_required: true\n")
		res := e.MustOK(t, "relay", "--config", path, "--check", "--json")
		var got checked
		res.JSON(t, &got)
		if !got.OK || got.Warnings == nil || len(got.Warnings) != 0 || strings.Contains(res.Stderr, warning) {
			t.Errorf("--check:\n%s", res)
		}
	})

	t.Run("a public relay with limits does not", func(t *testing.T) {
		path := e.WriteFile("limited.yaml", base()+`limits:
  events: {perPubkey: {events: 30, per: 1m, burst: 60}, perIP: {events: 120, per: 1m}}
  reads: {perIP: {events: 60, per: 1m}}
  http: {perIP: {events: 60, per: 1m}}
  blossom:
    uploads: {perPubkey: {events: 20, per: 1h}}
    downloads: {perIP: {events: 600, per: 1m}}
  connectionsPerIP: 20
  exempt: members
  trustedProxies: [10.0.0.0/8]
`)
		res := e.MustOK(t, "relay", "--config", path, "--check", "--json")
		var got checked
		res.JSON(t, &got)
		if !got.OK || len(got.Warnings) != 0 || strings.Contains(res.Stderr, warning) {
			t.Errorf("--check:\n%s", res)
		}
	})

	for name, c := range map[string]struct{ yaml, input string }{
		"negative events":    {"events: {perIP: {events: -1, per: 1m}}", "limits.events.perIP.events"},
		"events without per": {"reads: {perPubkey: {events: 5}}", "limits.reads.perPubkey.per"},
		"bad duration":       {"http: {perIP: {events: 5, per: soon}}", "soon"},
		"bad CIDR":           {"trustedProxies: [10.0.0.0/40]", "10.0.0.0/40"},
		"unknown exempt":     {"exempt: everyone", "everyone"},
	} {
		t.Run(name, func(t *testing.T) {
			path := e.WriteFile(strings.ReplaceAll(name, " ", "-")+".yaml", base()+"limits:\n  "+c.yaml+"\n")
			er := e.Run(t, "relay", "--config", path, "--check", "--json").ExpectErr(t, "invalid_input")
			if er.Input != c.input {
				t.Errorf("input = %q, want %q", er.Input, c.input)
			}
		})
	}
}
