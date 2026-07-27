package bunker

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/utils"
)

// TestDaemonHistory_GrantCoveredRequestsAreRecorded is the regression test
// for followup issue ("`ncli bunker history` returned empty after a real,
// completed `--grants` pairing", integration/agent-eval/followup/issues.md):
// a `--grants`-pre-authorized pairing (or any later request an existing
// "always allow" grant already covers) used to never produce a Request
// History entry, even though the session was real and the request was
// actually served.
//
// Root cause: Handler.Handle only reached h.Queue.Add (the sole path into
// Queue.OnResolved, which NewDaemon wires to Daemon.recordHistory) when
// Store.Decide returned Ask. A pending GrantSpec makes "connect" itself
// skip straight to execute() (see TestHandle_Connect_WithPendingGrantSpec_
// SkipsApprovalQueue -- a deliberate, necessary bypass: without it, an
// unattended agent's connect would hang forever waiting for a human on
// Queue.Add), and for every method after that, once a grant is Remembered,
// Store.Decide returns Allow, not Ask, for exactly the same reason -- so the
// gap wasn't specific to `--grants` or to "connect", but to *any* request
// already covered by a standing "always allow" grant.
//
// Fix: Handler.OnAutoApproved fires at both bypass points (handler.go),
// wired to Daemon.recordAutoApproved (daemon.go), which records a
// HistoryEntry with AutoApproved: true -- same persistence/compaction path
// as every other resolution, distinguished in board.go's historyStatus as
// "Auto-approved" rather than "Approved (always)" (nothing new was
// remembered) or a one-off "Approved" (nobody actually decided anything,
// this time).
func TestDaemonHistory_GrantCoveredRequestsAreRecorded(t *testing.T) {
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
		Store:        store,
		Queue:        NewQueue(0, time.Minute),
	})

	// Pair via `ncli bunker connect --grants <file>`'s own mechanism:
	// SetPendingSecret + SetPendingGrants, exactly as command.go wires it.
	daemon.handler.SetPendingSecret("s3cr3t")
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
	daemon.handler.SetPendingGrants(spec)

	connectReq := buildRequest(t, signerPub, nip46.MethodConnect, []string{signerPub, "s3cr3t"})
	connectResp := parseResponse(t, daemon.handler.Handle(connectReq, nip46.EncryptionNIP04))
	if connectResp.Result != "ack" {
		t.Fatalf("connect Result = %q, error = %q, want ack", connectResp.Result, connectResp.Error)
	}

	// A second, later request that the grant above already covers -- the
	// general case, not just the initial pairing handshake.
	pingReq := buildRequest(t, signerPub, nip46.MethodPing, nil)
	pingResp := parseResponse(t, daemon.handler.Handle(pingReq, nip46.EncryptionNIP04))
	if pingResp.Result != "pong" {
		t.Fatalf("ping Result = %q, error = %q, want pong", pingResp.Result, pingResp.Error)
	}

	// The pairing and the grant are real: Trusted Apps shows the session.
	sessions := store.List()
	if len(sessions) != 1 || sessions[0].Pubkey != clientPub {
		t.Fatalf("Store.List() = %+v, want one session for %s", sessions, clientPub)
	}
	if len(sessions[0].Grants) != 1 || sessions[0].Grants[0].Method != nip46.MethodPing {
		t.Fatalf("Grants = %+v, want one ping grant", sessions[0].Grants)
	}

	// Both requests now show up in history, most-recent-first, marked
	// AutoApproved -- neither ever touched Queue.Add, but both are
	// visible to an agent (or human) auditing "what actually happened".
	history := daemon.History()
	if len(history) != 2 {
		t.Fatalf("daemon.History() = %+v, want 2 entries", history)
	}
	if history[0].Method != nip46.MethodPing || !history[0].AutoApproved || history[0].Verdict != Allow {
		t.Errorf("history[0] = %+v, want an auto-approved, allowed ping (most recent first)", history[0])
	}
	if history[1].Method != nip46.MethodConnect || !history[1].AutoApproved || history[1].Verdict != Allow {
		t.Errorf("history[1] = %+v, want an auto-approved, allowed connect", history[1])
	}
	for _, h := range history {
		if h.ClientKey != clientPub {
			t.Errorf("history entry %+v has ClientKey %q, want %s", h, h.ClientKey, clientPub)
		}
		if h.Remembered {
			t.Errorf("history entry %+v has Remembered = true, want false -- auto-approval reuses an existing grant, it doesn't create a new one", h)
		}
		if h.Expired {
			t.Errorf("history entry %+v has Expired = true, want false -- an auto-approved request never sat in the queue to expire", h)
		}
	}
}
