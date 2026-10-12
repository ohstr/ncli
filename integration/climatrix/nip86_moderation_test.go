package climatrix

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// rpc calls a NIP-86 method on r as priv and returns the decoded result,
// failing on an HTTP or RPC error.
func rpc(t *testing.T, r *Relay, priv, method string, params ...any) any {
	t.Helper()
	if params == nil {
		params = []any{}
	}
	body, _ := json.Marshal(map[string]any{"method": method, "params": params})
	url := r.httpURL("/")
	auth := nip98{priv: priv, url: url, method: http.MethodPost, body: body}.header(t)
	code, out := do(t, http.MethodPost, url, auth, body, "Content-Type", "application/nostr+json+rpc")
	if code != http.StatusOK {
		t.Fatalf("%s: HTTP %d %s", method, code, out)
	}
	var resp struct {
		Result any    `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("%s: %v: %s", method, err, out)
	}
	if resp.Error != "" {
		t.Fatalf("%s: %s", method, resp.Error)
	}
	return resp.Result
}

// tryPublish publishes a fresh note of kind as priv and reports the
// relay's answer.
func tryPublish(t *testing.T, e *Env, r *Relay, a Actor, kind string) Result {
	t.Helper()
	draft := e.MustOK(t, "event", "new", "--kind", kind, "--content", "hi", "--pubkey", a.Npub)
	signed := e.RunStdin(t, draft.Stdout, "id", "sign", "-e", "-", "-o", "-", "--identity", a.Nsec)
	if signed.Code != 0 {
		t.Fatalf("sign: %s", signed)
	}
	return e.RunStdin(t, signed.Stdout, "publish", "-e", "-", "-s", r.URL, "--json")
}

// NIP-86 moderation on a relay without membership: bans, kind rules, the
// relay's name, IP blocks, and a ban surviving a restart.
func TestNip86Moderation(t *testing.T) {
	needsRelay(t)
	operator, alice, bob := A(t, "operator"), A(t, "alice"), A(t, "bob")
	r := StartRelay(t, "", map[string]any{
		"nip11": map[string]any{"privkey": operator.PrivHex},
		"nip86": map[string]any{"enabled": true, "admins": []string{alice.PubHex}},
	})
	e := NewEnv(t)

	if res := tryPublish(t, e, r, bob, "1"); res.Code != 0 {
		t.Fatalf("bob before the ban: %s", res)
	}

	rpc(t, r, alice.PrivHex, "banpubkey", bob.PubHex, "spam")
	if res := tryPublish(t, e, r, bob, "1"); res.Code == 0 || !strings.Contains(res.Stdout, "blocked: pubkey is banned") {
		t.Errorf("banned bob published: %s", res)
	}
	banned, _ := json.Marshal(rpc(t, r, alice.PrivHex, "listbannedpubkeys"))
	if !strings.Contains(string(banned), bob.PubHex) {
		t.Errorf("listbannedpubkeys: %s", banned)
	}

	rpc(t, r, alice.PrivHex, "disallowkind", 7)
	if res := tryPublish(t, e, r, alice, "7"); res.Code == 0 || !strings.Contains(res.Stdout, "kind 7 is not allowed") {
		t.Errorf("disallowed kind published: %s", res)
	}
	if res := tryPublish(t, e, r, alice, "1"); res.Code != 0 {
		t.Errorf("an allowed kind was refused: %s", res)
	}

	rpc(t, r, alice.PrivHex, "changerelayname", "Community")
	rpc(t, r, alice.PrivHex, "changerelayicon", "https://relay.example/icon.png")
	code, doc := do(t, http.MethodGet, r.httpURL("/"), "", nil, "Accept", "application/nostr+json")
	if code != http.StatusOK || !strings.Contains(doc, `"name":"Community"`) || !strings.Contains(doc, `"icon":"https://relay.example/icon.png"`) {
		t.Errorf("NIP-11 after rename: %d %s", code, doc)
	}

	t.Run("a blocked IP is refused but NIP-86 still answers", func(t *testing.T) {
		rpc(t, r, alice.PrivHex, "blockip", "127.0.0.1")
		rpc(t, r, alice.PrivHex, "blockip", "::1")
		if res := tryPublish(t, e, r, alice, "1"); res.Code == 0 {
			t.Errorf("published from a blocked IP: %s", res)
		}
		rpc(t, r, alice.PrivHex, "unblockip", "127.0.0.1")
		rpc(t, r, alice.PrivHex, "unblockip", "::1")
		if res := tryPublish(t, e, r, alice, "1"); res.Code != 0 {
			t.Errorf("refused after unblock: %s", res)
		}
	})

	t.Run("only admins may call", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"method": "banpubkey", "params": []any{alice.PubHex}})
		url := r.httpURL("/")
		auth := nip98{priv: bob.PrivHex, url: url, method: http.MethodPost, body: body}.header(t)
		if code, _ := do(t, http.MethodPost, url, auth, body, "Content-Type", "application/nostr+json+rpc"); code == http.StatusOK {
			t.Error("a non-admin called banpubkey")
		}
	})

	// Last: Restart ties the new process's cleanup to this subtest.
	t.Run("bans survive a restart", func(t *testing.T) {
		r.Restart(t)
		if res := tryPublish(t, e, r, alice, "1"); res.Code != 0 {
			t.Fatalf("relay not serving after restart: %s\n%s", res, r.Log())
		}
		if res := tryPublish(t, e, r, bob, "1"); res.Code == 0 {
			t.Errorf("bob published after a restart: %s", res)
		}
	})
}
