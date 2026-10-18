package relay

import (
	"html/template"
	"io/fs"
	"net/http"
	"regexp"
	"strings"

	"github.com/ohstr/nmilat/nip11"
)

// homepageEnabled reports whether browsers get a homepage at the relay's
// URL. On unless `homepage: false`.
func homepageEnabled() bool {
	return config.Homepage == nil || *config.Homepage
}

// wantsHomepage is a browser loading the relay's URL: an HTML GET that isn't
// a WebSocket upgrade. NIP-11 and NIP-86 are routed before this.
func wantsHomepage(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// hostRe is what a Host header may look like to be echoed into the CSP.
var hostRe = regexp.MustCompile(`^[A-Za-z0-9.\-\[\]:]+$`)

// homeCSP lets the page read the relay's own NIP-11 and WebSocket, and show
// profile and group pictures from anywhere; nothing else.
func homeCSP(r *http.Request) string {
	connect := "'self'"
	if hostRe.MatchString(r.Host) {
		connect += " ws://" + r.Host + " wss://" + r.Host
	}
	return "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data: https:; font-src 'self'; connect-src " + connect + "; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}

func setPageHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", homeCSP(r))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
}

// fallbackHome is served when the binary was built without the web pages.
var fallbackHome = template.Must(template.New("home").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Name}}</title>
<h1>{{.Name}}</h1>
{{if .Description}}<p>{{.Description}}</p>{{end}}
<p>This is a Nostr relay. Add it to your Nostr client.</p>
</html>`))

// homepage serves the built homepage (home.html and its /_home/ assets), or
// a plain page from NIP-11 when none was built.
type homepage struct {
	index  []byte
	assets http.Handler
	meta   func() nip11.Metadata
}

func newHomepage(dist fs.FS, meta func() nip11.Metadata) *homepage {
	h := &homepage{meta: meta}
	if dist != nil {
		if b, err := fs.ReadFile(dist, "home.html"); err == nil {
			h.index = b
		}
		h.assets = http.StripPrefix("/_home", http.FileServerFS(dist))
	}
	return h
}

func (h *homepage) servePage(w http.ResponseWriter, r *http.Request) {
	setPageHeaders(w, r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if h.index != nil {
		_, _ = w.Write(h.index)
		return
	}
	md := h.meta()
	_ = fallbackHome.Execute(w, md)
}

func (h *homepage) serveAssets(w http.ResponseWriter, r *http.Request) {
	if h.assets == nil || !strings.HasPrefix(r.URL.Path, "/_home/assets/") {
		http.NotFound(w, r)
		return
	}
	setPageHeaders(w, r)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	h.assets.ServeHTTP(w, r)
}
