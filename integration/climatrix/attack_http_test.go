package climatrix

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
)

// nip98 builds an Authorization header by hand, so tests can get every
// field wrong on purpose.
type nip98 struct {
	priv, url, method string
	body              []byte
	noPayload         bool
	createdAt         time.Time
	kind              int
}

func (n nip98) header(t *testing.T) string {
	t.Helper()
	kind := n.kind
	if kind == 0 {
		kind = 27235
	}
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	tags := [][]string{{"u", n.url}, {"method", n.method}, {"nonce", hex.EncodeToString(nonce[:])}}
	if !n.noPayload {
		sum := sha256.Sum256(n.body)
		tags = append(tags, []string{"payload", hex.EncodeToString(sum[:])})
	}
	ev := nip01.NewEvent(kind, "", tags...)
	if !n.createdAt.IsZero() {
		ev.CreatedAt = uint64(n.createdAt.Unix())
	}
	if err := ev.Sign(n.priv); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(ev)
	return "Nostr " + base64.StdEncoding.EncodeToString(b)
}

func (r *Relay) httpURL(path string) string {
	return fmt.Sprintf("http://localhost:%d%s", r.Port, path)
}

// do sends one HTTP request and returns status and body.
func do(t *testing.T, method, url, auth string, body []byte, hdr ...string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestAttack_AdminHTTP(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{"membership": map[string]any{"enabled": true}})
	op, eve := A(t, "operator"), A(t, "eve")
	invites := r.httpURL("/admin/membership/invites")
	members := r.httpURL("/admin/membership/members")
	stats := r.httpURL("/admin/worker/stats")
	body := []byte(`{"max_uses":1}`)

	ok := func(t *testing.T, code int, resp string) {
		t.Helper()
		if code >= 300 {
			t.Fatalf("legit admin request failed: %d %s", code, resp)
		}
	}
	denied := func(t *testing.T, what string, code int, resp string) {
		t.Helper()
		if code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 401/403 (%s)", what, code, strings.TrimSpace(resp))
		}
	}

	c0, b0 := do(t, "POST", invites, nip98{priv: op.PrivHex, url: invites, method: "POST", body: body}.header(t), body)
	ok(t, c0, b0)

	t.Run("no header", func(t *testing.T) {
		c, b := do(t, "GET", stats, "", nil)
		denied(t, "no auth", c, b)
	})
	t.Run("garbage header", func(t *testing.T) {
		for _, h := range []string{"Nostr", "Nostr !!!", "Bearer x", "Nostr " + base64.StdEncoding.EncodeToString([]byte("{}"))} {
			c, b := do(t, "GET", stats, h, nil)
			denied(t, "garbage "+h[:min(len(h), 10)], c, b)
		}
	})
	t.Run("non-admin key", func(t *testing.T) {
		c, b := do(t, "POST", invites, nip98{priv: eve.PrivHex, url: invites, method: "POST", body: body}.header(t), body)
		denied(t, "eve", c, b)
	})
	t.Run("signed for another endpoint", func(t *testing.T) {
		c, b := do(t, "POST", invites, nip98{priv: op.PrivHex, url: stats, method: "POST", body: body}.header(t), body)
		denied(t, "wrong u", c, b)
	})
	t.Run("signed for another method", func(t *testing.T) {
		c, b := do(t, "POST", invites, nip98{priv: op.PrivHex, url: invites, method: "GET", body: body}.header(t), body)
		denied(t, "wrong method", c, b)
	})
	t.Run("stale and future", func(t *testing.T) {
		for _, at := range []time.Time{time.Now().Add(-3 * time.Minute), time.Now().Add(3 * time.Minute)} {
			c, b := do(t, "POST", invites, nip98{priv: op.PrivHex, url: invites, method: "POST", body: body, createdAt: at}.header(t), body)
			denied(t, "time "+at.Format(time.Kitchen), c, b)
		}
	})
	t.Run("wrong kind", func(t *testing.T) {
		c, b := do(t, "POST", invites, nip98{priv: op.PrivHex, url: invites, method: "POST", body: body, kind: 1}.header(t), body)
		denied(t, "kind 1", c, b)
	})
	t.Run("payload for another body", func(t *testing.T) {
		h := nip98{priv: op.PrivHex, url: invites, method: "POST", body: body}.header(t)
		c, b := do(t, "POST", invites, h, []byte(`{"max_uses":0}`))
		denied(t, "swapped body", c, b)
	})
	t.Run("query string not covered by signature", func(t *testing.T) {
		h := nip98{priv: op.PrivHex, url: stats, method: "GET"}.header(t)
		c, b := do(t, "GET", stats+"?x=1", h, nil)
		denied(t, "extra query", c, b)
	})

	// A header with no payload tag binds nothing about the body: whoever
	// sees it can attach any body to the same URL and method for 60s.
	t.Run("captured header without payload tag reused with another body", func(t *testing.T) {
		h := nip98{priv: op.PrivHex, url: members, method: "POST", noPayload: true}.header(t)
		evil := []byte(`{"pubkey":"` + eve.PubHex + `"}`)
		c, b := do(t, "POST", members, h, evil)
		if c < 300 {
			t.Errorf("payload-less admin header accepted with an attacker's body: %d %s", c, b)
		}
	})

	// Within its 60s window an exact replay of a captured request is
	// accepted again -- for invites/create that hands the replayer a fresh
	// invite code.
	t.Run("exact replay of a captured request", func(t *testing.T) {
		h := nip98{priv: op.PrivHex, url: invites, method: "POST", body: body}.header(t)
		c1, b1 := do(t, "POST", invites, h, body)
		ok(t, c1, b1)
		c2, b2 := do(t, "POST", invites, h, body)
		if c2 < 300 {
			t.Errorf("replayed admin request accepted: %d %s", c2, b2)
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		big := bytes.Repeat([]byte("a"), 8<<20)
		c, _ := do(t, "POST", invites, nip98{priv: op.PrivHex, url: invites, method: "POST", body: big}.header(t), big)
		if c < 400 {
			t.Errorf("8 MiB admin body accepted: %d", c)
		}
	})

	t.Run("CLI admin calls in quick succession are not replays", func(t *testing.T) {
		e := NewEnv(t)
		for range 3 {
			e.MustOK(t, "relay", "invites", "create", "--max-uses", "1", "--config", r.ConfigPath, "--json")
		}
	})
	t.Run("CLI as non-admin is auth", func(t *testing.T) {
		cfg := r.AdminConfig(t, eve.PrivHex)
		NewEnv(t).Run(t, "relay", "stats", "--config", cfg, "--json").ExpectErr(t, "auth")
		NewEnv(t).Run(t, "relay", "members", "add", eve.PubHex, "--config", cfg, "--json").ExpectErr(t, "auth")
	})
	t.Run("CLI against a stopped relay is network", func(t *testing.T) {
		r2 := StartRelay(t, "", nil)
		r2.Stop()
		NewEnv(t).Run(t, "relay", "stats", "--config", r2.ConfigPath, "--json").ExpectErr(t, "network")
	})
	t.Run("membership routes with membership off are usage", func(t *testing.T) {
		r2 := StartRelay(t, "", nil)
		NewEnv(t).Run(t, "relay", "members", "list", "--config", r2.ConfigPath, "--json").ExpectErr(t, "usage")
	})
}
