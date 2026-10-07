// Package huddle is ncli's huddle client surface: a TUI showing who is in a
// voice room and who is speaking.
//
// The roster state below is deliberately separate from anything tview, so the
// part with actual behaviour -- who is present, who counts as speaking, and for
// how long -- is testable without a terminal.
package huddle

import (
	"sort"
	"sync"
	"time"

	"github.com/ohstr/ncli/huddle/client"
	"github.com/ohstr/nmilat/huddle/wire"
)

// SpeakingHold is how long a peer keeps showing as speaking after their last
// frame above the threshold. Without a hold the indicator flickers on every
// pause between words, because speech is not continuous at 20 ms granularity.
// It matches the reference implementation's own 600 ms hold.
const SpeakingHold = 600 * time.Millisecond

// Participant is one row of the roster, as the UI should show it.
type Participant struct {
	Pubkey string
	Index  uint8
	// Self marks this client's own entry, which is shown first and labelled
	// rather than being just another row.
	Self bool
	// Speaking is true while within SpeakingHold of the peer's last frame above
	// the threshold.
	Speaking bool
	// Level is the last level seen from this peer, for a meter. It is
	// sender-authored and untrusted, so it is display-only.
	Level int8
	// Heard is whether any audio has arrived from this peer at all. A peer who
	// has said nothing is present but silent, which is different from one whose
	// audio is not reaching us.
	Heard bool
}

// roster tracks presence and speaking state. Safe for concurrent use: frames
// arrive on the client's reader goroutine while the UI reads rows on tview's.
type roster struct {
	mu        sync.Mutex
	self      string
	present   map[string]client.Peer
	lastSpoke map[string]time.Time
	lastLevel map[string]int8
	heard     map[string]bool

	hold      time.Duration
	threshold int8
	now       func() time.Time
}

func newRoster(self string, hold time.Duration, threshold int8, now func() time.Time) *roster {
	if hold <= 0 {
		hold = SpeakingHold
	}
	if now == nil {
		now = time.Now
	}
	return &roster{
		self:      self,
		present:   make(map[string]client.Peer),
		lastSpoke: make(map[string]time.Time),
		lastLevel: make(map[string]int8),
		heard:     make(map[string]bool),
		hold:      hold,
		threshold: threshold,
		now:       now,
	}
}

// sync replaces presence from the client's roster. Speaking state is keyed by
// pubkey, not by routing index, so a peer whose index is reassigned keeps their
// row's state instead of appearing to go silent.
//
// It reports who left, because anything holding per-peer state keyed off the
// roster -- an Opus decoder, say -- has to drop it or it accumulates one entry
// per person who has ever been in the room.
func (r *roster) sync(peers []client.Peer) (departed []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	present := make(map[string]client.Peer, len(peers))
	for _, p := range peers {
		present[p.Pubkey] = p
	}
	r.present = present

	// Forget levels for anyone gone, so a stale meter does not linger on a row
	// that comes back.
	for pubkey := range r.lastLevel {
		if _, ok := present[pubkey]; !ok {
			delete(r.lastLevel, pubkey)
			delete(r.lastSpoke, pubkey)
			delete(r.heard, pubkey)
			departed = append(departed, pubkey)
		}
	}
	return departed
}

// heardFrame folds one inbound frame into the speaking state. Unattributed
// frames are ignored: with no pubkey there is no row to light up, and guessing
// from the routing index alone is exactly the misattribution the epoch exists to
// prevent.
func (r *roster) heardFrame(f client.Frame) {
	if !f.Attributed {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.heard[f.Author.Pubkey] = true
	r.lastLevel[f.Author.Pubkey] = f.Header.LevelDbov
	if f.Speaking(r.threshold) {
		r.lastSpoke[f.Author.Pubkey] = r.now()
	}
}

// participants returns the rows to render, in a stable order: this client first,
// then by routing index. Sorting by index rather than by level or name keeps rows
// from jumping around as people talk, which makes the list unreadable.
func (r *roster) participants() []Participant {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	out := make([]Participant, 0, len(r.present))
	for pubkey, peer := range r.present {
		last, spoke := r.lastSpoke[pubkey]
		level, hasLevel := r.lastLevel[pubkey]
		if !hasLevel {
			level = wire.LevelSilenceFloor
		}
		out = append(out, Participant{
			Pubkey:   pubkey,
			Index:    peer.Index,
			Self:     pubkey == r.self,
			Speaking: spoke && now.Sub(last) < r.hold,
			Level:    level,
			Heard:    r.heard[pubkey],
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Self != out[j].Self {
			return out[i].Self
		}
		return out[i].Index < out[j].Index
	})
	return out
}

// speakingCount is how many peers are currently speaking, for a header summary.
func (r *roster) speakingCount() int {
	n := 0
	for _, p := range r.participants() {
		if p.Speaking {
			n++
		}
	}
	return n
}
