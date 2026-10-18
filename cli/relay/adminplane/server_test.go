package adminplane

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/relay/metrics"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/utils"
	"github.com/rs/zerolog"
)

const (
	adminPriv    = "0acd12cbf0fb87cd13b17bc9b57dffd11b3870b407984cec5a4ce2a69b90268c"
	strangerPriv = "7687e9cd85027cf2d446a4b834f67afa6f2ace3cff8ac6139731c3802f339f8e"
	testHost     = "admin.test"
)

type harness struct {
	t       *testing.T
	srv     *Server
	h       http.Handler
	token   string
	m       *metrics.Metrics
	adminMu sync.Mutex
	admins  []string
}

func newHarness(t *testing.T, mutate func(*Settings)) *harness {
	t.Helper()
	adminPub, err := utils.GetPublicKey(adminPriv)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "metrics.tokens")
	tok, err := AddToken(path, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := &Settings{
		Addr: "127.0.0.1:0", Loopback: true, Hosts: map[string]bool{testHost: true}, Dashboard: true, SessionTTL: time.Minute,
		Metrics: true, TokensFile: path, ScrapeTTL: time.Hour,
	}
	if mutate != nil {
		mutate(s)
	}
	m := metrics.New("test")
	h := &harness{t: t, token: tok, m: m, admins: []string{adminPub}}
	hist := metrics.NewHistory(m, time.Second, time.Minute)
	hist.Sample(time.Now())
	srv, err := New(Options{
		Settings: s,
		Admins: func() []string {
			h.adminMu.Lock()
			defer h.adminMu.Unlock()
			return append([]string(nil), h.admins...)
		},
		Metrics: m,
		History: hist,
		UI:      fstest.MapFS{"index.html": {Data: []byte("<!doctype html>ui")}, "assets/app-1.js": {Data: []byte("x")}},
		Logger:  zerolog.Nop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.srv, h.h = srv, srv.Handler()
	return h
}

type reqOpt func(*http.Request)

func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func withCookie(c *http.Cookie) reqOpt {
	return func(r *http.Request) { r.AddCookie(c) }
}
func fromAddr(addr string) reqOpt { return func(r *http.Request) { r.RemoteAddr = addr } }

func (h *harness) do(method, path string, body []byte, opts ...reqOpt) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest(method, "http://"+testHost+path, bytes.NewReader(body))
	r.RemoteAddr = "127.0.0.1:40000"
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, r)
	return w
}

func nostrAuth(t *testing.T, priv, method, path string, body []byte) reqOpt {
	t.Helper()
	header, err := common.GenerateNIP98Header(priv, "http://"+testHost+path, method, body)
	if err != nil {
		t.Fatal(err)
	}
	return withHeader("Authorization", header)
}

func (h *harness) login() (*http.Cookie, string) {
	h.t.Helper()
	w := h.do("POST", "/admin/api/login", nil, nostrAuth(h.t, adminPriv, "POST", "/admin/api/login", nil))
	if w.Code != http.StatusOK {
		h.t.Fatalf("login = %d %s", w.Code, w.Body)
	}
	var info sessionJSON
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		h.t.Fatal(err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		h.t.Fatalf("login set %d cookies", len(cookies))
	}
	return cookies[0], info.CSRF
}

func TestMetricsNeedsItsToken(t *testing.T) {
	h := newHarness(t, nil)
	cookie, _ := h.login()

	cases := map[string][]reqOpt{
		"no credential":   nil,
		"wrong token":     {withHeader("Authorization", "Bearer "+TokenPrefix+"nope")},
		"basic scheme":    {withHeader("Authorization", "Basic "+h.token)},
		"session cookie":  {withCookie(cookie)},
		"admin NIP-98":    {nostrAuth(t, adminPriv, "GET", "/metrics", nil)},
		"token in cookie": {withCookie(&http.Cookie{Name: sessionCookie, Value: h.token})},
	}
	for name, opts := range cases {
		if w := h.do("GET", "/metrics", nil, opts...); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: /metrics = %d, want 401", name, w.Code)
		}
	}

	w := h.do("GET", "/metrics", nil, withHeader("Authorization", "Bearer "+h.token))
	if w.Code != http.StatusOK {
		t.Fatalf("valid token: /metrics = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"ncli_relay_events_total", "ncli_relay_build_info", "go_goroutines", `ncli_relay_admin_denied_total{reason="token"}`} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %s", want)
		}
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("content type = %q", w.Header().Get("Content-Type"))
	}
}

func TestMetricsTokenDoesNotOpenTheAPI(t *testing.T) {
	h := newHarness(t, nil)
	for _, path := range []string{"/admin/api/metrics", "/admin/api/history", "/admin/api/session"} {
		if w := h.do("GET", path, nil, withHeader("Authorization", "Bearer "+h.token)); w.Code != http.StatusUnauthorized {
			t.Errorf("%s with a metrics token = %d, want 401", path, w.Code)
		}
	}
}

func TestAPIWithNIP98(t *testing.T) {
	h := newHarness(t, nil)

	if w := h.do("GET", "/admin/api/metrics", nil, nostrAuth(t, adminPriv, "GET", "/admin/api/metrics", nil)); w.Code != http.StatusOK {
		t.Fatalf("admin NIP-98 = %d %s", w.Code, w.Body)
	}
	if w := h.do("GET", "/admin/api/metrics", nil, nostrAuth(t, strangerPriv, "GET", "/admin/api/metrics", nil)); w.Code != http.StatusUnauthorized {
		t.Errorf("non-admin NIP-98 = %d, want 401", w.Code)
	}
	if w := h.do("GET", "/admin/api/metrics", nil, nostrAuth(t, adminPriv, "GET", "/admin/api/history", nil)); w.Code != http.StatusUnauthorized {
		t.Errorf("NIP-98 signed for another URL = %d, want 401", w.Code)
	}

	replayed := nostrAuth(t, adminPriv, "GET", "/admin/api/history", nil)
	if w := h.do("GET", "/admin/api/history", nil, replayed); w.Code != http.StatusOK {
		t.Fatalf("first use = %d", w.Code)
	}
	if w := h.do("GET", "/admin/api/history", nil, replayed); w.Code != http.StatusUnauthorized {
		t.Errorf("replayed NIP-98 = %d, want 401", w.Code)
	}
}

func TestAPIBindsTheBody(t *testing.T) {
	h := newHarness(t, nil)
	var got []byte
	h.srv.Handle("POST /admin/api/echo", func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})
	signed := nostrAuth(t, adminPriv, "POST", "/admin/api/echo", []byte(`{"a":1}`))
	if w := h.do("POST", "/admin/api/echo", []byte(`{"a":2}`), signed); w.Code != http.StatusUnauthorized {
		t.Errorf("tampered body = %d, want 401", w.Code)
	}
	signed = nostrAuth(t, adminPriv, "POST", "/admin/api/echo", []byte(`{"a":1}`))
	if w := h.do("POST", "/admin/api/echo", []byte(`{"a":1}`), signed); w.Code != http.StatusOK {
		t.Fatalf("signed body = %d", w.Code)
	}
	if string(got) != `{"a":1}` {
		t.Errorf("handler saw body %q", got)
	}
}

func TestDashboardSession(t *testing.T) {
	h := newHarness(t, nil)

	if w := h.do("POST", "/admin/api/login", nil, nostrAuth(t, strangerPriv, "POST", "/admin/api/login", nil)); w.Code != http.StatusUnauthorized {
		t.Fatalf("non-admin login = %d, want 401", w.Code)
	}

	cookie, csrf := h.login()
	if cookie.Name != sessionCookie || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatalf("cookie = %+v", cookie)
	}
	if csrf == "" {
		t.Fatal("login returned no CSRF token")
	}

	if w := h.do("GET", "/admin/api/metrics", nil, withCookie(cookie)); w.Code != http.StatusOK {
		t.Fatalf("session GET = %d", w.Code)
	}
	if w := h.do("GET", "/admin/api/session", nil, withCookie(cookie)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), csrf) {
		t.Fatalf("session info = %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", "/admin/api/logout", nil, withCookie(cookie)); w.Code != http.StatusUnauthorized {
		t.Errorf("POST without CSRF = %d, want 401", w.Code)
	}
	if w := h.do("POST", "/admin/api/logout", nil, withCookie(cookie), withHeader(csrfHeader, "wrong")); w.Code != http.StatusUnauthorized {
		t.Errorf("POST with wrong CSRF = %d, want 401", w.Code)
	}
	if w := h.do("POST", "/admin/api/logout", nil, withCookie(cookie), withHeader(csrfHeader, csrf)); w.Code != http.StatusOK {
		t.Fatalf("logout = %d", w.Code)
	}
	if w := h.do("GET", "/admin/api/metrics", nil, withCookie(cookie)); w.Code != http.StatusUnauthorized {
		t.Errorf("session after logout = %d, want 401", w.Code)
	}
}

func TestSessionEndsWhenAdminRemoved(t *testing.T) {
	h := newHarness(t, nil)
	cookie, _ := h.login()
	h.adminMu.Lock()
	h.admins = nil
	h.adminMu.Unlock()
	if w := h.do("GET", "/admin/api/metrics", nil, withCookie(cookie)); w.Code != http.StatusUnauthorized {
		t.Fatalf("removed admin's session = %d, want 401", w.Code)
	}
}

func TestSessionExpires(t *testing.T) {
	s := newSessions(time.Minute)
	now := time.Now()
	id, _ := s.create("pk", now)
	if _, ok := s.get(id, []string{"pk"}, now.Add(59*time.Second)); !ok {
		t.Fatal("session expired early")
	}
	if _, ok := s.get(id, []string{"pk"}, now.Add(61*time.Second)); ok {
		t.Fatal("session outlived its TTL")
	}
}

func TestDashboardOffClosesLoginAndUI(t *testing.T) {
	h := newHarness(t, func(s *Settings) { s.Dashboard = false })
	if w := h.do("POST", "/admin/api/login", nil, nostrAuth(t, adminPriv, "POST", "/admin/api/login", nil)); w.Code == http.StatusOK {
		t.Error("login served with the dashboard off")
	}
	if w := h.do("GET", "/admin/ui/", nil); w.Code != http.StatusNotFound {
		t.Errorf("UI with dashboard off = %d, want 404", w.Code)
	}
	if w := h.do("GET", "/admin/api/metrics", nil, withCookie(&http.Cookie{Name: sessionCookie, Value: "x"})); w.Code != http.StatusUnauthorized {
		t.Errorf("cookie with dashboard off = %d, want 401", w.Code)
	}
}

func TestUIHeaders(t *testing.T) {
	h := newHarness(t, nil)
	w := h.do("GET", "/admin/ui/", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ui") {
		t.Fatalf("UI = %d %s", w.Code, w.Body)
	}
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q missing %q", csp, want)
		}
	}
	for k, v := range map[string]string{"X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer"} {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS header set")
	}
	if w := h.do("GET", "/admin/ui/assets/app-1.js", nil); !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset cache = %q", w.Header().Get("Cache-Control"))
	}
	if w := h.do("GET", "/", nil); w.Code != http.StatusFound {
		t.Errorf("root = %d, want redirect", w.Code)
	}
}

func TestUIPlaceholderWithoutBuild(t *testing.T) {
	h := newHarness(t, nil)
	srv, err := New(Options{Settings: h.srv.opts.Settings, Admins: h.srv.opts.Admins, Metrics: h.m, History: h.srv.opts.History})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://"+testHost+"/admin/ui/", nil)
	r.RemoteAddr = "127.0.0.1:1"
	srv.Handler().ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "just ui") {
		t.Errorf("placeholder = %s", w.Body)
	}
}

func TestAllowCIDRs(t *testing.T) {
	h := newHarness(t, func(s *Settings) { s.AllowCIDRs = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")} })
	if w := h.do("GET", "/metrics", nil, withHeader("Authorization", "Bearer "+h.token)); w.Code != http.StatusForbidden {
		t.Errorf("outside allowlist = %d, want 403", w.Code)
	}
	if w := h.do("GET", "/metrics", nil, fromAddr("10.2.3.4:5000"), withHeader("Authorization", "Bearer "+h.token)); w.Code != http.StatusOK {
		t.Errorf("inside allowlist = %d, want 200", w.Code)
	}
}

func TestRepeatedFailuresAreLockedOut(t *testing.T) {
	h := newHarness(t, nil)
	bad := withHeader("Authorization", "Bearer "+TokenPrefix+"guess")
	for i := 0; i < failLimit; i++ {
		h.do("GET", "/metrics", nil, bad)
	}
	if w := h.do("GET", "/metrics", nil, withHeader("Authorization", "Bearer "+h.token)); w.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures = %d, want 429", failLimit, w.Code)
	}
	if w := h.do("GET", "/metrics", nil, fromAddr("127.0.0.2:1"), withHeader("Authorization", "Bearer "+h.token)); w.Code != http.StatusOK {
		t.Errorf("another address = %d, want 200", w.Code)
	}
}

func TestScrapeIsCached(t *testing.T) {
	h := newHarness(t, nil)
	auth := withHeader("Authorization", "Bearer "+h.token)
	first := h.do("GET", "/metrics", nil, auth).Body.String()
	h.m.Message(relay.MsgReq)
	second := h.do("GET", "/metrics", nil, auth).Body.String()
	if first != second {
		t.Error("second scrape within the TTL rendered again")
	}
}

func TestStreamDeliversPoints(t *testing.T) {
	h := newHarness(t, nil)
	ts := httptest.NewServer(h.h)
	defer ts.Close()
	h.srv.opts.Settings.Hosts[ts.Listener.Addr().String()] = true

	path := "/admin/api/stream"
	header, err := common.GenerateNIP98Header(adminPriv, ts.URL+path, "GET", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+path, nil)
	req.Header.Set("Authorization", header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	go h.srv.opts.History.Sample(time.Now())
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if line, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var p metrics.Point
			if err := json.Unmarshal([]byte(line), &p); err != nil {
				t.Fatalf("bad point %q: %v", line, err)
			}
			return
		}
	}
	t.Fatalf("stream ended without a point: %v", sc.Err())
}

// A NIP-98 header is bound to the URL the client claims to call, so the
// listener must refuse any Host but its own: otherwise a signature the admin
// gave another site ("log in" at evil.example) works here, as does a
// DNS-rebound page.
func TestForeignHostIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	const evil = "evil.example"
	body := []byte{}
	header, err := common.GenerateNIP98Header(adminPriv, "http://"+evil+"/admin/api/login", "POST", body)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin/api/login", "/admin/api/metrics", "/metrics", "/admin/ui/"} {
		r := httptest.NewRequest("POST", "http://"+evil+path, bytes.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1"
		r.Header.Set("Authorization", header)
		w := httptest.NewRecorder()
		h.h.ServeHTTP(w, r)
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("%s for Host %s = %d, want 421", path, evil, w.Code)
		}
		if len(w.Result().Cookies()) != 0 {
			t.Errorf("%s for a foreign Host set a cookie", path)
		}
	}
	if w := h.do("GET", "/admin/api/metrics", nil, withHeader("Authorization", "Bearer x")); w.Code == http.StatusMisdirectedRequest {
		t.Error("the listener's own host was refused")
	}
}
