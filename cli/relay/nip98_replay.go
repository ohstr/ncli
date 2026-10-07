package relay

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// nip98Window is how long a NIP-98 event stays valid (nip98.Verify's
// ±60s), plus margin. An id only needs remembering that long.
const nip98Window = 2 * time.Minute

// replayGuard rejects a NIP-98 Authorization header seen before. NIP-98
// itself only bounds an event by time, so without this a captured admin
// request can be resent until it expires.
type replayGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newReplayGuard() *replayGuard { return &replayGuard{seen: map[string]time.Time{}} }

// first reports whether this header's event id is new, and remembers it.
// Call only after the header verified, so junk can't fill the map.
func (g *replayGuard) first(authHeader string, now time.Time) bool {
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
