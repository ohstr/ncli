package adminplane

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"
)

// sessionCookie is host-only, Secure and Path=/ by the __Host- prefix's own
// rules, so no other site or subdomain can set or read it.
const sessionCookie = "__Host-ncli_admin"

// csrfHeader must echo the session's CSRF token on every request that
// changes something.
const csrfHeader = "X-CSRF-Token"

// maxSessions bounds dashboard logins held at once.
const maxSessions = 64

type session struct {
	pubkey  string
	csrf    string
	expires time.Time
}

// sessions holds dashboard logins in memory: a restart logs everyone out.
type sessions struct {
	ttl time.Duration

	mu   sync.Mutex
	byID map[string]*session
}

func newSessions(ttl time.Duration) *sessions {
	return &sessions{ttl: ttl, byID: map[string]*session{}}
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *sessions) create(pubkey string, now time.Time) (id string, sess session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(now)
	if len(s.byID) >= maxSessions {
		// Evict the one closest to expiring rather than refuse a login.
		var oldest string
		for k, v := range s.byID {
			if oldest == "" || v.expires.Before(s.byID[oldest].expires) {
				oldest = k
			}
		}
		delete(s.byID, oldest)
	}
	id = randomToken()
	sess = session{pubkey: pubkey, csrf: randomToken(), expires: now.Add(s.ttl)}
	s.byID[id] = &sess
	return id, sess
}

// get returns the live session for id whose pubkey is still an admin.
func (s *sessions) get(id string, admins []string, now time.Time) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	if !ok {
		return session{}, false
	}
	if now.After(sess.expires) || !slices.Contains(admins, sess.pubkey) {
		delete(s.byID, id)
		return session{}, false
	}
	return *sess, true
}

func (s *sessions) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
}

func (s *sessions) sweep(now time.Time) {
	for k, v := range s.byID {
		if now.After(v.expires) {
			delete(s.byID, k)
		}
	}
}

func csrfMatches(sess session, r *http.Request) bool {
	got := r.Header.Get(csrfHeader)
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(sess.csrf)) == 1
}

func setSessionCookie(w http.ResponseWriter, id string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: id, Path: "/",
		Expires: expires, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

// failLimiter slows credential guessing: an address with failLimit failed
// attempts within failWindow is refused outright until the window passes.
type failLimiter struct {
	mu    sync.Mutex
	fails map[string]*failCount
}

type failCount struct {
	n     int
	since time.Time
}

const (
	failLimit     = 10
	failWindow    = time.Minute
	maxFailedAddr = 10_000
)

func newFailLimiter() *failLimiter { return &failLimiter{fails: map[string]*failCount{}} }

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (l *failLimiter) blocked(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[ip]
	if !ok {
		return false
	}
	if now.Sub(f.since) > failWindow {
		delete(l.fails, ip)
		return false
	}
	return f.n >= failLimit
}

func (l *failLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[ip]
	if !ok || now.Sub(f.since) > failWindow {
		if len(l.fails) >= maxFailedAddr {
			for k, v := range l.fails {
				if now.Sub(v.since) > failWindow {
					delete(l.fails, k)
				}
			}
			if len(l.fails) >= maxFailedAddr {
				return
			}
		}
		l.fails[ip] = &failCount{n: 1, since: now}
		return
	}
	f.n++
}
