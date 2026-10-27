package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/nip01"
)

// liveRelay is a fake relay that answers a REQ with its stored events
// (since-filtered), EOSE, then every event added later.
type liveRelay struct {
	server *httptest.Server
	url    string

	mu     sync.Mutex
	events []*nip01.Event
	conns  map[*websocket.Conn]func(*nip01.Event)
	sinces []uint64
}

func newLiveRelay(t *testing.T) *liveRelay {
	t.Helper()
	r := &liveRelay{conns: map[*websocket.Conn]func(*nip01.Event){}}
	up := websocket.Upgrader{}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r.serve(conn)
	}))
	t.Cleanup(r.server.Close)
	r.url = "ws" + r.server.URL[len("http"):]
	return r
}

func (r *liveRelay) serve(conn *websocket.Conn) {
	var wmu sync.Mutex
	write := func(v ...any) {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.WriteJSON(v)
	}
	defer func() {
		r.mu.Lock()
		delete(r.conns, conn)
		r.mu.Unlock()
	}()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg []json.RawMessage
		if json.Unmarshal(data, &msg) != nil || len(msg) < 3 {
			continue
		}
		var kind, subID string
		_ = json.Unmarshal(msg[0], &kind)
		if kind != "REQ" {
			continue
		}
		_ = json.Unmarshal(msg[1], &subID)
		var f nip01.SubscriptionFilter
		_ = json.Unmarshal(msg[2], &f)
		r.mu.Lock()
		r.sinces = append(r.sinces, f.Since)
		for _, ev := range r.events {
			if ev.CreatedAt >= f.Since {
				write("EVENT", subID, ev)
			}
		}
		write("EOSE", subID)
		r.conns[conn] = func(ev *nip01.Event) { write("EVENT", subID, ev) }
		r.mu.Unlock()
	}
}

// add stores ev and pushes it to live subscribers.
func (r *liveRelay) add(ev *nip01.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	for _, push := range r.conns {
		push(ev)
	}
}

// drop closes every client connection.
func (r *liveRelay) drop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for c := range r.conns {
		_ = c.Close()
	}
}

func (r *liveRelay) subscribers() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.conns)
}

func (r *liveRelay) lastSince() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sinces[len(r.sinces)-1]
}

func note(t *testing.T, content string, at int64) *nip01.Event {
	t.Helper()
	ev := nip01.NewEvent(1, content)
	ev.CreatedAt = uint64(at)
	if err := ev.Sign(testAuthPrivKey); err != nil {
		t.Fatal(err)
	}
	return ev
}

// collector runs Follow in the background and gathers what it emits.
type collector struct {
	mu   sync.Mutex
	got  []string
	done chan struct{}
	err  error
	stop context.CancelFunc
}

func startFollow(t *testing.T, relays []string, state *FollowState) *collector {
	t.Helper()
	targets, err := TargetsFromRelayList(relays)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &collector{done: make(chan struct{}), stop: cancel}
	go func() {
		defer close(c.done)
		c.err = Follow(ctx, targets, kindFilter(), FollowOptions{
			State: state,
			Emit: func(ev *nip01.Event, _ string) error {
				c.mu.Lock()
				c.got = append(c.got, ev.Content)
				c.mu.Unlock()
				return nil
			},
			Warn: func(relay string, err error) { t.Logf("%s: %v", relay, err) },
		})
	}()
	t.Cleanup(func() {
		cancel()
		// Follow saves its state on the way out; let it finish.
		select {
		case <-c.done:
		case <-time.After(5 * time.Second):
		}
	})
	return c
}

func (c *collector) wait(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := append([]string(nil), c.got...)
		c.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t.Fatalf("got %v, want %d events", c.got, n)
	return nil
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func fastBackoff(t *testing.T) {
	old := followBackoff
	followBackoff = [2]time.Duration{20 * time.Millisecond, 50 * time.Millisecond}
	t.Cleanup(func() { followBackoff = old })
}

func TestFollowDedupesAcrossRelays(t *testing.T) {
	a, b := newLiveRelay(t), newLiveRelay(t)
	now := time.Now().Unix()
	shared := note(t, "shared", now)
	a.add(shared)
	b.add(shared)
	a.add(note(t, "only a", now))

	c := startFollow(t, []string{a.url, b.url}, nil)
	c.wait(t, 2)
	waitFor(t, func() bool { return a.subscribers() == 1 && b.subscribers() == 1 })

	live := note(t, "live", now+1)
	a.add(live)
	b.add(live)
	got := c.wait(t, 3)
	time.Sleep(200 * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.got) != 3 {
		t.Fatalf("duplicates: %v", c.got)
	}
	_ = got
}

func TestFollowReconnectsWithoutLossOrRepeat(t *testing.T) {
	fastBackoff(t)
	r := newLiveRelay(t)
	now := time.Now().Unix()
	r.add(note(t, "before", now))

	c := startFollow(t, []string{r.url}, nil)
	c.wait(t, 1)
	waitFor(t, func() bool { return r.subscribers() == 1 })

	r.drop()
	// Published while the client is away.
	r.add(note(t, "while down", now+1))
	waitFor(t, func() bool { return r.subscribers() == 1 })
	r.add(note(t, "after", now+2))

	got := c.wait(t, 3)
	time.Sleep(200 * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.got) != 3 || got[0] != "before" {
		t.Fatalf("got %v", c.got)
	}
	// The resumed REQ starts near the newest event seen, not from scratch.
	if since := r.lastSince(); since == 0 || int64(since) > now {
		t.Fatalf("resumed since %d", since)
	}
}

func TestFollowStateResumes(t *testing.T) {
	r := newLiveRelay(t)
	now := time.Now().Unix()
	r.add(note(t, "one", now))
	r.add(note(t, "two", now))
	path := filepath.Join(t.TempDir(), "state.json")

	state, err := LoadFollowState(path)
	if err != nil {
		t.Fatal(err)
	}
	c := startFollow(t, []string{r.url}, state)
	c.wait(t, 2)
	c.stop()
	if <-c.done; c.err != nil {
		t.Fatal(c.err)
	}

	r.add(note(t, "three", now+1))
	state, err = LoadFollowState(path)
	if err != nil {
		t.Fatal(err)
	}
	c = startFollow(t, []string{r.url}, state)
	got := c.wait(t, 1)
	time.Sleep(200 * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.got) != 1 || got[0] != "three" {
		t.Fatalf("after restart: %v", c.got)
	}
}

func TestFollowFailures(t *testing.T) {
	targets, _ := TargetsFromRelayList([]string{"ws://127.0.0.1:1"})
	err := Follow(context.Background(), targets, kindFilter(), FollowOptions{DialTimeout: time.Second, Emit: func(*nip01.Event, string) error { return nil }})
	if !errors.Is(err, ErrNoReachableTargets) {
		t.Fatalf("unreachable: %v", err)
	}

	db := filepath.Join(t.TempDir(), "x.db")
	if err := os.WriteFile(db, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := TargetsFromRelayList([]string{db})
	if err != nil {
		t.Fatal(err)
	}
	if err := Follow(context.Background(), local, kindFilter(), FollowOptions{}); !errors.Is(err, ErrFollowLocal) {
		t.Fatalf("local: %v", err)
	}

	// A forged event never reaches Emit.
	r := newLiveRelay(t)
	bad := note(t, "forged", time.Now().Unix())
	bad.Content = "changed"
	r.add(bad)
	r.add(note(t, "good", time.Now().Unix()))
	c := startFollow(t, []string{r.url}, nil)
	got := c.wait(t, 1)
	time.Sleep(200 * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.got) != 1 || got[0] != "good" {
		t.Fatalf("got %v", c.got)
	}
}

// One reachable relay is enough; the other is retried quietly.
func TestFollowPartialOutage(t *testing.T) {
	fastBackoff(t)
	r := newLiveRelay(t)
	r.add(note(t, "up", time.Now().Unix()))
	c := startFollow(t, []string{"ws://127.0.0.1:1", r.url}, nil)
	c.wait(t, 1)
	c.stop()
	if <-c.done; c.err != nil {
		t.Fatalf("partial outage: %v", c.err)
	}
}

func TestFollowStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	s, _ := LoadFollowState(path)
	for i := 0; i < maxSeen+10; i++ {
		s.Mark(string(rune('a'+i%26))+time.Duration(i).String(), "r", int64(i))
	}
	if !s.Mark("x", "r", 5) || s.Mark("x", "r", 5) {
		t.Fatal("Mark must report a repeat")
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	back, err := LoadFollowState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.order) != maxSeen || !back.Seen("x") || back.Since("r") != int64(maxSeen+9) {
		t.Fatalf("seen %d since %d", len(back.order), back.Since("r"))
	}
	if _, err := LoadFollowState(filepath.Join(t.TempDir(), "missing.json")); err != nil {
		t.Fatal("a missing file is an empty state")
	}
}
