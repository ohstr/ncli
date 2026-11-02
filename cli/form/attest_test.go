package form

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/nmilat/nip59"
	"github.com/ohstr/nmilat/nipOA"
	"github.com/ohstr/nmilat/nippf"
)

// A form whose author key carries its owner's NIP-OA attestation: list and
// show print it in the client's column, before the labels and the title.
func TestFormAttested(t *testing.T) {
	alice, bob, owner := newActor(t, 1), newActor(t, 2), newActor(t, 3)
	ks, err := nipOA.NewKeySigner(owner.priv)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := nipOA.Sign(context.Background(), ks, alice.pub, "kind=1069")
	if err != nil {
		t.Fatal(err)
	}
	f := &nippf.Form{Title: "[agent of npub1x…] Deploy?", Private: true, Parties: []nippf.Party{{PubKey: bob.pub, Role: nippf.RoleResponder}},
		Questions: []nippf.Question{{Index: 0, Text: "q", Type: nippf.TypeSingle, Options: []nippf.Option{{ID: "a", Label: "A"}}}}}
	ev, err := f.Event(alice.pub, 100)
	if err != nil {
		t.Fatal(err)
	}
	ev.Tags = append(ev.Tags, tag)
	if err := nip59.PrepareRumor(ev, alice.pub); err != nil {
		t.Fatal(err)
	}
	views, _ := fold([]item{{ev: ev, private: true}}, bob.pub, 200)
	if len(views) != 1 {
		t.Fatal("no view")
	}
	v := views[0]
	if v.AttestedBy != owner.pub || v.AttestValid == nil || !*v.AttestValid {
		t.Fatalf("attestation: %+v", v)
	}
	v.AttestTrusted, v.attestWord = true, "agent"
	col := "[agent of " + social.Npub(owner.pub) + "]"
	if l := listLine(v); !strings.HasPrefix(l, shortID(v.Form.ID)+"  "+col+"  labels: -  (open") {
		t.Fatalf("form list: %q", l)
	}
	var b bytes.Buffer
	writeView(&b, v)
	if first := strings.Split(b.String(), "\n")[0]; !strings.HasPrefix(first, "form ") || !strings.Contains(first, "by "+npub(alice.pub)+", "+col+" ") {
		t.Fatalf("form show: %q", first)
	}
	v.AttestTrusted = false
	if l := listLine(v); !strings.Contains(l, "[attested by "+social.Npub(owner.pub)+", owner not trusted]  labels:") {
		t.Fatalf("form list, untrusted: %q", l)
	}
}
