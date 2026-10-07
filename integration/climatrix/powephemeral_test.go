package climatrix

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
)

// mined runs `ncli miner mine` at difficulty d and returns the event.
func mined(t *testing.T, e *Env, priv string, d int) *nip01.Event {
	t.Helper()
	out := filepath.Join(e.Dir, "mined-"+strconv.Itoa(d)+".json")
	e.MustOK(t, "miner", "mine", "--content", "pow "+strconv.Itoa(d), "--identity", priv, "-d", strconv.Itoa(d), "-o", out)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var ev nip01.Event
	if err := json.Unmarshal(b, &ev); err != nil {
		t.Fatal(err)
	}
	return &ev
}

func TestStrictPoW(t *testing.T) {
	needsRelay(t)
	const min = 16
	r := StartRelay(t, "", map[string]any{"pow": map[string]any{"strict": true, "min": min}})
	e := NewEnv(t)
	alice := A(t, "alice")
	c := Dial(t, r.URL)

	if ok, _ := c.Publish(Ev(t, alice.PrivHex, 1, "no work")); ok {
		t.Errorf("unmined event accepted at min %d", min)
	}
	if ok, _ := c.Publish(mined(t, e, alice.Nsec, 8)); ok {
		t.Errorf("8-bit event accepted at min %d", min)
	}
	if ok, msg := c.Publish(mined(t, e, alice.Nsec, min)); !ok {
		t.Errorf("%d-bit event refused: %s", min, msg)
	}

	// A nonce tag claiming the target, on an id that doesn't meet it.
	liar := nip01.NewEvent(1, "claims work", []string{"nonce", "1", strconv.Itoa(min)})
	if err := liar.Sign(alice.PrivHex); err != nil {
		t.Fatal(err)
	}
	if liar.ID[:4] != "0000" {
		if ok, _ := c.Publish(liar); ok {
			t.Errorf("nonce tag claiming %d bits accepted on id %s", min, liar.ID[:8])
		}
	}

	// The CLI reports it like any other rejection.
	p := e.WriteFile("plain.json", mustMarshal(t, []any{Ev(t, alice.PrivHex, 1, "cli no work")}))
	if res := e.Run(t, "publish", "-e", p, "-s", r.URL, "--json"); res.Code == 0 {
		t.Errorf("publish of an unmined event exited 0 on a strict-PoW relay")
	}
}

// Ephemeral kinds (20000-29999) reach subscriptions open when they arrive
// and are never served to a later query.
func TestEphemeralEvents(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	alice := A(t, "alice")

	listener := Dial(t, r.URL)
	live := listener.Live(F{"kinds": []int{20001}})
	ev := Ev(t, alice.PrivHex, 20001, "now or never")
	if ok, msg := Dial(t, r.URL).Publish(ev); !ok {
		t.Fatalf("ephemeral publish: %s", msg)
	}
	if !live.Saw(ev.ID, 2*time.Second) {
		t.Errorf("open subscription didn't receive the ephemeral event")
	}
	if sees(Dial(t, r.URL), ev, F{"ids": []string{ev.ID}}) {
		t.Errorf("ephemeral event served to a query after the fact")
	}
	var evs []corpusEvent
	NewEnv(t).MustOK(t, "find", ev.ID, "-s", r.URL, "--json").JSON(t, &evs)
	if len(evs) != 0 {
		t.Errorf("find returned an ephemeral event after the fact")
	}
}
