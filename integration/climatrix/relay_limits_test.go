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

	t.Run("a per-pubkey limit that never applies warns", func(t *testing.T) {
		path := e.WriteFile("unused.yaml", base()+`  url: wss://relay.example.com
blossom:
  enabled: true
limits:
  blossom:
    downloads: {perPubkey: {events: 60, per: 1m}}
`)
		res := e.MustOK(t, "relay", "--config", path, "--check", "--json")
		var got checked
		res.JSON(t, &got)
		if !got.OK || len(got.Warnings) != 1 || !strings.HasPrefix(got.Warnings[0], "limits.blossom.downloads.perPubkey counts only DELETEs") {
			t.Errorf("--check = %s", res.Stdout)
		}
		if !strings.Contains(res.Stderr, "limits.blossom.downloads.perPubkey") {
			t.Errorf("no warning on stderr:\n%s", res)
		}
	})

	t.Run("every config warning is listed and logged once", func(t *testing.T) {
		const portIgnored = "port is ignored: listen sets the relay's address"
		path := e.WriteFile("port-and-listen.yaml", base()+"port: 6000\nlisten: 127.0.0.1:7000\n")
		res := e.MustOK(t, "relay", "--config", path, "--check", "--json")
		var got checked
		res.JSON(t, &got)
		if !got.OK || len(got.Warnings) != 2 || got.Warnings[0] != portIgnored || got.Warnings[1] != warning {
			t.Errorf("--check = %s", res.Stdout)
		}
		for _, w := range []string{portIgnored, warning} {
			if n := strings.Count(res.Stderr, w); n != 1 {
				t.Errorf("%q logged %d times:\n%s", w, n, res.Stderr)
			}
		}
	})

	t.Run("a role-task warning is listed and logged once", func(t *testing.T) {
		const unlisted = "membership.members.can does not list blossom.download although blossom.membersOnly.download is on"
		path := e.WriteFile("role-tasks.yaml", base()+`  url: wss://relay.example.com
  limitation:
    auth_required: true
membership:
  enabled: true
  members:
    can: [createclaim]
blossom:
  enabled: true
  membersOnly:
    download: true
`)
		res := e.MustOK(t, "relay", "--config", path, "--check", "--json")
		var got checked
		res.JSON(t, &got)
		if !got.OK || len(got.Warnings) != 1 || !strings.HasPrefix(got.Warnings[0], unlisted) {
			t.Errorf("--check = %s", res.Stdout)
		}
		if n := strings.Count(res.Stderr, unlisted); n != 1 {
			t.Errorf("%q logged %d times:\n%s", unlisted, n, res.Stderr)
		}
	})

	t.Run("per-IP limits on loopback without a trusted proxy warn", func(t *testing.T) {
		const loopback = "listen is on loopback, so clients probably arrive through a local proxy: add it to limits.trustedProxies or every client shares one per-IP limit"
		limits := "limits:\n  events: {perIP: {events: 120, per: 1m}}\n  connectionsPerIP: 20\n"
		path := e.WriteFile("loopback.yaml", base()+"listen: 127.0.0.1:7000\n"+limits)
		res := e.MustOK(t, "relay", "--config", path, "--check", "--json")
		var got checked
		res.JSON(t, &got)
		if !got.OK || len(got.Warnings) != 1 || got.Warnings[0] != loopback || strings.Count(res.Stderr, loopback) != 1 {
			t.Errorf("--check:\n%s", res)
		}

		trusted := e.WriteFile("loopback-trusted.yaml", base()+"listen: 127.0.0.1:7000\n"+limits+"  trustedProxies: [127.0.0.1]\n")
		res = e.MustOK(t, "relay", "--config", trusted, "--check", "--json")
		got = checked{}
		res.JSON(t, &got)
		if !got.OK || len(got.Warnings) != 0 || strings.Contains(res.Stderr, loopback) {
			t.Errorf("--check with the proxy trusted:\n%s", res)
		}
	})

	for name, c := range map[string]struct{ yaml, input string }{
		"negative events":    {"events: {perIP: {events: -1, per: 1m}}", "limits.events.perIP.events"},
		"events without per": {"reads: {perPubkey: {events: 5}}", "limits.reads.perPubkey.per"},
		"bad duration":       {"http: {perIP: {events: 5, per: soon}}", "soon"},
		"bad CIDR":           {"trustedProxies: [10.0.0.0/40]", "10.0.0.0/40"},
		"unknown exempt":     {"exempt: everyone", "everyone"},
		"burst overflows":    {"events: {perIP: {events: 1, per: 1h, burst: 3000000}}", "limits.events.perIP.burst"},
		"under 1ns an event": {"reads: {perIP: {events: 2000, per: 1us}}", "limits.reads.perIP.events"},
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
