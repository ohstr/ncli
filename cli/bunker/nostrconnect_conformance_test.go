package bunker

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/utils"
)

// realWorldURI is a URI a shipping Nostr client actually produced, kept
// verbatim. Four relays, no metadata param, a non-hex secret. ncli used to
// reject it outright with "metadata query not found", which is the whole
// reason this file exists.
const realWorldURI = "nostrconnect://6109a3efcd74054f2335e3c1626c32f2d50d8b4241137b697e85d7d2e68e364a" +
	"?relay=wss%3A%2F%2Frelay.bullishbounty.com" +
	"&relay=wss%3A%2F%2Frelay.damus.io" +
	"&relay=wss%3A%2F%2Frelay.primal.net" +
	"&relay=wss%3A%2F%2Fbucket.coracle.social" +
	"&secret=sec-232306d55d240b50e0201b449980c58f"

// TestNostrconnectURICorpus runs the shapes ncli actually receives through
// the same parser `ncli bunker connect` uses. One URI shape rejected at the
// door is one app that can never pair, and the failure looks identical to
// a bad paste -- so the corpus is the guard, not a single happy-path case.
//
// The sources are deliberately not ncli's own: NIP-46's published example,
// nostr-tools' createNostrConnectURI output shape, and a URI captured from
// a real client. ncli's own historical form is here too, since a signer
// that stops reading what it used to emit strands whoever saved one.
func TestNostrconnectURICorpus(t *testing.T) {
	const pubkey = "83f3b2ae6aa368e8275397b9c26cf550101d63ebaab900d19dd4a4429f5ad8f5"

	tests := []struct {
		name       string
		uri        string
		wantRelays []string
		wantName   string
		wantPerms  string
		wantSecret string
	}{
		{
			name:       "a real client's URI, four relays and no metadata",
			uri:        realWorldURI,
			wantSecret: "sec-232306d55d240b50e0201b449980c58f",
			wantRelays: []string{
				"wss://relay.bullishbounty.com",
				"wss://relay.damus.io",
				"wss://relay.primal.net",
				"wss://bucket.coracle.social",
			},
		},
		{
			name: "the example printed in NIP-46 itself",
			uri: "nostrconnect://" + pubkey +
				"?relay=wss%3A%2F%2Frelay1.example.com" +
				"&perms=nip44_encrypt%2Cnip44_decrypt%2Csign_event%3A13" +
				"&name=My+Client" +
				"&secret=0s8j2djs" +
				"&relay=wss%3A%2F%2Frelay2.example2.com",
			wantSecret: "0s8j2djs",
			wantRelays: []string{"wss://relay1.example.com", "wss://relay2.example2.com"},
			wantName:   "My Client",
			wantPerms:  "nip44_encrypt,nip44_decrypt,sign_event:13",
		},
		{
			name: "nostr-tools createNostrConnectURI, every optional param set",
			uri: "nostrconnect://" + pubkey +
				"?relay=" + url.QueryEscape("wss://relay.one") +
				"&relay=" + url.QueryEscape("wss://relay.two") +
				"&secret=abc123" +
				"&perms=" + url.QueryEscape("sign_event:1,nip04_encrypt") +
				"&name=" + url.QueryEscape("Tools App") +
				"&url=" + url.QueryEscape("https://tools.example") +
				"&image=" + url.QueryEscape("https://tools.example/icon.png"),
			wantSecret: "abc123",
			wantRelays: []string{"wss://relay.one", "wss://relay.two"},
			wantName:   "Tools App",
			wantPerms:  "sign_event:1,nip04_encrypt",
		},
		{
			name: "the bare minimum NIP-46 allows: one relay and a secret",
			uri: "nostrconnect://" + pubkey +
				"?relay=" + url.QueryEscape("wss://relay.only") +
				"&secret=s",
			wantSecret: "s",
			wantRelays: []string{"wss://relay.only"},
		},
		{
			name: "ncli's own legacy metadata blob still reads",
			uri: "nostrconnect://" + pubkey +
				"?relay=" + url.QueryEscape("wss://relay.legacy") +
				"&secret=legacy" +
				"&metadata=" + url.QueryEscape(`{"name":"Legacy App","url":"https://legacy.example"}`),
			wantSecret: "legacy",
			wantRelays: []string{"wss://relay.legacy"},
			wantName:   "Legacy App",
		},
		{
			name: "a schemeless relay host, which ncli accepts everywhere else",
			uri: "nostrconnect://" + pubkey +
				"?relay=relay.schemeless.example&secret=s",
			wantSecret: "s",
			wantRelays: []string{"wss://relay.schemeless.example"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			schema, err := nip46.ParseNostrconnect(tc.uri)
			if err != nil {
				t.Fatalf("ParseNostrconnect() error = %v\nURI: %s", err, tc.uri)
			}

			var gotRelays []string
			for _, r := range schema.Relays {
				gotRelays = append(gotRelays, r.String())
			}
			if strings.Join(gotRelays, ",") != strings.Join(tc.wantRelays, ",") {
				t.Errorf("relays = %v, want %v", gotRelays, tc.wantRelays)
			}
			if schema.Secret != tc.wantSecret {
				t.Errorf("Secret = %q, want %q", schema.Secret, tc.wantSecret)
			}
			if schema.Metadata == nil {
				t.Fatal("Metadata is nil; the daemon reads Name/Url off it")
			}
			if schema.Metadata.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", schema.Metadata.Name, tc.wantName)
			}
			if schema.Perms != tc.wantPerms {
				t.Errorf("Perms = %q, want %q", schema.Perms, tc.wantPerms)
			}
		})
	}
}

// The corpus above is only meaningful if genuinely broken input still
// fails -- a parser that accepts everything would pass it too.
func TestNostrconnectURICorpus_Rejects(t *testing.T) {
	const pubkey = "83f3b2ae6aa368e8275397b9c26cf550101d63ebaab900d19dd4a4429f5ad8f5"

	tests := []struct {
		name string
		uri  string
	}{
		{"no secret", "nostrconnect://" + pubkey + "?relay=wss%3A%2F%2Fr.example"},
		{"no relay", "nostrconnect://" + pubkey + "?secret=s"},
		{"not a hex pubkey", "nostrconnect://nope?relay=wss%3A%2F%2Fr.example&secret=s"},
		{"wrong scheme", "bunker://" + pubkey + "?relay=wss%3A%2F%2Fr.example&secret=s"},
		{"no usable relay", "nostrconnect://" + pubkey + "?relay=" + url.QueryEscape("://bad") + "&secret=s"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := nip46.ParseNostrconnect(tc.uri); err == nil {
				t.Errorf("ParseNostrconnect(%q) = nil error, want a rejection", tc.uri)
			}
		})
	}
}

func TestParsePerms(t *testing.T) {
	kind := func(k int) *int { return &k }

	tests := []struct {
		name  string
		perms string
		want  []Grant
	}{
		{name: "empty", perms: ""},
		{
			name:  "a method with no params",
			perms: "nip44_encrypt",
			want:  []Grant{{Method: nip46.MethodNIP44Encrypt}},
		},
		{
			name:  "sign_event scoped to a kind",
			perms: "sign_event:1",
			want:  []Grant{{Method: nip46.MethodSignEvent, Kind: kind(1)}},
		},
		{
			name:  "the spec's own example list",
			perms: "nip44_encrypt,nip44_decrypt,sign_event:13,sign_event:14",
			want: []Grant{
				{Method: nip46.MethodNIP44Encrypt},
				{Method: nip46.MethodNIP44Decrypt},
				{Method: nip46.MethodSignEvent, Kind: kind(13)},
				{Method: nip46.MethodSignEvent, Kind: kind(14)},
			},
		},
		{
			name:  "whitespace and empty entries are tolerated",
			perms: " sign_event:1 , , nip04_encrypt ",
			want: []Grant{
				{Method: nip46.MethodSignEvent, Kind: kind(1)},
				{Method: nip46.MethodNIP04Encrypt},
			},
		},
		{
			name:  "one unknown entry must not cost the others",
			perms: "sign_event:1,nip77_teleport,ping",
			want: []Grant{
				{Method: nip46.MethodSignEvent, Kind: kind(1)},
				{Method: nip46.MethodPing},
			},
		},
		{
			name:  "a non-numeric kind is skipped, not guessed at",
			perms: "sign_event:notanumber,ping",
			want:  []Grant{{Method: nip46.MethodPing}},
		},
		{
			name:  "connect is never grantable",
			perms: "connect,ping",
			want:  []Grant{{Method: nip46.MethodPing}},
		},
		{
			name:  "a repeated scope is remembered once",
			perms: "sign_event:1,sign_event:1,ping,ping",
			want: []Grant{
				{Method: nip46.MethodSignEvent, Kind: kind(1)},
				{Method: nip46.MethodPing},
			},
		},
	}

	now := time.Now()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePerms(tc.perms, now)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d grants %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for i := range tc.want {
				if got[i].Method != tc.want[i].Method {
					t.Errorf("grant %d method = %q, want %q", i, got[i].Method, tc.want[i].Method)
				}
				if got[i].Verdict != Allow {
					t.Errorf("grant %d verdict = %v, want Allow", i, got[i].Verdict)
				}
				switch {
				case tc.want[i].Kind == nil && got[i].Kind != nil:
					t.Errorf("grant %d kind = %d, want any-kind", i, *got[i].Kind)
				case tc.want[i].Kind != nil && got[i].Kind == nil:
					t.Errorf("grant %d kind = any, want %d", i, *tc.want[i].Kind)
				case tc.want[i].Kind != nil && *got[i].Kind != *tc.want[i].Kind:
					t.Errorf("grant %d kind = %d, want %d", i, *got[i].Kind, *tc.want[i].Kind)
				}
			}
		})
	}
}

// A bunker:// pairing has no URI for the signer to read, so the client's
// name and URL arrive in connect's own params. Dropping them was why
// Trusted Apps showed a bare hex key for every app paired this way.
func TestHandle_Connect_ReadsClientMetadataAndPerms(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)

	const secret = "pairing-secret"
	h.SetPendingSecret(secret)

	meta, err := json.Marshal(nip46.Metadata{Name: "Bunker App", Url: "https://bunker.example"})
	if err != nil {
		t.Fatal(err)
	}
	req := buildRequest(t, signerPub, nip46.MethodConnect,
		[]string{signerPub, secret, "sign_event:1,ping", string(meta)})

	// A pending GrantSpec is what lets connect resolve without a human --
	// see Handle's own MethodConnect branch.
	h.SetPendingGrants(&GrantSpec{Grants: []GrantEntrySpec{{Method: nip46.MethodPing}}})

	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Error != "" {
		t.Fatalf("connect failed: %s", resp.Error)
	}

	sessions := h.Store.List()
	if len(sessions) != 1 {
		t.Fatalf("Store.List() = %+v, want one session", sessions)
	}
	if sessions[0].AppName != "Bunker App" {
		t.Errorf("AppName = %q, want the name from connect's own params", sessions[0].AppName)
	}
	if sessions[0].AppURL != "https://bunker.example" {
		t.Errorf("AppURL = %q, want the url from connect's own params", sessions[0].AppURL)
	}
	if got := h.Store.Decide(clientPub, nip46.MethodSignEvent, 1); got != Allow {
		t.Errorf("Decide(sign_event, kind 1) = %v, want the requested perms applied", got)
	}
}

// Absent, empty and malformed metadata all have to pair anyway: it is a
// display hint, and NIP-46 says a signer must not make it load-bearing.
func TestHandle_Connect_ToleratesMissingOrBrokenMetadata(t *testing.T) {
	tests := []struct {
		name   string
		params func(signerPub, secret string) []string
	}{
		{
			name:   "no metadata param at all",
			params: func(p, s string) []string { return []string{p, s} },
		},
		{
			name:   "empty metadata",
			params: func(p, s string) []string { return []string{p, s, "", ""} },
		},
		{
			name:   "metadata that is not JSON",
			params: func(p, s string) []string { return []string{p, s, "", "not-json"} },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, signerPub, _ := newTestHandler(t)
			const secret = "pairing-secret"
			h.SetPendingSecret(secret)
			h.SetPendingGrants(&GrantSpec{Grants: []GrantEntrySpec{{Method: nip46.MethodPing}}})

			req := buildRequest(t, signerPub, nip46.MethodConnect, tc.params(signerPub, secret))
			resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
			if resp.Error != "" {
				t.Fatalf("connect failed: %s", resp.Error)
			}
			if sessions := h.Store.List(); len(sessions) != 1 {
				t.Fatalf("Store.List() = %+v, want the pairing to land regardless", sessions)
			}
		})
	}
}

// nostr-tools sends switch_relays immediately after every pairing.
// Answering "unsupported method" left ncli's own relay list inert.
func TestHandle_SwitchRelays(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	if err := h.Store.Pair(clientPub, "", ""); err != nil {
		t.Fatal(err)
	}

	req := buildRequest(t, signerPub, nip46.MethodSwitchRelays, []string{})
	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Error != "" {
		t.Fatalf("switch_relays failed: %s", resp.Error)
	}

	// A JSON array, per spec -- deliberately not get_relays' read/write map.
	var relays []string
	if err := json.Unmarshal([]byte(resp.Result), &relays); err != nil {
		t.Fatalf("result %q is not a JSON array: %v", resp.Result, err)
	}
	if len(relays) != 1 || relays[0] != "wss://relay.example" {
		t.Errorf("relays = %v, want the signer's own list", relays)
	}
}

func TestHandle_SwitchRelays_NullWhenTheSignerHasNone(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	h.Relays = nil
	if err := h.Store.Pair(clientPub, "", ""); err != nil {
		t.Fatal(err)
	}

	req := buildRequest(t, signerPub, nip46.MethodSwitchRelays, []string{})
	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Result != "null" {
		t.Errorf("result = %q, want null (spec: nothing to change)", resp.Result)
	}
}

func TestHandle_Logout_AcksAndRevokes(t *testing.T) {
	h, signerPub, clientPub := newTestHandler(t)
	if err := h.Store.Pair(clientPub, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.Remember(clientPub, newGrant(nip46.MethodPing, Allow, nil, time.Now())); err != nil {
		t.Fatal(err)
	}

	req := buildRequest(t, signerPub, nip46.MethodLogout, []string{})
	resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
	if resp.Error != "" {
		t.Fatalf("logout failed: %s", resp.Error)
	}
	if resp.Result != "ack" {
		t.Errorf("result = %q, want ack", resp.Result)
	}
	if h.Store.IsPaired(clientPub) {
		t.Error("session survived logout")
	}
}

// Neither method belongs to a caller with no session -- and neither may
// quietly become a way to prompt an operator on a stranger's behalf.
func TestHandle_SessionMethods_RefusedWhenUnpaired(t *testing.T) {
	for _, method := range []string{nip46.MethodSwitchRelays, nip46.MethodLogout} {
		t.Run(method, func(t *testing.T) {
			h, signerPub, _ := newTestHandler(t)

			req := buildRequest(t, signerPub, method, []string{})
			resp := parseResponse(t, h.Handle(req, nip46.EncryptionNIP04))
			if resp.Error == "" {
				t.Errorf("%s from an unpaired client succeeded, want a refusal", method)
			}
		})
	}
}

// A relay named only by a pairing URI is dialed ad hoc. runRelay
// reconnects with backoff until its context ends, so without a bound a
// dead entry would keep a goroutine retrying for the daemon's whole life
// -- once per dead relay, every time a URI is pasted. The reproducer for
// this change lists four relays, so that is not a hypothetical shape.
func TestDaemon_NostrconnectFlow_StopsDialingADeadRelay(t *testing.T) {
	relay := newFakeRelay(t)

	clientPub, err := utils.GetPublicKey(testClientPriv)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	daemon := newNostrconnectDaemon(t, ctx, relay.url.String())
	client := newNostrconnectTestClient(t, ctx, relay.url, testClientPriv, clientPub)

	dead := deadRelayURL(t)
	const secret = "dead-relay-cleanup-secret"
	schema := &nip46.NostrconnectSchema{
		ClientPublickey: clientPub,
		Relay:           dead,
		Relays:          []*url.URL{dead, relay.url},
		Secret:          secret,
		Metadata:        &nip46.Metadata{},
	}

	initiateErr := make(chan error, 1)
	go func() { initiateErr <- daemon.InitiateNostrconnect(ctx, schema) }()

	client.awaitConnectResponse(t, secret)
	if err := <-initiateErr; err != nil {
		t.Fatalf("InitiateNostrconnect() error = %v", err)
	}

	// Backoff starts at a second and doubles, so anything still retrying
	// logs again well inside this window.
	settled := countLogsMentioning(daemon, dead.Host)
	time.Sleep(2500 * time.Millisecond)

	if grown := countLogsMentioning(daemon, dead.Host) - settled; grown > 0 {
		t.Errorf("%d further dial attempts against the dead relay after pairing; want the loop stopped", grown)
	}
}

func countLogsMentioning(d *Daemon, substr string) int {
	n := 0
	for _, line := range d.RecentLogs().Lines {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}
