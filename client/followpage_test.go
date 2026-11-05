package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/nip01"
)

// cappedRelay serves a REQ's stored events as most relays do: those within its since and until, the
// newest first, at most its cap (strfry's maxFilterLimit 500, others 1000), then EOSE. reqs counts REQs.
type cappedRelay struct {
	url    string
	cap    int
	mu     sync.Mutex
	events []*nip01.Event
	conns  map[*websocket.Conn]bool
	reqs   int
}

func newCappedRelay(t *testing.T, cap int) *cappedRelay {
	r := &cappedRelay{cap: cap, conns: map[*websocket.Conn]bool{}}
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r.mu.Lock()
		r.conns[conn] = true
		r.mu.Unlock()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg []json.RawMessage
			if json.Unmarshal(data, &msg) != nil || len(msg) < 3 {
				continue
			}
			var kind, sub string
			_ = json.Unmarshal(msg[0], &kind)
			if kind != "REQ" {
				continue
			}
			_ = json.Unmarshal(msg[1], &sub)
			var f nip01.SubscriptionFilter
			_ = json.Unmarshal(msg[2], &f)
			r.mu.Lock()
			r.reqs++
			var in []*nip01.Event
			for _, ev := range r.events {
				if ev.CreatedAt >= f.Since && (f.Until == 0 || ev.CreatedAt <= f.Until) {
					in = append(in, ev)
				}
			}
			sort.SliceStable(in, func(i, j int) bool { return in[i].CreatedAt > in[j].CreatedAt })
			if len(in) > r.cap {
				in = in[:r.cap]
			}
			for _, ev := range in {
				_ = conn.WriteJSON([]any{"EVENT", sub, ev})
			}
			_ = conn.WriteJSON([]any{"EOSE", sub})
			r.mu.Unlock()
		}
	}))
	t.Cleanup(srv.Close)
	r.url = "ws" + srv.URL[len("http"):]
	return r
}

// A peer's answer, sent while the follow was away (a dropped connection, a restart), its wrap dated
// back as NIP-59 has it, sat
// under a flood of junk wraps dated now. The resubscription took only what the relay served, the
// newest 500: the answer was never delivered. Now the stored events are paged back (until), so the
// first resumed follow delivers it, once.
func TestFollowCappedResumeDeliversAnswer(t *testing.T) {
	fastBackoff(t)
	r := newCappedRelay(t, 500)
	now := time.Now().Unix()
	r.events = append(r.events, note(t, "the peer's last message", now-3*3600))
	path := filepath.Join(t.TempDir(), "s.json")
	follow := func() []string {
		state, _ := LoadFollowState(path)
		targets, _ := TargetsFromRelayList([]string{r.url})
		var mu sync.Mutex
		var got []string
		followUntilPaged(t, targets, kindFilter(), FollowOptions{State: state, Overlap: 48*time.Hour + 10*time.Minute,
			Emit: func(ev *nip01.Event, _ string) error {
				mu.Lock()
				defer mu.Unlock()
				got = append(got, ev.Content)
				if len(ev.Content) > 4 && ev.Content[:4] == "junk" {
					return ErrSeenOnly
				}
				return nil
			}})
		mu.Lock()
		defer mu.Unlock()
		return got
	}
	t.Logf("first follow: %d delivered", len(follow()))
	// away: the answer (its wrap dated a day back) and 600 junk wraps dated now
	r.mu.Lock()
	r.events = append(r.events, note(t, "the peer's answer: OBJECT", now-24*3600))
	for i := range 600 {
		r.events = append(r.events, note(t, fmt.Sprintf("junk %d", i), now-int64(i%60)))
	}
	r.mu.Unlock()
	answers := 0
	for i := range 3 {
		r.mu.Lock()
		r.reqs = 0
		r.mu.Unlock()
		got := follow()
		answer := false
		for _, g := range got {
			answer = answer || g == "the peer's answer: OBJECT"
		}
		if answer {
			answers++
		}
		r.mu.Lock()
		reqs := r.reqs
		r.mu.Unlock()
		t.Logf("resumed follow %d: %d delivered, %d REQs; the answer among them: %v", i, len(got), reqs, answer)
		if i == 0 && !answer {
			t.Fatal("the first resumed follow didn't deliver the answer")
		}
		if reqs > followPages+1 {
			t.Fatalf("%d REQs", reqs)
		}
	}
	if answers != 1 {
		t.Fatalf("the answer delivered %d times", answers)
	}
}

// A follow's paging stops at its budget, and says so.
func TestFollowPagingBudget(t *testing.T) {
	fastBackoff(t)
	old := followPages
	followPages = 2
	t.Cleanup(func() { followPages = old })
	r := newCappedRelay(t, 60)
	now := time.Now().Unix()
	for i := range 300 {
		r.events = append(r.events, note(t, fmt.Sprintf("n %d", i), now-int64(i)))
	}
	targets, _ := TargetsFromRelayList([]string{r.url})
	var mu sync.Mutex
	got, warned := 0, ""
	followUntilPaged(t, targets, kindFilter(), FollowOptions{
		Emit: func(*nip01.Event, string) error { mu.Lock(); got++; mu.Unlock(); return nil },
		Warn: func(_ string, err error) { mu.Lock(); warned = err.Error(); mu.Unlock() }})
	mu.Lock()
	defer mu.Unlock()
	if got <= 60 || got >= 300 || !strings.Contains(warned, "weren't read") {
		t.Fatalf("%d delivered, warned %q", got, warned)
	}
}

// followUntilPaged runs Follow until its subscription's stored events, every page of them, were
// handled (followPaged), then stops it: a test waits on that, never on a wall clock. The minute is
// only how long a broken follow may hang before the test says so.
func followUntilPaged(t *testing.T, targets *TargetsSpec, filters *nip01.SubscriptionFilterGroup, opts FollowOptions) {
	t.Helper()
	paged := make(chan struct{}, 1)
	followPaged = func(string) {
		select {
		case paged <- struct{}{}:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Follow(ctx, targets, filters, opts) }()
	var why string
	select {
	case <-paged:
	case err := <-done:
		done <- err
		why = fmt.Sprintf("the follow ended before its stored events were read: %v", err)
	case <-time.After(time.Minute):
		why = "the follow's stored events weren't all read within a minute"
	}
	cancel()
	err := <-done
	followPaged = nil // Follow returned: no goroutine of it reads the hook any more
	if why != "" {
		t.Fatal(why)
	}
	if err != nil {
		t.Fatal(err)
	}
}
