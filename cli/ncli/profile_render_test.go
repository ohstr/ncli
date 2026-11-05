package ncli

import (
	"strings"
	"testing"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
)

var stubIdentity = client.IdentityInspection{
	Npub:      "npub1sg6plzptd64u62a878hep2kev88swjh3tw00gjsfl8f237lmu63q0uf63m",
	PubKeyHex: "82129f882b6eab9a95d3f8f7c8556c873c1d2bc55cf7a250993e9553efdf3543",
}

func populatedView() *profileView {
	following := 1284
	return &profileView{
		Npub:      "npub1sg6plzptd64u62a878hep2kev88swjh3tw00gjsfl8f237lmu63q0uf63m",
		PubKeyHex: "82129f882b6eab9a95d3f8f7c8556c873c1d2bc55cf7a250993e9553efdf3543",
		Metadata: &common.ProfileMetadata{
			Name:        "jack",
			DisplayName: "jack",
			About:       "Bitcoin, Nostr, and open protocols.",
			Website:     "https://example.com",
			Nip05:       "jack@example.com",
			Lud16:       "jack@example.com",
		},
		Nip05:     &nip05Status{Address: "jack@example.com", Verified: true},
		Following: &following,
		Relays: []profileRelay{
			{URL: "wss://relay.example.com", Read: true, Write: true},
			{URL: "wss://read.example.com", Read: true},
			{URL: "wss://write.example.com", Write: true},
		},
		Blossom:   []string{"https://blossom.example.com", "https://cdn.example.org"},
		UpdatedAt: 1758412800,
		Queried:   4,
	}
}

// TestRenderProfile_Layouts prints both cards so the layout can be eyeballed
// with `go test -run TestRenderProfile_Layouts -v`, and asserts the parts that
// must not silently disappear.
func TestRenderProfile_Layouts(t *testing.T) {
	t.Log("--- populated ---")
	renderProfile(populatedView())

	t.Log("--- sparse ---")
	renderProfile(&profileView{
		Npub:      "npub1abcdefghijklmnopqrstuvwxyz234567890abcdefghijklmnow9k2",
		PubKeyHex: "7f2a91c0aa11bb22cc33dd44ee55ff66007788990011223344556677888e41b33",
		Queried:   4,
	})
}

// TestBuildProfileView_NewestWins confirms the aggregation rule: several
// relays each return their own copy of a replaceable event, and the newest
// one has to win regardless of the order they arrived in.
func TestBuildProfileView_NewestWins(t *testing.T) {
	resolved := &stubIdentity
	events := []*nip01.Event{
		{Kind: 0, CreatedAt: 100, Content: `{"name":"old"}`},
		{Kind: 0, CreatedAt: 300, Content: `{"name":"new"}`},
		{Kind: 0, CreatedAt: 200, Content: `{"name":"middle"}`},
		{Kind: kindContacts, CreatedAt: 50, Tags: [][]string{{"p", "a"}, {"p", "b"}, {"e", "x"}}},
	}

	view := buildProfileView(resolved, events, 3)

	if view.Metadata == nil || view.Metadata.Name != "new" {
		t.Fatalf("metadata = %+v, want the CreatedAt=300 copy", view.Metadata)
	}
	if view.UpdatedAt != 300 {
		t.Errorf("UpdatedAt = %d, want 300", view.UpdatedAt)
	}
	// Only "p" tags count as follows; the "e" tag must not inflate it.
	if view.Following == nil || *view.Following != 2 {
		t.Errorf("Following = %v, want 2", view.Following)
	}
}

// NIP-85 assertions are kept newest per provider, newest first, and the
// card shows the newest.
func TestBuildProfileView_Stats(t *testing.T) {
	events := []*nip01.Event{
		{Kind: kindUserAssertion, PubKey: "aa", CreatedAt: 100, Tags: [][]string{{"d", "x"}, {"followers", "1"}}},
		{Kind: kindUserAssertion, PubKey: "aa", CreatedAt: 300, Tags: [][]string{{"d", "x"}, {"followers", "1204"}, {"zap_amt_recd", "21000"}, {"rank", "33"}}},
		{Kind: kindUserAssertion, PubKey: "bb", CreatedAt: 200, Tags: [][]string{{"d", "x"}, {"followers", "7"}}},
	}
	view := buildProfileView(&stubIdentity, events, 1)

	if len(view.Stats) != 2 || view.Stats[0].Provider != "aa" || view.Stats[0].Tags["followers"] != "1204" {
		t.Fatalf("stats = %+v", view.Stats)
	}
	if _, ok := view.Stats[0].Tags["d"]; ok {
		t.Error("the d tag is the profile itself, not a stat")
	}
	got := strings.Join(statsLines(view.Stats[0], len(view.Stats)), "\n")
	for _, want := range []string{"1,204 followers", "21,000 sats", "33/100", "+1 more in --json"} {
		if !strings.Contains(got, want) {
			t.Errorf("card lacks %q:\n%s", want, got)
		}
	}
}

// A NIP-85 stat value is its provider's text: withThousands groups digits
// in any string without checking it's numeric, and unixDate echoes a value
// it can't parse -- both must be escaped so a crafted assertion can't carry
// a control, newline or bidi rune to the terminal.
func TestStatsLines_EscapesHostileValues(t *testing.T) {
	s := profileStats{
		Provider: "aa",
		Tags: map[string]string{
			"followers":      "12\x1b[2J34",            // ESC in a "numeric" stat
			"following":      "5\u202e6",               // right-to-left override
			"zap_first_recd": "not-a-ts\nfailed login", // unixDate parse-fail fallback
		},
	}
	got := strings.Join(statsLines(s, 1), "\n")
	if strings.ContainsAny(got, "\x1b\u202e") {
		t.Errorf("stats lines leaked a raw control/bidi rune: %q", got)
	}
	// statsLines returns one string per line; a hostile value's own newline
	// must have been escaped, not split the join into extra entries.
	if n := len(statsLines(s, 1)); strings.Count(got, "\n") != n-1 {
		t.Errorf("a hostile value introduced a raw newline: %q", got)
	}
	// The digits still render (escaped, grouped), proving the value went
	// through the fixed path rather than being dropped.
	if !strings.Contains(got, `\x1b`) {
		t.Errorf("expected the ESC to survive as a visible escape, got: %q", got)
	}
}

// Under --no-verify the address wasn't checked, so it mustn't read as a
// mismatch.
func TestRenderNip05_Unchecked(t *testing.T) {
	got := renderNip05(&nip05Status{Address: "alice@example.com", Unchecked: true})
	if !strings.Contains(got, "not checked") || strings.Contains(got, "does not match") {
		t.Errorf("unchecked nip-05 rendered as %q", got)
	}
}

// TestBuildProfileView_MissingRecordsAreNilNotZero is the distinction the
// sparse card depends on: an identity that never published a contact list
// must read as "not published", never as "following 0".
func TestBuildProfileView_MissingRecordsAreNilNotZero(t *testing.T) {
	view := buildProfileView(&stubIdentity, nil, 2)

	if view.Following != nil {
		t.Errorf("Following = %v, want nil for an unpublished contact list", *view.Following)
	}
	if view.Relays != nil || view.Blossom != nil {
		t.Errorf("relays/blossom = %v/%v, want nil", view.Relays, view.Blossom)
	}
	if got := renderToString(view); !strings.Contains(got, "not published") {
		t.Errorf("sparse card should say \"not published\", got:\n%s", got)
	}
}

func renderToString(v *profileView) string {
	var b strings.Builder
	b.WriteString(renderFollowing(v))
	b.WriteString("\n")
	b.WriteString(footer(v))
	return b.String()
}
