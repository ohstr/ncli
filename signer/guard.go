package signer

import (
	"strings"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip19"
)

// Guard refuses any output that contains the signer's own key. It is
// built in and runs before the policy.
type Guard struct {
	needles []string
}

// NewGuard guards privKeyHex, its nsec, and any extra encodings of it
// (e.g. the ncryptsec it was loaded from).
func NewGuard(privKeyHex string, extra ...string) *Guard {
	g := &Guard{}
	g.add(privKeyHex)
	if nsec, err := nip19.EncodePrivateKey(privKeyHex); err == nil {
		g.add(nsec)
	}
	for _, e := range extra {
		g.add(e)
	}
	return g
}

func (g *Guard) add(s string) {
	if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
		g.needles = append(g.needles, s)
	}
}

// Contains reports whether any of texts holds the key, case-insensitively.
func (g *Guard) Contains(texts ...string) bool {
	if g == nil {
		return false
	}
	for _, t := range texts {
		lt := strings.ToLower(t)
		for _, n := range g.needles {
			if strings.Contains(lt, n) {
				return true
			}
		}
	}
	return false
}

// ContainsEvent checks the event's content and every tag element.
func (g *Guard) ContainsEvent(ev *nip01.Event) bool {
	if g.Contains(ev.Content) {
		return true
	}
	for _, t := range ev.Tags {
		if g.Contains(t...) {
			return true
		}
	}
	return false
}
