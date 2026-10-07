package climatrix

import (
	"slices"
	"testing"
)

// NIP-43 changes reach connections that are already open, the relay
// publishes its kind:13534 member list, and NIP-42's prefixes say which
// refusal is which.
func TestNIP43JoinAndMemberList(t *testing.T) {
	needsRelay(t)
	r := membershipRelay(t)
	alice, bob, op := A(t, "alice"), A(t, "bob"), A(t, "operator")
	e := NewEnv(t)
	e.MustOK(t, "relay", "members", "add", alice.PubHex, "--role", "admin", "--config", r.ConfigPath, "--json")

	t.Run("unauthenticated read is auth-required", func(t *testing.T) {
		if _, closed := Dial(t, r.URL).Req(F{"kinds": []int{1}}); !hasPrefix(closed, "auth-required:") {
			t.Errorf("anonymous REQ closed %q, want auth-required:", closed)
		}
	})

	t.Run("a join reaches the open connection", func(t *testing.T) {
		c := DialAs(t, r.URL, bob.PrivHex)
		if _, closed := c.Req(F{"kinds": []int{1}}); !hasPrefix(closed, "restricted:") {
			t.Fatalf("non-member REQ closed %q, want restricted:", closed)
		}
		addMember(t, r, bob.PubHex)
		if _, closed := c.Req(F{"kinds": []int{1}}); closed != "" {
			t.Errorf("after joining, the same connection is still refused: %q", closed)
		}
		if ok, msg := c.Publish(Ev(t, bob.PrivHex, 1, "in now")); !ok {
			t.Errorf("after joining, the same connection can't publish: %s", msg)
		}
	})

	memberList := func(t *testing.T) map[string][]string {
		t.Helper()
		evs, closed := DialAs(t, r.URL, alice.PrivHex).Req(F{"kinds": []int{13534}, "authors": []string{op.PubHex}})
		if closed != "" || len(evs) != 1 {
			t.Fatalf("kind:13534 by the relay: %d events, closed %q", len(evs), closed)
		}
		out := map[string][]string{}
		for _, tag := range evs[0].Tags {
			if len(tag) >= 2 && tag[0] == "member" {
				out[tag[1]] = tag[2:]
			}
		}
		return out
	}

	t.Run("the member list is published, with roles", func(t *testing.T) {
		list := memberList(t)
		if !slices.Contains(list[alice.PubHex], "admin") {
			t.Errorf("alice's roles in kind:13534: %v", list[alice.PubHex])
		}
		if _, ok := list[bob.PubHex]; !ok {
			t.Errorf("bob missing from kind:13534: %v", list)
		}
	})

	t.Run("a removal updates the list", func(t *testing.T) {
		e.MustOK(t, "relay", "members", "remove", bob.PubHex, "--config", r.ConfigPath, "--json")
		if _, ok := memberList(t)[bob.PubHex]; ok {
			t.Error("removed member still in kind:13534")
		}
	})
}
