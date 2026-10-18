package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/nip11"
)

func get(t *testing.T, url string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

const browserAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

func TestHomepageRouting(t *testing.T) {
	ts := bootScenario(t, RelayConfig{
		Nip11:        nip11.Metadata{Name: "Test Relay", PubKey: scenarioMemberPub},
		RelayNotesDb: "unused",
		Port:         defaultPort,
	})

	resp, body := get(t, ts.URL+"/", map[string]string{"Accept": browserAccept})
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("browser GET / = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(body, "<html") {
		t.Errorf("not an HTML page: %.200s", body)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self'", "frame-ancestors 'none'", "connect-src 'self' ws://" + strings.TrimPrefix(ts.URL, "http://")} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}

	// NIP-11 and WebSocket clients see what they always did.
	resp, body = get(t, ts.URL+"/", map[string]string{"Accept": nip11.ContentTypeHeader})
	var doc nip11.Metadata
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &doc) != nil || doc.Name != "Test Relay" {
		t.Errorf("NIP-11 = %d %.120s", resp.StatusCode, body)
	}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/", http.Header{"Accept": {browserAccept}})
	if err != nil {
		t.Fatalf("WebSocket upgrade with a browser Accept header: %v", err)
	}
	_ = conn.Close()

	// A non-browser GET (no HTML in Accept) is left to the WebSocket handler.
	if resp, _ := get(t, ts.URL+"/", nil); resp.StatusCode == http.StatusOK {
		t.Errorf("plain GET / served the homepage")
	}
}

func TestHomepageCanBeTurnedOff(t *testing.T) {
	off := false
	ts := bootScenario(t, RelayConfig{
		Nip11:        nip11.Metadata{Name: "Quiet", PubKey: scenarioMemberPub},
		RelayNotesDb: "unused",
		Port:         defaultPort,
		Homepage:     &off,
	})
	if resp, _ := get(t, ts.URL+"/", map[string]string{"Accept": browserAccept}); resp.StatusCode == http.StatusOK {
		t.Errorf("homepage: false still served a page (%d)", resp.StatusCode)
	}
	if resp, _ := get(t, ts.URL+"/_home/assets/x.js", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("assets with homepage off = %d", resp.StatusCode)
	}
}

func TestHomepageServesBuiltPageAndAssets(t *testing.T) {
	h := newHomepage(fstest.MapFS{
		"home.html":         {Data: []byte("<!doctype html><html>built</html>")},
		"assets/home-1.js":  {Data: []byte("console.log(1)")},
		"secret/config.txt": {Data: []byte("nope")},
	}, func() nip11.Metadata { return nip11.Metadata{} })

	w := httptest.NewRecorder()
	h.servePage(w, httptest.NewRequest("GET", "http://r.example/", nil))
	if !strings.Contains(w.Body.String(), "built") {
		t.Errorf("page = %s", w.Body)
	}

	w = httptest.NewRecorder()
	h.serveAssets(w, httptest.NewRequest("GET", "http://r.example/_home/assets/home-1.js", nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset = %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	for _, p := range []string{"/_home/secret/config.txt", "/_home/home.html", "/_home/../home.html"} {
		w = httptest.NewRecorder()
		h.serveAssets(w, httptest.NewRequest("GET", "http://r.example"+p, nil))
		if w.Code == 200 {
			t.Errorf("%s served outside assets/", p)
		}
	}
}

func TestHomepageFallbackEscapesNIP11(t *testing.T) {
	h := newHomepage(fstest.MapFS{}, func() nip11.Metadata {
		return nip11.Metadata{Name: `<script>alert(1)</script>`, Description: `"><img src=x onerror=alert(1)>`}
	})
	w := httptest.NewRecorder()
	h.servePage(w, httptest.NewRequest("GET", "http://r.example/", nil))
	body := w.Body.String()
	if strings.Contains(body, "<script>alert") || strings.Contains(body, "<img src=x") {
		t.Fatalf("NIP-11 text not escaped:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("fallback page lacks the relay name:\n%s", body)
	}
}

func TestHomeCSPIgnoresOddHosts(t *testing.T) {
	r := httptest.NewRequest("GET", "http://x/", nil)
	r.Host = "evil.example; script-src *"
	if csp := homeCSP(r); strings.Contains(csp, "evil") || strings.Contains(csp, "script-src *") {
		t.Errorf("Host leaked into the CSP: %q", csp)
	}
}
