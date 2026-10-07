package climatrix

import (
	"strings"
	"testing"
)

// groupsCLI runs a groups command against r as priv ("" for anonymous).
func groupsCLI(t *testing.T, r *Relay, priv string, args ...string) Result {
	t.Helper()
	full := append([]string{"groups"}, args...)
	full = append(full, "--relay", r.URL, "--json")
	if priv != "" {
		full = append(full, "--identity", priv)
	}
	return NewEnv(t).Run(t, full...)
}

func TestGroupsCLI(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	alice, bob, eve := A(t, "alice"), A(t, "bob"), A(t, "eve")

	if res := groupsCLI(t, r, alice.Nsec, "create", "team"); res.Code != 0 {
		t.Fatalf("create: %s", res)
	}

	t.Run("show json uses snake_case keys", func(t *testing.T) {
		res := groupsCLI(t, r, alice.Nsec, "show", "team")
		var d struct {
			Metadata map[string]any   `json:"metadata"`
			Admins   []map[string]any `json:"admins"`
			Members  []string         `json:"members"`
		}
		res.JSON(t, &d)
		if len(d.Admins) != 1 {
			t.Fatalf("admins: %v", d.Admins)
		}
		if d.Admins[0]["pubkey"] != alice.PubHex {
			t.Errorf("admins[0] = %v, want key \"pubkey\"", d.Admins[0])
		}
		if _, ok := d.Admins[0]["roles"]; !ok {
			t.Errorf("admins[0] = %v, want key \"roles\"", d.Admins[0])
		}
		if d.Metadata["private"] != true || d.Metadata["closed"] != true {
			t.Errorf("new group not private+closed by default: %v", d.Metadata)
		}
	})

	t.Run("re-creating an existing id is no takeover", func(t *testing.T) {
		res := groupsCLI(t, r, bob.Nsec, "create", "team")
		// The id is taken: conflict, with the per-relay report on stdout.
		res.expectErr(t, "conflict", true)
		if again := groupsCLI(t, r, alice.Nsec, "create", "team"); again.Code != 0 {
			t.Errorf("owner's own retry of create failed\n%s", again)
		}
		var d struct {
			Admins []map[string]any `json:"admins"`
		}
		groupsCLI(t, r, alice.Nsec, "show", "team").JSON(t, &d)
		for _, a := range d.Admins {
			if a["pubkey"] == bob.PubHex {
				t.Fatalf("bob became an admin of alice's group by re-creating its id")
			}
		}
		groupsCLI(t, r, bob.Nsec, "edit", "team", "--name", "pwned").ExpectErr(t, "auth")
	})

	t.Run("writes need an identity", func(t *testing.T) {
		groupsCLI(t, r, "", "create", "x").ExpectErr(t, "usage")
	})

	t.Run("unusual group ids", func(t *testing.T) {
		for _, id := range []string{"has space", "UPPER", strings.Repeat("a", 200)} {
			res := groupsCLI(t, r, alice.Nsec, "create", id)
			t.Logf("group id %q (len %d): exit %d", id[:min(len(id), 20)], len(id), res.Code)
		}
	})

	t.Run("show of a private group without membership is auth", func(t *testing.T) {
		groupsCLI(t, r, eve.Nsec, "show", "team").ExpectErr(t, "auth")
		// An anonymous read can't see the relay's restricted CLOSED (nmilat's
		// ReadEventsFromRelay treats it as EOSE), so it reports an empty
		// success instead of auth.
		anon := groupsCLI(t, r, "", "show", "team")
		known(t, "anon-restricted-read-looks-empty", anon.Code == 0 && strings.TrimSpace(anon.Stdout) == "{}",
			"anonymous show of a private group: exit 0, {}")
	})

	t.Run("show of a missing group looks the same as a private one", func(t *testing.T) {
		missing := groupsCLI(t, r, eve.Nsec, "show", "nosuchgroup")
		missing.ExpectErr(t, "auth")
	})

	t.Run("list hides private groups from outsiders", func(t *testing.T) {
		var l struct {
			Groups []map[string]any `json:"groups"`
		}
		groupsCLI(t, r, eve.Nsec, "list").JSON(t, &l)
		for _, g := range l.Groups {
			if g["id"] == "team" {
				t.Errorf("private group listed to a non-member")
			}
		}
		groupsCLI(t, r, alice.Nsec, "list").JSON(t, &l)
		found := false
		for _, g := range l.Groups {
			found = found || g["id"] == "team"
		}
		if !found {
			t.Errorf("admin can't list own private group")
		}
	})

	t.Run("relay down is network", func(t *testing.T) {
		dead := &Relay{URL: DeadRelayURL(t)}
		groupsCLI(t, dead, alice.Nsec, "show", "team").ExpectErr(t, "network")
	})
}
