package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip42"
	"github.com/ohstr/nmilat/nip46"
)

// loginRelay is a fake NIP-42 relay. It refuses REQ and EVENT with
// "auth-required:" until a valid login for its own URL and challenge.
type loginRelay struct {
	t         *testing.T
	server    *httptest.Server
	url       string
	challenge string

	// challengeLate sends AUTH only after the first refused REQ/EVENT.
	challengeLate bool
	// rejectAuth answers every login with OK false.
	rejectAuth bool
	// rechallenge sends a fresh AUTH after every refusal.
	rechallenge bool

	event *nip01.Event

	mu     sync.Mutex
	logins []*nip01.Event
	reqs   int
}

func newLoginRelay(t *testing.T, challenge string, event *nip01.Event, opts ...func(*loginRelay)) *loginRelay {
	t.Helper()
	r := &loginRelay{t: t, challenge: challenge, event: event}
	for _, o := range opts {
		o(r)
	}
	upgrader := websocket.Upgrader{}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
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

func (r *loginRelay) serve(conn *websocket.Conn) {
	var wmu sync.Mutex
	write := func(v ...interface{}) {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.WriteJSON(v)
	}
	challenged := false
	challenge := func() {
		challenged = true
		write("AUTH", r.challenge)
	}
	if !r.challengeLate {
		challenge()
	}
	authed := false
	refuse := func(kind, id string) {
		if !challenged || r.rechallenge {
			challenge()
		}
		if kind == "EVENT" {
			write("OK", id, false, "auth-required: valid NIP-42 authentication required")
		} else {
			write("CLOSED", id, "auth-required: valid NIP-42 authentication required")
		}
	}
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg []json.RawMessage
		if json.Unmarshal(data, &msg) != nil || len(msg) < 2 {
			return
		}
		var kind string
		_ = json.Unmarshal(msg[0], &kind)
		switch kind {
		case "AUTH":
			var ev nip01.Event
			_ = json.Unmarshal(msg[1], &ev)
			r.mu.Lock()
			r.logins = append(r.logins, &ev)
			r.mu.Unlock()
			err := nip42.ValidateAuthEvent(ev.Kind, ev.Tags, ev.CreatedAt, r.challenge, r.url)
			if err == nil {
				err = ev.Verify()
			}
			if err != nil || r.rejectAuth {
				write("OK", ev.ID, false, "restricted: not a member")
				continue
			}
			authed = true
			write("OK", ev.ID, true, "auth-success")
		case "REQ":
			var subID string
			_ = json.Unmarshal(msg[1], &subID)
			r.mu.Lock()
			r.reqs++
			r.mu.Unlock()
			if !authed {
				refuse("REQ", subID)
				continue
			}
			if r.event != nil {
				write("EVENT", subID, r.event)
			}
			write("EOSE", subID)
		case "EVENT":
			var ev nip01.Event
			_ = json.Unmarshal(msg[1], &ev)
			if !authed {
				refuse("EVENT", ev.ID)
				continue
			}
			write("OK", ev.ID, true, "")
		}
	}
}

func (r *loginRelay) loginCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.logins)
}

func (r *loginRelay) login(i int) *nip01.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.logins[i]
}

func (r *loginRelay) reqCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reqs
}

func (r *loginRelay) targets(t *testing.T) *TargetsSpec {
	t.Helper()
	targets, err := TargetsFromRelayList([]string{r.url})
	if err != nil {
		t.Fatal(err)
	}
	return targets
}

func kindFilter() *nip01.SubscriptionFilterGroup {
	return nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{Kinds: []int{1}})
}

func signedNote(t *testing.T) *nip01.Event {
	t.Helper()
	ev := nip01.NewEvent(1, "login test")
	if err := ev.Sign(testAuthPrivKey); err != nil {
		t.Fatal(err)
	}
	return ev
}

// denySigner refuses every request the way a policy signer does.
type denySigner struct{ pub string }

func (d denySigner) PubKey() string { return d.pub }
func (d denySigner) Sign(context.Context, *nip01.Event) error {
	return nip46.Deny(`rule "rest": denied by rule`)
}

// countingSigner records every event it is asked to sign.
type countingSigner struct {
	Signer
	mu    sync.Mutex
	kinds []int
}

func (c *countingSigner) signed() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.kinds...)
}

func (c *countingSigner) Sign(ctx context.Context, ev *nip01.Event) error {
	c.mu.Lock()
	c.kinds = append(c.kinds, ev.Kind)
	c.mu.Unlock()
	return c.Signer.Sign(ctx, ev)
}

func withShortLoginWait(t *testing.T) {
	t.Helper()
	old := loginWait
	loginWait = 2 * time.Second
	t.Cleanup(func() { loginWait = old })
}

func TestLogin_ReadSignsOnlyOneLoginPerConnection(t *testing.T) {
	ev := signedNote(t)
	r := newLoginRelay(t, "c1", ev)
	sgn := &countingSigner{Signer: KeySigner(testAuthPrivKey)}

	events, err := QueryTargetsWithAuth(context.Background(), r.targets(t), kindFilter(), 5*time.Second, sgn)
	if err != nil || len(events) != 1 || events[0].ID != ev.ID {
		t.Fatalf("events = %v, err = %v", events, err)
	}
	if k := sgn.signed(); len(k) != 1 || k[0] != nip42.KindClientAuth {
		t.Fatalf("signed kinds = %v, want exactly one 22242", k)
	}
	login := r.login(0)
	if tagValue(login, "relay") != r.url || tagValue(login, "challenge") != "c1" || login.Content != "" {
		t.Fatalf("login event = %+v", login)
	}
}

func TestLogin_ChallengeOnlyAfterFirstREQ(t *testing.T) {
	ev := signedNote(t)
	r := newLoginRelay(t, "late", ev, func(r *loginRelay) { r.challengeLate = true })

	events, err := QueryTargetsWithAuth(context.Background(), r.targets(t), kindFilter(), 5*time.Second, KeySigner(testAuthPrivKey))
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %v, err = %v", events, err)
	}
	if r.loginCount() != 1 || r.reqCount() != 2 {
		t.Fatalf("logins = %d, reqs = %d; want 1 and 2", r.loginCount(), r.reqCount())
	}
}

func TestLogin_SeveralRelaysEachWithItsOwnChallenge(t *testing.T) {
	ev := signedNote(t)
	a := newLoginRelay(t, "challenge-a", ev)
	b := newLoginRelay(t, "challenge-b", ev, func(r *loginRelay) { r.challengeLate = true })
	targets, err := TargetsFromRelayList([]string{a.url, b.url})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := QueryTargetsWithAuth(context.Background(), targets, kindFilter(), 5*time.Second, KeySigner(testAuthPrivKey)); err != nil {
		t.Fatal(err)
	}
	for _, r := range []*loginRelay{a, b} {
		if r.loginCount() != 1 {
			t.Fatalf("%s: logins = %d, want 1", r.url, r.loginCount())
		}
		l := r.login(0)
		if tagValue(l, "relay") != r.url || tagValue(l, "challenge") != r.challenge {
			t.Fatalf("%s got a login for relay %q challenge %q", r.url, tagValue(l, "relay"), tagValue(l, "challenge"))
		}
	}
}

func TestLogin_PublishRetriesOnceAfterLogin(t *testing.T) {
	r := newLoginRelay(t, "pub", nil, func(r *loginRelay) { r.challengeLate = true })
	report, err := PublishToTargets(context.Background(), r.targets(t), []*nip01.Event{signedNote(t)}, KeySigner(testAuthPrivKey))
	if err != nil || !report.AllSucceeded() || report.LoginError != nil {
		t.Fatalf("report = %+v, err = %v", report, err)
	}
	if r.loginCount() != 1 {
		t.Fatalf("logins = %d, want 1", r.loginCount())
	}
}

func TestLogin_Failures(t *testing.T) {
	withShortLoginWait(t)
	cases := []struct {
		name   string
		signer Signer
		opts   []func(*loginRelay)
		want   LoginReason
	}{
		{"no signer", nil, nil, LoginRequired},
		{"signer refused", denySigner{pub: "00"}, nil, LoginSignerRefused},
		{"relay refused", KeySigner(testAuthPrivKey), []func(*loginRelay){func(r *loginRelay) { r.rejectAuth = true }}, LoginRelayRefused},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/read", func(t *testing.T) {
			r := newLoginRelay(t, "f", nil, tc.opts...)
			_, err := QueryTargetsWithAuth(context.Background(), r.targets(t), kindFilter(), 5*time.Second, tc.signer)
			var le *LoginError
			if !errors.As(err, &le) || le.Reason != tc.want || !errors.Is(err, ErrRestricted) {
				t.Fatalf("err = %v, want reason %s", err, tc.want)
			}
		})
		t.Run(tc.name+"/publish", func(t *testing.T) {
			r := newLoginRelay(t, "f", nil, tc.opts...)
			report, err := PublishToTargets(context.Background(), r.targets(t), []*nip01.Event{signedNote(t)}, tc.signer)
			if err != nil {
				t.Fatal(err)
			}
			var le *LoginError
			if !errors.As(report.LoginError, &le) || le.Reason != tc.want {
				t.Fatalf("login error = %v, want reason %s", report.LoginError, tc.want)
			}
		})
	}
}

func TestLogin_NeverLoops(t *testing.T) {
	withShortLoginWait(t)
	r := newLoginRelay(t, "loop", nil, func(r *loginRelay) { r.rejectAuth = true; r.rechallenge = true })
	sgn := &countingSigner{Signer: KeySigner(testAuthPrivKey)}
	report, err := PublishToTargets(context.Background(), r.targets(t), []*nip01.Event{signedNote(t)}, sgn)
	if err != nil || report.LoginError == nil {
		t.Fatalf("report = %+v, err = %v", report, err)
	}
	time.Sleep(200 * time.Millisecond)
	if r.loginCount() != 1 || len(sgn.signed()) != 1 {
		t.Fatalf("logins = %d, signed = %v; want one login", r.loginCount(), sgn.signed())
	}
}

// tamperSigner signs, then swaps the relay tag for another one.
type tamperSigner struct{ Signer }

func (ts tamperSigner) Sign(ctx context.Context, ev *nip01.Event) error {
	ev.Tags = [][]string{{"relay", "wss://other.example"}, {"challenge", tagValue(ev, "challenge")}}
	return ts.Signer.Sign(ctx, ev)
}

func TestSignLogin_RejectsAnotherEvent(t *testing.T) {
	ev, err := signLogin(context.Background(), KeySigner(testAuthPrivKey), "wss://a.example", "x")
	if err != nil || tagValue(ev, "relay") != "wss://a.example" || ev.Kind != nip42.KindClientAuth {
		t.Fatalf("ev = %+v, err = %v", ev, err)
	}
	if _, err := signLogin(context.Background(), tamperSigner{KeySigner(testAuthPrivKey)}, "wss://a.example", "x"); err == nil {
		t.Fatal("accepted a login signed for another relay")
	}
}

func tagValue(ev *nip01.Event, name string) string {
	for _, t := range ev.Tags {
		if len(t) >= 2 && t[0] == name {
			return t[1]
		}
	}
	return ""
}
