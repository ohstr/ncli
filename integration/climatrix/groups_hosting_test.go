package climatrix

import (
	"strings"
	"testing"
)

// The groups block's hosting options on a real relay: a groups.admins key
// moderates a group it holds no role in, a codeless join to a closed group
// waits for an admin (pendingJoins), and members read the moderation log
// (moderationLog) and the described roles (kind 39003).
func TestGroupsHostingConfig(t *testing.T) {
	needsRelay(t)
	alice, bob, carol, eve := A(t, "alice"), A(t, "bob"), A(t, "carol"), A(t, "eve")
	r := StartRelay(t, "", map[string]any{
		"groups": map[string]any{
			"roles": map[string]any{
				"admin":     []int{9000, 9001, 9002, 9005, 9008, 9009, 9010},
				"moderator": []int{9010},
			},
			"roleDescriptions": map[string]any{"moderator": "keeps the peace"},
			"admins":           []string{bob.Npub},
			"moderationLog":    true,
			"pendingJoins":     true,
		},
	})
	e := NewEnv(t)

	if res := groupsCLI(t, r, alice.Nsec, "create", "hosted"); res.Code != 0 {
		t.Fatalf("create: %s", res)
	}

	// pendingJoins: carol's codeless join waits instead of being refused.
	res := e.Run(t, "groups", "join", "hosted", "--relay", r.URL, "--identity", carol.Nsec)
	if res.Code != 0 || !strings.Contains(res.Stdout, "pending") {
		t.Fatalf("pending join: %s", res)
	}
	if members(t, r, "hosted")[carol.PubHex] {
		t.Fatal("a pending requester became a member")
	}

	// groups.admins: bob moderates alice's group without a role in it, and
	// accepting carol clears her request.
	if res := groupsCLI(t, r, bob.Nsec, "members", "add", "hosted", carol.PubHex); res.Code != 0 {
		t.Fatalf("bob (a groups.admins key) put-user: %s", res)
	}
	if !members(t, r, "hosted")[carol.PubHex] {
		t.Fatal("carol isn't a member after bob added her")
	}
	if evs := findEvents(t, e, "--kinds", "9021", "-s", r.URL, "--auth-identity", alice.Nsec); len(evs) != 0 {
		t.Errorf("a pending join survived its acceptance: %d", len(evs))
	}

	// An outsider is still refused.
	groupsCLI(t, r, eve.Nsec, "members", "add", "hosted", eve.PubHex).expectErr(t, "auth", true)

	// moderationLog: a member reads the put-user; an outsider doesn't.
	logged := findEvents(t, e, "--kinds", "9000", "-s", r.URL, "--auth-identity", carol.Nsec)
	if len(logged) == 0 {
		t.Error("a member sees no moderation log")
	}
	if evs := findEvents(t, e, "--kinds", "9000", "-s", r.URL); len(evs) != 0 {
		t.Errorf("an anonymous reader saw %d log entries", len(evs))
	}

	// roleDescriptions: the group's kind 39003 carries them.
	roles := findEvents(t, e, "--kinds", "39003", "-s", r.URL, "--auth-identity", alice.Nsec)
	if len(roles) != 1 {
		t.Fatalf("kind 39003 events: %d, want 1", len(roles))
	}
	found := false
	for _, tag := range roles[0].Tags {
		if len(tag) >= 3 && tag[0] == "role" && tag[1] == "moderator" && tag[2] == "keeps the peace" {
			found = true
		}
	}
	if !found {
		t.Errorf("39003 tags %v lack the moderator description", roles[0].Tags)
	}
}
