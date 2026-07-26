package bunker

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip04"
	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/utils"
)

const (
	testSignerPriv = "5a3c66fe899f8922d5cff0030b5affa83bcad6b7913e5681395a21979fd25bbf"
	testClientPriv = "0acd12cbf0fb87cd13b17bc9b57dffd11b3870b407984cec5a4ce2a69b90268c"
)

func newTestHandler(t *testing.T) (*Handler, string, string) {
	t.Helper()

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
	h := &Handler{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Store:        store,
		Queue:        NewQueue(0, time.Minute),
		Relays:       []string{"wss://relay.example"},
	}
	return h, signerPub, clientPub
}

// buildRequest signs a request event from the client to the signer and
// returns it parsed, exactly as daemon.go's read loop would hand it to
// Handler.Handle.
func buildRequest(t *testing.T, signerPub, method string, params []string) *nip46.RequestEvent {
	t.Helper()
	ev, reqID, err := nip46.NewRequestEvent(testClientPriv, signerPub, method, params, nip46.EncryptionNIP04)
	if err != nil {
		t.Fatal(err)
	}
	if err := ev.Sign(testClientPriv); err != nil {
		t.Fatal(err)
	}
	parsed, err := nip46.ParseRequestEvent(ev, testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.RequestID != reqID {
		t.Fatalf("RequestID = %q, want %q", parsed.RequestID, reqID)
	}
	return parsed
}

func parseResponse(t *testing.T, resp *nip01.Event) *nip46.ResponseEvent {
	t.Helper()
	if resp == nil {
		t.Fatal("Handle() returned a nil response event")
	}
	if err := resp.Verify(); err != nil {
		t.Fatalf("response event failed to verify: %v", err)
	}
	parsed, err := nip46.ParseResponseEvent(resp, testClientPriv)
	if err != nil {
		t.Fatalf("ParseResponseEvent() error = %v", err)
	}
	return parsed
}

func TestHandle_Connect_NoPairingInProgress(t *testing.T) {
	h, signerPub, _ := newTestHandler(t)
	req := buildRequest(t, signerPub, nip46.MethodConnect, []string{signerPub, "some-secret"})

	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Error == "" {
		t.Error("expected an error response when no pairing is in progress")
	}
}

func TestHandle_Connect_SecretMismatch(t *testing.T) {
	h, signerPub, _ := newTestHandler(t)
	h.SetPendingSecret("correct-secret")
	req := buildRequest(t, signerPub, nip46.MethodConnect, []string{signerPub, "wrong-secret"})

	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Error == "" {
		t.Error("expected an error response on secret mismatch")
	}
}

func TestHandle_Connect_SecretMatch_Approved(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	h.SetPendingSecret("correct-secret")
	// "connect" itself goes through the same Ask/Allow/Deny pipeline as
	// any other method once the secret checks out -- pre-grant it here to
	// isolate the secret-check behavior from the approval-queue behavior
	// (covered separately below).
	if err := h.Store.Remember(clientPub, GrantForever(nip46.MethodConnect, nil, time.Now())); err != nil {
		t.Fatal(err)
	}

	req := buildRequest(t, signerPub, nip46.MethodConnect, []string{signerPub, "correct-secret"})
	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Result != "ack" {
		t.Errorf("Result = %q, want ack", resp.Result)
	}

	// The secret is single-use: a second attempt with the same secret
	// must fail even though it was correct the first time.
	req2 := buildRequest(t, signerPub, nip46.MethodConnect, []string{signerPub, "correct-secret"})
	resp2 := parseResponse(t, h.Handle(req2, nip46.EncryptionNIP04))
	if resp2.Error == "" {
		t.Error("expected the second connect with a spent secret to fail")
	}
}

func TestHandle_Connect_ExpiredSecretRejected(t *testing.T) {
	h, signerPub, _ := newTestHandler(t)
	h.SetPendingSecret("correct-secret")
	// Reach past SetPendingSecret's own TTL directly rather than sleeping
	// PairingSecretTTL (5 minutes) in a test -- simulates "nobody connected
	// in time" without actually waiting for it.
	h.mu.Lock()
	h.pendingSecretExpiresAt = time.Now().Add(-time.Second)
	h.mu.Unlock()

	req := buildRequest(t, signerPub, nip46.MethodConnect, []string{signerPub, "correct-secret"})
	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Error == "" {
		t.Error("expected an error response for a secret past its expiry, even though it's otherwise correct")
	}

	// The expired secret must not linger to be matched by some other stray
	// request either -- takePendingSecretIfMatches clears it once found
	// expired.
	h.mu.Lock()
	stillSet := h.pendingSecret != ""
	h.mu.Unlock()
	if stillSet {
		t.Error("expired secret was not cleared")
	}
}

func TestHandle_Connect_GoesThroughApprovalQueueWhenNoGrant(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	h.SetPendingSecret("s3cr3t")
	req := buildRequest(t, signerPub, nip46.MethodConnect, []string{signerPub, "s3cr3t"})

	done := make(chan *nip01.Event, 1)
	go func() { done <- h.Handle(req, nip46.EncryptionNIP04) }()

	waitFor(t, func() bool { return len(h.Queue.List()) == 1 })
	pending := h.Queue.List()[0]
	if pending.ClientKey != clientPub || pending.Method != nip46.MethodConnect {
		t.Fatalf("pending = %+v, want connect from %s", pending, clientPub)
	}
	if err := h.Queue.Resolve(pending.ID, Allow, false); err != nil {
		t.Fatal(err)
	}

	resp := parseResponse(t, <-done)
	if resp.Result != "ack" {
		t.Errorf("Result = %q, want ack", resp.Result)
	}

	// Trusted Apps fix: a human approving this connect once (never
	// choosing "Always") must still be enough to register the app --
	// board.go's SessionsTable must not stay empty just because nothing
	// was ever explicitly remembered for it.
	sessions := h.Store.List()
	if len(sessions) != 1 || sessions[0].Pubkey != clientPub {
		t.Fatalf("Store.List() after an approved connect = %+v, want a Trusted Apps entry for %s", sessions, clientPub)
	}
	if len(sessions[0].Grants) != 0 {
		t.Errorf("Grants = %+v, want empty (Approve Once must not itself remember a permission)", sessions[0].Grants)
	}
}

// TestHandle_Connect_WithPendingGrantSpec_SkipsApprovalQueue guards the
// fix `ncli bunker connect --grants <file>` needs to actually be
// unattended-usable: without it, "connect" itself would still go through
// Store.Decide/Queue.Add like TestHandle_Connect_GoesThroughApprovalQueue
// WhenNoGrant proves it does for an unscripted pairing -- fine when a
// human is at the TUI to click Approve, but it would silently hang
// forever for the agent use case examples/bunker/agent.yaml exists for,
// with nobody there to click anything. Staging a GrantSpec via
// SetPendingGrants (after SetPendingSecret) is itself the approval
// decision for this one pairing attempt, so Handle must resolve "connect"
// immediately once the secret matches, never touching the queue.
func TestHandle_Connect_WithPendingGrantSpec_SkipsApprovalQueue(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	h.SetPendingSecret("s3cr3t")

	spec, err := LoadGrantSpec(writeSpecFile(t, `
kind: bunker
spec:
  nickname: "Scripted App"
  grants:
    - method: ping
`))
	if err != nil {
		t.Fatal(err)
	}
	h.SetPendingGrants(spec)

	req := buildRequest(t, signerPub, nip46.MethodConnect, []string{signerPub, "s3cr3t"})
	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Result != "ack" {
		t.Fatalf("Result = %q, error = %q, want ack", resp.Result, resp.Error)
	}
	if got := len(h.Queue.List()); got != 0 {
		t.Errorf("Queue.List() len = %d, want 0 -- connect must never have gone through Ask/Queue.Add", got)
	}

	sessions := h.Store.List()
	if len(sessions) != 1 || sessions[0].Pubkey != clientPub {
		t.Fatalf("Store.List() = %+v, want one session for %s", sessions, clientPub)
	}
	if sessions[0].Nickname != "Scripted App" {
		t.Errorf("Nickname = %q, want %q", sessions[0].Nickname, "Scripted App")
	}
	if len(sessions[0].Grants) != 1 || sessions[0].Grants[0].Method != nip46.MethodPing {
		t.Fatalf("Grants = %+v, want one ping grant", sessions[0].Grants)
	}
}

func TestHandle_Ping(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	if err := h.Store.Remember(clientPub, GrantForever(nip46.MethodPing, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	req := buildRequest(t, signerPub, nip46.MethodPing, nil)

	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Result != "pong" {
		t.Errorf("Result = %q, want pong", resp.Result)
	}
}

func TestHandle_GetPublicKey(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	if err := h.Store.Remember(clientPub, GrantForever(nip46.MethodGetPublicKey, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	req := buildRequest(t, signerPub, nip46.MethodGetPublicKey, nil)

	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Result != signerPub {
		t.Errorf("Result = %q, want %q", resp.Result, signerPub)
	}
}

func TestHandle_GetRelays(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	if err := h.Store.Remember(clientPub, GrantForever(nip46.MethodGetRelays, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	req := buildRequest(t, signerPub, nip46.MethodGetRelays, nil)

	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	var relays map[string]map[string]bool
	if err := json.Unmarshal([]byte(resp.Result), &relays); err != nil {
		t.Fatalf("get_relays result isn't valid JSON: %v", err)
	}
	if !relays["wss://relay.example"]["write"] {
		t.Errorf("relays = %v, want wss://relay.example writable", relays)
	}
}

func TestHandle_SignEvent_Allowed(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	kind := 1
	if err := h.Store.Remember(clientPub, GrantForever(nip46.MethodSignEvent, &kind, time.Now())); err != nil {
		t.Fatal(err)
	}

	unsigned := nip01.NewUnsignedEvent(1, signerPub, "hello world")
	raw, _ := json.Marshal(unsigned)
	req := buildRequest(t, signerPub, nip46.MethodSignEvent, []string{string(raw)})

	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	var signed nip01.Event
	if err := json.Unmarshal([]byte(resp.Result), &signed); err != nil {
		t.Fatalf("sign_event result isn't a valid event: %v", err)
	}
	if err := signed.Verify(); err != nil {
		t.Errorf("signed event failed to verify: %v", err)
	}
	if signed.PubKey != signerPub {
		t.Errorf("signed pubkey = %q, want signer's own %q", signed.PubKey, signerPub)
	}
}

// TestHandle_SignEvent_FiresOnSigned guards the hook daemon.go's own
// signed-event history depends on (recordSignedEvent): OnSigned must fire
// with the same request's ID and the fully signed (not the original
// unsigned) event, and only for sign_event -- never for a method that
// never signs anything.
func TestHandle_SignEvent_FiresOnSigned(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	kind := 1
	if err := h.Store.Remember(clientPub, GrantForever(nip46.MethodSignEvent, &kind, time.Now())); err != nil {
		t.Fatal(err)
	}
	// Also granted so the second Handle() call below (MethodPing) resolves
	// immediately instead of blocking on Queue.Add for a human decision
	// that would never come -- Store.Decide defaults to Ask, not Allow,
	// for any method/kind without an explicit grant.
	if err := h.Store.Remember(clientPub, GrantForever(nip46.MethodPing, nil, time.Now())); err != nil {
		t.Fatal(err)
	}

	var gotID string
	var gotEvent *nip01.Event
	h.OnSigned = func(requestID string, event *nip01.Event) {
		gotID, gotEvent = requestID, event
	}

	unsigned := nip01.NewUnsignedEvent(1, signerPub, "hello world")
	raw, _ := json.Marshal(unsigned)
	req := buildRequest(t, signerPub, nip46.MethodSignEvent, []string{string(raw)})

	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	if gotID != req.RequestID {
		t.Errorf("OnSigned requestID = %q, want %q", gotID, req.RequestID)
	}
	if gotEvent == nil || gotEvent.Sig == "" {
		t.Fatalf("OnSigned event = %+v, want a fully signed event", gotEvent)
	}
	if err := gotEvent.Verify(); err != nil {
		t.Errorf("OnSigned event failed to verify: %v", err)
	}

	// A method that never signs anything must never fire OnSigned.
	gotID, gotEvent = "", nil
	pingReq := buildRequest(t, signerPub, nip46.MethodPing, nil)
	if resp := parseResponse(t, h.Handle(pingReq, nip46.EncryptionNIP04)); resp.Error != "" {
		t.Fatalf("unexpected ping error: %s", resp.Error)
	}
	if gotEvent != nil {
		t.Errorf("OnSigned fired for MethodPing: %+v", gotEvent)
	}
}

func TestHandle_SignEvent_DifferentKindStillAsks(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	kind1 := 1
	if err := h.Store.Remember(clientPub, GrantForever(nip46.MethodSignEvent, &kind1, time.Now())); err != nil {
		t.Fatal(err)
	}

	unsigned := nip01.NewUnsignedEvent(7, signerPub, "a reaction")
	raw, _ := json.Marshal(unsigned)
	req := buildRequest(t, signerPub, nip46.MethodSignEvent, []string{string(raw)})

	done := make(chan *nip01.Event, 1)
	go func() { done <- h.Handle(req, nip46.EncryptionNIP04) }()

	waitFor(t, func() bool { return len(h.Queue.List()) == 1 })
	pending := h.Queue.List()[0]
	if pending.Kind != 7 {
		t.Fatalf("pending.Kind = %d, want 7", pending.Kind)
	}
	if err := h.Queue.Resolve(pending.ID, Deny, false); err != nil {
		t.Fatal(err)
	}
	resp := parseResponse(t, <-done)
	if resp.Error == "" {
		t.Error("expected rejection for the un-granted kind")
	}
}

func TestHandle_SignEvent_PubkeyConflictRejected(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	kind := 1
	if err := h.Store.Remember(clientPub, GrantForever(nip46.MethodSignEvent, &kind, time.Now())); err != nil {
		t.Fatal(err)
	}

	unsigned := nip01.NewUnsignedEvent(1, "0000000000000000000000000000000000000000000000000000000000000000", "x")
	raw, _ := json.Marshal(unsigned)
	req := buildRequest(t, signerPub, nip46.MethodSignEvent, []string{string(raw)})

	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Error == "" {
		t.Error("expected rejection for a pubkey that conflicts with the signer identity")
	}
}

func TestHandle_SignEvent_MalformedParamsRejectedWithoutPanic(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	kind := 1
	if err := h.Store.Remember(clientPub, GrantForever(nip46.MethodSignEvent, &kind, time.Now())); err != nil {
		t.Fatal(err)
	}

	for _, params := range [][]string{
		{"not json at all"},
		{"{"},
		{},
	} {
		req := buildRequest(t, signerPub, nip46.MethodSignEvent, params)
		resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
		if resp.Error == "" {
			t.Errorf("params %v: expected an error response, got a signed result", params)
		}
	}
}

func TestHandle_Deny(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	if err := h.Store.Remember(clientPub, DenyAlways(nip46.MethodPing, time.Now())); err != nil {
		t.Fatal(err)
	}
	req := buildRequest(t, signerPub, nip46.MethodPing, nil)

	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Error == "" {
		t.Error("expected rejection under a deny-always grant")
	}
}

func TestHandle_NIP04EncryptDecrypt(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	if err := h.Store.Remember(clientPub, GrantForever(nip46.MethodNIP04Encrypt, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.Remember(clientPub, GrantForever(nip46.MethodNIP04Decrypt, nil, time.Now())); err != nil {
		t.Fatal(err)
	}

	encReq := buildRequest(t, signerPub, nip46.MethodNIP04Encrypt, []string{clientPub, "hello"})
	encResp := parseResponse(t, h.Handle(encReq, nip46.EncryptionNIP04))
	if encResp.Error != "" {
		t.Fatalf("encrypt error: %s", encResp.Error)
	}

	// The signer encrypted to the client's own pubkey using its identity
	// key -- confirm the client could decrypt it with nip04 directly (an
	// independent check that this isn't just echoing ciphertext back).
	plain, err := nip04.Decrypt(encResp.Result, h.IdentityPub, testClientPriv)
	if err != nil {
		t.Fatalf("client-side nip04.Decrypt() error = %v", err)
	}
	if plain != "hello" {
		t.Errorf("decrypted = %q, want hello", plain)
	}

	decReq := buildRequest(t, signerPub, nip46.MethodNIP04Decrypt, []string{clientPub, encResp.Result})
	decResp := parseResponse(t, h.Handle(decReq, nip46.EncryptionNIP04))
	if decResp.Result != "hello" {
		t.Errorf("nip04_decrypt Result = %q, want hello", decResp.Result)
	}
}

func TestHandle_NIP44EncryptDecrypt(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	if err := h.Store.Remember(clientPub, GrantForever(nip46.MethodNIP44Encrypt, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.Remember(clientPub, GrantForever(nip46.MethodNIP44Decrypt, nil, time.Now())); err != nil {
		t.Fatal(err)
	}

	encReq := buildRequest(t, signerPub, nip46.MethodNIP44Encrypt, []string{clientPub, "secret note"})
	encResp := parseResponse(t, h.Handle(encReq, nip46.EncryptionNIP04))
	if encResp.Error != "" {
		t.Fatalf("encrypt error: %s", encResp.Error)
	}

	decReq := buildRequest(t, signerPub, nip46.MethodNIP44Decrypt, []string{clientPub, encResp.Result})
	decResp := parseResponse(t, h.Handle(decReq, nip46.EncryptionNIP04))
	if decResp.Result != "secret note" {
		t.Errorf("nip44_decrypt Result = %q, want %q", decResp.Result, "secret note")
	}
}

func TestHandle_UnsupportedMethod(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	if err := h.Store.Remember(clientPub, GrantForever("some_future_method", nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	req := buildRequest(t, signerPub, "some_future_method", nil)

	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Error == "" || !strings.Contains(resp.Error, "unsupported") {
		t.Errorf("Error = %q, want it to mention unsupported method", resp.Error)
	}
}
