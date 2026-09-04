package bunker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip04"
	"github.com/ohstr/nmilat/nip44"
	"github.com/ohstr/nmilat/nip46"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/ohstr/nmilat/utils"
	"github.com/ohstr/nmilat/wire"
)

// connState is one connected client's write lock plus its live
// subscriptions -- gorilla/websocket forbids concurrent writes to the same
// *websocket.Conn from multiple goroutines, and fanOut (triggered by
// whichever connection's read-loop goroutine happens to receive an EVENT)
// writes to every OTHER matching connection too, so every write to a given
// conn (whether from its own read loop or another's fanOut) must go
// through that same conn's own mutex.
type connState struct {
	mu   sync.Mutex
	subs map[string]*nip01.SubscriptionFilterGroup
}

func (cs *connState) write(conn *websocket.Conn, msg []byte) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	_ = conn.WriteMessage(websocket.TextMessage, msg)
}

// fakeRelay is a minimal single-process Nostr relay used only to exercise
// Daemon's own subscribe/dispatch/publish wiring end-to-end, without a real
// network -- the same spirit as client/relay_dial_test.go's mockWSServer,
// extended to actually fan out EVENT/REQ/OK like a real relay does, since
// Daemon needs a relay that talks back, not just accepts a connection.
type fakeRelay struct {
	server *httptest.Server
	url    *url.URL

	mu     sync.Mutex
	conns  map[*websocket.Conn]*connState
	events []*nip01.Event // published events, replayed to a matching REQ's filter before its EOSE -- the same "stored events, then EOSE" a real relay does, needed for tests where a REQ arrives after the matching EVENT was already published
}

func newFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()
	fr := &fakeRelay{conns: map[*websocket.Conn]*connState{}}

	upgrader := websocket.Upgrader{}
	fr.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		defer fr.forget(conn)

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			fr.handle(conn, data)
		}
	}))
	t.Cleanup(fr.server.Close)

	u, err := url.Parse("ws" + fr.server.URL[len("http"):])
	if err != nil {
		t.Fatal(err)
	}
	fr.url = u
	return fr
}

func (fr *fakeRelay) forget(conn *websocket.Conn) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	delete(fr.conns, conn)
}

func (fr *fakeRelay) stateFor(conn *websocket.Conn) *connState {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	cs, ok := fr.conns[conn]
	if !ok {
		cs = &connState{subs: map[string]*nip01.SubscriptionFilterGroup{}}
		fr.conns[conn] = cs
	}
	return cs
}

func (fr *fakeRelay) handle(conn *websocket.Conn, data []byte) {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || len(raw) == 0 {
		return
	}
	var kind string
	_ = json.Unmarshal(raw[0], &kind)

	cs := fr.stateFor(conn)

	switch kind {
	case "REQ":
		if len(raw) < 3 {
			return
		}
		var subID string
		_ = json.Unmarshal(raw[1], &subID)

		filters := nip01.NewSubscriptionFilterGroup()
		for _, fraw := range raw[2:] {
			var f nip01.SubscriptionFilter
			if json.Unmarshal(fraw, &f) == nil {
				filters.Add(&f)
			}
		}

		cs.mu.Lock()
		cs.subs[subID] = filters
		cs.mu.Unlock()

		fr.mu.Lock()
		stored := make([]*nip01.Event, len(fr.events))
		copy(stored, fr.events)
		fr.mu.Unlock()
		for _, ev := range stored {
			if filters.Match(ev) {
				cs.write(conn, mustJSON([]any{"EVENT", subID, ev}))
			}
		}

		cs.write(conn, mustJSON([]any{"EOSE", subID}))

	case "EVENT":
		if len(raw) != 2 {
			return
		}
		var ev nip01.Event
		if err := json.Unmarshal(raw[1], &ev); err != nil {
			return
		}
		cs.write(conn, mustJSON([]any{"OK", ev.ID, true, ""}))
		fr.mu.Lock()
		fr.events = append(fr.events, &ev)
		fr.mu.Unlock()
		fr.fanOut(&ev)

	case "CLOSE":
		if len(raw) != 2 {
			return
		}
		var subID string
		_ = json.Unmarshal(raw[1], &subID)
		cs.mu.Lock()
		delete(cs.subs, subID)
		cs.mu.Unlock()
	}
}

// fanOut delivers ev to every connection with a matching subscription --
// including, per real relay behavior, back to the connection that
// published it, if that connection's own filter also matches (e.g. a
// signer subscribed on kind:24133/#p:self will see a client's own request
// event delivered to it, which is exactly the delivery path Daemon relies
// on).
func (fr *fakeRelay) fanOut(ev *nip01.Event) {
	fr.mu.Lock()
	targets := make(map[*websocket.Conn]*connState, len(fr.conns))
	for conn, cs := range fr.conns {
		targets[conn] = cs
	}
	fr.mu.Unlock()

	for conn, cs := range targets {
		cs.mu.Lock()
		matches := make([]string, 0, len(cs.subs))
		for subID, filters := range cs.subs {
			if filters.Match(ev) {
				matches = append(matches, subID)
			}
		}
		cs.mu.Unlock()

		for _, subID := range matches {
			cs.write(conn, mustJSON([]any{"EVENT", subID, ev}))
		}
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// testNIP46Client simulates a real NIP-46 "app" -- one keypair, one relay
// connection, one persistent request subscription -- for integration
// tests that need to drive a Daemon through its actual wire protocol
// rather than calling Handler.Handle directly. Generalizes what used to
// be a hand-rolled connect+subscribe+encrypt+sign+send+wait block
// duplicated in every such test, so a new scenario (a new encryption
// shape, a new method) is a few lines instead of a copy-pasted block --
// see sendEncryptOpts's own doc comment for why that duplication mattered
// in practice: it already hid a real bug once.
type testNIP46Client struct {
	t         *testing.T
	priv, pub string
	conn      *relayclient.Connection
	events    <-chan *wire.EventSubscriptionResponse
}

func newTestNIP46Client(t *testing.T, ctx context.Context, relayURL *url.URL, priv string) *testNIP46Client {
	t.Helper()
	pub, err := utils.GetPublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := relayclient.Connect(ctx, relayURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)

	subID := fmt.Sprintf("test-client-%s", pub[:8])
	conn.SubscribeWithID(subID, nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds: []int{nip46.KindRequest},
		Tags:  map[string][]string{"p": {pub}},
	}))

	return &testNIP46Client{t: t, priv: priv, pub: pub, conn: conn, events: conn.Events(subID)}
}

// sendEncryptOpts controls exactly how send encrypts and tags a request,
// independently of each other -- deliberately allowing shapes a correctly
// implemented client would never produce, since that's exactly what
// TestDaemon_EncryptionScenarios needs to reproduce every real-world
// mismatch found so far: nip46.ParseRequestEvent trusts the "encryption"
// tag's presence/exact-string-value alone (see parseRequestEventFallback's
// own doc comment for the two shapes that used to slip past it -- no tag
// at all, and a tag of "nip44" rather than nip46's own "nip44_v2" constant
// -- both of which a real, correctly-behaving client can produce and
// neither of which is "corrupt data").
type sendEncryptOpts struct {
	encryption string // cipher actually used to encrypt content; "" defaults to nip46.EncryptionNIP04
	tag        string // "encryption" tag value attached to the event, independent of `encryption`; "" omits the tag entirely
	corrupt    bool   // if true, content is garbage under any scheme regardless of encryption/tag
}

// send builds, encrypts per opts, signs, and publishes one request event
// addressed to signerPub, returning its request ID.
func (c *testNIP46Client) send(signerPub, method string, params []string, opts sendEncryptOpts) string {
	c.t.Helper()
	reqID := fmt.Sprintf("req-%s-%d", method, time.Now().UnixNano())

	content := "not valid ciphertext under any scheme"
	if !opts.corrupt {
		encryption := opts.encryption
		if encryption == "" {
			encryption = nip46.EncryptionNIP04
		}
		plaintext, err := json.Marshal(nip46.Request{RequestID: reqID, Method: method, Params: params})
		if err != nil {
			c.t.Fatal(err)
		}

		var ciphertext string
		if encryption == nip46.EncryptionNIP44V2 {
			key, err := (&Handler{IdentityPriv: c.priv}).nip44ConversationKey(signerPub)
			if err != nil {
				c.t.Fatal(err)
			}
			ciphertext, err = nip44.Encrypt(string(plaintext), key)
			if err != nil {
				c.t.Fatal(err)
			}
		} else {
			var err error
			ciphertext, err = nip04.Encrypt(string(plaintext), c.priv, signerPub)
			if err != nil {
				c.t.Fatal(err)
			}
		}
		content = ciphertext
	}

	tags := [][]string{{"p", signerPub}}
	if opts.tag != "" {
		tags = append(tags, []string{"encryption", opts.tag})
	}
	ev := nip01.NewUnsignedEvent(nip46.KindRequest, c.pub, content, tags...)
	if err := ev.Sign(c.priv); err != nil {
		c.t.Fatal(err)
	}
	if !c.conn.Send(ev) {
		c.t.Fatal("failed to send request over the fake relay")
	}
	return reqID
}

// waitForResponse blocks for reqID's response (or timeout), decrypting it
// the same way a real client would -- ParseResponseEvent auto-detects the
// scheme from the response's own tag, which handleIncoming always sets to
// whatever scheme actually decrypted the request (see
// parseRequestEventFallback), not necessarily nip46's first guess.
// Returns (nil, false) on timeout instead of failing the test, since "no
// response" is exactly what a dropped/corrupt-request scenario expects.
func (c *testNIP46Client) waitForResponse(reqID string, timeout time.Duration) (*nip46.Response, bool) {
	c.t.Helper()
	select {
	case ev := <-c.events:
		resp, err := nip46.ParseResponseEvent(ev.Event, c.priv)
		if err != nil {
			c.t.Fatalf("received a response but failed to parse it: %v", err)
		}
		if resp.RequestID != reqID {
			c.t.Fatalf("RequestID = %q, want %q", resp.RequestID, reqID)
		}
		return &resp.Response, true
	case <-time.After(timeout):
		return nil, false
	}
}

func TestDaemon_SignEventRoundTrip(t *testing.T) {
	relay := newFakeRelay(t)

	signerPub, err := utils.GetPublicKey(testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}
	clientPub, err := utils.GetPublicKey(testClientPriv)
	if err != nil {
		t.Fatal(err)
	}

	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	kind := 1
	if err := store.Remember(clientPub, GrantForever(nip46.MethodSignEvent, &kind, time.Now())); err != nil {
		t.Fatal(err)
	}

	var logMu sync.Mutex
	var logs []string
	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Relays:       []string{relay.url.String()},
		Store:        store,
		Queue:        NewQueue(0, time.Minute),
		OnLog: func(format string, args ...any) {
			logMu.Lock()
			defer logMu.Unlock()
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = daemon.Run(ctx) }()

	// A second connection to the same fake relay plays the "app" side:
	// subscribe for responses addressed to it, then publish a sign_event
	// request addressed to the daemon's identity.
	clientConn, err := relayclient.Connect(ctx, relay.url)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	// SubscribeWithID + Events, not Subscribe -- Subscribe's channel
	// closes at EOSE (which the fake relay sends immediately after
	// registering the REQ, before any response exists yet), which would
	// otherwise race the response event against an already-closed
	// channel. This mirrors exactly why daemon.go itself avoids Subscribe
	// for its own long-lived listen.
	clientSubID := "test-client-sub"
	clientConn.SubscribeWithID(clientSubID, nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds: []int{nip46.KindRequest},
		Tags:  map[string][]string{"p": {clientPub}},
	}))
	respEvents := clientConn.Events(clientSubID)

	unsigned := nip01.NewUnsignedEvent(1, signerPub, "hello from a test client")
	rawEvent, _ := json.Marshal(unsigned)

	reqEvent, reqID, err := nip46.NewRequestEvent(testClientPriv, signerPub, nip46.MethodSignEvent, []string{string(rawEvent)}, nip46.EncryptionNIP04)
	if err != nil {
		t.Fatal(err)
	}
	if err := reqEvent.Sign(testClientPriv); err != nil {
		t.Fatal(err)
	}
	if !clientConn.Send(reqEvent) {
		t.Fatal("failed to send request over the fake relay")
	}

	select {
	case ev := <-respEvents:
		respEvent, err := nip46.ParseResponseEvent(ev.Event, testClientPriv)
		if err != nil {
			t.Fatalf("ParseResponseEvent() error = %v", err)
		}
		if respEvent.RequestID != reqID {
			t.Fatalf("RequestID = %q, want %q", respEvent.RequestID, reqID)
		}
		if respEvent.Error != "" {
			t.Fatalf("unexpected error response: %s", respEvent.Error)
		}
		var signed nip01.Event
		if err := json.Unmarshal([]byte(respEvent.Result), &signed); err != nil {
			t.Fatalf("result isn't a signed event: %v", err)
		}
		if err := signed.Verify(); err != nil {
			t.Fatalf("signed event failed to verify: %v", err)
		}
		if signed.Content != "hello from a test client" {
			t.Errorf("Content = %q, want the original content preserved", signed.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("never received a response over the fake relay")
	}

	logMu.Lock()
	defer logMu.Unlock()
	if len(logs) == 0 {
		t.Error("OnLog was never called -- the activity feed hook isn't wired up")
	}
	for _, line := range logs {
		if strings.Contains(line, testSignerPriv) || strings.Contains(line, "hello from a test client") {
			t.Errorf("log line leaked private key material or request content: %q", line)
		}
	}
}

// TestDaemon_NostrconnectFlow covers the signer-speaks-first direction
// (InitiateNostrconnect/sendRequestAndAwait) against the local fake relay
// -- previously this had zero coverage outside
// TestLive_BunkerFlow_ConnectAndSignEvent, which is gated behind the
// "integration" build tag and a real external relay, so it never runs by
// default. Plays the client side by hand (receive the daemon's own
// "connect" request, echo the secret back as a response) since this
// direction is the one place a "client" in these tests must respond
// rather than initiate.
func TestDaemon_NostrconnectFlow(t *testing.T) {
	relay := newFakeRelay(t)

	signerPub, err := utils.GetPublicKey(testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}
	clientPub, err := utils.GetPublicKey(testClientPriv)
	if err != nil {
		t.Fatal(err)
	}

	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Relays:       []string{relay.url.String()},
		Store:        store,
		Queue:        NewQueue(0, time.Minute),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = daemon.Run(ctx) }()

	// Client side: connect independently and listen for the daemon's own
	// outgoing "connect" request, addressed to this pubkey.
	clientConn, err := relayclient.Connect(ctx, relay.url)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	clientSubID := "test-nostrconnect-sub"
	clientConn.SubscribeWithID(clientSubID, nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds: []int{nip46.KindRequest},
		Tags:  map[string][]string{"p": {clientPub}},
	}))
	incoming := clientConn.Events(clientSubID)

	const secret = "nostrconnect-test-secret"
	schema := &nip46.NostrconnectSchema{
		ClientPublickey: clientPub,
		Relay:           relay.url,
		Secret:          secret,
	}

	initiateErr := make(chan error, 1)
	go func() { initiateErr <- daemon.InitiateNostrconnect(ctx, schema) }()

	select {
	case ev := <-incoming:
		req, err := nip46.ParseRequestEvent(ev.Event, testClientPriv)
		if err != nil {
			t.Fatalf("client failed to parse the daemon's connect request: %v", err)
		}
		if req.Method != nip46.MethodConnect {
			t.Fatalf("Method = %q, want connect", req.Method)
		}

		respEvent, err := nip46.NewResponseEvent(testClientPriv, signerPub, req.RequestID, secret, nip46.EncryptionNIP44V2)
		if err != nil {
			t.Fatal(err)
		}
		if err := respEvent.Sign(testClientPriv); err != nil {
			t.Fatal(err)
		}
		if !clientConn.Send(respEvent) {
			t.Fatal("failed to send the client's echo response")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client never received the daemon's outgoing connect request")
	}

	select {
	case err := <-initiateErr:
		if err != nil {
			t.Errorf("InitiateNostrconnect() error = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("InitiateNostrconnect never returned after the client's echo response")
	}
}

// TestDaemon_EncryptionScenarios is the systematic matrix behind
// parseRequestEventFallback: every request-encoding shape a real client
// (correctly-behaving or not) can produce, run through the actual wire
// protocol via testNIP46Client, rather than one hand-rolled test per bug
// as each was found. The two "must fall back" cases below are exactly the
// two real interop gaps this session found live (an actual client's
// request got dropped, caught via the newly-wired daemon log panel) --
// this is what stops a third variant from needing its own bespoke test
// discovered the same way.
func TestDaemon_EncryptionScenarios(t *testing.T) {
	tests := []struct {
		name       string
		opts       sendEncryptOpts
		wantResult string // "" if no response should arrive at all
	}{
		{
			name:       "nip04, tagged",
			opts:       sendEncryptOpts{encryption: nip46.EncryptionNIP04, tag: nip46.EncryptionNIP04},
			wantResult: "pong",
		},
		{
			name:       "nip04, untagged (the default a plain client sends)",
			opts:       sendEncryptOpts{encryption: nip46.EncryptionNIP04, tag: ""},
			wantResult: "pong",
		},
		{
			name:       "nip44_v2, correctly tagged",
			opts:       sendEncryptOpts{encryption: nip46.EncryptionNIP44V2, tag: nip46.EncryptionNIP44V2},
			wantResult: "pong",
		},
		{
			name:       "nip44, untagged -- must fall back (found live 2026-07-23)",
			opts:       sendEncryptOpts{encryption: nip46.EncryptionNIP44V2, tag: ""},
			wantResult: "pong",
		},
		{
			name:       `nip44, tagged "nip44" not nip46's "nip44_v2" -- must also fall back`,
			opts:       sendEncryptOpts{encryption: nip46.EncryptionNIP44V2, tag: "nip44"},
			wantResult: "pong",
		},
		{
			name:       "corrupt content -- must still be dropped, not laxly accepted",
			opts:       sendEncryptOpts{corrupt: true},
			wantResult: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relay := newFakeRelay(t)

			signerPub, err := utils.GetPublicKey(testSignerPriv)
			if err != nil {
				t.Fatal(err)
			}
			clientPub, err := utils.GetPublicKey(testClientPriv)
			if err != nil {
				t.Fatal(err)
			}

			store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			// Pre-grant ping so this isolates encryption-handling from the
			// separately-covered approval-queue behavior.
			if err := store.Remember(clientPub, GrantForever(nip46.MethodPing, nil, time.Now())); err != nil {
				t.Fatal(err)
			}

			var logMu sync.Mutex
			var logs []string
			daemon := NewDaemon(DaemonConfig{
				IdentityPriv: testSignerPriv,
				IdentityPub:  signerPub,
				Relays:       []string{relay.url.String()},
				Store:        store,
				Queue:        NewQueue(0, time.Minute),
				OnLog: func(format string, args ...any) {
					logMu.Lock()
					defer logMu.Unlock()
					logs = append(logs, fmt.Sprintf(format, args...))
				},
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = daemon.Run(ctx) }()

			client := newTestNIP46Client(t, ctx, relay.url, testClientPriv)
			reqID := client.send(signerPub, nip46.MethodPing, []string{}, tt.opts)

			resp, ok := client.waitForResponse(reqID, 3*time.Second)
			if tt.wantResult == "" {
				if ok {
					t.Fatalf("got a response, want none (dropped): %+v", resp)
				}
				logMu.Lock()
				defer logMu.Unlock()
				found := false
				for _, line := range logs {
					if strings.Contains(line, "dropped unparseable request") {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected a 'dropped unparseable request' log line; got: %v", logs)
				}
				return
			}

			if !ok {
				logMu.Lock()
				t.Fatalf("never received a response over the fake relay; daemon logs: %v", logs)
				logMu.Unlock()
			}
			if resp.Result != tt.wantResult {
				t.Errorf("Result = %q, want %q (Error = %q)", resp.Result, tt.wantResult, resp.Error)
			}
		})
	}
}

// TestDaemon_BunkerPairing_LandsInPendingQueue exercises the exact signal
// board.go's watchForPairing/hasPendingConnect depend on: a client that
// speaks first over the bunker:// flow, with no pre-existing grant (unlike
// TestLive_BunkerFlow_ConnectAndSignEvent's live-relay test, which
// pre-grants specifically to skip past this and isn't run by default
// anyway, being gated behind the "integration" build tag), must show up
// as a Method-"connect" entry in the queue -- i.e. in exactly what
// ListPending() (and so t.client.ListPending() from the TUI) returns.
// TestHandle_Connect_GoesThroughApprovalQueueWhenNoGrant already covers
// this at the Handler level directly; this covers the same behavior
// through the real wire protocol over a relay, the same round trip
// TestDaemon_SignEventRoundTrip proves for sign_event.
func TestDaemon_BunkerPairing_LandsInPendingQueue(t *testing.T) {
	relay := newFakeRelay(t)

	signerPub, err := utils.GetPublicKey(testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}

	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	queue := NewQueue(0, time.Minute)

	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Relays:       []string{relay.url.String()},
		Store:        store,
		Queue:        queue,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = daemon.Run(ctx) }()

	uri, err := daemon.NewBunkerPairing()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	secret := parsed.Query().Get("secret")
	if secret == "" {
		t.Fatal("bunker:// URI had no secret")
	}

	clientConn, err := relayclient.Connect(ctx, relay.url)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	connectReq, _, err := nip46.NewRequestEvent(testClientPriv, signerPub, nip46.MethodConnect,
		[]string{signerPub, secret}, nip46.EncryptionNIP04)
	if err != nil {
		t.Fatal(err)
	}
	if err := connectReq.Sign(testClientPriv); err != nil {
		t.Fatal(err)
	}
	if !clientConn.Send(connectReq) {
		t.Fatal("failed to send connect request over the fake relay")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if hasPendingConnect(queue.List()) {
			return // success
		}
		time.Sleep(20 * time.Millisecond)
	}

	pending := queue.List()
	t.Fatalf("connect request never landed in the pending queue; queue.List() = %+v", pending)
}

// TestDaemon_BunkerPairing_ApprovedEndToEnd goes one step further than
// TestDaemon_BunkerPairing_LandsInPendingQueue: approves the pending
// connect request (standing in for a human clicking "Approve" in the TUI)
// and confirms the client actually receives an "ack" over the wire --
// the full loop from "generate a URI" to "the connecting app is
// unblocked," not just "a pending entry appeared."
func TestDaemon_BunkerPairing_ApprovedEndToEnd(t *testing.T) {
	relay := newFakeRelay(t)

	signerPub, err := utils.GetPublicKey(testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}

	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	queue := NewQueue(0, time.Minute)

	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Relays:       []string{relay.url.String()},
		Store:        store,
		Queue:        queue,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = daemon.Run(ctx) }()

	uri, err := daemon.NewBunkerPairing()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	secret := parsed.Query().Get("secret")
	if secret == "" {
		t.Fatal("bunker:// URI had no secret")
	}

	client := newTestNIP46Client(t, ctx, relay.url, testClientPriv)
	reqID := client.send(signerPub, nip46.MethodConnect, []string{signerPub, secret}, sendEncryptOpts{})

	var pendingID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && pendingID == "" {
		for _, p := range queue.List() {
			if p.Method == nip46.MethodConnect {
				pendingID = p.ID
				break
			}
		}
		if pendingID == "" {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if pendingID == "" {
		t.Fatal("connect request never landed in the pending queue")
	}

	if err := queue.Resolve(pendingID, Allow, false); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	resp, ok := client.waitForResponse(reqID, 5*time.Second)
	if !ok {
		t.Fatal("client never received a response after the connect request was approved")
	}
	if resp.Error != "" {
		t.Fatalf("Error = %q, want a clean ack", resp.Error)
	}
	if resp.Result != "ack" {
		t.Errorf("Result = %q, want ack", resp.Result)
	}
}

// TestDaemon_BunkerPairing_WrongSecretThenRightSecret checks a wrong guess
// doesn't consume or otherwise disturb the real armed secret -- a
// mistyped/stale-copy attempt shouldn't lock a legitimate follow-up
// attempt out.
func TestDaemon_BunkerPairing_WrongSecretThenRightSecret(t *testing.T) {
	relay := newFakeRelay(t)

	signerPub, err := utils.GetPublicKey(testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}

	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	queue := NewQueue(0, time.Minute)

	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Relays:       []string{relay.url.String()},
		Store:        store,
		Queue:        queue,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = daemon.Run(ctx) }()

	uri, err := daemon.NewBunkerPairing()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	secret := parsed.Query().Get("secret")

	client := newTestNIP46Client(t, ctx, relay.url, testClientPriv)

	wrongReqID := client.send(signerPub, nip46.MethodConnect, []string{signerPub, secret + "-wrong"}, sendEncryptOpts{})
	wrongResp, ok := client.waitForResponse(wrongReqID, 5*time.Second)
	if !ok {
		t.Fatal("never received a response to the wrong secret")
	}
	if wrongResp.Error == "" {
		t.Error("expected an error response for the wrong secret")
	}

	rightReqID := client.send(signerPub, nip46.MethodConnect, []string{signerPub, secret}, sendEncryptOpts{})
	deadline := time.Now().Add(5 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
		for _, p := range queue.List() {
			if p.ID == rightReqID || (p.Method == nip46.MethodConnect) {
				found = true
				break
			}
		}
		if !found {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !found {
		t.Fatal("the correct secret, sent right after a wrong guess, never landed in the pending queue")
	}
}

// TestDaemon_BunkerPairing_SecretExpiresForReal proves the countdown
// board.go's showBunkerURI displays is truthful, not just cosmetic: past
// PairingSecretTTL (overridden here to something a test can actually
// wait out), even the objectively correct secret is rejected and never
// reaches the pending queue -- through the real wire protocol and real
// elapsed time, not by poking pendingSecretExpiresAt directly the way
// TestHandle_Connect_ExpiredSecretRejected does at the Handler level.
func TestDaemon_BunkerPairing_SecretExpiresForReal(t *testing.T) {
	relay := newFakeRelay(t)

	signerPub, err := utils.GetPublicKey(testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}

	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	queue := NewQueue(0, time.Minute)

	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Relays:       []string{relay.url.String()},
		Store:        store,
		Queue:        queue,
	})
	daemon.handler.pairingSecretTTL = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = daemon.Run(ctx) }()

	uri, err := daemon.NewBunkerPairing()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	secret := parsed.Query().Get("secret")

	time.Sleep(250 * time.Millisecond) // past the 100ms override, comfortably

	client := newTestNIP46Client(t, ctx, relay.url, testClientPriv)
	reqID := client.send(signerPub, nip46.MethodConnect, []string{signerPub, secret}, sendEncryptOpts{})

	resp, ok := client.waitForResponse(reqID, 5*time.Second)
	if !ok {
		t.Fatal("never received a response for the expired secret")
	}
	if resp.Error == "" {
		t.Error("expected an error response for a secret past its expiry, even though it's otherwise correct")
	}
	for _, p := range queue.List() {
		if p.Method == nip46.MethodConnect {
			t.Errorf("an expired secret's connect request must not reach the pending queue, got: %+v", p)
		}
	}
}

// TestDaemon_Revoke_BlocksSubsequentRequests proves Revoke's effect
// through the real wire protocol: a granted app's request auto-succeeds,
// revoking removes that grant, and the exact same request from the exact
// same app afterward goes back to needing a decision (Ask) instead of
// silently continuing to succeed. Revoke itself is already covered at the
// Store level directly (policy_test.go's TestRevoke) and over IPC
// (ipc_test.go) -- this is the one place its effect is proven to actually
// reach a live request in flight.
func TestDaemon_Revoke_BlocksSubsequentRequests(t *testing.T) {
	relay := newFakeRelay(t)

	signerPub, err := utils.GetPublicKey(testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}
	clientPub, err := utils.GetPublicKey(testClientPriv)
	if err != nil {
		t.Fatal(err)
	}

	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remember(clientPub, GrantForever(nip46.MethodPing, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	queue := NewQueue(0, time.Minute)

	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Relays:       []string{relay.url.String()},
		Store:        store,
		Queue:        queue,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = daemon.Run(ctx) }()

	client := newTestNIP46Client(t, ctx, relay.url, testClientPriv)

	firstReqID := client.send(signerPub, nip46.MethodPing, []string{}, sendEncryptOpts{})
	firstResp, ok := client.waitForResponse(firstReqID, 5*time.Second)
	if !ok {
		t.Fatal("never received a response to the first (granted) ping")
	}
	if firstResp.Result != "pong" {
		t.Fatalf("first ping Result = %q, want pong (Error = %q)", firstResp.Result, firstResp.Error)
	}

	revoked, err := store.Revoke(clientPub)
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("Revoke() reported nothing to revoke")
	}

	secondReqID := client.send(signerPub, nip46.MethodPing, []string{}, sendEncryptOpts{})

	// No grant left -- this must go to Ask, not auto-succeed. Confirm it
	// lands in the pending queue rather than getting an immediate pong.
	deadline := time.Now().Add(2 * time.Second)
	pending := false
	for time.Now().Before(deadline) && !pending {
		for _, p := range queue.List() {
			if p.ID == secondReqID {
				pending = true
				break
			}
		}
		if !pending {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !pending {
		t.Fatal("the second ping (after revoke) never went to the pending queue -- the revoked grant is still being honored")
	}

	select {
	case ev := <-client.events:
		t.Fatalf("got an immediate response to the post-revoke ping, want it held pending: %+v", ev.Event)
	case <-time.After(300 * time.Millisecond):
		// expected: still pending, no response yet
	}
}

// TestDaemon_RelayStatuses checks RelayStatuses reports a live fake relay
// as connected and an unreachable one as not, in the configured order --
// the data board.go's IdentityBar relay line is built on.
func TestDaemon_RelayStatuses(t *testing.T) {
	relay := newFakeRelay(t)

	signerPub, err := utils.GetPublicKey(testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}

	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	const deadRelay = "ws://127.0.0.1:1" // nothing listens here -- must never connect

	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Relays:       []string{relay.url.String(), deadRelay},
		Store:        store,
		Queue:        NewQueue(0, time.Minute),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = daemon.Run(ctx) }()

	// runRelay dials asynchronously -- poll briefly for the live relay's
	// connection to register rather than assuming a fixed sleep is both
	// long enough and not needlessly slow.
	deadline := time.Now().Add(3 * time.Second)
	var statuses []RelayStatus
	for time.Now().Before(deadline) {
		statuses = daemon.RelayStatuses()
		if len(statuses) == 2 && statuses[0].Connected {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(statuses) != 2 {
		t.Fatalf("RelayStatuses() returned %d entries, want 2", len(statuses))
	}
	if statuses[0].URL != relay.url.String() || !statuses[0].Connected {
		t.Errorf("statuses[0] = %+v, want the fake relay connected", statuses[0])
	}
	if statuses[1].URL != deadRelay || statuses[1].Connected {
		t.Errorf("statuses[1] = %+v, want the unreachable relay disconnected", statuses[1])
	}
}

// TestDaemon_RelayStatuses_ConnectingUntilFirstAttemptResolves guards the
// startup-race fix directly (board.go's AlertBar/formatRelayStatuses):
// RelayStatuses must report Connecting=true for a relay that hasn't had
// its first dial attempt resolve yet -- e.g. the very first Update a
// freshly Init'd AlertBar runs, synchronously, at the same instant
// Daemon.Run has only just spawned its runRelay goroutines and none of
// them have dialed anything yet -- and Connecting=false again the moment
// markAttempted fires for it, regardless of whether that attempt
// succeeded. Exercises markAttempted directly rather than through a real
// dial so this is deterministic, not timing-dependent.
func TestDaemon_RelayStatuses_ConnectingUntilFirstAttemptResolves(t *testing.T) {
	signerPub, err := utils.GetPublicKey(testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}
	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	const relayA, relayB = "wss://a.example", "wss://b.example"
	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Relays:       []string{relayA, relayB},
		Store:        store,
		Queue:        NewQueue(0, time.Minute),
	})

	for _, s := range daemon.RelayStatuses() {
		if s.Connected {
			t.Errorf("status %+v: want Connected=false before Run ever starts", s)
		}
		if !s.Connecting {
			t.Errorf("status %+v: want Connecting=true before any dial attempt has resolved", s)
		}
	}

	// relayA's first attempt resolves (as a failure, same as
	// runRelay would report on a real dial error) -- only it should
	// leave the "still trying for the first time" state; relayB is
	// untouched and must still read as Connecting.
	daemon.markAttempted(relayA)

	for _, s := range daemon.RelayStatuses() {
		wantConnecting := s.URL != relayA
		if s.Connecting != wantConnecting {
			t.Errorf("status %+v: Connecting = %v, want %v", s, s.Connecting, wantConnecting)
		}
	}
}

// TestDaemon_FetchProfile exercises fetchProfile end-to-end against the
// same fake relay TestDaemon_SignEventRoundTrip uses: a kind:0 for the
// signing identity is published first (fakeRelay now replays stored
// events matching a REQ's filter before that REQ's EOSE, the same order a
// real relay uses), then fetchProfile is called directly and Profile()
// checked for the resolved name/nip05.
func TestDaemon_FetchProfile(t *testing.T) {
	relay := newFakeRelay(t)

	signerPub, err := utils.GetPublicKey(testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}

	publisher, err := relayclient.Connect(context.Background(), relay.url)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()

	profileEvent := nip01.NewUnsignedEvent(0, signerPub, `{"name":"alice","display_name":"Alice","nip05":"alice@example.com"}`)
	if err := profileEvent.Sign(testSignerPriv); err != nil {
		t.Fatal(err)
	}
	if !publisher.Send(profileEvent) {
		t.Fatal("failed to publish the profile event")
	}
	// Send is fire-and-forget over the wire; give the fake relay's read
	// loop a moment to actually store it before the daemon's own REQ
	// (below) races in and finds nothing to replay.
	time.Sleep(50 * time.Millisecond)

	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Relays:       []string{relay.url.String()},
		Store:        store,
		Queue:        NewQueue(0, time.Minute),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	daemon.fetchProfile(ctx)

	name, nip05 := daemon.Profile()
	if name != "Alice" {
		t.Errorf("Profile() name = %q, want %q", name, "Alice")
	}
	if nip05 != "alice@example.com" {
		t.Errorf("Profile() nip05 = %q, want %q", nip05, "alice@example.com")
	}
}

// TestDaemon_FetchProfileNoneFound checks fetchProfile leaves Profile()
// at its empty zero value when no relay has anything for the identity --
// the permanent, ordinary state for a signer with no published kind:0,
// not an error.
func TestDaemon_FetchProfileNoneFound(t *testing.T) {
	relay := newFakeRelay(t)

	signerPub, err := utils.GetPublicKey(testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}

	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Relays:       []string{relay.url.String()},
		Store:        store,
		Queue:        NewQueue(0, time.Minute),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	daemon.fetchProfile(ctx)

	name, nip05 := daemon.Profile()
	if name != "" || nip05 != "" {
		t.Errorf("Profile() = (%q, %q), want both empty", name, nip05)
	}
}

// TestDaemon_RecentLogs checks log()'s two jobs: every call still reaches
// cfg.OnLog (the spawned-daemon file-logging path, unchanged), and it also
// accumulates into RecentLogs' in-memory tail -- the piece that lets a TUI
// attached over IPC actually see daemon activity, which nothing did before
// (see DaemonLogWatcher's own doc comment in board.go).
func TestDaemon_RecentLogs(t *testing.T) {
	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var onLogCalls []string
	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		Store:        store,
		Queue:        NewQueue(0, time.Minute),
		OnLog: func(format string, args ...any) {
			onLogCalls = append(onLogCalls, fmt.Sprintf(format, args...))
		},
	})

	daemon.log("request method=%s from=%s", "connect", "abc123")
	daemon.log("relay %s: connected", "wss://relay.example")

	if len(onLogCalls) != 2 {
		t.Fatalf("OnLog was called %d times, want 2 -- the on-disk logging path must keep working unchanged", len(onLogCalls))
	}

	snap := daemon.RecentLogs()
	if snap.Total != 2 {
		t.Errorf("Total = %d, want 2", snap.Total)
	}
	if len(snap.Lines) != 2 {
		t.Fatalf("len(Lines) = %d, want 2", len(snap.Lines))
	}
	if !strings.Contains(snap.Lines[0], "request method=connect from=abc123") {
		t.Errorf("Lines[0] = %q, want it to contain the formatted first log line", snap.Lines[0])
	}
	if !strings.Contains(snap.Lines[1], "relay wss://relay.example: connected") {
		t.Errorf("Lines[1] = %q, want it to contain the formatted second log line", snap.Lines[1])
	}
}

// TestDaemon_RecentLogs_BoundsTheTailButKeepsCounting checks logTail
// rotates at maxLogTail while Total keeps counting past it -- what lets
// DaemonLogWatcher's Total-based cursor (not a raw slice index) tell "how
// many got shown" apart from "how many are still in the tail."
func TestDaemon_RecentLogs_BoundsTheTailButKeepsCounting(t *testing.T) {
	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		Store:        store,
		Queue:        NewQueue(0, time.Minute),
	})

	const total = maxLogTail + 10
	for i := range total {
		daemon.log("line %d", i)
	}

	snap := daemon.RecentLogs()
	if snap.Total != total {
		t.Errorf("Total = %d, want %d", snap.Total, total)
	}
	if len(snap.Lines) != maxLogTail {
		t.Errorf("len(Lines) = %d, want maxLogTail (%d)", len(snap.Lines), maxLogTail)
	}
	if !strings.Contains(snap.Lines[0], fmt.Sprintf("line %d", total-maxLogTail)) {
		t.Errorf("Lines[0] = %q, want the oldest surviving line (line %d)", snap.Lines[0], total-maxLogTail)
	}
	if !strings.Contains(snap.Lines[len(snap.Lines)-1], fmt.Sprintf("line %d", total-1)) {
		t.Errorf("last Line = %q, want the newest line (line %d)", snap.Lines[len(snap.Lines)-1], total-1)
	}
}

// TestDaemon_History_RecordsFromQueueResolution guards NewDaemon's own
// wiring (cfg.Queue.OnResolved(d.recordHistory)) end to end: a human
// decision on a real Queue must show up in Daemon.History() with the
// right fields, and newest-first (the opposite order List()/RecentLogs
// use) once a second one is resolved after it.
func TestDaemon_History_RecordsFromQueueResolution(t *testing.T) {
	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	queue := NewQueue(0, time.Minute)
	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		Store:        store,
		Queue:        queue,
	})

	go func() { _, _ = queue.Add(Pending{ID: "req-1", ClientKey: "app1", Method: "sign_event", Kind: 1}) }()
	waitFor(t, func() bool { return len(queue.List()) == 1 })
	if err := queue.Resolve("req-1", Allow, true); err != nil {
		t.Fatal(err)
	}

	go func() { _, _ = queue.Add(Pending{ID: "req-2", ClientKey: "app2", Method: "ping"}) }()
	waitFor(t, func() bool { return len(queue.List()) == 1 })
	if err := queue.Resolve("req-2", Deny, false); err != nil {
		t.Fatal(err)
	}

	history := daemon.History()
	if len(history) != 2 {
		t.Fatalf("History() len = %d, want 2", len(history))
	}
	// Newest first: req-2 (resolved second) before req-1.
	if history[0].ID != "req-2" || history[0].Verdict != Deny || history[0].Remembered {
		t.Errorf("history[0] = %+v, want req-2/Deny/Remembered=false", history[0])
	}
	if history[1].ID != "req-1" || history[1].Verdict != Allow || !history[1].Remembered || history[1].Kind != 1 {
		t.Errorf("history[1] = %+v, want req-1/Allow/Remembered=true/Kind=1", history[1])
	}
}

// TestDaemon_History_RecordsSignedEvent guards recordSignedEvent's own
// wiring (d.handler.OnSigned = d.recordSignedEvent, set in NewDaemon) end
// to end, through the real Handler.Handle sign_event path -- unlike
// TestDaemon_History_RecordsFromQueueResolution above, which resolves the
// Queue directly and so never actually signs anything. This is also the
// concurrency case queue.go's Resolve comment (onResolved before
// close(e.done)) exists for: recordHistory must create the HistoryEntry
// before Handle's own goroutine (woken by that same close) reaches
// recordSignedEvent and needs it to already exist.
func TestDaemon_History_RecordsSignedEvent(t *testing.T) {
	signerPub, err := utils.GetPublicKey(testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}

	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	queue := NewQueue(0, time.Minute)
	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Store:        store,
		Queue:        queue,
	})

	unsigned := nip01.NewUnsignedEvent(1, signerPub, "hello from history test")
	raw, _ := json.Marshal(unsigned)
	req := buildRequest(t, signerPub, nip46.MethodSignEvent, []string{string(raw)})

	done := make(chan *nip01.Event, 1)
	go func() { done <- daemon.handler.Handle(req, nip46.EncryptionNIP04) }()

	waitFor(t, func() bool { return len(queue.List()) == 1 })
	if err := queue.Resolve(req.RequestID, Allow, false); err != nil {
		t.Fatal(err)
	}

	resp := parseResponse(t, <-done)
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	history := daemon.History()
	if len(history) != 1 {
		t.Fatalf("History() len = %d, want 1", len(history))
	}
	if history[0].ID != req.RequestID {
		t.Fatalf("history[0].ID = %q, want %q", history[0].ID, req.RequestID)
	}
	if history[0].Event == nil {
		t.Fatal("history[0].Event = nil, want the signed event")
	}
	if history[0].Event.Content != "hello from history test" {
		t.Errorf("history[0].Event.Content = %q, want the original content", history[0].Event.Content)
	}
	if err := history[0].Event.Verify(); err != nil {
		t.Errorf("history[0].Event failed to verify: %v", err)
	}
}

// TestDaemon_EventLog_PersistsSignedEventAcrossRestart guards the actual
// point of EventLog: not just that recordHistory/recordSignedEvent call
// into it (eventlog_test.go covers EventLog itself in isolation), but
// that a real Daemon wired with one durably persists a signed event, and
// that a *second*, freshly constructed Daemon -- reloaded from the same
// path, the same way command.go's runDaemonProcess/runInProcess do on a
// real restart -- shows that history immediately via InitialHistory,
// with no daemon.historyTail state carried over in memory.
func TestDaemon_EventLog_PersistsSignedEventAcrossRestart(t *testing.T) {
	signerPub, err := utils.GetPublicKey(testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}

	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	walPath := filepath.Join(t.TempDir(), "events.wal")
	eventLog, _, err := LoadEventLog(walPath)
	if err != nil {
		t.Fatal(err)
	}

	queue := NewQueue(0, time.Minute)
	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Store:        store,
		Queue:        queue,
		EventLog:     eventLog,
	})

	unsigned := nip01.NewUnsignedEvent(1, signerPub, "hello from restart test")
	raw, _ := json.Marshal(unsigned)
	req := buildRequest(t, signerPub, nip46.MethodSignEvent, []string{string(raw)})

	done := make(chan *nip01.Event, 1)
	go func() { done <- daemon.handler.Handle(req, nip46.EncryptionNIP04) }()

	waitFor(t, func() bool { return len(queue.List()) == 1 })
	if err := queue.Resolve(req.RequestID, Allow, false); err != nil {
		t.Fatal(err)
	}
	if resp := parseResponse(t, <-done); resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if err := eventLog.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a restart: a fresh load of the same path, with no
	// in-process state carried over at all.
	reloadedLog, reloadedHistory, err := LoadEventLog(walPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reloadedLog.Close() }()

	if len(reloadedHistory) != 1 {
		t.Fatalf("reloaded history len = %d, want 1: %+v", len(reloadedHistory), reloadedHistory)
	}
	if reloadedHistory[0].ID != req.RequestID || reloadedHistory[0].Verdict != Allow {
		t.Errorf("reloaded history[0] = %+v, want ID=%s/Verdict=Allow", reloadedHistory[0], req.RequestID)
	}
	if reloadedHistory[0].Event == nil || reloadedHistory[0].Event.Content != "hello from restart test" {
		t.Fatalf("reloaded history[0].Event = %+v, want the signed event with its original content", reloadedHistory[0].Event)
	}

	restarted := NewDaemon(DaemonConfig{
		IdentityPriv:   testSignerPriv,
		IdentityPub:    signerPub,
		Store:          store,
		Queue:          NewQueue(0, time.Minute),
		EventLog:       reloadedLog,
		InitialHistory: reloadedHistory,
	})
	history := restarted.History()
	if len(history) != 1 || history[0].ID != req.RequestID {
		t.Fatalf("restarted daemon History() = %+v, want the one reloaded entry", history)
	}
	if history[0].Event == nil || history[0].Event.Content != "hello from restart test" {
		t.Errorf("restarted daemon History()[0].Event = %+v, want the signed event preserved across the simulated restart", history[0].Event)
	}
}

// TestDaemon_EventLog_InterruptedPendingSurfacesAsExpiredAfterRestart
// guards the plan's central design decision end to end: a request that
// was pending (recorded via recordAdded/Queue.OnAdded, since Handle
// blocks in Queue.Add) when the process stops, with no human decision
// ever recorded for it, must surface as a terminal Expired HistoryEntry
// on the next load -- not silently lost, and not left waiting forever
// for a decision that (per NIP-46's own resend-on-timeout convention)
// the live path will already have superseded. The goroutine blocked in
// Handle/Queue.Add is deliberately never resolved and left running for
// the rest of the test process -- that's the point: this simulates the
// daemon process itself stopping (a crash, or an ordinary restart with
// something still on screen undecided), not a clean in-test shutdown.
func TestDaemon_EventLog_InterruptedPendingSurfacesAsExpiredAfterRestart(t *testing.T) {
	signerPub, err := utils.GetPublicKey(testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}

	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	walPath := filepath.Join(t.TempDir(), "events.wal")
	eventLog, _, err := LoadEventLog(walPath)
	if err != nil {
		t.Fatal(err)
	}

	queue := NewQueue(0, time.Minute)
	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Store:        store,
		Queue:        queue,
		EventLog:     eventLog,
	})

	unsigned := nip01.NewUnsignedEvent(1, signerPub, "never decided before the crash")
	raw, _ := json.Marshal(unsigned)
	req := buildRequest(t, signerPub, nip46.MethodSignEvent, []string{string(raw)})

	go daemon.handler.Handle(req, nip46.EncryptionNIP04) // deliberately never resolved -- see doc comment
	waitFor(t, func() bool { return len(queue.List()) == 1 })

	// Simulate the crash/restart: load the same path fresh, with no
	// in-process Queue state carried over.
	reloadedLog, reloadedHistory, err := LoadEventLog(walPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reloadedLog.Close() }()

	if len(reloadedHistory) != 1 {
		t.Fatalf("reloaded history len = %d, want 1: %+v", len(reloadedHistory), reloadedHistory)
	}
	h := reloadedHistory[0]
	if h.ID != req.RequestID || h.Verdict != Deny || !h.Expired {
		t.Errorf("reloaded history[0] = %+v, want ID=%s/Verdict=Deny/Expired=true", h, req.RequestID)
	}
}

// TestDaemon_History_BoundsTheTail mirrors
// TestDaemon_RecentLogs_BoundsTheTailButKeepsCounting for the history
// tail: past maxHistoryTail entries, the oldest are dropped and
// History() still returns newest-first.
func TestDaemon_History_BoundsTheTail(t *testing.T) {
	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		Store:        store,
		Queue:        NewQueue(0, time.Minute),
	})

	const total = maxHistoryTail + 10
	for i := range total {
		daemon.recordHistory(ResolvedEvent{
			Pending: Pending{ID: fmt.Sprintf("req-%d", i), Method: "ping"},
			Verdict: Allow,
		})
	}

	history := daemon.History()
	if len(history) != maxHistoryTail {
		t.Fatalf("History() len = %d, want maxHistoryTail (%d)", len(history), maxHistoryTail)
	}
	if history[0].ID != fmt.Sprintf("req-%d", total-1) {
		t.Errorf("history[0].ID = %q, want the newest entry (req-%d)", history[0].ID, total-1)
	}
	if history[len(history)-1].ID != fmt.Sprintf("req-%d", total-maxHistoryTail) {
		t.Errorf("last entry ID = %q, want the oldest surviving one (req-%d)", history[len(history)-1].ID, total-maxHistoryTail)
	}
}

// TestDaemon_EventLog_RuntimeCompactionBoundsTheFile guards recordHistory's
// own CompactDue/compact wiring (eventlog_test.go's TestEventLog_CompactDue
// checks the EventLog primitive in isolation) across many compaction
// cycles, not just the first one: a daemon that runs long enough to cross
// maxHistoryTail resolutions five times over must still have events.wal
// bounded to maxHistoryTail lines after every single cycle, not just the
// first -- proving the runtime trigger keeps firing for as long as the
// process stays up, rather than compacting once and then reverting to
// unbounded growth.
func TestDaemon_EventLog_RuntimeCompactionBoundsTheFile(t *testing.T) {
	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	walPath := filepath.Join(t.TempDir(), "events.wal")
	eventLog, _, err := LoadEventLog(walPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = eventLog.Close() }()

	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		Store:        store,
		Queue:        NewQueue(0, time.Minute),
		EventLog:     eventLog,
	})

	const cycles = 5
	for c := range cycles {
		for i := range maxHistoryTail {
			daemon.recordHistory(ResolvedEvent{
				Pending: Pending{ID: fmt.Sprintf("req-%d-%d", c, i), Method: "ping"},
				Verdict: Allow,
			})
		}
		if lineCount := countLines(t, walPath); lineCount > maxHistoryTail {
			t.Fatalf("after cycle %d: file line count = %d, want <= maxHistoryTail (%d)", c, lineCount, maxHistoryTail)
		}
	}

	history := daemon.History()
	if len(history) != maxHistoryTail {
		t.Fatalf("History() len = %d, want maxHistoryTail (%d)", len(history), maxHistoryTail)
	}
	if want := fmt.Sprintf("req-%d-%d", cycles-1, maxHistoryTail-1); history[0].ID != want {
		t.Errorf("history[0].ID = %q, want the newest entry (%q)", history[0].ID, want)
	}

	reloadedLog, reloadedHistory, err := LoadEventLog(walPath)
	if err != nil {
		t.Fatalf("LoadEventLog after runtime compaction: %v", err)
	}
	defer func() { _ = reloadedLog.Close() }()
	if len(reloadedHistory) != maxHistoryTail {
		t.Errorf("reloaded history len = %d, want maxHistoryTail (%d)", len(reloadedHistory), maxHistoryTail)
	}
}

// TestDaemon_EventLog_RuntimeCompactionUnderConcurrency drives many
// concurrent recordHistory calls -- the real code path CompactDue/compact
// were wired into, unlike eventlog_test.go's TestEventLog_CompactDue which
// exercises the EventLog primitive alone -- through a real EventLog and
// checks what -race alone can't: the file stays well under total (proving
// the runtime trigger actually fired repeatedly rather than only
// LoadEventLog's own once-at-startup trim eventually catching up) and, once
// everything settles, the file is still perfectly parseable. Concurrent
// recordHistory calls race on two independent locks (historyMu, then
// EventLog's own mu) in sequence rather than one combined one, so this is
// what proves that's still safe, not just assumed; run with -race.
func TestDaemon_EventLog_RuntimeCompactionUnderConcurrency(t *testing.T) {
	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	walPath := filepath.Join(t.TempDir(), "events.wal")
	eventLog, _, err := LoadEventLog(walPath)
	if err != nil {
		t.Fatal(err)
	}

	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		Store:        store,
		Queue:        NewQueue(0, time.Minute),
		EventLog:     eventLog,
	})

	const total = maxHistoryTail * 5
	var wg sync.WaitGroup
	for i := range total {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			daemon.recordHistory(ResolvedEvent{
				Pending: Pending{ID: fmt.Sprintf("req-%d", i), Method: "ping"},
				Verdict: Allow,
			})
		}(i)
	}
	wg.Wait()

	if err := eventLog.Close(); err != nil {
		t.Fatal(err)
	}

	if lineCount := countLines(t, walPath); lineCount >= total {
		t.Errorf("file line count = %d, want well under total (%d) -- runtime compaction should have bounded it", lineCount, total)
	}

	reloadedLog, reloadedHistory, err := LoadEventLog(walPath)
	if err != nil {
		t.Fatalf("LoadEventLog after concurrent compaction: %v", err)
	}
	defer func() { _ = reloadedLog.Close() }()
	if len(reloadedHistory) != maxHistoryTail {
		t.Fatalf("reloaded history len = %d, want maxHistoryTail (%d)", len(reloadedHistory), maxHistoryTail)
	}

	history := daemon.History()
	if len(history) != maxHistoryTail {
		t.Fatalf("daemon.History() len = %d, want maxHistoryTail (%d)", len(history), maxHistoryTail)
	}
}
