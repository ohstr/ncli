package climatrix

import (
	"testing"
)

// NIP-33 addressable events are looked up by their "d" tag. The relay's
// NIP-29 gate also reads "d" (group metadata kinds carry the group id
// there), and must not swallow every other kind's "#d" lookup.
func TestAddressableLookups(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	r.Seed(t)
	e := NewEnv(t)
	alice := A(t, "alice")

	t.Run("find by #d", func(t *testing.T) {
		art := firstOfKind(t, loadCorpus(t), 30023)
		var d string
		for _, tag := range art.Tags {
			if len(tag) > 1 && tag[0] == "d" {
				d = tag[1]
			}
		}
		evs := findEvents(t, e, "--kinds", "30023", "--tag", "d="+d, "-s", r.URL)
		if len(evs) != 1 || evs[0].ID != art.ID {
			t.Errorf("kind 30023 by #d=%s: %d events, want the article", d, len(evs))
		}
	})

	t.Run("space show finds a created space", func(t *testing.T) {
		e.MustOK(t, "space", "create", "standup", "--relay", r.URL, "--identity", alice.Nsec, "--json")
		var s struct {
			Spaces []map[string]any `json:"spaces"`
		}
		e.MustOK(t, "space", "show", "standup", "--relay", r.URL, "--json").JSON(t, &s)
		if len(s.Spaces) != 1 || s.Spaces[0]["identifier"] != "standup" {
			t.Errorf("space show standup: %v", s.Spaces)
		}
	})

	t.Run("a kind-less #d probe for a missing group is still refused", func(t *testing.T) {
		c := Dial(t, r.URL)
		if _, closed := c.Req(F{"#d": []string{"no-such-group"}}); !hasPrefix(closed, "restricted:", "auth-required:") {
			t.Errorf("kind-less #d probe: closed %q, want refused (no group existence oracle)", closed)
		}
		if _, closed := c.Req(F{"kinds": []int{39000}, "#d": []string{"no-such-group"}}); !hasPrefix(closed, "restricted:", "auth-required:") {
			t.Errorf("39000 #d probe: closed %q, want refused", closed)
		}
	})
}
