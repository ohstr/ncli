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
