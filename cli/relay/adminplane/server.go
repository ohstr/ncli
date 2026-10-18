package adminplane

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ohstr/ncli/cli/relay/metrics"
	"github.com/ohstr/nmilat/nip98"
	"github.com/prometheus/common/expfmt"
	"github.com/rs/zerolog"
)

// maxBodyBytes bounds an admin request body.
const maxBodyBytes = 1 << 20

// Options configures a Server.
type Options struct {
	Settings *Settings
	// Admins returns the pubkeys allowed to use the API, read per request so
	// a removed admin loses access at once.
	Admins  func() []string
	Metrics *metrics.Metrics
	History *metrics.History
	// UI is the built dashboard (index.html at its root); nil serves a
	// placeholder page.
	UI     fs.FS
	Logger zerolog.Logger
}

// Server is the admin listener.
type Server struct {
	opts     Options
	mux      *http.ServeMux
	tokens   *tokenSet
	sessions *sessions
	replay   *ReplayGuard
	limiter  *failLimiter
	http     *http.Server

	scrapeMu   sync.Mutex
	scrapeBody []byte
	scrapeAt   time.Time
}

type ctxKey struct{}

// Caller returns the authenticated admin pubkey of an API request.
func Caller(r *http.Request) string {
	pk, _ := r.Context().Value(ctxKey{}).(string)
	return pk
}

// New builds the server; Start begins listening.
func New(opts Options) (*Server, error) {
	if opts.Settings == nil {
		return nil, errors.New("adminplane: no settings")
	}
	s := &Server{
		opts:     opts,
		mux:      http.NewServeMux(),
		sessions: newSessions(opts.Settings.SessionTTL),
		replay:   NewReplayGuard(),
		limiter:  newFailLimiter(),
	}
	if opts.Settings.Metrics {
		t, err := newTokenSet(opts.Settings.TokensFile)
		if err != nil {
			return nil, err
		}
		s.tokens = t
		s.mux.HandleFunc("GET /metrics", s.serveMetrics)
	}

	s.Handle("GET /admin/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.opts.History.Latest())
	})
	s.Handle("GET /admin/api/history", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.opts.History.Columns())
	})
	s.Handle("GET /admin/api/stream", s.serveStream)
	// Who is calling: `ncli relay dashboard` checks it even when the relay
	// serves no dashboard of its own.
	s.Handle("GET /admin/api/session", s.sessionInfo)

	if opts.Settings.Dashboard {
		s.mux.HandleFunc("POST /admin/api/login", s.login)
		s.Handle("POST /admin/api/logout", s.logout)
		s.mux.Handle("GET /admin/ui/", http.StripPrefix("/admin/ui", s.uiHandler()))
		s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/ui/", http.StatusFound)
		})
	}

	s.http = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          nil,
	}
	if tc := opts.Settings.TLS; tc != nil {
		cfg, err := tlsConfig(tc)
		if err != nil {
			return nil, err
		}
		s.http.TLSConfig = cfg
	}
	return s, nil
}

func tlsConfig(tc *TLSConfig) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(tc.Cert, tc.Key)
	if err != nil {
		return nil, invalidErr(tc.Cert, "admin.tls: %v", err)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	if tc.ClientCA != "" {
		pem, err := os.ReadFile(tc.ClientCA)
		if err != nil {
			return nil, invalidErr(tc.ClientCA, "admin.tls.clientCA: %v", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, invalidErr(tc.ClientCA, "admin.tls.clientCA: no certificates found")
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

// Handle registers an API route behind admin authentication: a NIP-98
// signature from an admin, or a dashboard session (plus its CSRF token for
// anything but GET).
func (s *Server) Handle(pattern string, h http.HandlerFunc) {
	s.mux.Handle(pattern, s.requireAdmin(h))
}

// Handler is the full handler chain, for tests and for Start.
func (s *Server) Handler() http.Handler {
	return s.gate(s.mux)
}

// Start listens on the configured address and serves in the background.
// A bind failure is returned at once.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.opts.Settings.Addr)
	if err != nil {
		return fmt.Errorf("admin listener: %w", err)
	}
	if s.http.TLSConfig != nil {
		ln = tls.NewListener(ln, s.http.TLSConfig)
	}
	s.opts.Logger.Info().
		Str("addr", ln.Addr().String()).
		Bool("tls", s.http.TLSConfig != nil).
		Bool("dashboard", s.opts.Settings.Dashboard).
		Bool("metrics", s.opts.Settings.Metrics).
		Msg("admin listener started")
	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.opts.Logger.Error().Err(err).Msg("admin listener stopped")
		}
	}()
	return nil
}

// Shutdown stops the listener, closing open streams.
func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.http.Shutdown(ctx); err != nil {
		return s.http.Close()
	}
	return nil
}

// gate applies the address allowlist, the failure limiter and the headers
// every response carries.
func (s *Server) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "no-store")
		if s.opts.Settings.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}

		// Only this listener's own names: a request for another host (DNS
		// rebinding, or a NIP-98 header signed for another site) would
		// otherwise verify against the URL it claims.
		if !s.opts.Settings.Hosts[strings.ToLower(r.Host)] {
			s.deny(w, "host", http.StatusMisdirectedRequest)
			return
		}
		ip := clientIP(r)
		if len(s.opts.Settings.AllowCIDRs) > 0 && !s.allowed(ip) {
			s.deny(w, "cidr", http.StatusForbidden)
			return
		}
		if s.limiter.blocked(ip, time.Now()) {
			http.Error(w, "too many failed attempts, try again later", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowed(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range s.opts.Settings.AllowCIDRs {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// deny answers a refused request without saying which check failed, and
// counts it by reason.
func (s *Server) deny(w http.ResponseWriter, reason string, status int) {
	if s.opts.Metrics != nil {
		s.opts.Metrics.AdminDenied(reason)
	}
	http.Error(w, http.StatusText(status), status)
}

func (s *Server) failAuth(w http.ResponseWriter, r *http.Request, reason string) {
	s.limiter.fail(clientIP(r), time.Now())
	s.deny(w, reason, http.StatusUnauthorized)
}

func (s *Server) admins() []string {
	if s.opts.Admins == nil {
		return nil
	}
	return s.opts.Admins()
}

// verifyNIP98 checks r's NIP-98 header against the admins, binding the body,
// and records it so it can't be replayed. It hands the body back on r.
func (s *Server) verifyNIP98(w http.ResponseWriter, r *http.Request) (string, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
		return "", false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	pubkey, err := nip98.Verify(r, nip98.Options{AllowedPubkeys: s.admins(), Body: body, RequirePayload: true})
	if err != nil {
		s.failAuth(w, r, "nip98")
		return "", false
	}
	if !s.replay.First(r.Header.Get("Authorization"), time.Now()) {
		s.failAuth(w, r, "replay")
		return "", false
	}
	return pubkey, true
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Authorization"), "Nostr ") {
			pubkey, ok := s.verifyNIP98(w, r)
			if !ok {
				return
			}
			next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, pubkey)))
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err != nil || !s.opts.Settings.Dashboard {
			s.failAuth(w, r, "session")
			return
		}
		sess, ok := s.sessions.get(c.Value, s.admins(), time.Now())
		if !ok {
			clearSessionCookie(w)
			s.failAuth(w, r, "session")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !csrfMatches(sess, r) {
			s.failAuth(w, r, "csrf")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess.pubkey)))
	})
}

type sessionJSON struct {
	Pubkey    string `json:"pubkey"`
	ExpiresAt int64  `json:"expires_at"`
	CSRF      string `json:"csrf"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	pubkey, ok := s.verifyNIP98(w, r)
	if !ok {
		return
	}
	id, sess := s.sessions.create(pubkey, time.Now())
	setSessionCookie(w, id, sess.expires)
	s.opts.Logger.Info().Str("pubkey", pubkey).Str("remote", clientIP(r)).Msg("dashboard login")
	writeJSON(w, http.StatusOK, sessionJSON{Pubkey: pubkey, ExpiresAt: sess.expires.Unix(), CSRF: sess.csrf})
}

func (s *Server) sessionInfo(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		writeJSON(w, http.StatusOK, sessionJSON{Pubkey: Caller(r)})
		return
	}
	sess, ok := s.sessions.get(c.Value, s.admins(), time.Now())
	if !ok {
		s.failAuth(w, r, "session")
		return
	}
	writeJSON(w, http.StatusOK, sessionJSON{Pubkey: sess.pubkey, ExpiresAt: sess.expires.Unix(), CSRF: sess.csrf})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.remove(c.Value)
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// serveMetrics answers a scrape from a rendering at most ScrapeTTL old, so
// any number of scrapers costs one render per TTL.
func (s *Server) serveMetrics(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !s.tokens.valid(strings.TrimSpace(token), time.Now()) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="ncli-relay-metrics"`)
		s.failAuth(w, r, "token")
		return
	}
	body, err := s.renderMetrics(time.Now())
	if err != nil {
		http.Error(w, "failed to render metrics", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", string(expfmt.NewFormat(expfmt.TypeTextPlain)))
	_, _ = w.Write(body)
}

func (s *Server) renderMetrics(now time.Time) ([]byte, error) {
	s.scrapeMu.Lock()
	defer s.scrapeMu.Unlock()
	if s.scrapeBody != nil && now.Sub(s.scrapeAt) < s.opts.Settings.ScrapeTTL {
		return s.scrapeBody, nil
	}
	fams, err := s.opts.Metrics.Registry.Gather()
	if err != nil && len(fams) == 0 {
		return nil, err
	}
	var buf bytes.Buffer
	enc := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, f := range fams {
		if err := enc.Encode(f); err != nil {
			return nil, err
		}
	}
	s.scrapeBody, s.scrapeAt = buf.Bytes(), now
	return s.scrapeBody, nil
}

// streamHeartbeat keeps idle proxies from closing a quiet stream.
const streamHeartbeat = 15 * time.Second

func (s *Server) serveStream(w http.ResponseWriter, r *http.Request) {
	points, cancel, ok := s.opts.History.Subscribe()
	if !ok {
		http.Error(w, "too many live viewers", http.StatusServiceUnavailable)
		return
	}
	defer cancel()

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	write := func(data []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := w.Write(data); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write([]byte("retry: 5000\n\n")) {
		return
	}

	beat := time.NewTicker(streamHeartbeat)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case p := <-points:
			data, err := json.Marshal(p)
			if err != nil {
				continue
			}
			if !write(append(append([]byte("event: point\ndata: "), data...), '\n', '\n')) {
				return
			}
		case <-beat.C:
			if !write([]byte(": keepalive\n\n")) {
				return
			}
		}
	}
}

// uiCSP allows only the dashboard's own files: no inline script, no other
// origin, no framing.
const uiCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

const placeholderPage = `<!doctype html><meta charset="utf-8"><title>ncli relay dashboard</title>
<p>The dashboard was not built into this binary. Build it with <code>just ui</code>, then rebuild ncli.
The admin API and /metrics work without it.</p>`

func (s *Server) uiHandler() http.Handler { return UIHandler(s.opts.UI) }

// UIHandler serves a built dashboard (rooted at its index.html, mounted
// with its /admin/ui prefix stripped) under the dashboard's CSP, or a
// placeholder page when ui holds no build.
func UIHandler(ui fs.FS) http.Handler {
	hasIndex := false
	if ui != nil {
		if _, err := fs.Stat(ui, "index.html"); err == nil {
			hasIndex = true
		}
	}
	files := http.FileServerFS(orEmpty(ui))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", uiCSP)
		if !hasIndex {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(placeholderPage))
			return
		}
		// Hashed asset names never change, so they may be cached.
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

func orEmpty(f fs.FS) fs.FS {
	if f == nil {
		return emptyFS{}
	}
	return f
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
