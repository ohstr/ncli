package climatrix

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// adminReq sends one request to a relay's admin listener. host overrides the
// Host header when set.
func adminReq(t *testing.T, client *http.Client, method, rawURL, host string, hdr map[string]string, body []byte) (int, string, *http.Response) {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp
}

func addToken(t *testing.T, file, name string) string {
	t.Helper()
	var out struct{ Token string }
	NewEnv(t).MustOK(t, "relay", "metrics", "token", "add", name, "--file", file, "--json").JSON(t, &out)
	return out.Token
}

// A NIP-98 signature binds the URL the client says it called. The admin
// listener must refuse other hosts, or a header the admin signed for some
// other site ("log in with Nostr" at evil.example) would work here.
func TestAdminPlane_ForeignHostRefused(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{"admin": map[string]any{"dashboard": map[string]any{"enabled": true}}})
	op := A(t, "operator")
	base := "http://" + r.AdminAddr

	evilURL := "http://evil.example/admin/api/login"
	h := nip98{priv: op.PrivHex, url: evilURL, method: "POST"}.header(t)
	code, _, resp := adminReq(t, nil, "POST", base+"/admin/api/login", "evil.example", map[string]string{"Authorization": h}, nil)
	if code != http.StatusMisdirectedRequest || len(resp.Cookies()) != 0 {
		t.Fatalf("login for a foreign Host: %d, cookies %v; want 421 and none", code, resp.Cookies())
	}

	// The same signature made for this listener's own URL works.
	own := base + "/admin/api/login"
	h = nip98{priv: op.PrivHex, url: own, method: "POST"}.header(t)
	if code, body, _ := adminReq(t, nil, "POST", own, "", map[string]string{"Authorization": h}, nil); code != http.StatusOK {
		t.Fatalf("login for its own host: %d %s", code, body)
	}
}

func sessionCookie(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == "__Host-ncli_admin" {
			return c.Name + "=" + c.Value
		}
	}
	return ""
}

// The browser flow end to end: one NIP-98 login, then cookie + CSRF.
func TestAdminPlane_DashboardSession(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{"admin": map[string]any{"dashboard": map[string]any{"enabled": true, "sessionTTL": "2m"}}})
	op, eve := A(t, "operator"), A(t, "eve")
	base := "http://" + r.AdminAddr
	login := base + "/admin/api/login"

	code, _, _ := adminReq(t, nil, "POST", login, "", map[string]string{"Authorization": nip98{priv: eve.PrivHex, url: login, method: "POST"}.header(t)}, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("non-admin login = %d, want 401", code)
	}

	code, body, resp := adminReq(t, nil, "POST", login, "", map[string]string{"Authorization": nip98{priv: op.PrivHex, url: login, method: "POST"}.header(t)}, nil)
	if code != http.StatusOK {
		t.Fatalf("login = %d %s", code, body)
	}
	var sess struct {
		Pubkey    string
		ExpiresAt int64 `json:"expires_at"`
		CSRF      string
	}
	if err := json.Unmarshal([]byte(body), &sess); err != nil || sess.Pubkey != op.PubHex || sess.CSRF == "" {
		t.Fatalf("login body %s: %v", body, err)
	}
	if ttl := time.Until(time.Unix(sess.ExpiresAt, 0)); ttl > 2*time.Minute || ttl < time.Minute {
		t.Errorf("session TTL %v, want about the configured 2m", ttl)
	}
	raw := resp.Header.Get("Set-Cookie")
	for _, want := range []string{"__Host-ncli_admin=", "HttpOnly", "Secure", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(raw, want) {
			t.Errorf("Set-Cookie %q lacks %s", raw, want)
		}
	}
	cookie := sessionCookie(resp)

	withCookie := map[string]string{"Cookie": cookie}
	for _, path := range []string{"/admin/api/metrics", "/admin/api/history", "/admin/api/indexes", "/admin/api/jobs", "/admin/api/session"} {
		if code, body, _ := adminReq(t, nil, "GET", base+path, "", withCookie, nil); code != http.StatusOK {
			t.Errorf("GET %s with the session = %d %s", path, code, body)
		}
	}
	rebuild := base + "/admin/api/indexes/zaps/rebuild"
	if code, _, _ := adminReq(t, nil, "POST", rebuild, "", withCookie, []byte(`{}`)); code != http.StatusUnauthorized {
		t.Errorf("POST without CSRF = %d, want 401", code)
	}
	if code, body, _ := adminReq(t, nil, "POST", rebuild, "", map[string]string{"Cookie": cookie, "X-CSRF-Token": sess.CSRF, "Content-Type": "application/json"}, []byte(`{}`)); code != http.StatusAccepted && code != http.StatusOK {
		t.Errorf("POST with CSRF = %d %s", code, body)
	}
	// A session never opens /metrics.
	if code, _, _ := adminReq(t, nil, "GET", base+"/metrics", "", withCookie, nil); code != http.StatusNotFound && code != http.StatusUnauthorized {
		t.Errorf("/metrics with a session = %d", code)
	}
	if code, _, _ := adminReq(t, nil, "POST", base+"/admin/api/logout", "", map[string]string{"Cookie": cookie, "X-CSRF-Token": sess.CSRF}, nil); code != http.StatusOK {
		t.Fatalf("logout = %d", code)
	}
	if code, _, _ := adminReq(t, nil, "GET", base+"/admin/api/metrics", "", withCookie, nil); code != http.StatusUnauthorized {
		t.Errorf("session after logout = %d, want 401", code)
	}
}

// Tokens rotate on a live relay without a restart.
func TestAdminPlane_TokenRotation(t *testing.T) {
	needsRelay(t)
	tokens := filepath.Join(t.TempDir(), "metrics.tokens")
	first := addToken(t, tokens, "first")
	r := StartRelay(t, "", map[string]any{"admin": map[string]any{"metrics": map[string]any{"enabled": true, "tokensFile": tokens, "scrapeCacheTTL": "0s"}}})
	scrape := func(tok string) int {
		code, _, _ := adminReq(t, nil, "GET", "http://"+r.AdminAddr+"/metrics", "", map[string]string{"Authorization": "Bearer " + tok}, nil)
		return code
	}
	if c := scrape(first); c != http.StatusOK {
		t.Fatalf("first token = %d", c)
	}
	second := addToken(t, tokens, "second")
	NewEnv(t).MustOK(t, "relay", "metrics", "token", "remove", "first", "--file", tokens, "--json")
	deadline := time.Now().Add(5 * time.Second)
	for scrape(first) != http.StatusUnauthorized || scrape(second) != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatalf("after rotation: first=%d second=%d, want 401 and 200", scrape(first), scrape(second))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// writeCA creates a CA and a leaf it signs, for 127.0.0.1, returning PEM
// paths: ca cert, leaf cert, leaf key.
func writeCA(t *testing.T, dir, name string, client bool) (caPath, certPath, keyPath string, pair tls.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name + " CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	usage := x509.ExtKeyUsageServerAuth
	if client {
		usage = x509.ExtKeyUsageClientAuth
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
		ExtKeyUsage: []x509.ExtKeyUsage{usage}, KeyUsage: x509.KeyUsageDigitalSignature,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(leafKey)
	write := func(file, kind string, der []byte) string {
		p := filepath.Join(dir, file)
		if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	caPath = write(name+"-ca.pem", "CERTIFICATE", caDER)
	// The leaf file carries its chain, so trusting it alone is enough.
	certPEM := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...)
	certPath = filepath.Join(dir, name+".pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	keyPath = write(name+"-key.pem", "EC PRIVATE KEY", keyDER)
	pair, err = tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestAdminPlane_TLS(t *testing.T) {
	needsRelay(t)
	dir := t.TempDir()
	serverCA, cert, key, _ := writeCA(t, dir, "server", false)
	tokens := filepath.Join(dir, "metrics.tokens")
	token := addToken(t, tokens, "prom")
	r := StartRelay(t, "", map[string]any{"admin": map[string]any{
		"tls":     map[string]any{"cert": cert, "key": key},
		"metrics": map[string]any{"enabled": true, "tokensFile": tokens},
	}})

	// The CLI trusts admin.tls.cert from the config and talks https.
	e := NewEnv(t)
	var st struct{ Indexes []any }
	e.MustOK(t, "relay", "status", "--config", r.ConfigPath, "--json").JSON(t, &st)
	if len(st.Indexes) != 2 {
		t.Errorf("status over TLS: %+v", st)
	}

	pool := x509.NewCertPool()
	pem, _ := os.ReadFile(serverCA)
	pool.AppendCertsFromPEM(pem)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	if code, _, _ := adminReq(t, client, "GET", "https://"+r.AdminAddr+"/metrics", "", map[string]string{"Authorization": "Bearer " + token}, nil); code != http.StatusOK {
		t.Errorf("https /metrics = %d", code)
	}
	// Plain http to a TLS listener gets nowhere.
	if resp, err := http.Get("http://" + r.AdminAddr + "/metrics"); err == nil {
		if resp.StatusCode == http.StatusOK {
			t.Error("plain http answered 200 on a TLS listener")
		}
		_ = resp.Body.Close()
	}
}

func TestAdminPlane_MutualTLS(t *testing.T) {
	needsRelay(t)
	dir := t.TempDir()
	serverCA, cert, key, _ := writeCA(t, dir, "server", false)
	clientCA, _, _, clientPair := writeCA(t, dir, "client", true)
	tokens := filepath.Join(dir, "metrics.tokens")
	token := addToken(t, tokens, "prom")
	r := StartRelay(t, "", map[string]any{"admin": map[string]any{
		"tls":     map[string]any{"cert": cert, "key": key, "clientCA": clientCA},
		"metrics": map[string]any{"enabled": true, "tokensFile": tokens},
	}})
	pool := x509.NewCertPool()
	pem, _ := os.ReadFile(serverCA)
	pool.AppendCertsFromPEM(pem)
	url := "https://" + r.AdminAddr + "/metrics"
	auth := map[string]string{"Authorization": "Bearer " + token}

	noCert := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if resp, err := noCert.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("mTLS listener answered a client with no certificate: %d", resp.StatusCode)
	}

	withCert := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{clientPair}}}}
	if code, _, _ := adminReq(t, withCert, "GET", url, "", auth, nil); code != http.StatusOK {
		t.Errorf("mTLS client = %d, want 200", code)
	}
	// A certificate is not a credential: the token is still required.
	if code, _, _ := adminReq(t, withCert, "GET", url, "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("mTLS client without token = %d, want 401", code)
	}
}

func TestAdminPlane_AllowCIDRs(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", map[string]any{"admin": map[string]any{"allowCIDRs": []string{"10.0.0.0/8"}}})
	if code, _, _ := adminReq(t, nil, "GET", "http://"+r.AdminAddr+"/admin/api/metrics", "", nil, nil); code != http.StatusForbidden {
		t.Errorf("request from outside the allowlist = %d, want 403", code)
	}
	NewEnv(t).Run(t, "relay", "status", "--config", r.ConfigPath, "--json").ExpectErr(t, "auth")
}

// Every unsafe admin setup stops the relay before it binds anything.
func TestAdminPlane_StartupRefusals(t *testing.T) {
	needsRelay(t)
	dir := t.TempDir()
	loose := filepath.Join(dir, "loose.tokens")
	_ = os.WriteFile(loose, nil, 0o644)
	_ = os.Chmod(loose, 0o644)
	plain := filepath.Join(dir, "plain.tokens")
	_ = os.WriteFile(plain, []byte("ncli_mt_leaked\n"), 0o600)
	_, cert, key, _ := writeCA(t, dir, "server", false)

	cases := map[string]struct {
		admin map[string]any
		code  string
		want  string
	}{
		"public without TLS":     {map[string]any{"listen": "0.0.0.0:5999"}, "usage", "not loopback"},
		"relay port":             {map[string]any{"listen": "127.0.0.1:%PORT%"}, "usage", "own port"},
		"metrics without tokens": {map[string]any{"metrics": map[string]any{"enabled": true}}, "usage", "tokensFile"},
		"world-readable tokens":  {map[string]any{"metrics": map[string]any{"enabled": true, "tokensFile": loose}}, "invalid_input", "chmod 600"},
		"plaintext token":        {map[string]any{"metrics": map[string]any{"enabled": true, "tokensFile": plain}}, "invalid_input", "plaintext"},
		"wildcard without hosts": {map[string]any{"listen": "0.0.0.0:5999", "tls": map[string]any{"cert": cert, "key": key}}, "usage", "admin.hosts"},
		"bad session TTL":        {map[string]any{"dashboard": map[string]any{"enabled": true, "sessionTTL": "1w"}}, "invalid_input", "sessionTTL"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			port := allocPort(t)
			raw, _ := json.Marshal(c.admin)
			admin := strings.ReplaceAll(string(raw), "%PORT%", fmt.Sprint(port))
			cfg := fmt.Sprintf("port: %d\nnip11: {privkey: %s}\nstore: %s\nlogdir: %s\nadmin: %s\n",
				port, A(t, "operator").PrivHex, filepath.Join(t.TempDir(), "notes.db"), t.TempDir(), admin)
			path := filepath.Join(t.TempDir(), "relay.yaml")
			if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
			rep := NewEnv(t).Run(t, "relay", "--config", path, "--json").ExpectErr(t, c.code)
			if !strings.Contains(rep.Error, c.want) {
				t.Errorf("error %q lacks %q", rep.Error, c.want)
			}
		})
	}
}

// runFor runs ncli for d, then interrupts it and returns its stdout.
func runFor(t *testing.T, d time.Duration, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin(t), args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.Env = NewEnv(t).environ()
	var out bytes.Buffer
	cmd.Stdout = &out
	_ = cmd.Run()
	return out.String()
}

func TestRelayStatusWatchStreamsNDJSON(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	out := runFor(t, 5*time.Second, "relay", "status", "--watch", "--config", r.ConfigPath, "--json")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("status --watch printed %d line(s), want one per 2s:\n%s", len(lines), out)
	}
	for _, l := range lines {
		var st struct {
			Metrics struct{ V map[string]float64 }
			Indexes []any
		}
		if err := json.Unmarshal([]byte(l), &st); err != nil || st.Metrics.V == nil {
			t.Errorf("not a status object: %q (%v)", l, err)
		}
	}
}

// Text output is what a person reads: check the parts they rely on.
func TestRelayAdminTextOutput(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	r.Seed(t)
	e := NewEnv(t)
	cfg := r.ConfigPath

	status := e.MustOK(t, "relay", "status", "--config", cfg).Stdout
	for _, want := range []string{"Uptime", "Connections", "Messages/s", "Events/s", "Latency p99", "Store", "Queues", "Security", "Process", "Index search", "Index zaps", "cache.search.enabled"} {
		if !strings.Contains(status, want) {
			t.Errorf("status text lacks %q:\n%s", want, status)
		}
	}
	list := e.MustOK(t, "relay", "index", "list", "--config", cfg).Stdout
	if !strings.Contains(list, "INDEX") || !strings.Contains(list, "zaps") || !strings.Contains(list, "unavailable") {
		t.Errorf("index list:\n%s", list)
	}
	follow := e.MustOK(t, "relay", "index", "rebuild", "zaps", "--follow", "--config", cfg).Stdout
	if !strings.Contains(follow, "zaps rebuild: succeeded") {
		t.Errorf("rebuild --follow:\n%s", follow)
	}
	start := e.MustOK(t, "relay", "index", "rebuild", "zaps", "--config", cfg)
	if !strings.Contains(start.Stdout, "zaps rebuild: job ") || !strings.Contains(start.Stderr, "jobs show") {
		t.Errorf("rebuild without --follow should name the job and how to follow it:\n%s", start)
	}
	jobs := e.MustOK(t, "relay", "jobs", "list", "--config", cfg).Stdout
	if !strings.Contains(jobs, "ID") || !strings.Contains(jobs, "rebuild") || !strings.Contains(jobs, "succeeded") {
		t.Errorf("jobs list:\n%s", jobs)
	}
	removed := e.Run(t, "relay", "stats", "--config", cfg)
	if removed.Code != 2 || !strings.Contains(removed.Stderr, "ncli relay status") {
		t.Errorf("relay stats in text mode: exit %d\n%s", removed.Code, removed.Stderr)
	}
}

// `ncli relay dashboard` in front of a real relay: link, cookie, CSRF,
// signed forwarding, rebinding guard.
func TestRelayDashboardLocal(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	r.Seed(t)
	port := allocPort(t)
	cmd := exec.Command(bin(t), "relay", "dashboard", "--no-open", "--port", fmt.Sprint(port), "--config", r.ConfigPath, "--json")
	cmd.Env = NewEnv(t).environ()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	var info struct{ URL, Pubkey string }
	sc := bufio.NewScanner(stdout)
	if !sc.Scan() || json.Unmarshal(sc.Bytes(), &info) != nil || info.URL == "" {
		t.Fatalf("no link printed: %q", sc.Text())
	}
	if info.Pubkey != A(t, "operator").PubHex {
		t.Errorf("dashboard reports pubkey %s", info.Pubkey)
	}
	u, _ := url.Parse(info.URL)
	base := "http://" + u.Host
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	code, _, resp := adminReq(t, noRedirect, "GET", info.URL, "", nil, nil)
	if code != http.StatusFound {
		t.Fatalf("link = %d, want redirect", code)
	}
	var cookie string
	for _, c := range resp.Cookies() {
		cookie = c.Name + "=" + c.Value
	}
	if code, _, _ := adminReq(t, noRedirect, "GET", info.URL, "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("reused link = %d, want 401", code)
	}

	withCookie := map[string]string{"Cookie": cookie}
	code, body, _ := adminReq(t, nil, "GET", base+"/admin/api/session", "", withCookie, nil)
	var sess struct{ Pubkey, CSRF string }
	if code != http.StatusOK || json.Unmarshal([]byte(body), &sess) != nil || sess.CSRF == "" {
		t.Fatalf("session = %d %s", code, body)
	}
	if code, body, _ := adminReq(t, nil, "GET", base+"/admin/api/history", "", withCookie, nil); code != http.StatusOK || !strings.Contains(body, `"series"`) {
		t.Errorf("forwarded history = %d %.80s", code, body)
	}
	if code, body, _ := adminReq(t, nil, "GET", base+"/admin/ui/", "", withCookie, nil); code != http.StatusOK || body == "" {
		t.Errorf("ui = %d", code)
	}
	rebuild := base + "/admin/api/indexes/zaps/rebuild"
	if code, _, _ := adminReq(t, nil, "POST", rebuild, "", withCookie, []byte(`{}`)); code != http.StatusForbidden {
		t.Errorf("POST without CSRF = %d, want 403", code)
	}
	if code, body, _ := adminReq(t, nil, "POST", rebuild, "", map[string]string{"Cookie": cookie, "X-CSRF-Token": sess.CSRF, "Content-Type": "application/json"}, []byte(`{}`)); code != http.StatusAccepted && code != http.StatusOK {
		t.Errorf("POST with CSRF = %d %s", code, body)
	}
	if code, _, _ := adminReq(t, nil, "GET", base+"/admin/api/history", "rebound.example:"+fmt.Sprint(port), withCookie, nil); code != http.StatusForbidden {
		t.Errorf("rebound Host = %d, want 403", code)
	}
	if code, _, _ := adminReq(t, nil, "GET", base+"/admin/api/history", "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("no cookie = %d, want 401", code)
	}
}
