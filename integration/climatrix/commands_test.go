package climatrix

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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

// TestRelayJSONLogs: the server's own logging honors --json once it starts.
func TestRelayJSONLogs(t *testing.T) {
	needsRelay(t)
	r := StartRelayArgs(t, "", nil, "--json")
	r.Seed(t)
	NewEnv(t).MustOK(t, "find", "-s", r.URL, "--kinds", "1", "--json")
	Result{Args: []string{"relay", "--json"}, Stderr: r.Log()}.ExpectCleanJSONStderr(t)
}

func TestRelayAdmin(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{"membership": map[string]any{"enabled": true}, "cache": zapsIndex})
	r.Seed(t)
	e := NewEnv(t)
	cfg := r.ConfigPath
	bob := A(t, "bob")

	t.Run("status", func(t *testing.T) {
		var s struct {
			Metrics struct {
				V      map[string]float64 `json:"v"`
				Totals map[string]float64 `json:"totals"`
			}
			Indexes []struct{ Name string }
		}
		e.MustOK(t, "relay", "status", "--config", cfg, "--json").JSON(t, &s)
		if _, ok := s.Metrics.V["uptime_seconds"]; !ok {
			t.Errorf("status metrics lack uptime: %v", s.Metrics.V)
		}
		if s.Metrics.Totals["events_total:accepted"] == 0 {
			t.Errorf("status shows no accepted events after seeding")
		}
		if len(s.Indexes) != 3 {
			t.Errorf("status indexes: %+v", s.Indexes)
		}
		if out := e.MustOK(t, "relay", "status", "--config", cfg).Stdout; !strings.Contains(out, "Connections") {
			t.Errorf("text status:\n%s", out)
		}
	})

	t.Run("index jobs", func(t *testing.T) {
		out := e.MustOK(t, "relay", "index", "rebuild", "zaps", "--follow", "--config", cfg, "--json").Stdout
		lines := strings.Split(strings.TrimSpace(out), "\n")
		var last struct{ ID, State string }
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil || last.State != "succeeded" {
			t.Fatalf("rebuild --follow last line %q: %v", lines[len(lines)-1], err)
		}
		var shown struct{ ID, State, Kind string }
		e.MustOK(t, "relay", "jobs", "show", last.ID, "--config", cfg, "--json").JSON(t, &shown)
		if shown.ID != last.ID || shown.Kind != "rebuild" {
			t.Errorf("jobs show: %+v", shown)
		}
		// Cancelling a finished job reports it unchanged.
		var cancelled struct{ ID, State string }
		e.MustOK(t, "relay", "jobs", "cancel", last.ID, "--config", cfg, "--json").JSON(t, &cancelled)
		if cancelled.State != "succeeded" {
			t.Errorf("cancel of a finished job: %+v", cancelled)
		}
		var list struct{ Jobs []struct{ ID string } }
		e.MustOK(t, "relay", "jobs", "list", "--config", cfg, "--json").JSON(t, &list)
		if len(list.Jobs) == 0 || list.Jobs[0].ID != last.ID {
			t.Errorf("jobs list: %+v", list.Jobs)
		}
		var dry struct {
			DryRun []map[string]any `json:"dry_run"`
		}
		e.MustOK(t, "relay", "index", "rebuild", "all", "--dry-run", "--config", cfg, "--json").JSON(t, &dry)
		// stats is off here: no profile stats or search.
		if len(dry.DryRun) != 1 || dry.DryRun[0]["index"] != "zaps" {
			t.Errorf("dry run over available indexes: %+v", dry.DryRun)
		}

		e.Run(t, "relay", "index", "drop", "zaps", "--config", cfg, "--json").ExpectErr(t, "usage")
		var dropped struct{ State string }
		e.MustOK(t, "relay", "index", "drop", "zaps", "--yes", "--config", cfg, "--json").JSON(t, &dropped)
		if dropped.State != "succeeded" {
			t.Errorf("drop zaps: %q", dropped.State)
		}

		// Search isn't configured on this relay: a feature that's off is
		// usage, not a "started" that silently fails in the background.
		e.Run(t, "relay", "index", "rebuild", "search", "--config", cfg, "--json").ExpectErr(t, "usage")
		e.Run(t, "relay", "index", "drop", "search", "--yes", "--config", cfg, "--json").ExpectErr(t, "usage")
		e.Run(t, "relay", "index", "rebuild", "nope", "--config", cfg, "--json").ExpectErr(t, "not_found")
		rep := e.Run(t, "relay", "index", "rebuild", "search", "--reverify", "--config", cfg, "--json").ExpectErr(t, "usage")
		if !strings.Contains(rep.Error, "relay verify") {
			t.Errorf("--reverify: %q lacks the replacement", rep.Error)
		}
		e.Run(t, "relay", "jobs", "show", "deadbeef", "--config", cfg, "--json").ExpectErr(t, "not_found")
		e.Run(t, "relay", "status", "--config", r.AdminConfig(t, bob.PrivHex), "--json").ExpectErr(t, "auth")
	})

	t.Run("verify", func(t *testing.T) {
		var out struct {
			Profiles []struct {
				Pubkey string
				Nip05  struct{ Status string }
				Stats  map[string]any
			}
		}
		e.MustOK(t, "relay", "verify", bob.PubHex, "--config", cfg, "--json").JSON(t, &out)
		// Profile stats are off here, so there are none to report.
		if len(out.Profiles) != 1 || out.Profiles[0].Pubkey != bob.PubHex || out.Profiles[0].Stats != nil {
			t.Errorf("verify: %+v", out)
		}
		// No stored kind 0 to check.
		e.Run(t, "relay", "verify", bob.PubHex, "--refresh", "--config", cfg, "--json").ExpectErr(t, "not_found")
		e.Run(t, "relay", "verify", "not-a-key", "--config", cfg, "--json").ExpectErr(t, "invalid_input")
	})

	t.Run("removed commands point to their replacements", func(t *testing.T) {
		for old, hint := range map[string]string{"stats": "relay status", "reindex": "index rebuild", "clear": "index drop"} {
			rep := e.Run(t, "relay", old, "--config", cfg, "--json").ExpectErr(t, "usage")
			if !strings.Contains(rep.Error, hint) {
				t.Errorf("relay %s: %q lacks %q", old, rep.Error, hint)
			}
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
		if er := e.Run(t, "space", "show", "nosuch", "--relay", r.URL, "--json").ExpectErr(t, "not_found"); er.Input != "nosuch" {
			t.Errorf("show of a missing space: input = %q, want nosuch", er.Input)
		}
		if res := e.Run(t, "space", "show", "nosuch", "--relay", r.URL); res.Code != exitCodes["not_found"] || res.Stdout != "" {
			t.Errorf("text show of a missing space: %s", res)
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

	t.Run("headless join and chat", func(t *testing.T) {
		space := "30312:" + alice.PubHex + ":standup"
		bob, carol := A(t, "bob"), A(t, "carol")

		joins := make(chan Result, 2)
		go func() {
			joins <- e.Run(t, "space", "join", space, "--relay", r.URL, "--identity", alice.Nsec, "--duration", "8s", "--json")
		}()
		time.Sleep(time.Second)
		go func() {
			joins <- e.Run(t, "space", "join", space, "--relay", r.URL, "--identity", bob.Nsec, "--duration", "4s", "--json")
		}()
		time.Sleep(2 * time.Second)

		var sent struct {
			Results []struct {
				ID       string `json:"id"`
				Accepted bool   `json:"accepted"`
			} `json:"results"`
		}
		e.MustOK(t, "space", "chat", "send", space, "--relay", r.URL, "hello room", "--identity", carol.Nsec, "--json").JSON(t, &sent)
		if len(sent.Results) != 1 || !sent.Results[0].Accepted {
			t.Fatalf("chat send: %+v", sent)
		}

		var streams []Result
		for range 2 {
			res := <-joins
			if res.Code != 0 {
				t.Fatalf("headless join failed\n%s", res)
			}
			res.ExpectCleanJSONStderr(t)
			streams = append(streams, res)
		}
		// alice's join runs longest, so it saw bob arrive and leave.
		aliceStream := streams[0]
		if !strings.Contains(aliceStream.Stdout, `"self":"`+alice.PubHex+`"`) {
			aliceStream = streams[1]
		}
		var types []string
		var chat map[string]any
		for _, line := range strings.Split(strings.TrimSpace(aliceStream.Stdout), "\n") {
			var ev map[string]any
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				t.Fatalf("stream line is not JSON: %q", line)
			}
			types = append(types, ev["type"].(string))
			if ev["type"] == "participant_joined" && ev["pubkey"] != bob.PubHex {
				t.Errorf("participant_joined for %v, want bob", ev["pubkey"])
			}
			if ev["type"] == "chat" {
				chat = ev
			}
		}
		want := []string{"joined", "participant_joined", "participant_left", "ended"}
		for _, w := range want {
			if !slices.Contains(types, w) {
				t.Errorf("stream lacks %q: %v", w, types)
			}
		}
		if types[0] != "joined" || types[len(types)-1] != "ended" {
			t.Errorf("stream order: %v", types)
		}
		if chat == nil || chat["content"] != "hello room" || chat["id"] != sent.Results[0].ID {
			t.Errorf("chat event = %v", chat)
		}

		var list struct {
			Activity string           `json:"activity"`
			Messages []map[string]any `json:"messages"`
		}
		res := e.MustOK(t, "space", "chat", "list", space, "--relay", r.URL, "--json")
		res.ExpectJSONArray(t, "messages")
		res.JSON(t, &list)
		if list.Activity != space || len(list.Messages) != 1 || list.Messages[0]["content"] != "hello room" {
			t.Errorf("chat list: %+v", list)
		}

		e.MustOK(t, "space", "chat", "send", space, "--relay", r.URL, "re: hello", "--reply", sent.Results[0].ID, "--identity", bob.Nsec, "--json")
		e.MustOK(t, "space", "chat", "list", space, "--relay", r.URL, "--json").JSON(t, &list)
		if len(list.Messages) != 2 || list.Messages[1]["parent"] != sent.Results[0].ID {
			t.Errorf("reply not threaded: %+v", list.Messages)
		}
	})

	t.Run("join and chat errors", func(t *testing.T) {
		e.Run(t, "space", "join", "standup", "--relay", r.URL, "--duration", "-1s", "--json").ExpectErr(t, "invalid_input")
		e.Run(t, "space", "join", "30312:"+alice.PubHex+":nosuch", "--relay", r.URL, "--identity", alice.Nsec, "--json").ExpectErr(t, "not_found")
		e.Run(t, "space", "chat", "send", "standup", "hi", "--relay", r.URL, "--identity", alice.Nsec, "--json").ExpectErr(t, "invalid_input")
		e.Run(t, "space", "chat", "send", "30312:"+alice.PubHex+":standup", "hi", "--relay", r.URL, "--reply", "nothex", "--identity", alice.Nsec, "--json").ExpectErr(t, "invalid_input")
		e.Run(t, "space", "chat", "list", "30312:"+alice.PubHex+":nosuch", "--relay", r.URL, "--json").ExpectErr(t, "not_found")
		e.Run(t, "space", "chat", "--json").ExpectErr(t, "usage")
		off := StartRelay(t, "", nil)
		e.MustOK(t, "space", "create", "quiet", "--relay", off.URL, "--identity", alice.Nsec, "--json")
		e.Run(t, "space", "join", "30312:"+alice.PubHex+":quiet", "--relay", off.URL, "--identity", alice.Nsec, "--json").ExpectErr(t, "unsupported")
	})
}
