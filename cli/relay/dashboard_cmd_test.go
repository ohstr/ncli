package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ohstr/ncli/cli/relay/adminplane"
)

const proxyAddr = "127.0.0.1:41234"

// newTestProxy fronts upstream (standing in for the admin listener).
func newTestProxy(t *testing.T, upstream http.Handler) *dashboardProxy {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	return &dashboardProxy{
		privKey: testPrivKey, base: up.URL, client: up.Client(), pubkey: "pk",
		addr: proxyAddr, token: "tok", session: "sess", csrf: "csrf",
		ui: http.StripPrefix("/admin/ui", adminplane.UIHandler(fstest.MapFS{"index.html": {Data: []byte("ui")}})),
	}
}

func proxyDo(p *dashboardProxy, method, target, host string, cookie bool, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+proxyAddr+target, strings.NewReader(""))
	r.Host = host
	if cookie {
		r.AddCookie(&http.Cookie{Name: localCookie, Value: "sess"})
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}

func TestDashboardProxyGuards(t *testing.T) {
	var sawAuth string
	p := newTestProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))

	if w := proxyDo(p, "GET", "/admin/api/metrics", "evil.example", true, nil); w.Code != http.StatusForbidden {
		t.Errorf("rebinding host = %d, want 403", w.Code)
	}
	if w := proxyDo(p, "GET", "/admin/api/metrics", proxyAddr, false, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("no cookie = %d, want 401", w.Code)
	}
	if w := proxyDo(p, "GET", "/?t=wrong", proxyAddr, false, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong link = %d, want 401", w.Code)
	}

	w := proxyDo(p, "GET", "/?t=tok", proxyAddr, false, nil)
	if w.Code != http.StatusFound {
		t.Fatalf("link = %d, want redirect", w.Code)
	}
	c := w.Result().Cookies()
	if len(c) != 1 || c[0].Value != "sess" || !c[0].HttpOnly || c[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("link cookie = %+v", c)
	}
	if w := proxyDo(p, "GET", "/?t=tok", proxyAddr, false, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("reused link = %d, want 401", w.Code)
	}

	if w := proxyDo(p, "GET", "/admin/api/session", proxyAddr, true, nil); !strings.Contains(w.Body.String(), `"csrf":"csrf"`) {
		t.Errorf("session = %s", w.Body)
	}
	for _, path := range []string{"/admin/api/login", "/admin/api/logout"} {
		if w := proxyDo(p, "POST", path, proxyAddr, true, map[string]string{"X-CSRF-Token": "csrf"}); w.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, w.Code)
		}
	}
	if w := proxyDo(p, "POST", "/admin/api/indexes/zaps/rebuild", proxyAddr, true, nil); w.Code != http.StatusForbidden {
		t.Errorf("POST without CSRF = %d, want 403", w.Code)
	}

	w = proxyDo(p, "GET", "/admin/api/metrics", proxyAddr, true, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ok") {
		t.Fatalf("forwarded GET = %d %s", w.Code, w.Body)
	}
	if !strings.HasPrefix(sawAuth, "Nostr ") {
		t.Errorf("upstream saw Authorization %q, want a NIP-98 header", sawAuth)
	}
	if w := proxyDo(p, "POST", "/admin/api/indexes/zaps/rebuild", proxyAddr, true, map[string]string{"X-CSRF-Token": "csrf"}); w.Code != http.StatusOK {
		t.Errorf("POST with CSRF = %d", w.Code)
	}
	if w := proxyDo(p, "GET", "/admin/ui/", proxyAddr, true, nil); !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Errorf("UI CSP = %q", w.Header().Get("Content-Security-Policy"))
	}
}
