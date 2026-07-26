package bunker

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/ohstr/nmilat/utils"
)

// newTestDaemon builds a real Daemon -- the same Store/Queue/Handler wiring
// NewDaemon assembles in production, minus any relay connection (nothing
// here needs one: requests are handed to daemon.handler.Handle directly,
// exactly what daemon.go's handleIncoming does per relay event).
func newTestDaemon(t *testing.T) (daemon *Daemon, signerPub, clientPub string) {
	t.Helper()
	signerPub, err := utils.GetPublicKey(testSignerPriv)
	if err != nil {
		t.Fatal(err)
	}
	clientPub, err = utils.GetPublicKey(testClientPriv)
	if err != nil {
		t.Fatal(err)
	}

	store, err := LoadStore(filepath.Join(t.TempDir(), "sessions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	daemon = NewDaemon(DaemonConfig{
		IdentityPriv: testSignerPriv,
		IdentityPub:  signerPub,
		Store:        store,
		Queue:        NewQueue(0, time.Minute),
	})
	return daemon, signerPub, clientPub
}

// localClientFor wraps daemon the same way command.go's Windows/local path
// (and every other test in this package) does.
func localClientFor(daemon *Daemon) BunkerClient {
	return newLocalClient(daemon, time.Now(), func() {})
}

// ipcClientFor wraps daemon behind a real Unix-socket Server/ipcClient pair
// -- the actual `ncli bunker attach` production path (Linux/macOS), where
// every Approve/Reject call (and the Grant it may carry) crosses a real
// JSON-over-socket boundary instead of a plain in-process function call.
// This is the one path none of this package's existing tests drove a
// remembered grant through -- see the scenario tests below.
func ipcClientFor(t *testing.T, daemon *Daemon) BunkerClient {
	t.Helper()
	lc := localClientFor(daemon)
	socketPath := startTestServer(t, lc)
	client, err := DialIPC(socketPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// sendAndAwaitDecision sends one request through daemon.handler.Handle
// (blocking, exactly as production does -- see Handler.Handle's own doc
// comment on why callers must invoke it from a per-request goroutine),
// waits for it to either resolve immediately (a grant already decided it)
// or land in the queue, and -- only in the latter case -- resolves it via
// decide. Returns the parsed response and whether a human decision was
// actually needed, so callers can assert on both the outcome and whether
// Decide short-circuited the queue entirely.
func sendAndAwaitDecision(t *testing.T, daemon *Daemon, req *nip46.RequestEvent, decide func(pendingID string)) (resp *nip46.ResponseEvent, wentToQueue bool) {
	t.Helper()

	done := make(chan *nip01.Event, 1)
	go func() { done <- daemon.handler.Handle(req, nip46.EncryptionNIP04) }()

	select {
	case ev := <-done:
		return parseResponse(t, ev), false
	case <-time.After(150 * time.Millisecond):
		// Didn't resolve immediately -- must be waiting in the queue for a
		// human decision.
	}

	waitFor(t, func() bool { return len(daemon.cfg.Queue.List()) == 1 })
	pending := daemon.cfg.Queue.List()[0]
	decide(pending.ID)

	return parseResponse(t, <-done), true
}

// pairApp completes a bunker://-style connect handshake for the test client
// pubkey, approved Once (remember=nil). Store.Pair fires regardless of
// whether the decision was remembered -- see Store.Pair's own doc comment
// and TestHandle_Connect_GoesThroughApprovalQueueWhenNoGrant -- so this is
// what actually puts the app in Trusted Apps, before it sends any other
// method. Every scenario below starts here: a real NIP-46 client always
// connects before doing anything else, so testing e.g. Approve Once on a
// bare "ping" with no prior pairing (as an earlier version of this file
// did) exercises a request shape that can't happen against a real client.
func pairApp(t *testing.T, daemon *Daemon, client BunkerClient, signerPub string) {
	t.Helper()
	daemon.handler.SetPendingSecret("s3cr3t")
	req := buildRequest(t, signerPub, nip46.MethodConnect, []string{signerPub, "s3cr3t"})
	resp, wentToQueue := sendAndAwaitDecision(t, daemon, req, func(id string) {
		if err := client.Approve(id, nil); err != nil {
			t.Fatal(err)
		}
	})
	if !wentToQueue || resp.Result != "ack" {
		t.Fatalf("pairApp: wentToQueue=%v result=%q, want true/ack", wentToQueue, resp.Result)
	}
}

// TestPermissionScenarios drives every button openApprovalDialog/
// openDurationDialog offers (Approve Once, Approve Always -- all four
// durations/budget it constructs grants for -- Reject Once, Reject Always)
// through the exact BunkerClient.Approve/Reject entry points board.go
// calls, over both transports a real daemon is ever driven through
// (localClient -- the Windows/in-process path, and ipcClient -- the
// Linux/macOS `ncli bunker attach` path). Existing tests (handler_test.go,
// policy_test.go) either pre-seed Store.Remember directly or resolve the
// Queue directly with Queue.Resolve -- neither exercises the actual
// Approve/Reject(remember) call that's supposed to persist a grant AND
// unblock the pending request in one step, which is what a human clicking
// "Always" in the TUI really does.
func TestPermissionScenarios(t *testing.T) {
	transports := []struct {
		name string
		wrap func(t *testing.T, d *Daemon) BunkerClient
	}{
		{"LocalClient", func(t *testing.T, d *Daemon) BunkerClient { return localClientFor(d) }},
		{"IPCClient", ipcClientFor},
	}

	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			t.Run("ApproveOnce_NeverRemembered", func(t *testing.T) {
				daemon, signerPub, clientPub := newTestDaemon(t)
				client := tr.wrap(t, daemon)
				pairApp(t, daemon, client, signerPub)

				for i := range 2 {
					req := buildRequest(t, signerPub, nip46.MethodPing, nil)
					resp, wentToQueue := sendAndAwaitDecision(t, daemon, req, func(id string) {
						if err := client.Approve(id, nil); err != nil {
							t.Fatal(err)
						}
					})
					if !wentToQueue {
						t.Fatalf("request %d resolved without asking -- Approve Once must never persist a grant", i)
					}
					if resp.Result != "pong" {
						t.Fatalf("request %d result = %q, want pong", i, resp.Result)
					}
				}

				sessions := daemon.cfg.Store.List()
				if len(sessions) != 1 || sessions[0].Pubkey != clientPub || len(sessions[0].Grants) != 0 {
					t.Fatalf("Store.List() = %+v, want one grantless session for %s", sessions, clientPub)
				}
			})

			t.Run("ApproveAlways_PersistsAndSkipsFutureAsks", func(t *testing.T) {
				daemon, signerPub, _ := newTestDaemon(t)
				client := tr.wrap(t, daemon)

				req1 := buildRequest(t, signerPub, nip46.MethodPing, nil)
				resp1, wentToQueue1 := sendAndAwaitDecision(t, daemon, req1, func(id string) {
					grant := GrantForever(nip46.MethodPing, nil, time.Now())
					if err := client.Approve(id, &grant); err != nil {
						t.Fatal(err)
					}
				})
				if !wentToQueue1 || resp1.Result != "pong" {
					t.Fatalf("first request: wentToQueue=%v result=%q, want true/pong", wentToQueue1, resp1.Result)
				}

				sessions := daemon.cfg.Store.List()
				if len(sessions) != 1 || len(sessions[0].Grants) != 1 {
					t.Fatalf("Store.List() after Approve Always = %+v, want one session with one grant", sessions)
				}

				// The whole point of "Always": a second request must be
				// answered without ever touching the queue.
				req2 := buildRequest(t, signerPub, nip46.MethodPing, nil)
				resp2, wentToQueue2 := sendAndAwaitDecision(t, daemon, req2, func(id string) {
					t.Fatalf("second request reached the approval queue (id=%s) -- the remembered grant should have auto-approved it", id)
				})
				if wentToQueue2 {
					t.Fatal("second request went to the queue -- Approve Always did not stick")
				}
				if resp2.Result != "pong" {
					t.Fatalf("second request result = %q, want pong", resp2.Result)
				}
			})

			t.Run("RejectOnce_NeverRemembered", func(t *testing.T) {
				daemon, signerPub, clientPub := newTestDaemon(t)
				client := tr.wrap(t, daemon)
				pairApp(t, daemon, client, signerPub)

				for i := range 2 {
					req := buildRequest(t, signerPub, nip46.MethodPing, nil)
					resp, wentToQueue := sendAndAwaitDecision(t, daemon, req, func(id string) {
						if err := client.Reject(id, nil); err != nil {
							t.Fatal(err)
						}
					})
					if !wentToQueue {
						t.Fatalf("request %d resolved without asking -- Reject Once must never persist a grant", i)
					}
					if resp.Error == "" {
						t.Fatalf("request %d was not rejected", i)
					}
				}

				sessions := daemon.cfg.Store.List()
				if len(sessions) != 1 || sessions[0].Pubkey != clientPub || len(sessions[0].Grants) != 0 {
					t.Fatalf("Store.List() = %+v, want one grantless session for %s", sessions, clientPub)
				}
			})

			t.Run("RejectAlways_PersistsDenyAndSkipsFutureAsks", func(t *testing.T) {
				daemon, signerPub, _ := newTestDaemon(t)
				client := tr.wrap(t, daemon)

				req1 := buildRequest(t, signerPub, nip46.MethodPing, nil)
				resp1, wentToQueue1 := sendAndAwaitDecision(t, daemon, req1, func(id string) {
					grant := DenyAlways(nip46.MethodPing, time.Now())
					if err := client.Reject(id, &grant); err != nil {
						t.Fatal(err)
					}
				})
				if !wentToQueue1 || resp1.Error == "" {
					t.Fatalf("first request: wentToQueue=%v error=%q, want true/non-empty", wentToQueue1, resp1.Error)
				}

				req2 := buildRequest(t, signerPub, nip46.MethodPing, nil)
				resp2, wentToQueue2 := sendAndAwaitDecision(t, daemon, req2, func(id string) {
					t.Fatalf("second request reached the approval queue (id=%s) -- Reject Always should have auto-rejected it", id)
				})
				if wentToQueue2 {
					t.Fatal("second request went to the queue -- Reject Always did not stick")
				}
				if resp2.Error == "" {
					t.Fatal("second request was not auto-rejected")
				}
			})

			// sign_event's kind axis: the TUI's "Always: kind N" button must
			// only ever cover that one kind, never silently widen to others
			// -- even once resolved through Approve/Store.Remember, not just
			// Store.Decide directly (policy_test.go's own coverage).
			t.Run("SignEvent_ExactKindGrant_OtherKindStillAsks", func(t *testing.T) {
				daemon, signerPub, _ := newTestDaemon(t)
				client := tr.wrap(t, daemon)

				unsigned1 := nip01.NewUnsignedEvent(1, signerPub, "hello")
				raw1b, err := json.Marshal(unsigned1)
				if err != nil {
					t.Fatal(err)
				}
				raw1 := string(raw1b)
				req1 := buildRequest(t, signerPub, nip46.MethodSignEvent, []string{raw1})

				kind1 := 1
				resp1, wentToQueue1 := sendAndAwaitDecision(t, daemon, req1, func(id string) {
					grant := GrantForever(nip46.MethodSignEvent, &kind1, time.Now())
					if err := client.Approve(id, &grant); err != nil {
						t.Fatal(err)
					}
				})
				if !wentToQueue1 || resp1.Error != "" {
					t.Fatalf("kind 1 request: wentToQueue=%v error=%q, want true/empty", wentToQueue1, resp1.Error)
				}

				// Same kind again: must not ask.
				reqSame := buildRequest(t, signerPub, nip46.MethodSignEvent, []string{raw1})
				respSame, wentToQueueSame := sendAndAwaitDecision(t, daemon, reqSame, func(id string) {
					t.Fatalf("same-kind request reached the approval queue (id=%s)", id)
				})
				if wentToQueueSame || respSame.Error != "" {
					t.Fatalf("same-kind request: wentToQueue=%v error=%q, want false/empty", wentToQueueSame, respSame.Error)
				}

				// A different kind must still ask -- the exact-kind grant
				// must never silently widen.
				unsigned7 := nip01.NewUnsignedEvent(7, signerPub, "+")
				raw7b, err := json.Marshal(unsigned7)
				if err != nil {
					t.Fatal(err)
				}
				raw7 := string(raw7b)
				req7 := buildRequest(t, signerPub, nip46.MethodSignEvent, []string{raw7})
				resp7, wentToQueue7 := sendAndAwaitDecision(t, daemon, req7, func(id string) {
					if err := client.Reject(id, nil); err != nil {
						t.Fatal(err)
					}
				})
				if !wentToQueue7 {
					t.Fatal("kind 7 request resolved without asking -- the kind:1 grant must not cover kind 7")
				}
				if resp7.Error == "" {
					t.Fatal("kind 7 request was not rejected")
				}
			})

			// Budget-limited grants ("Next 10 uses" in openDurationDialog)
			// must exhaust back to Ask once resolved through the real
			// Approve path, matching policy_test.go's direct-Store coverage.
			t.Run("ApproveAlways_BudgetLimited_ExhaustsBackToAsk", func(t *testing.T) {
				daemon, signerPub, _ := newTestDaemon(t)
				client := tr.wrap(t, daemon)

				// GrantForUses(..., 1, ...) covers exactly one *future*
				// automatic approval -- req1 itself is decided directly by
				// the human's explicit Approve call (Queue.Resolve, never
				// touching resolveGrantLocked's own budget decrement), so
				// the budget only starts being spent from req2 onward. See
				// policy_test.go's TestDecide_BudgetLimitedGrantExhausts for
				// the same accounting proven directly against Store.Decide.
				req1 := buildRequest(t, signerPub, nip46.MethodPing, nil)
				resp1, wentToQueue1 := sendAndAwaitDecision(t, daemon, req1, func(id string) {
					grant := GrantForUses(nip46.MethodPing, nil, 1, time.Now())
					if err := client.Approve(id, &grant); err != nil {
						t.Fatal(err)
					}
				})
				if !wentToQueue1 || resp1.Result != "pong" {
					t.Fatalf("first request: wentToQueue=%v result=%q", wentToQueue1, resp1.Result)
				}

				// Use 1 of 1: must not ask.
				req2 := buildRequest(t, signerPub, nip46.MethodPing, nil)
				resp2, wentToQueue2 := sendAndAwaitDecision(t, daemon, req2, func(id string) {
					t.Fatalf("second (still-budgeted) request reached the queue (id=%s)", id)
				})
				if wentToQueue2 || resp2.Result != "pong" {
					t.Fatalf("second request: wentToQueue=%v result=%q, want false/pong", wentToQueue2, resp2.Result)
				}

				// Budget exhausted: must ask again.
				req3 := buildRequest(t, signerPub, nip46.MethodPing, nil)
				resp3, wentToQueue3 := sendAndAwaitDecision(t, daemon, req3, func(id string) {
					if err := client.Approve(id, nil); err != nil {
						t.Fatal(err)
					}
				})
				if !wentToQueue3 {
					t.Fatal("third request resolved without asking -- the 2-use budget should have been exhausted")
				}
				if resp3.Result != "pong" {
					t.Fatalf("third request result = %q, want pong", resp3.Result)
				}
			})

			// Duration-limited grants ("1 hour"/"24 hours"/"7 days" in
			// openDurationDialog) must expire back to Ask once resolved
			// through the real Approve path.
			t.Run("ApproveAlways_DurationLimited_ExpiresBackToAsk", func(t *testing.T) {
				daemon, signerPub, _ := newTestDaemon(t)
				client := tr.wrap(t, daemon)

				fc := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
				daemon.cfg.Store.now = fc.Now

				req1 := buildRequest(t, signerPub, nip46.MethodPing, nil)
				resp1, wentToQueue1 := sendAndAwaitDecision(t, daemon, req1, func(id string) {
					grant := GrantForDuration(nip46.MethodPing, nil, time.Hour, fc.Now())
					if err := client.Approve(id, &grant); err != nil {
						t.Fatal(err)
					}
				})
				if !wentToQueue1 || resp1.Result != "pong" {
					t.Fatalf("first request: wentToQueue=%v result=%q", wentToQueue1, resp1.Result)
				}

				// Still within the hour: must not ask.
				req2 := buildRequest(t, signerPub, nip46.MethodPing, nil)
				resp2, wentToQueue2 := sendAndAwaitDecision(t, daemon, req2, func(id string) {
					t.Fatalf("second (still-valid) request reached the queue (id=%s)", id)
				})
				if wentToQueue2 || resp2.Result != "pong" {
					t.Fatalf("second request: wentToQueue=%v result=%q, want false/pong", wentToQueue2, resp2.Result)
				}

				// Past the hour: must ask again.
				fc.Advance(2 * time.Hour)
				req3 := buildRequest(t, signerPub, nip46.MethodPing, nil)
				resp3, wentToQueue3 := sendAndAwaitDecision(t, daemon, req3, func(id string) {
					if err := client.Approve(id, nil); err != nil {
						t.Fatal(err)
					}
				})
				if !wentToQueue3 {
					t.Fatal("third request resolved without asking -- the 1-hour grant should have expired")
				}
				if resp3.Result != "pong" {
					t.Fatalf("third request result = %q, want pong", resp3.Result)
				}
			})

			// board.go's "Manage Grants" overlay: 'x' on one grant among
			// several must revoke only that one, over the real
			// BunkerClient.RevokeGrant path (including, for the IPC
			// transport, a *int Kind crossing the socket both ways).
			t.Run("RevokeGrant_LeavesOtherGrantsAlone", func(t *testing.T) {
				daemon, signerPub, clientPub := newTestDaemon(t)
				client := tr.wrap(t, daemon)

				req1 := buildRequest(t, signerPub, nip46.MethodPing, nil)
				_, wentToQueue1 := sendAndAwaitDecision(t, daemon, req1, func(id string) {
					grant := GrantForever(nip46.MethodPing, nil, time.Now())
					if err := client.Approve(id, &grant); err != nil {
						t.Fatal(err)
					}
				})
				if !wentToQueue1 {
					t.Fatal("ping request did not go through the approval queue")
				}

				unsigned := nip01.NewUnsignedEvent(1, signerPub, "hello")
				rawB, err := json.Marshal(unsigned)
				if err != nil {
					t.Fatal(err)
				}
				kind1 := 1
				req2 := buildRequest(t, signerPub, nip46.MethodSignEvent, []string{string(rawB)})
				_, wentToQueue2 := sendAndAwaitDecision(t, daemon, req2, func(id string) {
					grant := GrantForever(nip46.MethodSignEvent, &kind1, time.Now())
					if err := client.Approve(id, &grant); err != nil {
						t.Fatal(err)
					}
				})
				if !wentToQueue2 {
					t.Fatal("sign_event request did not go through the approval queue")
				}

				revoked, err := client.RevokeGrant(clientPub, nip46.MethodSignEvent, &kind1)
				if err != nil {
					t.Fatal(err)
				}
				if !revoked {
					t.Fatal("RevokeGrant() = false, want true")
				}

				// ping must still be auto-approved -- untouched.
				req3 := buildRequest(t, signerPub, nip46.MethodPing, nil)
				resp3, wentToQueue3 := sendAndAwaitDecision(t, daemon, req3, func(id string) {
					t.Fatalf("ping request reached the queue (id=%s) -- RevokeGrant must not have touched it", id)
				})
				if wentToQueue3 || resp3.Result != "pong" {
					t.Fatalf("ping after RevokeGrant(sign_event): wentToQueue=%v result=%q, want false/pong", wentToQueue3, resp3.Result)
				}

				// sign_event kind 1 must ask again -- its grant is gone.
				req4 := buildRequest(t, signerPub, nip46.MethodSignEvent, []string{string(rawB)})
				_, wentToQueue4 := sendAndAwaitDecision(t, daemon, req4, func(id string) {
					if err := client.Reject(id, nil); err != nil {
						t.Fatal(err)
					}
				})
				if !wentToQueue4 {
					t.Fatal("sign_event kind 1 resolved without asking -- its grant should have been revoked")
				}
			})

			// board.go's "Manage Grants" overlay: 'e' (Extend) re-scopes an
			// existing grant with no pending request involved at all,
			// through BunkerClient.SetGrant.
			t.Run("SetGrant_ExtendsExistingGrantWithoutAPendingRequest", func(t *testing.T) {
				daemon, signerPub, clientPub := newTestDaemon(t)
				client := tr.wrap(t, daemon)

				budget1 := GrantForUses(nip46.MethodPing, nil, 1, time.Now())
				if err := client.SetGrant(clientPub, budget1); err != nil {
					t.Fatal(err)
				}

				// Use up the sole use.
				req1 := buildRequest(t, signerPub, nip46.MethodPing, nil)
				resp1, wentToQueue1 := sendAndAwaitDecision(t, daemon, req1, func(id string) {
					t.Fatalf("first request reached the queue (id=%s) -- SetGrant should have covered it", id)
				})
				if wentToQueue1 || resp1.Result != "pong" {
					t.Fatalf("first request: wentToQueue=%v result=%q, want false/pong", wentToQueue1, resp1.Result)
				}

				// Budget exhausted: must ask again -- extend it in place to
				// "forever" (Store.Remember's own same-scope replacement,
				// see TestRemember_ReplacesSameScope) while deciding it.
				req2 := buildRequest(t, signerPub, nip46.MethodPing, nil)
				_, wentToQueue2 := sendAndAwaitDecision(t, daemon, req2, func(id string) {
					forever := GrantForever(nip46.MethodPing, nil, time.Now())
					if err := client.SetGrant(clientPub, forever); err != nil {
						t.Fatal(err)
					}
					if err := client.Approve(id, nil); err != nil {
						t.Fatal(err)
					}
				})
				if !wentToQueue2 {
					t.Fatal("second request resolved without asking -- the 1-use budget should have been exhausted first")
				}

				// The extended grant must now cover future requests.
				req3 := buildRequest(t, signerPub, nip46.MethodPing, nil)
				resp3, wentToQueue3 := sendAndAwaitDecision(t, daemon, req3, func(id string) {
					t.Fatalf("third request reached the queue (id=%s) -- the extended grant should have covered it", id)
				})
				if wentToQueue3 || resp3.Result != "pong" {
					t.Fatalf("third request: wentToQueue=%v result=%q, want false/pong", wentToQueue3, resp3.Result)
				}
			})
		})
	}
}

// extractBunkerSecret pulls the single-use pairing secret back out of a
// bunker:// URI generated by NewBunkerPairingWithGrants/client.Connect --
// the standing-in-for-a-QR-code shape a real client would present back on
// its own "connect" request, same as BunkerURI's own doc comment
// describes.
func extractBunkerSecret(t *testing.T, uri string) string {
	t.Helper()
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("failed to parse bunker:// URI %q: %v", uri, err)
	}
	secret := u.Query().Get("secret")
	if secret == "" {
		t.Fatalf("bunker:// URI %q has no secret param", uri)
	}
	return secret
}

// TestConnect_WithGrants_BunkerDirection is `ncli bunker connect --grants
// <file>`'s own reason to exist, proven end to end: the app that presents
// the freshly generated bunker:// URI's secret gets the spec's grants (and
// nickname) applied with no human decision in between, and only those --
// over both transports, since ipcClient's own GrantSpec JSON round trip
// (KindsSpec.MarshalJSON/UnmarshalJSON) is exactly where
// TestGrantSpec_IPCRoundTrip found a real bug during development.
func TestConnect_WithGrants_BunkerDirection(t *testing.T) {
	transports := []struct {
		name string
		wrap func(t *testing.T, d *Daemon) BunkerClient
	}{
		{"LocalClient", func(t *testing.T, d *Daemon) BunkerClient { return localClientFor(d) }},
		{"IPCClient", ipcClientFor},
	}

	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			daemon, signerPub, clientPub := newTestDaemon(t)
			client := tr.wrap(t, daemon)

			spec, err := LoadGrantSpec(writeSpecFile(t, `
kind: bunker
spec:
  nickname: "Scripted App"
  grants:
    - method: ping
    - method: sign_event
      kinds: [1]
      expires: 24h
`))
			if err != nil {
				t.Fatal(err)
			}

			uri, err := client.Connect("", spec)
			if err != nil {
				t.Fatal(err)
			}
			secret := extractBunkerSecret(t, uri)

			connReq := buildRequest(t, signerPub, nip46.MethodConnect, []string{signerPub, secret})
			connResp := parseResponse(t, daemon.handler.Handle(connReq, nip46.EncryptionNIP04))
			if connResp.Result != "ack" {
				t.Fatalf("connect result = %q, error = %q, want ack", connResp.Result, connResp.Error)
			}

			sessions := daemon.cfg.Store.List()
			if len(sessions) != 1 || sessions[0].Pubkey != clientPub {
				t.Fatalf("Store.List() = %+v, want one session for %s", sessions, clientPub)
			}
			if sessions[0].Nickname != "Scripted App" {
				t.Errorf("Nickname = %q, want %q", sessions[0].Nickname, "Scripted App")
			}
			if len(sessions[0].Grants) != 2 {
				t.Fatalf("Grants = %+v, want 2 (ping + sign_event kind 1)", sessions[0].Grants)
			}

			// The whole point: ping must now be auto-approved, no human
			// decision needed.
			pingReq := buildRequest(t, signerPub, nip46.MethodPing, nil)
			resp, wentToQueue := sendAndAwaitDecision(t, daemon, pingReq, func(id string) {
				t.Fatalf("ping request reached the queue (id=%s) -- the spec's grant should have covered it", id)
			})
			if wentToQueue || resp.Result != "pong" {
				t.Fatalf("ping after grants-on-connect: wentToQueue=%v result=%q, want false/pong", wentToQueue, resp.Result)
			}

			// sign_event kind 7 (not granted -- only kind 1 was) must
			// still ask, proving the spec didn't silently over-grant.
			unsigned := nip01.NewUnsignedEvent(7, signerPub, "+")
			raw, err := json.Marshal(unsigned)
			if err != nil {
				t.Fatal(err)
			}
			signReq := buildRequest(t, signerPub, nip46.MethodSignEvent, []string{string(raw)})
			_, wentToQueue2 := sendAndAwaitDecision(t, daemon, signReq, func(id string) {
				if err := client.Reject(id, nil); err != nil {
					t.Fatal(err)
				}
			})
			if !wentToQueue2 {
				t.Fatal("kind 7 sign_event resolved without asking -- only kind 1 was granted")
			}
		})
	}
}

// TestConnect_WithGrants_NostrconnectDirection mirrors the above for the
// signer-speaks-first flow (InitiateNostrconnectWithGrants), where the
// app's pubkey is already known from the nostrconnect:// URI itself, so
// grants are applied directly rather than staged on the handler -- see
// that method's own doc comment. Plays the client side by hand (receive
// the daemon's own "connect" request, echo the secret back), the same
// technique TestDaemon_NostrconnectFlow (daemon_test.go) already
// established for this direction.
func TestConnect_WithGrants_NostrconnectDirection(t *testing.T) {
	relay := newFakeRelay(t)

	daemon, signerPub, clientPub := newTestDaemon(t)
	client := localClientFor(daemon)
	daemon.cfg.Relays = []string{relay.url.String()}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go daemon.Run(ctx)

	clientConn, err := relayclient.Connect(ctx, relay.url)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	clientSubID := "test-nostrconnect-grants-sub"
	clientConn.SubscribeWithID(clientSubID, nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds: []int{nip46.KindRequest},
		Tags:  map[string][]string{"p": {clientPub}},
	}))
	incoming := clientConn.Events(clientSubID)

	spec, err := LoadGrantSpec(writeSpecFile(t, `
kind: bunker
spec:
  nickname: "Nostrconnect App"
  grants:
    - method: ping
`))
	if err != nil {
		t.Fatal(err)
	}

	const secret = "nostrconnect-grants-secret"
	nostrconnectURI := "nostrconnect://" + clientPub +
		"?relay=" + url.QueryEscape(relay.url.String()) +
		"&secret=" + secret +
		"&metadata=" + url.QueryEscape(`{"name":"Nostrconnect App"}`)

	connectErr := make(chan error, 1)
	go func() {
		_, err := client.Connect(nostrconnectURI, spec)
		connectErr <- err
	}()

	select {
	case ev := <-incoming:
		req, err := nip46.ParseRequestEvent(ev.Event, testClientPriv)
		if err != nil {
			t.Fatalf("client failed to parse the daemon's connect request: %v", err)
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
	case err := <-connectErr:
		if err != nil {
			t.Fatalf("Connect() error = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect never returned after the client's echo response")
	}

	sessions := daemon.cfg.Store.List()
	if len(sessions) != 1 || sessions[0].Pubkey != clientPub {
		t.Fatalf("Store.List() = %+v, want one session for %s", sessions, clientPub)
	}
	if sessions[0].Nickname != "Nostrconnect App" {
		t.Errorf("Nickname = %q, want %q (spec wins over self-reported metadata)", sessions[0].Nickname, "Nostrconnect App")
	}
	if len(sessions[0].Grants) != 1 {
		t.Fatalf("Grants = %+v, want 1 (ping)", sessions[0].Grants)
	}
}
