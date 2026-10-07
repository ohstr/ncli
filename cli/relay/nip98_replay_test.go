package relay

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestServeOnce(t *testing.T) {
	g := newReplayGuard()
	status := http.StatusOK
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
	call := func(h string) int {
		req := httptest.NewRequest("POST", "/", nil)
		req.Header.Set("Authorization", h)
		rec := httptest.NewRecorder()
		serveOnce(g, next, rec, req)
		return rec.Code
	}
	if c := call(header("ok")); c != http.StatusOK {
		t.Fatalf("first use: %d", c)
	}
	if c := call(header("ok")); c != http.StatusUnauthorized {
		t.Fatalf("second use of an accepted event: %d, want 401", c)
	}
	status = http.StatusUnauthorized
	call(header("refused"))
	status = http.StatusOK
	if c := call(header("refused")); c != http.StatusOK {
		t.Fatalf("an event the handler refused was recorded as used: %d", c)
	}
}

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
