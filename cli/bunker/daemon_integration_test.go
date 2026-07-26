//go:build integration

// Live-relay integration tests, run against a real external relay
// (relay.ohstr.com) rather than a local mock -- kept behind the
// "integration" build tag so the default `go test ./...`/CI run never
// depends on network access or that relay's uptime. Run explicitly with:
//
//	go test -tags integration ./cli/bunker/... -run Live -v
package bunker

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
	relayclient "github.com/ohstr/nmilat/relay/client"
)

const liveRelayURL = "wss://relay.ohstr.com"

func generateTestKeypair(t *testing.T) (privHex, pubHex string) {
	t.Helper()
	id, err := client.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id.PrivKeyHex, id.PubKeyHex
}

// liveTestClient plays the "app" side of a NIP-46 exchange directly
// against the real relay -- its own keypair, its own connection, driving
// requests at a *Daemon under test the same way a real Nostr client would.
type liveTestClient struct {
	privKey string
	pubKey  string
	conn    *relayclient.Connection
}

func newLiveTestClient(t *testing.T, ctx context.Context) *liveTestClient {
	t.Helper()

	priv, pub := generateTestKeypair(t)

	u, err := url.Parse(liveRelayURL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := relayclient.Connect(ctx, u)
	if err != nil {
		t.Skipf("could not reach live relay %s: %v", liveRelayURL, err)
	}
	t.Cleanup(conn.Close)

	return &liveTestClient{privKey: priv, pubKey: pub, conn: conn}
}

func TestLive_BunkerFlow_ConnectAndSignEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	signerPriv, signerPub := generateTestKeypair(t)

	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	daemon := NewDaemon(DaemonConfig{
		IdentityPriv: signerPriv,
		IdentityPub:  signerPub,
		Relays:       []string{liveRelayURL},
		Store:        store,
		Queue:        NewQueue(0, time.Minute),
		OnLog:        func(format string, args ...any) { t.Logf("daemon: "+format, args...) },
	})
	go daemon.Run(ctx)
	time.Sleep(2 * time.Second) // let the relay connection establish

	testClient := newLiveTestClient(t, ctx)

	// "connect" itself goes through the same Decide/approval-queue
	// pipeline as every other method (see handler.go's Handle) -- a
	// brand-new pubkey with no grant would otherwise sit in the pending
	// queue waiting for a human decision that never comes here. Pre-grant
	// it, standing in for a human approving the pairing in the TUI (the
	// approval-queue mechanics themselves are already covered by
	// handler_test.go's TestHandle_Connect_GoesThroughApprovalQueueWhenNoGrant
	// -- this test's job is proving the real wire protocol/relay round
	// trip, not re-covering that).
	if err := store.Remember(testClient.pubKey, GrantForever(nip46.MethodConnect, nil, time.Now())); err != nil {
		t.Fatal(err)
	}

	// bunker:// flow: the daemon "displays" a pairing secret, the client
	// speaks first with a matching connect request.
	uri, err := daemon.NewBunkerPairing()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := bunkerURISecret(uri)
	if err != nil {
		t.Fatal(err)
	}

	connectReq, connectReqID, err := nip46.NewRequestEvent(testClient.privKey, signerPub, nip46.MethodConnect,
		[]string{signerPub, secret}, nip46.EncryptionNIP44V2)
	if err != nil {
		t.Fatal(err)
	}
	if err := connectReq.Sign(testClient.privKey); err != nil {
		t.Fatal(err)
	}

	respEvents := subscribeForResponses(ctx, testClient)

	if !testClient.conn.Send(connectReq) {
		t.Fatal("failed to send connect request")
	}

	connectResp := waitForResponse(t, respEvents, testClient.privKey, connectReqID, 20*time.Second)
	if connectResp.Error != "" {
		t.Fatalf("connect rejected: %s", connectResp.Error)
	}
	if connectResp.Result != "ack" {
		t.Errorf("connect Result = %q, want ack", connectResp.Result)
	}

	// The connecting app now has no remembered grant yet -- a sign_event
	// request must go through approval. Pre-grant it directly on the
	// Store (standing in for a human clicking "Always: kind 1" in the
	// TUI) so this test can complete unattended.
	kind := 1
	if err := store.Remember(testClient.pubKey, GrantForever(nip46.MethodSignEvent, &kind, time.Now())); err != nil {
		t.Fatal(err)
	}

	unsigned := nip01.NewUnsignedEvent(1, signerPub, "hello from the live relay integration test")
	rawEvent, _ := json.Marshal(unsigned)
	signReq, signReqID, err := nip46.NewRequestEvent(testClient.privKey, signerPub, nip46.MethodSignEvent, []string{string(rawEvent)}, nip46.EncryptionNIP44V2)
	if err != nil {
		t.Fatal(err)
	}
	if err := signReq.Sign(testClient.privKey); err != nil {
		t.Fatal(err)
	}
	if !testClient.conn.Send(signReq) {
		t.Fatal("failed to send sign_event request")
	}

	signResp := waitForResponse(t, respEvents, testClient.privKey, signReqID, 20*time.Second)
	if signResp.Error != "" {
		t.Fatalf("sign_event rejected: %s", signResp.Error)
	}
	var signed nip01.Event
	if err := json.Unmarshal([]byte(signResp.Result), &signed); err != nil {
		t.Fatalf("result isn't a signed event: %v", err)
	}
	if err := signed.Verify(); err != nil {
		t.Fatalf("signed event failed to verify: %v", err)
	}

	// A different, non-granted kind must still be asked about -- proven
	// here by confirming the exact-kind grant doesn't cover it: Decide
	// directly (equivalent to what handler.go would consult) rather than
	// actually blocking this test on a human approval that will never
	// come.
	if got := store.Decide(testClient.pubKey, nip46.MethodSignEvent, 7); got != Ask {
		t.Errorf("Decide() for an ungranted kind = %v, want Ask", got)
	}
}

func TestLive_SensitiveKindAlwaysAsksEvenUnderBroadGrant(t *testing.T) {
	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const appPub = "app-under-test"
	if err := store.Remember(appPub, GrantForever(nip46.MethodSignEvent, nil, time.Now())); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []int{0, 3, 5} {
		if got := store.Decide(appPub, nip46.MethodSignEvent, kind); got != Ask {
			t.Errorf("kind %d under a broad any-kind grant = %v, want Ask", kind, got)
		}
	}
	if got := store.Decide(appPub, nip46.MethodSignEvent, 1); got != Allow {
		t.Errorf("kind 1 under the same grant = %v, want Allow", got)
	}
}

func TestLive_GrantSurvivesDaemonRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.yaml")
	const appPub = "restart-test-app"

	s1, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Remember(appPub, GrantForDuration(nip46.MethodPing, nil, time.Hour, time.Now())); err != nil {
		t.Fatal(err)
	}

	// Simulate a daemon restart: a fresh Store loaded from the same file.
	s2, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Decide(appPub, nip46.MethodPing, 0); got != Allow {
		t.Errorf("Decide() after reload = %v, want Allow (the grant must survive a restart)", got)
	}
}

// -- small helpers specific to this file --

func bunkerURISecret(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	return u.Query().Get("secret"), nil
}

func subscribeForResponses(ctx context.Context, c *liveTestClient) <-chan *nip01.Event {
	subID := "live-test-" + c.pubKey[:8]
	c.conn.SubscribeWithID(subID, nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds: []int{nip46.KindRequest},
		Tags:  map[string][]string{"p": {c.pubKey}},
	}))
	raw := c.conn.Events(subID)

	out := make(chan *nip01.Event, 16)
	go func() {
		defer close(out)
		for {
			select {
			case ev, ok := <-raw:
				if !ok {
					return
				}
				select {
				case out <- ev.Event:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func waitForResponse(t *testing.T, events <-chan *nip01.Event, clientPriv, wantReqID string, timeout time.Duration) *nip46.Response {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-events:
			resp, err := nip46.ParseResponseEvent(ev, clientPriv)
			if err != nil {
				continue // not decryptable with our key, or not a response shape -- keep waiting
			}
			if resp.RequestID != wantReqID {
				continue
			}
			return &resp.Response
		case <-deadline:
			t.Fatalf("timed out waiting for a response to request %s", wantReqID)
			return nil
		}
	}
}
