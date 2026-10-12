package climatrix

import "testing"

// groups.roles gives a configured role real power: a moderator may set
// pins (9010) but not remove members (9001), and the default relay still
// refuses both to a non-admin.
func TestGroupsRolesConfig(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{
		"groups": map[string]any{"roles": map[string]any{
			"admin":     []int{9000, 9001, 9002, 9005, 9008, 9009, 9010},
			"moderator": []int{9010},
		}},
	})
	alice, bob, carol := A(t, "alice"), A(t, "bob"), A(t, "carol")

	if res := groupsCLI(t, r, alice.Nsec, "create", "roles"); res.Code != 0 {
		t.Fatalf("create: %s", res)
	}
	if res := groupsCLI(t, r, alice.Nsec, "members", "add", "roles", carol.PubHex); res.Code != 0 {
		t.Fatalf("add carol: %s", res)
	}
	if res := groupsCLI(t, r, alice.Nsec, "members", "add", "roles", bob.PubHex, "moderator"); res.Code != 0 {
		t.Fatalf("add bob as moderator: %s", res)
	}

	if res := groupsCLI(t, r, bob.Nsec, "pins", "set", "roles"); res.Code != 0 {
		t.Errorf("moderator pins set refused: %s", res)
	}
	groupsCLI(t, r, bob.Nsec, "members", "remove", "roles", carol.PubHex).expectErr(t, "auth", true)

	t.Run("bad roles fail at startup", func(t *testing.T) {
		e := NewEnv(t)
		cfg := e.WriteFile("relay.yaml", "nip11:\n  privkey: "+A(t, "operator").PrivHex+"\nstore: "+e.Dir+"/r.db\ngroups:\n  roles:\n    moderator: [9010]\n")
		e.Run(t, "relay", "--config", cfg, "--json").ExpectErr(t, "invalid_input")
	})
}
