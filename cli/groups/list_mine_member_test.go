package groups

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
)

// newGroupsTestRelay starts a REAL, store-backed relay (relay.NewSessionHandler,
// not a mock) -- the --mine/--member test below exists specifically to prove
// the new flag's filtering holds up against the real server-side gate
// (nmilat's own deniedPrivateGroupEvent/deniedPrivateGroupPotentialEvent,
// relay/groups.go), not just against a hand-rolled fixture that assumes the
// server behaves correctly. authRequired is left off: a community relay
// gates on NIP-29 group privacy specifically, not a blanket NIP-42
// requirement -- see nmilat's own
// TestPrivateGroup_UntaggedQueryHidesPrivateGroupFromNonMembers for the
// server-side half of this same scenario.
func newGroupsTestRelay(t *testing.T) *url.URL {
	t.Helper()
	f, err := os.CreateTemp("", "groups-list-mine-member-*.db")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	t.Cleanup(func() { _ = os.Remove(f.Name()) })

	// The relay validates a client's AUTH "relay" tag against its own
	// configured nip11.Metadata.URL, so that URL has to be known before the
	// handshake can succeed -- start unstarted, first to learn the port
	// httptest assigns.
	srv := httptest.NewUnstartedServer(nil)
	wsURL, err := url.Parse("ws://" + srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	relayIdentity, err := client.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity (relay): %v", err)
	}

	metadata := &nip11.Metadata{
		Name: "groups-list-mine-member-test",
		URL:  wsURL.String(),
		Self: relayIdentity.PubKeyHex,
		Limitation: nip11.Limitation{
			MaxMessageLength: 1024 * 1024,
		},
	}
	store, err := relay.NewEventStore(f.Name(), &metadata.Limitation)
	if err != nil {
		t.Fatalf("relay.NewEventStore: %v", err)
	}
	t.Cleanup(store.Close)

	// kind:39000/39002 mirrors are relay-authored (relay/groups.go's
	// publishSelfSigned), which no-ops with no PrivKey configured --
	// relay.New's simplified constructor never sets one, hence building the
	// store+handler directly instead.
	handler := relay.NewSessionHandler(store, metadata, nil, relay.WithSessionPrivKey(relayIdentity.PrivKeyHex))
	handler.VerificationWorker.Start(1)
	t.Cleanup(handler.VerificationWorker.Stop)

	srv.Config.Handler = handler
	srv.Start()
	t.Cleanup(srv.Close)

	return wsURL
}

// execGroupsCmd runs "ncli groups <args...>" against a freshly built command
// tree (so each call starts with clean flag state), returning stdout and
// whatever error RunE produced. --json defaults on so callers parsing a
// read's output don't also have to pass it on every call; a setup call
// (create/edit) ignores stdout entirely.
func execGroupsCmd(t *testing.T, relayURL *url.URL, args ...string) (stdout string, err error) {
	t.Helper()
	cmd := NewGroupsCommand()
	cmd.PersistentFlags().Bool("json", true, "")
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(append([]string{"--relay", relayURL.String()}, args...))

	origStdout := os.Stdout
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	os.Stdout = w

	outCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		outCh <- buf.String()
	}()

	err = cmd.Execute()

	_ = w.Close()
	os.Stdout = origStdout
	stdout = <-outCh
	return stdout, err
}

func groupIDsFromListJSON(t *testing.T, stdout string) map[string]bool {
	t.Helper()
	var payload struct {
		Groups []struct {
			ID string `json:"id"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("unmarshal groups list JSON (%q): %v", stdout, err)
	}
	ids := make(map[string]bool, len(payload.Groups))
	for _, g := range payload.Groups {
		ids[g.ID] = true
	}
	return ids
}

// waitForGroupsListCount polls "groups list --identity creatorIdentity"
// (the broadest view available, bypassing --mine/--member filtering
// entirely) until it reports at least want groups, or fails the test after
// 2s -- the same durability race nmilat's own integration tests guard
// against (EventStore's batched write queue means a query sent immediately
// after create/edit's own OK can legitimately race ahead of that commit).
func waitForGroupsListCount(t *testing.T, relayURL *url.URL, creatorIdentity string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		stdout, err := execGroupsCmd(t, relayURL, "list", "--identity", creatorIdentity)
		if err == nil && len(groupIDsFromListJSON(t, stdout)) >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("groups list never reported >= %d groups (last stdout=%q err=%v)", want, stdout, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestGroupsList_Mine_RequiresIdentity and
// TestGroupsList_Member_ValidatesPubkey need no relay at all: both errors
// fire before runList ever reaches the network call, so --relay only needs
// to parse, never actually be dialed.
func TestGroupsList_Mine_RequiresIdentity(t *testing.T) {
	cmd := NewGroupsCommand()
	cmd.PersistentFlags().Bool("json", false, "")
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"--relay", "ws://localhost:1", "list", "--mine"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("--mine with no --identity = nil error, want a usage error")
	}
	var cliErr *common.CLIError
	if errors.As(err, &cliErr) && cliErr.Code != common.CodeUsage {
		t.Errorf("error code = %v, want %v", cliErr.Code, common.CodeUsage)
	}
}

func TestGroupsList_Member_ValidatesPubkey(t *testing.T) {
	cmd := NewGroupsCommand()
	cmd.PersistentFlags().Bool("json", false, "")
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"--relay", "ws://localhost:1", "list", "--member", "not-a-real-pubkey"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("--member with a malformed pubkey = nil error, want an invalid-input error")
	}
}

func TestGroupsList_MineAndMemberMutuallyExclusive(t *testing.T) {
	cmd := NewGroupsCommand()
	cmd.PersistentFlags().Bool("json", false, "")
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"--relay", "ws://localhost:1", "list", "--mine", "--member", "0000000000000000000000000000000000000000000000000000000000000000"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("--mine and --member together = nil error, want cobra's mutually-exclusive error")
	}
}

// TestGroupsList_MineAndMember_EndToEnd is the real-relay regression test
// for both ncli's own --mine/--member feature and, as a second layer of
// confidence independent of nmilat's own server-side test, the P0 fix this
// session shipped in nmilat (relay/groups.go's deniedPrivateGroupEvent):
// an untagged/#p-tagged group-kind query must never leak a private group's
// existence to a non-member, authenticated or not.
func TestGroupsList_MineAndMember_EndToEnd(t *testing.T) {
	relayURL := newGroupsTestRelay(t)

	creator, err := client.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity (creator): %v", err)
	}
	outsider, err := client.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity (outsider): %v", err)
	}

	const publicGroupID = "mine-member-public-group"
	const privateGroupID = "mine-member-private-group"

	if _, err := execGroupsCmd(t, relayURL, "create", publicGroupID, "--identity", creator.Nsec); err != nil {
		t.Fatalf("create %s: %v", publicGroupID, err)
	}
	if _, err := execGroupsCmd(t, relayURL, "edit", publicGroupID, "--identity", creator.Nsec, "--public", "--open"); err != nil {
		t.Fatalf("edit %s to public: %v", publicGroupID, err)
	}
	if _, err := execGroupsCmd(t, relayURL, "create", privateGroupID, "--identity", creator.Nsec); err != nil {
		t.Fatalf("create %s: %v", privateGroupID, err)
	}

	waitForGroupsListCount(t, relayURL, creator.Nsec, 2)

	t.Run("anonymous list sees only the public group", func(t *testing.T) {
		stdout, err := execGroupsCmd(t, relayURL, "list")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		ids := groupIDsFromListJSON(t, stdout)
		if !ids[publicGroupID] || ids[privateGroupID] || len(ids) != 1 {
			t.Errorf("anonymous list = %v, want exactly {%s}", ids, publicGroupID)
		}
	})

	t.Run("creator --mine sees both", func(t *testing.T) {
		stdout, err := execGroupsCmd(t, relayURL, "list", "--identity", creator.Nsec, "--mine")
		if err != nil {
			t.Fatalf("list --mine: %v", err)
		}
		ids := groupIDsFromListJSON(t, stdout)
		if !ids[publicGroupID] || !ids[privateGroupID] || len(ids) != 2 {
			t.Errorf("creator --mine = %v, want exactly {%s, %s}", ids, publicGroupID, privateGroupID)
		}
	})

	t.Run("outsider --mine sees neither (not a member of either)", func(t *testing.T) {
		stdout, err := execGroupsCmd(t, relayURL, "list", "--identity", outsider.Nsec, "--mine")
		if err != nil {
			t.Fatalf("list --mine: %v", err)
		}
		ids := groupIDsFromListJSON(t, stdout)
		if len(ids) != 0 {
			t.Errorf("outsider --mine = %v, want empty", ids)
		}
	})

	t.Run("anonymous --member <creator> sees only the public group", func(t *testing.T) {
		stdout, err := execGroupsCmd(t, relayURL, "list", "--member", creator.PubKeyHex)
		if err != nil {
			t.Fatalf("list --member: %v", err)
		}
		ids := groupIDsFromListJSON(t, stdout)
		if !ids[publicGroupID] || ids[privateGroupID] || len(ids) != 1 {
			t.Errorf("anonymous --member <creator> = %v, want exactly {%s} -- membership in a private group must not be visible without authenticating as a member of it", ids, publicGroupID)
		}
	})

	t.Run("authenticated --member <creator> (as the creator) sees both", func(t *testing.T) {
		stdout, err := execGroupsCmd(t, relayURL, "list", "--identity", creator.Nsec, "--member", creator.PubKeyHex)
		if err != nil {
			t.Fatalf("list --member: %v", err)
		}
		ids := groupIDsFromListJSON(t, stdout)
		if !ids[publicGroupID] || !ids[privateGroupID] || len(ids) != 2 {
			t.Errorf("authenticated --member <creator> = %v, want exactly {%s, %s}", ids, publicGroupID, privateGroupID)
		}
	})
}

