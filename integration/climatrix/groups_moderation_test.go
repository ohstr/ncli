package climatrix

import (
	"testing"
)

// members returns a group's member pubkeys as alice (its admin) sees them.
func members(t *testing.T, r *Relay, id string) map[string]bool {
	t.Helper()
	var d struct {
		Members []string `json:"members"`
	}
	groupsCLI(t, r, A(t, "alice").Nsec, "show", id).JSON(t, &d)
	out := map[string]bool{}
	for _, m := range d.Members {
		out[m] = true
	}
	return out
}

func TestGroupsModeration(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	alice, bob, eve, carol := A(t, "alice"), A(t, "bob"), A(t, "eve"), A(t, "carol")

	mustGroups := func(t *testing.T, priv string, args ...string) Result {
		t.Helper()
		res := groupsCLI(t, r, priv, args...)
		if res.Code != 0 {
			t.Fatalf("groups %v: %s", args, res)
		}
		return res
	}
	mustGroups(t, alice.Nsec, "create", "mod")
	mustGroups(t, alice.Nsec, "members", "add", "mod", bob.PubHex)

	t.Run("non-admin moderation is refused", func(t *testing.T) {
		for _, args := range [][]string{
			{"members", "add", "mod", eve.PubHex},
			{"members", "remove", "mod", bob.PubHex},
			{"edit", "mod", "--name", "x"},
			{"invite", "mod"},
			{"pins", "set", "mod"},
			{"delete", "mod"},
		} {
			// bob is a member, not an admin.
			groupsCLI(t, r, bob.Nsec, args...).expectErr(t, "auth", true)
		}
		if !members(t, r, "mod")[bob.PubHex] || members(t, r, "mod")[eve.PubHex] {
			t.Errorf("roster changed by refused moderation")
		}
	})

	t.Run("admin of another group can't moderate this one", func(t *testing.T) {
		mustGroups(t, eve.Nsec, "create", "eves")
		groupsCLI(t, r, eve.Nsec, "members", "add", "mod", eve.PubHex).expectErr(t, "auth", true)
		groupsCLI(t, r, eve.Nsec, "delete", "mod").expectErr(t, "auth", true)
	})

	t.Run("closed group: join needs a valid invite", func(t *testing.T) {
		res := groupsCLI(t, r, carol.Nsec, "join", "mod")
		if members(t, r, "mod")[carol.PubHex] {
			t.Fatalf("closed group admitted a join without an invite (exit %d)", res.Code)
		}
		groupsCLI(t, r, carol.Nsec, "join", "mod", "--invite-code", "wrong-code")
		if members(t, r, "mod")[carol.PubHex] {
			t.Fatalf("closed group admitted a join with a wrong invite code")
		}
		var inv struct{ Code string }
		mustGroups(t, alice.Nsec, "invite", "mod").JSON(t, &inv)
		mustGroups(t, carol.Nsec, "join", "mod", "--invite-code", inv.Code)
		if !members(t, r, "mod")[carol.PubHex] {
			t.Fatalf("valid invite code didn't admit carol")
		}
	})

	t.Run("open group: join is admitted", func(t *testing.T) {
		mustGroups(t, alice.Nsec, "create", "open1")
		mustGroups(t, alice.Nsec, "edit", "open1", "--open", "--public")
		mustGroups(t, eve.Nsec, "join", "open1", "--reason", "hi")
		if !members(t, r, "open1")[eve.PubHex] {
			t.Errorf("open group didn't admit a join")
		}
	})

	t.Run("leave removes membership", func(t *testing.T) {
		mustGroups(t, carol.Nsec, "leave", "mod", "--reason", "bye")
		if members(t, r, "mod")[carol.PubHex] {
			t.Errorf("carol still a member after leave")
		}
	})

	t.Run("admin removes a member, who then can't read", func(t *testing.T) {
		mustGroups(t, alice.Nsec, "members", "remove", "mod", bob.PubHex)
		if members(t, r, "mod")[bob.PubHex] {
			t.Fatalf("bob still listed after removal")
		}
		groupsCLI(t, r, bob.Nsec, "show", "mod").ExpectErr(t, "auth")
	})

	t.Run("pins and delete-event", func(t *testing.T) {
		msg := Ev(t, alice.PrivHex, 9, "pin me", []string{"h", "mod"})
		if ok, why := DialAs(t, r.URL, alice.PrivHex).Publish(msg); !ok {
			t.Fatalf("post: %s", why)
		}
		mustGroups(t, alice.Nsec, "pins", "set", "mod", "--event", msg.ID)
		mustGroups(t, alice.Nsec, "pins", "set", "mod")
		groupsCLI(t, r, eve.Nsec, "delete-event", "mod", msg.ID).expectErr(t, "auth", true)
		mustGroups(t, alice.Nsec, "delete-event", "mod", msg.ID)
		if sees(DialAs(t, r.URL, alice.PrivHex), msg, F{"ids": []string{msg.ID}}) {
			t.Errorf("deleted group event still served")
		}
	})

	t.Run("tree hides private groups from outsiders", func(t *testing.T) {
		mustGroups(t, alice.Nsec, "create", "child", "--parent", "mod")
		var tree struct {
			Roots []string                  `json:"roots"`
			Nodes map[string]map[string]any `json:"nodes"`
		}
		mustGroups(t, alice.Nsec, "tree").JSON(t, &tree)
		if _, ok := tree.Nodes["child"]; !ok {
			t.Errorf("admin's tree lacks child: %v", tree.Nodes)
		}
		var outside struct {
			Nodes map[string]map[string]any `json:"nodes"`
		}
		groupsCLI(t, r, eve.Nsec, "tree").JSON(t, &outside)
		for _, id := range []string{"mod", "child"} {
			if _, ok := outside.Nodes[id]; ok {
				t.Errorf("outsider's tree shows private group %s", id)
			}
		}
	})

	t.Run("delete", func(t *testing.T) {
		mustGroups(t, alice.Nsec, "delete", "open1")
		groupsCLI(t, r, eve.Nsec, "show", "open1").ExpectErr(t, "auth")
	})
}
