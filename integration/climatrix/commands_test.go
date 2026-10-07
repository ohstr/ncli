package climatrix

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestApply(t *testing.T) {
	needsRelay(t)
	src := StartRelay(t, "", nil)
	src.Seed(t)
	e := NewEnv(t)

	// sync and inspect render a TUI; headless they are a usage error, not
	// a crash. Their terminal runs belong to the PTY tests.
	t.Run("sync and inspect need a terminal", func(t *testing.T) {
		db := filepath.Join(e.Dir, "synced.db")
		spec := e.WriteFile("sync.yaml", `kind: sync
spec:
  from:
    relay: `+src.URL+`
  to:
    type: local
    path: `+db+`
    ensure: create
  direction: down
  filters:
    - kinds: [0]
`)
		e.Run(t, "apply", "-f", spec, "--json").ExpectErr(t, "usage")

		other := StartRelay(t, "", nil)
		spec = e.WriteFile("inspect.yaml", `kind: inspect
spec:
  targets:
    - `+src.URL+`
    - `+other.URL+`
  filters:
    - kinds: [1]
      limit: 10
`)
		e.Run(t, "apply", "-f", spec, "--json").ExpectErr(t, "usage")
	})

	t.Run("bad specs", func(t *testing.T) {
		e.Run(t, "apply", "--json").ExpectErr(t, "usage")
		missing := filepath.Join(e.Dir, "missing.yaml")
		if er := e.Run(t, "apply", "-f", missing, "--json").ExpectErr(t, "usage"); er.Input != missing {
			t.Errorf("input = %q, want the spec path", er.Input)
		}
		bad := e.WriteFile("bad.yaml", "kind: nosuchkind\nspec: {}\n")
		r := e.Run(t, "apply", "-f", bad, "--json")
		if r.Code == 0 {
			t.Errorf("unknown spec kind accepted\n%s", r)
		}
		garbage := e.WriteFile("garbage.yaml", "{{{")
		if r := e.Run(t, "apply", "-f", garbage, "--json"); r.Code == 0 {
			t.Errorf("unparseable spec accepted\n%s", r)
		}
	})
}

func TestRelayAdmin(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{"membership": map[string]any{"enabled": true}})
	r.Seed(t)
	e := NewEnv(t)
	cfg := r.ConfigPath
	bob := A(t, "bob")

	t.Run("stats", func(t *testing.T) {
		var s map[string]any
		e.MustOK(t, "relay", "stats", "--config", cfg, "--json").JSON(t, &s)
		if s["status"] != "active" {
			t.Errorf("stats: %v", s)
		}
		if !strings.Contains(e.MustOK(t, "relay", "stats", "--config", cfg).Stdout, "ACTIVE") {
			t.Errorf("text stats lacks status")
		}
	})

	t.Run("reindex and clear", func(t *testing.T) {
		var s struct{ Status string }
		e.MustOK(t, "relay", "reindex", "zaps", "--config", cfg, "--json").JSON(t, &s)
		if s.Status == "" {
			t.Errorf("reindex zaps: empty status")
		}
		e.MustOK(t, "relay", "clear", "zaps", "--config", cfg, "--json").JSON(t, &s)
		if s.Status != "deleted" {
			t.Errorf("clear zaps: %q", s.Status)
		}
		// Search isn't configured on this relay: a feature that's off is
		// usage, not a "started" that silently fails in the background.
		for _, c := range [][]string{{"reindex", "search"}, {"clear", "search"}} {
			e.Run(t, append(append([]string{"relay"}, c...), "--config", cfg, "--json")...).ExpectErr(t, "usage")
		}
	})

	t.Run("members lifecycle", func(t *testing.T) {
		e.MustOK(t, "relay", "members", "add", bob.PubHex, "--config", cfg, "--json")
		var show map[string]any
		e.MustOK(t, "relay", "members", "show", bob.PubHex, "--config", cfg, "--json").JSON(t, &show)
		if show["pubkey"] != bob.PubHex {
			t.Errorf("show: %v", show)
		}
		var list struct {
			Members []map[string]any `json:"members"`
		}
		e.MustOK(t, "relay", "members", "list", "--config", cfg, "--json").JSON(t, &list)
		if len(list.Members) == 0 {
			t.Errorf("list empty after add")
		}
		e.MustOK(t, "relay", "members", "remove", bob.PubHex, "--config", cfg, "--json")
		e.Run(t, "relay", "members", "show", bob.PubHex, "--config", cfg, "--json").ExpectErr(t, "not_found")
		e.Run(t, "relay", "members", "add", "notapubkey", "--config", cfg, "--json").ExpectErr(t, "invalid_input")
		e.Run(t, "relay", "members", "add", bob.Npub, "--config", cfg, "--json")
	})

	t.Run("invites lifecycle", func(t *testing.T) {
		code := createInvite(t, r, 2, "1h")
		var list struct {
			Invites []map[string]any `json:"invites"`
		}
		e.MustOK(t, "relay", "invites", "list", "--config", cfg, "--json").JSON(t, &list)
		found := false
		for _, inv := range list.Invites {
			found = found || inv["code"] == code
		}
		if !found {
			t.Errorf("created invite not listed")
		}
		e.MustOK(t, "relay", "invites", "revoke", code, "--config", cfg, "--json")
		e.Run(t, "relay", "invites", "revoke", "no-such-code", "--config", cfg, "--json").ExpectErr(t, "not_found")
		e.Run(t, "relay", "invites", "create", "--ttl", "nonsense", "--config", cfg, "--json").ExpectErr(t, "usage")
	})

	t.Run("roles", func(t *testing.T) {
		var role map[string]any
		e.MustOK(t, "relay", "roles", "create", "mod", "--label", "Moderator", "--config", cfg, "--json").JSON(t, &role)
		if role["id"] != "mod" {
			t.Errorf("create: %v", role)
		}
		var list struct {
			Roles []map[string]any `json:"roles"`
		}
		e.MustOK(t, "relay", "roles", "list", "--config", cfg, "--json").JSON(t, &list)
		if len(list.Roles) != 1 {
			t.Errorf("roles: %v", list.Roles)
		}
		e.Run(t, "relay", "members", "add", bob.PubHex, "--role", "nosuchrole", "--config", cfg, "--json")
	})

	t.Run("relay serve config errors", func(t *testing.T) {
		e.Run(t, "relay", "--json").ExpectErr(t, "usage")
		e.Run(t, "relay", "--config", filepath.Join(e.Dir, "nope.yaml"), "--json").ExpectErr(t, "usage")
		bad := e.WriteFile("bad-relay.yaml", "port: notaport\n")
		if res := e.Run(t, "relay", "--config", bad, "--json"); res.Code == 0 {
			t.Errorf("relay started on an invalid config")
		}
		e.Run(t, "relay", "-c", "x", "--config", cfg, "--json").ExpectErr(t, "usage")
		e.Run(t, "relay", "-c", "../escape", "--json").ExpectErr(t, "invalid_input")
	})
}

func TestSpaceAndHuddle(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{"huddle": map[string]any{"enabled": true}})
	e := NewEnv(t)
	alice := A(t, "alice")

	e.MustOK(t, "space", "create", "standup", "--summary", "Daily", "--relay", r.URL, "--identity", alice.Nsec, "--json")

	t.Run("list and show", func(t *testing.T) {
		var l struct {
			Spaces []map[string]any `json:"spaces"`
		}
		e.MustOK(t, "space", "list", "--relay", r.URL, "--json").JSON(t, &l)
		if len(l.Spaces) != 1 || l.Spaces[0]["identifier"] != "standup" || l.Spaces[0]["service"] != r.URL {
			t.Errorf("list: %v", l.Spaces)
		}
		e.MustOK(t, "space", "show", "standup", "--relay", r.URL, "--json").JSON(t, &l)
		if len(l.Spaces) != 1 {
			t.Errorf("show: %v", l.Spaces)
		}
		e.MustOK(t, "space", "show", "nosuch", "--relay", r.URL, "--json").JSON(t, &l)
		if len(l.Spaces) != 0 {
			t.Errorf("show of a missing space: %v", l.Spaces)
		}
	})

	t.Run("bad space ids", func(t *testing.T) {
		for _, id := range []string{"a/b", "a?b", "a#b", "a%b"} {
			e.Run(t, "space", "create", id, "--relay", r.URL, "--identity", alice.Nsec, "--json").ExpectErr(t, "invalid_input")
		}
		e.Run(t, "space", "create", "x", "--relay", r.URL, "--json").ExpectErr(t, "usage")
	})

	t.Run("huddle list", func(t *testing.T) {
		var h struct {
			Rooms []map[string]any `json:"rooms"`
		}
		e.MustOK(t, "huddle", "list", "--relay", r.URL, "--json").JSON(t, &h)
		if h.Rooms == nil {
			t.Errorf("rooms is null, want []")
		}
		off := StartRelay(t, "", nil)
		e.Run(t, "huddle", "list", "--relay", off.URL, "--json").ExpectErr(t, "unsupported")
		e.Run(t, "huddle", "list", "--relay", DeadRelayURL(t), "--json").ExpectErr(t, "network")
	})

	t.Run("join needs a terminal", func(t *testing.T) {
		e.Run(t, "space", "join", "standup", "--relay", r.URL, "--json").ExpectErr(t, "usage")
	})
}
