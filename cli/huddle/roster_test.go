package huddle

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/ncli/huddleclient"
	"github.com/ohstr/nmilat/huddle/wire"
)

// clock is a hand-wound time source, so the speaking hold is asserted at exact
// boundaries instead of by sleeping.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Unix(1700000000, 0)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func peers(spec ...any) []huddleclient.Peer {
	out := make([]huddleclient.Peer, 0, len(spec)/2)
	for i := 0; i+1 < len(spec); i += 2 {
		out = append(out, huddleclient.Peer{
			Pubkey: spec[i].(string),
			Index:  uint8(spec[i+1].(int)),
		})
	}
	return out
}

func frame(pubkey string, level int8) huddleclient.Frame {
	return huddleclient.Frame{
		Author:     huddleclient.Peer{Pubkey: pubkey},
		Header:     wire.FrameHeader{LevelDbov: level},
		Attributed: true,
	}
}

const testThreshold = -55

func find(t *testing.T, rows []Participant, pubkey string) Participant {
	t.Helper()
	for _, r := range rows {
		if r.Pubkey == pubkey {
			return r
		}
	}
	t.Fatalf("no row for %q in %+v", pubkey, rows)
	return Participant{}
}

func TestRosterOrdersSelfFirstThenByIndex(t *testing.T) {
	c := newClock()
	r := newRoster("me", SpeakingHold, testThreshold, c.now)
	// Deliberately out of order, and with self in the middle.
	r.sync(peers("zed", 9, "me", 4, "abe", 2))

	got := r.participants()
	if len(got) != 3 {
		t.Fatalf("want 3 rows, got %d", len(got))
	}
	if !got[0].Self || got[0].Pubkey != "me" {
		t.Errorf("want self first, got %+v", got[0])
	}
	if got[1].Pubkey != "abe" || got[2].Pubkey != "zed" {
		t.Errorf("want remainder by index (abe=2, zed=9), got %q then %q", got[1].Pubkey, got[2].Pubkey)
	}
}

func TestRosterOrderIsStableAsPeopleTalk(t *testing.T) {
	c := newClock()
	r := newRoster("me", SpeakingHold, testThreshold, c.now)
	r.sync(peers("me", 0, "abe", 1, "zed", 2))

	before := r.participants()
	// The loudest peer is last in index order; it must not be promoted.
	r.heardFrame(frame("zed", -5))
	after := r.participants()

	for i := range before {
		if before[i].Pubkey != after[i].Pubkey {
			t.Fatalf("row order moved when a peer spoke: %q -> %q at %d",
				before[i].Pubkey, after[i].Pubkey, i)
		}
	}
	if !find(t, after, "zed").Speaking {
		t.Error("the peer that spoke should be marked speaking")
	}
}

func TestSpeakingHoldExpiresAndRefreshes(t *testing.T) {
	c := newClock()
	r := newRoster("me", SpeakingHold, testThreshold, c.now)
	r.sync(peers("me", 0, "abe", 1))

	r.heardFrame(frame("abe", -20))
	if !find(t, r.participants(), "abe").Speaking {
		t.Fatal("should be speaking immediately after a loud frame")
	}

	// Just inside the hold: still speaking. This is the whole point of the
	// hold -- a gap between words must not blink the indicator off.
	c.advance(SpeakingHold - time.Millisecond)
	if !find(t, r.participants(), "abe").Speaking {
		t.Error("should still be speaking just inside the hold")
	}

	// A new loud frame here must restart the hold, not be ignored.
	r.heardFrame(frame("abe", -20))
	c.advance(SpeakingHold - time.Millisecond)
	if !find(t, r.participants(), "abe").Speaking {
		t.Error("a fresh frame should have refreshed the hold")
	}

	c.advance(2 * time.Millisecond)
	if find(t, r.participants(), "abe").Speaking {
		t.Error("should have stopped speaking once the hold elapsed")
	}
}

func TestQuietFrameIsHeardButNotSpeaking(t *testing.T) {
	c := newClock()
	r := newRoster("me", SpeakingHold, testThreshold, c.now)
	r.sync(peers("me", 0, "abe", 1))

	r.heardFrame(frame("abe", testThreshold-1))
	row := find(t, r.participants(), "abe")
	if row.Speaking {
		t.Error("a frame below the threshold must not count as speaking")
	}
	if !row.Heard {
		t.Error("a quiet frame is still audio arriving, so Heard must be set")
	}
	if row.Level != testThreshold-1 {
		t.Errorf("want the observed level %d, got %d", testThreshold-1, row.Level)
	}
}

func TestSilentPeerShowsTheSilenceFloorAndNotHeard(t *testing.T) {
	c := newClock()
	r := newRoster("me", SpeakingHold, testThreshold, c.now)
	r.sync(peers("me", 0, "abe", 1))

	row := find(t, r.participants(), "abe")
	if row.Heard {
		t.Error("a peer who has sent nothing must not be marked Heard")
	}
	if row.Level != wire.LevelSilenceFloor {
		t.Errorf("want the silence floor %d for an unheard peer, got %d",
			wire.LevelSilenceFloor, row.Level)
	}
}

func TestUnattributedFrameIsIgnored(t *testing.T) {
	c := newClock()
	r := newRoster("me", SpeakingHold, testThreshold, c.now)
	r.sync(peers("me", 0, "abe", 1))

	// A frame the client could not attribute: lighting up a row from the
	// routing index alone is the misattribution the epoch exists to prevent.
	f := frame("abe", -10)
	f.Attributed = false
	r.heardFrame(f)

	if find(t, r.participants(), "abe").Speaking {
		t.Error("an unattributed frame must not mark anyone as speaking")
	}
	if find(t, r.participants(), "abe").Heard {
		t.Error("an unattributed frame must not mark anyone as heard")
	}
}

func TestSpeakingSurvivesAnIndexReassignment(t *testing.T) {
	c := newClock()
	r := newRoster("me", SpeakingHold, testThreshold, c.now)
	r.sync(peers("me", 0, "abe", 1))
	r.heardFrame(frame("abe", -10))

	// Same person, new routing index (someone left and indices rotated).
	r.sync(peers("me", 0, "abe", 7))

	row := find(t, r.participants(), "abe")
	if row.Index != 7 {
		t.Errorf("want the new index 7, got %d", row.Index)
	}
	if !row.Speaking {
		t.Error("state is keyed by pubkey, so a reindexed peer must not appear to go silent")
	}
}

func TestDepartedPeerLeavesNoStaleState(t *testing.T) {
	c := newClock()
	r := newRoster("me", SpeakingHold, testThreshold, c.now)
	r.sync(peers("me", 0, "abe", 1))
	r.heardFrame(frame("abe", -10))

	r.sync(peers("me", 0))
	if len(r.participants()) != 1 {
		t.Fatalf("departed peer should be gone, got %+v", r.participants())
	}

	// Rejoining starts clean: no carried-over meter or speaking indicator.
	r.sync(peers("me", 0, "abe", 1))
	row := find(t, r.participants(), "abe")
	if row.Speaking || row.Heard || row.Level != wire.LevelSilenceFloor {
		t.Errorf("rejoining peer carried stale state: %+v", row)
	}
}

func TestFrameFromSomeoneNotInTheRosterIsNotInvented(t *testing.T) {
	c := newClock()
	r := newRoster("me", SpeakingHold, testThreshold, c.now)
	r.sync(peers("me", 0))

	// Audio can arrive a beat before the roster update that explains it. It
	// must not conjure a row -- presence comes from the roster alone.
	r.heardFrame(frame("ghost", -10))
	if len(r.participants()) != 1 {
		t.Errorf("a frame must not create a roster row, got %+v", r.participants())
	}

	// ...but once the roster catches up, the already-received audio counts.
	r.sync(peers("me", 0, "ghost", 3))
	if !find(t, r.participants(), "ghost").Speaking {
		t.Error("audio received just before the roster update should still register")
	}
}

func TestSpeakingCount(t *testing.T) {
	c := newClock()
	r := newRoster("me", SpeakingHold, testThreshold, c.now)
	r.sync(peers("me", 0, "abe", 1, "zed", 2))

	if got := r.speakingCount(); got != 0 {
		t.Errorf("want 0 speaking initially, got %d", got)
	}
	r.heardFrame(frame("abe", -10))
	r.heardFrame(frame("zed", -10))
	if got := r.speakingCount(); got != 2 {
		t.Errorf("want 2 speaking, got %d", got)
	}
	c.advance(SpeakingHold)
	if got := r.speakingCount(); got != 0 {
		t.Errorf("want 0 speaking after the hold elapsed, got %d", got)
	}
}

func TestRosterDefaults(t *testing.T) {
	r := newRoster("me", 0, testThreshold, nil)
	if r.hold != SpeakingHold {
		t.Errorf("a non-positive hold should fall back to SpeakingHold, got %v", r.hold)
	}
	if r.now == nil {
		t.Error("a nil clock should fall back to time.Now")
	}
	// Exercise the fallback clock so a broken default cannot hide.
	r.sync(peers("me", 0))
	r.heardFrame(frame("me", -10))
	if !find(t, r.participants(), "me").Speaking {
		t.Error("the default clock should produce a live speaking state")
	}
}

// TestRosterConcurrentUse is the -race check: frames arrive on the client's
// reader goroutine while the UI reads rows on tview's.
func TestRosterConcurrentUse(t *testing.T) {
	c := newClock()
	r := newRoster("me", SpeakingHold, testThreshold, c.now)

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				switch w {
				case 0:
					r.sync(peers("me", 0, "abe", i%250, "zed", 251))
				case 1:
					r.heardFrame(frame("abe", -10))
				case 2:
					_ = r.participants()
				case 3:
					c.advance(time.Millisecond)
					_ = r.speakingCount()
				}
			}
		}(w)
	}
	wg.Wait()

	// Sanity: the state is still coherent after the storm.
	r.sync(peers("me", 0))
	if got := len(r.participants()); got != 1 {
		t.Errorf("want 1 row after the storm, got %d", got)
	}
	_ = strconv.Itoa(0)
}
