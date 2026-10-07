package climatrix

import (
	"testing"

	"github.com/ohstr/nmilat/nip01"
)

// groupShape is one NIP-29 visibility/join-policy combination.
type groupShape struct {
	name            string
	private, closed bool
}

var groupShapes = []groupShape{
	{"public-open", false, false},
	{"public-closed", false, true},
	{"private-open", true, false},
	{"private-closed", true, true},
}

// setupGroup creates id as alice with shape, adds bob as a member, and
// has alice post one chat message into it. Returns that message.
func setupGroup(t *testing.T, r *Relay, id string, shape groupShape) *nip01.Event {
	t.Helper()
	alice, bob := A(t, "alice"), A(t, "bob")
	if res := groupsCLI(t, r, alice.Nsec, "create", id); res.Code != 0 {
		t.Fatalf("create %s: %s", id, res)
	}
	vis, join := "--public", "--open"
	if shape.private {
		vis = "--private"
	}
	if shape.closed {
		join = "--closed"
	}
	if res := groupsCLI(t, r, alice.Nsec, "edit", id, vis, join); res.Code != 0 {
		t.Fatalf("edit %s: %s", id, res)
	}
	if res := groupsCLI(t, r, alice.Nsec, "members", "add", id, bob.PubHex); res.Code != 0 {
		t.Fatalf("members add: %s", res)
	}
	msg := Ev(t, alice.PrivHex, 9, "secret from "+id, []string{"h", id})
	if ok, why := DialAs(t, r.URL, alice.PrivHex).Publish(msg); !ok {
		t.Fatalf("admin can't post into own group %s: %s", id, why)
	}
	return msg
}

// sees reports whether c receives msg via filter.
func sees(c *Raw, msg *nip01.Event, filter F) bool {
	evs, _ := c.Req(filter)
	for _, ev := range evs {
		if ev.ID == msg.ID {
			return true
		}
	}
	return false
}

func TestGroupsPrivacy(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	alice, bob, eve := A(t, "alice"), A(t, "bob"), A(t, "eve")

	for _, shape := range groupShapes {
		id := "g-" + shape.name
		msg := setupGroup(t, r, id, shape)

		t.Run(shape.name, func(t *testing.T) {
			readers := []struct {
				who    string
				conn   *Raw
				member bool
			}{
				{"admin", DialAs(t, r.URL, alice.PrivHex), true},
				{"member", DialAs(t, r.URL, bob.PrivHex), true},
				{"outsider", DialAs(t, r.URL, eve.PrivHex), false},
				{"anonymous", Dial(t, r.URL), false},
			}
			for _, rd := range readers {
				want := rd.member || !shape.private
				for _, f := range []struct {
					name   string
					filter F
				}{
					{"by #h", F{"#h": []string{id}}},
					{"by kind", F{"kinds": []int{9}}},
					{"by id", F{"ids": []string{msg.ID}}},
					{"by author", F{"authors": []string{alice.PubHex}, "kinds": []int{9}}},
				} {
					if got := sees(rd.conn, msg, f.filter); got != want {
						t.Errorf("%s reading %s: sees=%v, want %v", rd.who, f.name, got, want)
					}
				}
			}

			// Writing: members always post. NIP-29 gates non-member writes
			// with a separate "restricted" flag, which nmilat doesn't
			// support yet -- so today an outsider can post into every shape.
			// Logged, not asserted, until that's decided.
			post := Ev(t, bob.PrivHex, 9, "member posting into "+id, []string{"h", id})
			if ok, why := DialAs(t, r.URL, bob.PrivHex).Publish(post); !ok {
				t.Errorf("member posting refused: %s", why)
			}
			out := Ev(t, eve.PrivHex, 9, "outsider posting into "+id, []string{"h", id})
			ok, why := DialAs(t, r.URL, eve.PrivHex).Publish(out)
			t.Logf("outsider posting into %s: accepted=%v %s", shape.name, ok, why)
			if ok && shape.private && sees(DialAs(t, r.URL, eve.PrivHex), out, F{"ids": []string{out.ID}}) {
				t.Errorf("outsider can read back its own post in a private group")
			}

			// Group metadata follows the same visibility.
			meta := F{"kinds": []int{39000}, "#d": []string{id}}
			evs, _ := DialAs(t, r.URL, eve.PrivHex).Req(meta)
			if shape.private && len(evs) > 0 {
				t.Errorf("outsider read private group metadata")
			}
		})
	}
}
