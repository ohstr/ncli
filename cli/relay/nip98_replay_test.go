package relay

import (
	"encoding/base64"
	"testing"
	"time"
)

func header(id string) string {
	return "Nostr " + base64.StdEncoding.EncodeToString([]byte(`{"id":"`+id+`","kind":27235}`))
}

func TestReplayGuard(t *testing.T) {
	g := newReplayGuard()
	now := time.Now()
	if !g.first(header("a"), now) {
		t.Fatal("first use rejected")
	}
	if g.first(header("a"), now.Add(time.Second)) {
		t.Fatal("replay within the window accepted")
	}
	if !g.first(header("b"), now) {
		t.Fatal("a different event rejected")
	}
	if !g.first(header("a"), now.Add(nip98Window+time.Second)) {
		t.Fatal("id not forgotten after the window")
	}
	for _, bad := range []string{"", "Nostr", "Nostr !!", "Bearer x", "Nostr " + base64.StdEncoding.EncodeToString([]byte("{}"))} {
		if g.first(bad, now) {
			t.Errorf("header %q with no event id accepted", bad)
		}
	}
}
