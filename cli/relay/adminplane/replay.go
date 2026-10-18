package adminplane

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// nip98Window is how long a NIP-98 event stays valid (nip98.Verify's
// ±60s), plus margin. An id only needs remembering that long.
const nip98Window = 2 * time.Minute

// ReplayGuard rejects a NIP-98 Authorization header seen before. NIP-98
// itself only bounds an event by time, so without this a captured admin
// request can be resent until it expires.
type ReplayGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// NewReplayGuard returns an empty ReplayGuard.
func NewReplayGuard() *ReplayGuard { return &ReplayGuard{seen: map[string]time.Time{}} }

// First reports whether this header's event id is new, and remembers it.
// Call only after the header verified, so junk can't fill the map.
func (g *ReplayGuard) First(authHeader string, now time.Time) bool {
	id := nip98EventID(authHeader)
	if id == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, exp := range g.seen {
		if now.After(exp) {
			delete(g.seen, k)
		}
	}
	if _, dup := g.seen[id]; dup {
		return false
	}
	g.seen[id] = now.Add(nip98Window)
	return true
}

// Used reports whether this header's event id was already accepted,
// without recording it.
func (g *ReplayGuard) Used(authHeader string, now time.Time) bool {
	id := nip98EventID(authHeader)
	if id == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	exp, ok := g.seen[id]
	return ok && now.Before(exp)
}

// ServeOnce serves next unless its NIP-98 event was already used, and
// records the event once next accepted it (2xx) -- so only verified
// requests are remembered, and an unverified one can't fill the record.
func ServeOnce(g *ReplayGuard, next http.Handler, w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if auth != "" && g.Used(auth, time.Now()) {
		http.Error(w, "NIP-98 event already used", http.StatusUnauthorized)
		return
	}
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(rec, r)
	if auth != "" && rec.status < 300 {
		g.First(auth, time.Now())
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func nip98EventID(authHeader string) string {
	raw, ok := strings.CutPrefix(authHeader, "Nostr ")
	if !ok {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return ""
	}
	var ev struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(b, &ev) != nil {
		return ""
	}
	return ev.ID
}
