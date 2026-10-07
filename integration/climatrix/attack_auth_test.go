package climatrix

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip42"
)

// Attack tests: each subtest tries to get around a relay gate and
// asserts it holds. A failure here is a security finding, not a flake.

func authRelay(t *testing.T) *Relay {
	return StartRelay(t, "", map[string]any{
		"nip11": map[string]any{"limitation": map[string]any{"auth_required": true}},
	})
}

func membershipRelay(t *testing.T) *Relay {
	return StartRelay(t, "", map[string]any{
		"nip11":      map[string]any{"limitation": map[string]any{"auth_required": true, "membership_required": true}},
		"membership": map[string]any{"enabled": true},
	})
}

func addMember(t *testing.T, r *Relay, pub string) {
	t.Helper()
	NewEnv(t).MustOK(t, "relay", "members", "add", pub, "--config", r.ConfigPath, "--json")
}

func TestAttack_NIP42Auth(t *testing.T) {
	needsRelay(t)
	r := authRelay(t)
	eve := A(t, "eve")

	signedAuth := func(t *testing.T, challenge, relay string, createdAt int64) *nip01.Event {
		ev := nip42.NewAuthEvent(challenge, relay)
		if createdAt != 0 {
			ev.CreatedAt = uint64(createdAt)
		}
		if err := ev.Sign(eve.PrivHex); err != nil {
			t.Fatal(err)
		}
		return ev
	}
	rejected := func(t *testing.T, c *Raw, ev *nip01.Event, what string) {
		t.Helper()
		if ok, msg := c.AuthEvent(ev); ok {
			t.Errorf("%s accepted as AUTH (%s)", what, msg)
		}
		// Still unauthenticated: a write must be refused.
		if ok, msg := c.Publish(Ev(t, eve.PrivHex, 1, "after bad auth")); ok {
			t.Errorf("after %s, publish accepted: %s", what, msg)
		}
	}

	t.Run("anonymous write and read refused", func(t *testing.T) {
		c := Dial(t, r.URL)
		ok, msg := c.Publish(Ev(t, eve.PrivHex, 1, "anon"))
		if ok {
			t.Fatalf("anon publish accepted on an auth_required relay")
		}
		// NIP-42: an unauthenticated client gets "auth-required:" so it
		// knows to AUTH; "restricted:" means "authed, still not allowed".
		if !hasPrefix(msg, "auth-required:") {
			t.Errorf("anon EVENT: %q, want auth-required:", msg)
		}
		evs, closed := c.Req(F{"kinds": []int{1}})
		if len(evs) != 0 || !hasPrefix(closed, "auth-required:") {
			t.Errorf("anon REQ: %d events, closed %q", len(evs), closed)
		}
		// Seed so a leaked count would show as non-zero.
		seed := DialAs(t, r.URL, eve.PrivHex)
		for i := range 3 {
			seed.Publish(Ev(t, eve.PrivHex, 1, "seed "+strconv.Itoa(i)))
		}
		// COUNT is a read: same gate as REQ, refused with CLOSED.
		for _, f := range []F{{"kinds": []int{1}}, {"authors": []string{eve.PubHex}}} {
			n, closed := c.Count(f)
			if n > 0 || !hasPrefix(closed, "auth-required:") {
				t.Errorf("anon COUNT %v: n=%d closed %q", f, n, closed)
			}
		}
	})
	t.Run("wrong challenge", func(t *testing.T) {
		c := Dial(t, r.URL)
		c.Challenge()
		rejected(t, c, signedAuth(t, "not-the-challenge", r.URL, 0), "wrong challenge")
	})
	t.Run("wrong relay tag", func(t *testing.T) {
		c := Dial(t, r.URL)
		rejected(t, c, signedAuth(t, c.Challenge(), "ws://evil.test", 0), "wrong relay tag")
	})
	t.Run("relay tag with trailing slash", func(t *testing.T) {
		c := Dial(t, r.URL)
		ok, _ := c.AuthEvent(signedAuth(t, c.Challenge(), r.URL+"/", 0))
		t.Logf("trailing-slash relay tag accepted=%v (NIP-42 says URL normalization is up to the relay)", ok)
	})
	t.Run("stale created_at", func(t *testing.T) {
		c := Dial(t, r.URL)
		rejected(t, c, signedAuth(t, c.Challenge(), r.URL, time.Now().Add(-20*time.Minute).Unix()), "stale AUTH")
	})
	t.Run("future created_at", func(t *testing.T) {
		c := Dial(t, r.URL)
		rejected(t, c, signedAuth(t, c.Challenge(), r.URL, time.Now().Add(20*time.Minute).Unix()), "future AUTH")
	})
	t.Run("bad signature", func(t *testing.T) {
		c := Dial(t, r.URL)
		ev := signedAuth(t, c.Challenge(), r.URL, 0)
		ev.Sig = strings.Repeat("1", 128)
		rejected(t, c, ev, "bad-sig AUTH")
	})
	t.Run("id does not match content", func(t *testing.T) {
		c := Dial(t, r.URL)
		ev := signedAuth(t, c.Challenge(), r.URL, 0)
		ev.PubKey = A(t, "alice").PubHex // claim to be alice, keep eve's sig
		rejected(t, c, ev, "pubkey-swapped AUTH")
	})
	t.Run("wrong kind", func(t *testing.T) {
		c := Dial(t, r.URL)
		ev := nip01.NewEvent(1, "", []string{"relay", r.URL}, []string{"challenge", c.Challenge()})
		_ = ev.Sign(eve.PrivHex)
		rejected(t, c, ev, "kind-1 AUTH")
	})
	t.Run("replayed from another connection", func(t *testing.T) {
		a := Dial(t, r.URL)
		ev := signedAuth(t, a.Challenge(), r.URL, 0)
		if ok, msg := a.AuthEvent(ev); !ok {
			t.Fatalf("legit AUTH rejected: %s", msg)
		}
		b := Dial(t, r.URL)
		b.Challenge()
		rejected(t, b, ev, "replayed AUTH")
	})
	t.Run("AUTH event published as a normal event is not stored", func(t *testing.T) {
		c := DialAs(t, r.URL, eve.PrivHex)
		ev := signedAuth(t, "x", r.URL, 0)
		c.Publish(ev)
		evs, _ := c.Req(F{"kinds": []int{nip42.KindClientAuth}})
		if len(evs) != 0 {
			t.Errorf("kind 22242 AUTH events are stored and served (%d)", len(evs))
		}
	})
	t.Run("authenticated write works", func(t *testing.T) {
		c := DialAs(t, r.URL, eve.PrivHex)
		if ok, msg := c.Publish(Ev(t, eve.PrivHex, 1, "hello")); !ok {
			t.Errorf("authed publish refused: %s", msg)
		}
	})
}

func TestAttack_NIP43Membership(t *testing.T) {
	needsRelay(t)
	r := membershipRelay(t)
	alice, eve, op := A(t, "alice"), A(t, "eve"), A(t, "operator")
	addMember(t, r, alice.PubHex)

	member := DialAs(t, r.URL, alice.PrivHex)
	if ok, msg := member.Publish(Ev(t, alice.PrivHex, 1, "member post")); !ok {
		t.Fatalf("member publish refused: %s", msg)
	}

	t.Run("non-member write, read, count refused", func(t *testing.T) {
		c := DialAs(t, r.URL, eve.PrivHex)
		if ok, msg := c.Publish(Ev(t, eve.PrivHex, 1, "let me in")); ok || !hasPrefix(msg, "restricted:", "blocked:") {
			t.Errorf("non-member publish: ok=%v %q", ok, msg)
		}
		if evs, closed := c.Req(F{"kinds": []int{1}}); len(evs) != 0 || closed == "" {
			t.Errorf("non-member REQ: %d events, closed %q", len(evs), closed)
		}
		// NIP-45: a refused COUNT is answered with CLOSED, not silence.
		n, closed := c.Count(F{"kinds": []int{1}})
		if n > 0 || !hasPrefix(closed, "restricted:") {
			t.Errorf("non-member COUNT: n=%d closed %q", n, closed)
		}
		if evs, closed := c.Req(F{"authors": []string{alice.PubHex}}); len(evs) != 0 {
			t.Errorf("non-member read alice's events by author: %d (closed %q)", len(evs), closed)
		}
	})

	t.Run("non-member smuggles own event through a member's connection", func(t *testing.T) {
		smuggled := Ev(t, eve.PrivHex, 1, "smuggled via alice")
		ok, msg := member.Publish(smuggled)
		evs, _ := member.Req(F{"ids": []string{smuggled.ID}})
		t.Logf("member conn relaying a non-member's event: ok=%v %q stored=%d", ok, msg, len(evs))
		if ok && len(evs) == 1 {
			t.Errorf("a non-member's event got onto a membership-required relay by riding a member's connection")
		}
	})

	t.Run("non-member republishes a member's event", func(t *testing.T) {
		c := DialAs(t, r.URL, eve.PrivHex)
		ev := Ev(t, alice.PrivHex, 1, "alice wrote this")
		ok, msg := c.Publish(ev)
		t.Logf("rebroadcast by non-member: ok=%v %q (decision pending: see plan)", ok, msg)
	})

	t.Run("forged NIP-43 add-user does not grant membership", func(t *testing.T) {
		c := DialAs(t, r.URL, eve.PrivHex)
		forged := Ev(t, eve.PrivHex, 8000, "", []string{"-"}, []string{"p", eve.PubHex})
		c.Publish(forged)
		list := Ev(t, eve.PrivHex, 13534, "", []string{"-"}, []string{"member", eve.PubHex})
		c.Publish(list)
		// Fresh connection, so any cached membership is re-evaluated.
		c2 := DialAs(t, r.URL, eve.PrivHex)
		if ok, msg := c2.Publish(Ev(t, eve.PrivHex, 1, "am i in")); ok {
			t.Errorf("eve became a member via a self-signed 8000/13534: %s", msg)
		}
		NewEnv(t).Run(t, "relay", "members", "show", eve.PubHex, "--config", r.ConfigPath, "--json").ExpectErr(t, "not_found")
	})

	t.Run("forged membership list signed by someone else is never served as the relay's", func(t *testing.T) {
		c := DialAs(t, r.URL, alice.PrivHex)
		evs, _ := c.Req(F{"kinds": []int{13534}})
		for _, ev := range evs {
			if ev.PubKey != op.PubHex {
				t.Errorf("kind 13534 served from non-relay key %s", ev.PubKey[:8])
			}
		}
	})

	t.Run("join request with no or bad claim is refused", func(t *testing.T) {
		c := DialAs(t, r.URL, eve.PrivHex)
		for _, claim := range []string{"", "nonexistent-code", strings.Repeat("A", 5000)} {
			join := Ev(t, eve.PrivHex, 28934, "", []string{"-"}, []string{"claim", claim})
			ok, msg := c.Publish(join)
			if ok {
				c2 := DialAs(t, r.URL, eve.PrivHex)
				if pok, _ := c2.Publish(Ev(t, eve.PrivHex, 1, "joined?")); pok {
					t.Errorf("claim %q (len %d) made eve a member: %s", claim[:min(len(claim), 16)], len(claim), msg)
				}
			}
		}
	})

	t.Run("join request for someone else (NIP-70 protected) refused", func(t *testing.T) {
		code := createInvite(t, r, 5, "")
		c := DialAs(t, r.URL, eve.PrivHex)
		// mallory's join request, sent over eve's authenticated connection.
		mallory := A(t, "mallory")
		join := Ev(t, mallory.PrivHex, 28934, "", []string{"-"}, []string{"claim", code})
		ok, msg := c.Publish(join)
		if ok {
			c2 := DialAs(t, r.URL, mallory.PrivHex)
			if pok, _ := c2.Publish(Ev(t, mallory.PrivHex, 1, "in?")); pok {
				t.Errorf("a protected join request was accepted from a connection not authed as its author: %s", msg)
			}
		}
	})

	t.Run("single-use invite cannot be reused", func(t *testing.T) {
		code := createInvite(t, r, 1, "")
		bob, carol := A(t, "bob"), A(t, "carol")
		if !joinWith(t, r, bob.PrivHex, code) {
			t.Fatalf("first claim of a fresh invite failed")
		}
		if joinWith(t, r, carol.PrivHex, code) {
			t.Errorf("single-use invite claimed twice")
		}
	})

	t.Run("revoked invite cannot be claimed", func(t *testing.T) {
		code := createInvite(t, r, 5, "")
		NewEnv(t).MustOK(t, "relay", "invites", "revoke", code, "--config", r.ConfigPath, "--json")
		if joinWith(t, r, A(t, "agent").PrivHex, code) {
			t.Errorf("revoked invite still works")
		}
	})

	t.Run("expired invite cannot be claimed", func(t *testing.T) {
		code := createInvite(t, r, 5, "1s")
		time.Sleep(2500 * time.Millisecond)
		if joinWith(t, r, A(t, "admin").PrivHex, code) {
			t.Errorf("expired invite still works")
		}
	})

	t.Run("removed member loses access, including open subscriptions", func(t *testing.T) {
		mallory := A(t, "mallory")
		addMember(t, r, mallory.PubHex)
		c := DialAs(t, r.URL, mallory.PrivHex)
		live := c.Live(F{"kinds": []int{1}})
		if live.Closed != "" {
			t.Fatalf("member live sub refused: %s", live.Closed)
		}
		NewEnv(t).MustOK(t, "relay", "members", "remove", mallory.PubHex, "--config", r.ConfigPath, "--json")

		after := Ev(t, alice.PrivHex, 1, "posted after mallory was removed")
		if ok, msg := member.Publish(after); !ok {
			t.Fatalf("alice publish: %s", msg)
		}
		if live.Saw(after.ID, 2*time.Second) {
			t.Errorf("removed member's open subscription still receives new events")
		}
		if !strings.HasPrefix(live.Closed, "restricted:") {
			t.Errorf("open subscription closed with %q, want a restricted: CLOSED", live.Closed)
		}
		if ok, msg := c.Publish(Ev(t, mallory.PrivHex, 1, "still here")); ok {
			t.Errorf("removed member can still publish on the connection it had: %s", msg)
		}
		c2 := DialAs(t, r.URL, mallory.PrivHex)
		if evs, closed := c2.Req(F{"kinds": []int{1}}); len(evs) > 0 || closed == "" {
			t.Errorf("removed member can still read on a new connection: %d", len(evs))
		}
	})
}

// createInvite issues a NIP-43 invite through the admin CLI.
func createInvite(t *testing.T, r *Relay, maxUses int, ttl string) string {
	t.Helper()
	args := []string{"relay", "invites", "create", "--config", r.ConfigPath, "--json", "--max-uses", strconv.Itoa(maxUses)}
	if ttl != "" {
		args = append(args, "--ttl", ttl)
	}
	var inv struct{ Code string }
	NewEnv(t).MustOK(t, args...).JSON(t, &inv)
	if inv.Code == "" {
		t.Fatal("no invite code")
	}
	return inv.Code
}

// joinWith claims code as priv and reports whether priv can then post.
func joinWith(t *testing.T, r *Relay, priv, code string) bool {
	t.Helper()
	c := DialAs(t, r.URL, priv)
	pub := Ev(t, priv, 1, "x").PubKey
	join := Ev(t, priv, 28934, "", []string{"-"}, []string{"claim", code})
	c.Publish(join)
	time.Sleep(200 * time.Millisecond)
	c2 := DialAs(t, r.URL, priv)
	ok, _ := c2.Publish(Ev(t, priv, 1, "member now? "+pub[:6]))
	return ok
}
