package climatrix

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip42"
)

// Raw is a bare NIP-01 client with full control over every frame -- for
// attack tests that a well-behaved client library would refuse to send.
type Raw struct {
	t   *testing.T
	url string
	ws  *websocket.Conn
	wmu sync.Mutex

	mu        sync.Mutex
	challenge string
	gotChal   chan struct{}
	oks       map[string]chan okMsg
	subs      map[string]*subState
	notices   []string
	closed    chan struct{}
	nextSub   atomic.Int64
}

type okMsg struct {
	ok  bool
	msg string
}

type subState struct {
	events chan *nip01.Event
	eose   chan struct{}
	closed chan string
	count  chan int
}

const rawWait = 5 * time.Second

// Dial connects without authenticating.
func Dial(t *testing.T, url string) *Raw {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	r := &Raw{t: t, url: url, ws: ws, gotChal: make(chan struct{}),
		oks: map[string]chan okMsg{}, subs: map[string]*subState{}, closed: make(chan struct{})}
	t.Cleanup(func() { _ = ws.Close() })
	go r.read()
	return r
}

// DialAs connects and authenticates as priv (NIP-42), failing the test
// if the relay rejects it.
func DialAs(t *testing.T, url, priv string) *Raw {
	t.Helper()
	r := Dial(t, url)
	if ok, msg := r.Auth(priv); !ok {
		t.Fatalf("AUTH as %s rejected: %s", priv[:8], msg)
	}
	return r
}

func (r *Raw) read() {
	defer close(r.closed)
	for {
		_, data, err := r.ws.ReadMessage()
		if err != nil {
			return
		}
		var msg []json.RawMessage
		if json.Unmarshal(data, &msg) != nil || len(msg) == 0 {
			continue
		}
		var typ string
		_ = json.Unmarshal(msg[0], &typ)
		str := func(i int) string {
			var s string
			if i < len(msg) {
				_ = json.Unmarshal(msg[i], &s)
			}
			return s
		}
		r.mu.Lock()
		switch typ {
		case "AUTH":
			if r.challenge == "" {
				r.challenge = str(1)
				close(r.gotChal)
			}
		case "OK":
			var ok bool
			if len(msg) > 2 {
				_ = json.Unmarshal(msg[2], &ok)
			}
			ch := r.okChan(str(1))
			select {
			case ch <- okMsg{ok, str(3)}:
			default:
			}
		case "EVENT":
			if s := r.subs[str(1)]; s != nil && len(msg) > 2 {
				var ev nip01.Event
				if json.Unmarshal(msg[2], &ev) == nil {
					select {
					case s.events <- &ev:
					default:
					}
				}
			}
		case "EOSE":
			if s := r.subs[str(1)]; s != nil {
				select {
				case s.eose <- struct{}{}:
				default:
				}
			}
		case "CLOSED":
			if s := r.subs[str(1)]; s != nil {
				select {
				case s.closed <- str(2):
				default:
				}
			}
		case "COUNT":
			if s := r.subs[str(1)]; s != nil && len(msg) > 2 {
				var c struct {
					Count int `json:"count"`
				}
				_ = json.Unmarshal(msg[2], &c)
				select {
				case s.count <- c.Count:
				default:
				}
			}
		case "NOTICE":
			r.notices = append(r.notices, str(1))
		}
		r.mu.Unlock()
	}
}

func (r *Raw) okChan(id string) chan okMsg {
	ch, ok := r.oks[id]
	if !ok {
		ch = make(chan okMsg, 1)
		r.oks[id] = ch
	}
	return ch
}

// Send writes any JSON value as one frame.
func (r *Raw) Send(v any) {
	r.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		r.t.Fatal(err)
	}
	r.SendText(string(b))
}

// SendText writes a raw text frame, malformed or not.
func (r *Raw) SendText(s string) {
	r.wmu.Lock()
	defer r.wmu.Unlock()
	_ = r.ws.WriteMessage(websocket.TextMessage, []byte(s))
}

// Challenge waits for the relay's AUTH challenge.
func (r *Raw) Challenge() string {
	r.t.Helper()
	select {
	case <-r.gotChal:
	case <-time.After(rawWait):
		r.t.Fatalf("no AUTH challenge from %s", r.url)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.challenge
}

// Auth answers the challenge correctly as priv.
func (r *Raw) Auth(priv string) (bool, string) {
	r.t.Helper()
	ev := nip42.NewAuthEvent(r.Challenge(), r.url)
	if err := ev.Sign(priv); err != nil {
		r.t.Fatal(err)
	}
	return r.AuthEvent(ev)
}

// AuthEvent sends ev as an AUTH answer and waits for its OK.
func (r *Raw) AuthEvent(ev *nip01.Event) (bool, string) {
	r.t.Helper()
	r.mu.Lock()
	ch := r.okChan(ev.ID)
	r.mu.Unlock()
	r.Send([]any{"AUTH", ev})
	return r.waitOK(ch)
}

// Publish sends ev and returns the relay's OK.
func (r *Raw) Publish(ev *nip01.Event) (bool, string) {
	r.t.Helper()
	r.mu.Lock()
	ch := r.okChan(ev.ID)
	r.mu.Unlock()
	r.Send([]any{"EVENT", ev})
	return r.waitOK(ch)
}

func (r *Raw) waitOK(ch chan okMsg) (bool, string) {
	r.t.Helper()
	select {
	case m := <-ch:
		return m.ok, m.msg
	case <-r.closed:
		return false, "connection closed"
	case <-time.After(rawWait):
		return false, "timeout: no OK"
	}
}

func (r *Raw) newSub() (string, *subState) {
	id := fmt.Sprintf("s%d", r.nextSub.Add(1))
	s := &subState{events: make(chan *nip01.Event, 1024), eose: make(chan struct{}, 1),
		closed: make(chan string, 1), count: make(chan int, 1)}
	r.mu.Lock()
	r.subs[id] = s
	r.mu.Unlock()
	return id, s
}

// F is one REQ filter.
type F map[string]any

// Req runs a one-shot query: events until EOSE, or the CLOSED reason.
func (r *Raw) Req(filters ...F) (events []*nip01.Event, closed string) {
	r.t.Helper()
	id, s := r.newSub()
	r.Send(append([]any{"REQ", id}, toAny(filters)...))
	for {
		select {
		case ev := <-s.events:
			events = append(events, ev)
		case <-s.eose:
			for {
				select {
				case ev := <-s.events:
					events = append(events, ev)
				default:
					r.Send([]any{"CLOSE", id})
					return events, ""
				}
			}
		case msg := <-s.closed:
			return events, msg
		case <-time.After(rawWait):
			r.t.Fatalf("REQ %v: no EOSE/CLOSED", filters)
		}
	}
}

// Count runs a NIP-45 COUNT; closed is the CLOSED reason if refused.
func (r *Raw) Count(filters ...F) (n int, closed string) {
	r.t.Helper()
	id, s := r.newSub()
	r.Send(append([]any{"COUNT", id}, toAny(filters)...))
	select {
	case n := <-s.count:
		return n, ""
	case msg := <-s.closed:
		return 0, msg
	case <-time.After(rawWait):
		return -1, "timeout: no COUNT"
	}
}

// Live opens a subscription and returns it after EOSE; Next waits for
// the next live event.
type Live struct {
	r      *Raw
	id     string
	s      *subState
	Closed string
}

func (r *Raw) Live(filters ...F) *Live {
	r.t.Helper()
	id, s := r.newSub()
	r.Send(append([]any{"REQ", id}, toAny(filters)...))
	l := &Live{r: r, id: id, s: s}
	for {
		select {
		case <-s.events:
		case <-s.eose:
			return l
		case msg := <-s.closed:
			l.Closed = msg
			return l
		case <-time.After(rawWait):
			r.t.Fatalf("live REQ: no EOSE")
		}
	}
}

// Next returns the next live event, or nil after wait.
func (l *Live) Next(wait time.Duration) *nip01.Event {
	select {
	case ev := <-l.s.events:
		return ev
	case msg := <-l.s.closed:
		l.Closed = msg
		return nil
	case <-time.After(wait):
		return nil
	}
}

// Saw reports whether event id arrives on this subscription within wait,
// skipping any other live events.
func (l *Live) Saw(id string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return false
		}
		ev := l.Next(left)
		if ev == nil {
			return false
		}
		if ev.ID == id {
			return true
		}
	}
}

// Notices returns every NOTICE so far.
func (r *Raw) Notices() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.notices...)
}

// IsClosed reports whether the relay dropped the connection.
func (r *Raw) IsClosed() bool {
	select {
	case <-r.closed:
		return true
	default:
		return false
	}
}

func toAny(fs []F) []any {
	out := make([]any, len(fs))
	for i, f := range fs {
		out[i] = map[string]any(f)
	}
	return out
}

// Ev builds and signs an event as priv.
func Ev(t *testing.T, priv string, kind int, content string, tags ...[]string) *nip01.Event {
	t.Helper()
	ev := nip01.NewEvent(kind, content, tags...)
	if err := ev.Sign(priv); err != nil {
		t.Fatal(err)
	}
	return ev
}


func hasPrefix(msg string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}
