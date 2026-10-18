package climatrix

import (
	"net/http"
	"testing"
)

// admin.pubkeys lets people sign in to the dashboard as themselves; the
// relay's own key keeps working for the CLI.
func TestAdminPlane_DedicatedPubkeys(t *testing.T) {
	needsRelay(t)
	bob, eve := A(t, "bob"), A(t, "eve")
	r := StartRelay(t, "", map[string]any{"admin": map[string]any{
		"pubkeys":   []string{bob.PubHex},
		"dashboard": map[string]any{"enabled": true},
	}})
	base := "http://" + r.AdminAddr
	login := base + "/admin/api/login"

	code, body, resp := adminReq(t, nil, "POST", login, "", map[string]string{"Authorization": nip98{priv: bob.PrivHex, url: login, method: "POST"}.header(t)}, nil)
	if code != http.StatusOK || sessionCookie(resp) == "" {
		t.Fatalf("listed key sign-in = %d %s", code, body)
	}
	if code, _, _ := adminReq(t, nil, "GET", base+"/admin/api/indexes", "", map[string]string{"Cookie": sessionCookie(resp)}, nil); code != http.StatusOK {
		t.Errorf("listed key's session on the API = %d", code)
	}
	if code, _, _ := adminReq(t, nil, "POST", login, "", map[string]string{"Authorization": nip98{priv: eve.PrivHex, url: login, method: "POST"}.header(t)}, nil); code != http.StatusUnauthorized {
		t.Errorf("unlisted key sign-in = %d, want 401", code)
	}

	// The relay key (what `ncli relay status` signs with) still works,
	// and so does a listed key from the CLI.
	NewEnv(t).MustOK(t, "relay", "status", "--config", r.ConfigPath, "--json")
	NewEnv(t).MustOK(t, "relay", "status", "--config", r.AdminConfig(t, bob.PrivHex), "--json")
	NewEnv(t).Run(t, "relay", "status", "--config", r.AdminConfig(t, eve.PrivHex), "--json").ExpectErr(t, "auth")
}
