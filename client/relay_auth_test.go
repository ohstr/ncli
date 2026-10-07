package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ohstr/nmilat/nip01"
)

// testAuthPrivKey is an arbitrary, never-funded key used only to sign
// throwaway events and authenticate in these tests.
const testAuthPrivKey = "0acd12cbf0fb87cd13b17bc9b57dffd11b3870b407984cec5a4ce2a69b90268c"

// newMembershipGatedTestRelay starts an in-process relay that challenges
// on connect (NIP-42) and closes any REQ as "restricted: ..." until a
// signed AUTH event arrives, mirroring a real MembershipRequired relay
// closely enough to prove identityHex actually reaches
// relayclient.ReadEventsFromRelayWithAuth through this package's own
// Find/DumpFromTargets/mergeEventsFromTargets plumbing -- the handshake
// and retry logic themselves are nmilat's own, already covered there.
func newMembershipGatedTestRelay(t *testing.T, event *nip01.Event) *httptest.Server {
	t.Helper()
	const challenge = "ncli-identity-threading-test"

	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		if err := conn.WriteJSON([]interface{}{"AUTH", challenge}); err != nil {
			return
		}

		authed := false
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg []json.RawMessage
			if err := json.Unmarshal(data, &msg); err != nil || len(msg) < 2 {
				return
			}
			var kind string
			if err := json.Unmarshal(msg[0], &kind); err != nil {
				return
			}

			switch kind {
			case "AUTH":
				var ev nip01.Event
				if err := json.Unmarshal(msg[1], &ev); err != nil {
					return
				}
				if err := ev.Verify(); err != nil {
					return
				}
				authed = true
				_ = conn.WriteJSON([]interface{}{"OK", ev.ID, true, "auth-success"})

			case "REQ":
				var subID string
				if err := json.Unmarshal(msg[1], &subID); err != nil {
					return
				}
				if !authed {
					_ = conn.WriteJSON([]interface{}{"CLOSED", subID, "restricted: valid NIP-43 membership required"})
					continue
				}
				if event != nil {
					_ = conn.WriteJSON([]interface{}{"EVENT", subID, event})
				}
				_ = conn.WriteJSON([]interface{}{"EOSE", subID})
			}
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func testRelayURL(t *testing.T, server *httptest.Server) string {
	t.Helper()
	return "ws" + server.URL[len("http"):]
}

// newAlwaysRestrictedTestRelay is newMembershipGatedTestRelay's counterpart
// for the case nmilat#64 added a signal for: an identity that
// authenticates successfully (a real, valid signature) but is still
// denied every REQ -- a non-member, not an unauthenticated connection.
// Always "restricted: ...", auth or not, so a caller can't mistake this
// for "nothing matched."
func newAlwaysRestrictedTestRelay(t *testing.T) *httptest.Server {
	t.Helper()
	const challenge = "ncli-restricted-signal-test"

	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		if err := conn.WriteJSON([]interface{}{"AUTH", challenge}); err != nil {
			return
		}

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg []json.RawMessage
			if err := json.Unmarshal(data, &msg); err != nil || len(msg) < 2 {
				return
			}
			var kind string
			if err := json.Unmarshal(msg[0], &kind); err != nil {
				return
			}

			switch kind {
			case "AUTH":
				var ev nip01.Event
				if err := json.Unmarshal(msg[1], &ev); err != nil {
					return
				}
				if err := ev.Verify(); err != nil {
					return
				}
				_ = conn.WriteJSON([]interface{}{"OK", ev.ID, true, "auth-success"})

			case "REQ":
				var subID string
				if err := json.Unmarshal(msg[1], &subID); err != nil {
					return
				}
				_ = conn.WriteJSON([]interface{}{"CLOSED", subID, "restricted: valid membership required"})
			}
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestMergeEventsFromTargets_AuthenticatesWithIdentity(t *testing.T) {
	event := nip01.NewEvent(1, "members only")
	if err := event.Sign(testAuthPrivKey); err != nil {
		t.Fatal(err)
	}
	server := newMembershipGatedTestRelay(t, event)

	targets, err := TargetsFromRelayList([]string{testRelayURL(t, server)})
	if err != nil {
		t.Fatal(err)
	}
	filters := nip01.NewSubscriptionFilterGroup()
	filters.Add(&nip01.SubscriptionFilter{Kinds: []int{1}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := mergeEventsFromTargets(ctx, targets, filters, 0, testAuthPrivKey)
	if err != nil {
		t.Fatalf("mergeEventsFromTargets() with identity error = %v", err)
	}
	if len(events) != 1 || events[0].ID != event.ID {
		t.Fatalf("mergeEventsFromTargets() events = %v, want exactly [%s]", events, event.ID)
	}
}

func TestMergeEventsFromTargets_AnonymousGetsNothingFromGatedRelay(t *testing.T) {
	event := nip01.NewEvent(1, "members only")
	if err := event.Sign(testAuthPrivKey); err != nil {
		t.Fatal(err)
	}
	server := newMembershipGatedTestRelay(t, event)

	targets, err := TargetsFromRelayList([]string{testRelayURL(t, server)})
	if err != nil {
		t.Fatal(err)
	}
	filters := nip01.NewSubscriptionFilterGroup()
	filters.Add(&nip01.SubscriptionFilter{Kinds: []int{1}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Refused, and says so: an empty result would read as "nothing matched".
	events, err := mergeEventsFromTargets(ctx, targets, filters, 0, "")
	if !errors.Is(err, ErrRestricted) {
		t.Fatalf("mergeEventsFromTargets() with no identity error = %v, want ErrRestricted", err)
	}
	if len(events) != 0 {
		t.Fatalf("mergeEventsFromTargets() events = %v, want none against a gated relay with no identity", events)
	}
}

func TestDumpFromTargets_ThreadsIdentityThrough(t *testing.T) {
	event := nip01.NewEvent(1, "members only")
	if err := event.Sign(testAuthPrivKey); err != nil {
		t.Fatal(err)
	}
	server := newMembershipGatedTestRelay(t, event)

	targets, err := TargetsFromRelayList([]string{testRelayURL(t, server)})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	outPath := filepath.Join(t.TempDir(), "out.json")
	if err := DumpFromTargets(ctx, targets, outPath, nil, 0, testAuthPrivKey); err != nil {
		t.Fatalf("DumpFromTargets() with identity error = %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("expected %s to be written once authenticated: %v", outPath, err)
	}
	var got []*nip01.Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("output file is not valid JSON events: %v", err)
	}
	if len(got) != 1 || got[0].ID != event.ID {
		t.Fatalf("output events = %v, want exactly [%s]", got, event.ID)
	}
}

func TestFind_ThreadsIdentityThrough(t *testing.T) {
	event := nip01.NewEvent(1, "members only")
	if err := event.Sign(testAuthPrivKey); err != nil {
		t.Fatal(err)
	}
	server := newMembershipGatedTestRelay(t, event)

	targets, err := TargetsFromRelayList([]string{testRelayURL(t, server)})
	if err != nil {
		t.Fatal(err)
	}
	filtersSpec := []*FilterSpec{NewFilterSpec(&nip01.SubscriptionFilter{Kinds: []int{1}})}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	savePath := filepath.Join(t.TempDir(), "found.json")
	if err := Find(ctx, nil, filtersSpec, targets, savePath, 0, testAuthPrivKey); err != nil {
		t.Fatalf("Find() with identity error = %v", err)
	}

	data, err := os.ReadFile(savePath)
	if err != nil {
		t.Fatalf("expected %s to be written once authenticated: %v", savePath, err)
	}
	var got []*nip01.Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("saved file is not valid JSON events: %v", err)
	}
	if len(got) != 1 || got[0].ID != event.ID {
		t.Fatalf("saved events = %v, want exactly [%s]", got, event.ID)
	}
}

func TestMergeEventsFromTargets_ReturnsErrRestrictedWhenAuthenticatedButDenied(t *testing.T) {
	server := newAlwaysRestrictedTestRelay(t)
	targets, err := TargetsFromRelayList([]string{testRelayURL(t, server)})
	if err != nil {
		t.Fatal(err)
	}
	filters := nip01.NewSubscriptionFilterGroup()
	filters.Add(&nip01.SubscriptionFilter{Kinds: []int{1}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := mergeEventsFromTargets(ctx, targets, filters, 0, testAuthPrivKey)
	if !errors.Is(err, ErrRestricted) {
		t.Fatalf("mergeEventsFromTargets() error = %v, want ErrRestricted", err)
	}
	if len(events) != 0 {
		t.Errorf("events = %v, want none", events)
	}
}

func TestQueryTargetsWithAuth_ReturnsErrRestrictedWhenAuthenticatedButDenied(t *testing.T) {
	server := newAlwaysRestrictedTestRelay(t)
	targets, err := TargetsFromRelayList([]string{testRelayURL(t, server)})
	if err != nil {
		t.Fatal(err)
	}
	filters := nip01.NewSubscriptionFilterGroup()
	filters.Add(&nip01.SubscriptionFilter{Kinds: []int{1}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := QueryTargetsWithAuth(ctx, targets, filters, 0, testAuthPrivKey); !errors.Is(err, ErrRestricted) {
		t.Fatalf("QueryTargetsWithAuth() error = %v, want ErrRestricted", err)
	}

	// Anonymous gets the same signal: refused, not "nothing matched".
	if _, err := QueryTargets(ctx, targets, filters, 0); !errors.Is(err, ErrRestricted) {
		t.Errorf("QueryTargets() (anonymous) error = %v, want ErrRestricted", err)
	}
}

func TestFind_ReturnsErrRestrictedWhenAuthenticatedButDenied(t *testing.T) {
	server := newAlwaysRestrictedTestRelay(t)
	targets, err := TargetsFromRelayList([]string{testRelayURL(t, server)})
	if err != nil {
		t.Fatal(err)
	}
	filtersSpec := []*FilterSpec{NewFilterSpec(&nip01.SubscriptionFilter{Kinds: []int{1}})}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = Find(ctx, nil, filtersSpec, targets, "", 0, testAuthPrivKey)
	if !errors.Is(err, ErrRestricted) {
		t.Fatalf("Find() error = %v, want ErrRestricted", err)
	}
}

func TestDumpFromTargets_ReturnsErrRestrictedWhenAuthenticatedButDenied(t *testing.T) {
	server := newAlwaysRestrictedTestRelay(t)
	targets, err := TargetsFromRelayList([]string{testRelayURL(t, server)})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	outPath := filepath.Join(t.TempDir(), "out.json")
	err = DumpFromTargets(ctx, targets, outPath, nil, 0, testAuthPrivKey)
	if !errors.Is(err, ErrRestricted) {
		t.Fatalf("DumpFromTargets() error = %v, want ErrRestricted", err)
	}
	if _, statErr := os.Stat(outPath); statErr == nil {
		t.Error("output file was written despite a restricted result")
	}
}
