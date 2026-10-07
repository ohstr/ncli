package climatrix

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
)

// TestBunkerHeadless: everything the bunker TUI does, driven with no
// terminal -- start, pair, approve/reject pending requests, grants, stop.
func TestBunkerHeadless(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)
	signer, app := A(t, "alice"), A(t, "carol")

	var st struct {
		Running     bool   `json:"running"`
		IdentityPub string `json:"identity_pub"`
	}
	e.MustOK(t, "bunker", "--identity", signer.Nsec, "--relay", r.URL, "--json").JSON(t, &st)
	t.Cleanup(func() { e.Run(t, "bunker", "stop", "--json") })
	if !st.Running || st.IdentityPub != signer.PubHex {
		t.Fatalf("headless start: %+v", st)
	}

	t.Run("start again reports the running daemon", func(t *testing.T) {
		e.MustOK(t, "bunker", "--json").JSON(t, &st)
		if st.IdentityPub != signer.PubHex {
			t.Errorf("second start: %+v", st)
		}
		e.Run(t, "bunker", "--identity", A(t, "bob").Nsec, "--json").ExpectErr(t, "conflict")
	})

	var pair struct {
		Result string `json:"result"`
	}
	e.MustOK(t, "bunker", "connect", "--json").JSON(t, &pair)
	u, err := url.Parse(pair.Result)
	if err != nil || u.Query().Get("secret") == "" {
		t.Fatalf("no bunker:// URI with a secret: %q", pair.Result)
	}

	c := Dial(t, r.URL)
	responses := c.Live(F{"kinds": []int{nip46.KindRequest}, "#p": []string{app.PubHex}})

	// call sends a request; decide (if non-nil) resolves it through
	// `bunker pending` once it shows up there.
	call := func(method string, params []string, decide func(id string)) *nip46.Response {
		t.Helper()
		done := make(chan *nip46.Response, 1)
		go func() { done <- nip46Call(t, c, responses, app.PrivHex, signer.PubHex, method, params) }()
		if decide != nil {
			deadline := time.Now().Add(15 * time.Second)
			for id := ""; id == ""; {
				if time.Now().After(deadline) {
					t.Fatalf("%s never showed up in bunker pending list", method)
				}
				var pending []struct {
					ID     string `json:"id"`
					Method string `json:"method"`
				}
				e.MustOK(t, "bunker", "pending", "list", "--json").JSON(t, &pending)
				for _, p := range pending {
					if p.Method == method {
						id = p.ID
					}
				}
				if id == "" {
					time.Sleep(100 * time.Millisecond)
				} else {
					decide(id)
				}
			}
		}
		select {
		case resp := <-done:
			return resp
		case <-time.After(25 * time.Second):
			t.Fatalf("%s: no answer", method)
			return nil
		}
	}
	approve := func(extra ...string) func(string) {
		return func(id string) {
			e.MustOK(t, append([]string{"bunker", "pending", "approve", id, "--json"}, extra...)...)
		}
	}
	signReq := func(kind int) []string {
		b, _ := json.Marshal(nip01.NewUnsignedEvent(kind, signer.PubHex, "please sign"))
		return []string{string(b)}
	}

	resp := call(nip46.MethodConnect, []string{signer.PubHex, u.Query().Get("secret")}, approve())
	if resp.Error != "" || resp.Result != "ack" {
		t.Fatalf("connect: result %q error %q", resp.Result, resp.Error)
	}
	e.MustOK(t, "bunker", "sessions", "grants", app.PubHex, "--json").ExpectJSONArray(t, "")

	t.Run("approve once, then always", func(t *testing.T) {
		if resp := call(nip46.MethodSignEvent, signReq(1), approve()); resp.Error != "" {
			t.Fatalf("approved once: %q", resp.Error)
		}
		if resp := call(nip46.MethodSignEvent, signReq(1), approve("--always", "--for", "1h")); resp.Error != "" {
			t.Fatalf("approved always: %q", resp.Error)
		}
		// Now covered by the remembered grant: no pending step.
		if resp := call(nip46.MethodSignEvent, signReq(1), nil); resp.Error != "" {
			t.Fatalf("granted kind 1: %q", resp.Error)
		}
	})

	t.Run("reject", func(t *testing.T) {
		resp := call(nip46.MethodSignEvent, signReq(7), func(id string) {
			e.MustOK(t, "bunker", "pending", "reject", id, "--json")
		})
		if resp.Error == "" || resp.Result != "" {
			t.Errorf("rejected sign_event answered: %q", resp.Result)
		}
	})

	t.Run("set-grant", func(t *testing.T) {
		spec := filepath.Join(t.TempDir(), "grants.yaml")
		if err := os.WriteFile(spec, []byte("kind: bunker\nspec:\n  grants:\n    - method: sign_event\n      kinds: [7]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		e.MustOK(t, "bunker", "sessions", "set-grant", app.PubHex, "--grants", spec, "--json")
		if resp := call(nip46.MethodSignEvent, signReq(7), nil); resp.Error != "" {
			t.Fatalf("kind 7 after set-grant: %q", resp.Error)
		}
		e.Run(t, "bunker", "sessions", "set-grant", A(t, "bob").PubHex, "--grants", spec, "--json").ExpectErr(t, "not_found")
	})

	t.Run("rename, revoke-grant, revoke", func(t *testing.T) {
		e.MustOK(t, "bunker", "sessions", "rename", app.PubHex, "carol-app", "--json")
		var sessions []struct {
			Pubkey   string `json:"pubkey"`
			Nickname string `json:"nickname"`
		}
		e.MustOK(t, "bunker", "sessions", "list", "--json").JSON(t, &sessions)
		if len(sessions) != 1 || sessions[0].Nickname != "carol-app" {
			t.Errorf("after rename: %+v", sessions)
		}
		e.MustOK(t, "bunker", "sessions", "revoke-grant", app.PubHex, "--method", "sign_event", "--kind", "7", "--json")
		e.Run(t, "bunker", "sessions", "revoke-grant", app.PubHex, "--method", "sign_event", "--kind", "7", "--json").ExpectErr(t, "not_found")
		e.MustOK(t, "bunker", "sessions", "revoke", app.PubHex, "--json")
		e.MustOK(t, "bunker", "sessions", "list", "--json").ExpectJSONArray(t, "")
	})

	t.Run("errors", func(t *testing.T) {
		e.MustOK(t, "bunker", "pending", "list", "--json").ExpectJSONArray(t, "")
		e.Run(t, "bunker", "pending", "approve", "nosuch", "--json").ExpectErr(t, "not_found")
		e.Run(t, "bunker", "pending", "approve", "nosuch", "--for", "1h", "--json").ExpectErr(t, "usage")
		e.Run(t, "bunker", "pending", "--json").ExpectErr(t, "usage")
		e.Run(t, "bunker", "attach", "--json").ExpectErr(t, "usage")
	})

	t.Run("stop", func(t *testing.T) {
		e.MustOK(t, "bunker", "stop", "--json")
		e.MustOK(t, "bunker", "status", "--json").JSON(t, &st)
		if st.Running {
			t.Errorf("still running after stop")
		}
		e.Run(t, "bunker", "pending", "list", "--json").ExpectErr(t, "not_found")
	})
}
