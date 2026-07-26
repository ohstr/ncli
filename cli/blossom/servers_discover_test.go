package blossom

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipB7"
)

// TestLatestBlossomServerList_PicksHighestCreatedAt is the pure-logic unit
// test for the "more than one kind:10063 event came back" case discover.go
// must tolerate: kind:10063 is a NIP-01 replaceable event, so a relay is
// only supposed to return the latest one per (pubkey, kind), but "discover"
// doesn't trust that -- if several come back anyway (e.g. two different
// relays each serving their own idea of "latest"), the newest by CreatedAt
// must win regardless of input order.
func TestLatestBlossomServerList_PicksHighestCreatedAt(t *testing.T) {
	older := &nip01.Event{ID: "older", CreatedAt: 100}
	newer := &nip01.Event{ID: "newer", CreatedAt: 200}

	t.Run("newer first", func(t *testing.T) {
		got := latestBlossomServerList([]*nip01.Event{newer, older})
		if got != newer {
			t.Errorf("got %v, want the newer event", got)
		}
	})

	t.Run("older first", func(t *testing.T) {
		got := latestBlossomServerList([]*nip01.Event{older, newer})
		if got != newer {
			t.Errorf("got %v, want the newer event", got)
		}
	})

	t.Run("empty", func(t *testing.T) {
		if got := latestBlossomServerList(nil); got != nil {
			t.Errorf("got %v, want nil for no events", got)
		}
	})
}

// TestServersDiscover_FindsPublishedList is the end-to-end proof for gap
// 2: identity A publishes a kind:10063 server list to a relay (via
// "servers add --publish"), and a completely separate runner/config
// (simulating a different user, B, who has never manually configured any
// of A's servers) discovers A's list by npub against that same relay.
func TestServersDiscover_FindsPublishedList(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}

	relayURL := mockRelayServer(t)

	// A: publishes a two-server list.
	a := newRunner(t)
	a.mustRun("prefs", "relays", "add", relayURL)
	a.mustRun("blossom", "servers", "add", "https://a1.example", "--identity", a.nsec, "--publish")
	a.mustRun("blossom", "servers", "add", "https://a2.example", "--identity", a.nsec, "--publish")

	aIDOut, _ := a.mustRun("id", a.nsec, "--json")
	var aIdentity struct {
		Npub   string `json:"npub"`
		PubHex string `json:"pub_hex"`
	}
	if err := json.Unmarshal([]byte(aIDOut), &aIdentity); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, aIDOut)
	}

	// B: a separate runner (separate prefs.yaml/XDG_CONFIG_HOME, separate
	// identity) pointed at the same relay, with none of A's servers
	// configured locally.
	b := newRunner(t)
	b.mustRun("prefs", "relays", "add", relayURL)

	out, _ := b.mustRun("blossom", "servers", "discover", aIdentity.Npub, "--json")
	var got struct {
		Pubkey  string   `json:"pubkey"`
		Servers []string `json:"servers"`
		Event   string   `json:"event"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	if got.Pubkey != aIdentity.PubHex {
		t.Errorf("pubkey = %q, want %q", got.Pubkey, aIdentity.PubHex)
	}
	if got.Event == "" {
		t.Error("event id is empty, want the discovered kind:10063 event's id")
	}
	wantServers := []string{"https://a1.example", "https://a2.example"}
	if len(got.Servers) != len(wantServers) {
		t.Fatalf("servers = %v, want %v", got.Servers, wantServers)
	}
	for _, w := range wantServers {
		found := false
		for _, s := range got.Servers {
			if s == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("servers = %v, missing %q", got.Servers, w)
		}
	}

	// B's own local list must be untouched -- discover without --add is
	// read-only.
	listOut, _ := b.mustRun("blossom", "servers", "list", "--json")
	var list struct {
		Servers []string `json:"servers"`
	}
	if err := json.Unmarshal([]byte(listOut), &list); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, listOut)
	}
	if len(list.Servers) != 0 {
		t.Errorf("B's local server list = %v, want none -- discover without --add must not mutate local config", list.Servers)
	}
}

// TestServersDiscover_NotFoundForAnIdentityThatNeverPublished proves
// discover fails clearly (not_found, exit code 4) when the target identity
// has no kind:10063 event on any configured relay, rather than reporting
// an empty success.
func TestServersDiscover_NotFoundForAnIdentityThatNeverPublished(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}

	relayURL := mockRelayServer(t)

	neverPublished, err := client.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}

	r := newRunner(t)
	r.mustRun("prefs", "relays", "add", relayURL)

	stdout, stderr, code := r.run("blossom", "servers", "discover", neverPublished.Npub, "--json")
	if code != 4 { // common.CodeNotFound's exit code
		t.Errorf("exit code = %d, want 4 (not_found)", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	wantKind := fmt.Sprintf("%d", nipB7.KindBlossomServerList)
	if !strings.Contains(stderr, wantKind) {
		t.Errorf("stderr = %q, want it to mention kind %s", stderr, wantKind)
	}
}

// TestServersDiscover_AddMergesIntoLocalList proves --add actually writes
// the discovered servers into the local prefs.yaml list (the "adopt what I
// found" convenience), on top of whatever was already configured, and
// dedupes against an already-present entry.
func TestServersDiscover_AddMergesIntoLocalList(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}

	relayURL := mockRelayServer(t)

	a := newRunner(t)
	a.mustRun("prefs", "relays", "add", relayURL)
	a.mustRun("blossom", "servers", "add", "https://a1.example", "--identity", a.nsec, "--publish")
	a.mustRun("blossom", "servers", "add", "https://a2.example", "--identity", a.nsec, "--publish")

	aIDOut, _ := a.mustRun("id", a.nsec, "--json")
	var aIdentity struct {
		Npub string `json:"npub"`
	}
	if err := json.Unmarshal([]byte(aIDOut), &aIdentity); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, aIDOut)
	}

	b := newRunner(t)
	b.mustRun("prefs", "relays", "add", relayURL)
	// B already has a1 configured locally (pre-existing, unrelated to
	// discovery) -- --add must not duplicate it.
	b.mustRun("blossom", "servers", "add", "https://a1.example")
	b.mustRun("blossom", "servers", "add", "https://already-had.example")

	b.mustRun("blossom", "servers", "discover", aIdentity.Npub, "--add")

	listOut, _ := b.mustRun("blossom", "servers", "list", "--json")
	var list struct {
		Servers []string `json:"servers"`
	}
	if err := json.Unmarshal([]byte(listOut), &list); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, listOut)
	}

	want := map[string]bool{
		"https://already-had.example": true,
		"https://a1.example":          true,
		"https://a2.example":          true,
	}
	if len(list.Servers) != len(want) {
		t.Fatalf("servers = %v, want exactly %v (deduped, no repeats)", list.Servers, want)
	}
	for _, s := range list.Servers {
		if !want[s] {
			t.Errorf("unexpected server %q in list %v", s, list.Servers)
		}
	}
}
