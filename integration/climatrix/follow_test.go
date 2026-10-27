package climatrix

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
)

func TestFindFollow(t *testing.T) {
	needsRelay(t)
	r := StartRelay(t, "", nil)
	e := NewEnv(t)
	alice := A(t, "alice")
	state := filepath.Join(e.Dir, "follow.state")
	args := []string{"find", "-f", "--kinds", "1", "--authors", alice.PubHex, "-s", r.URL, "--state", state, "--json"}

	first := publishNote(t, e, r.URL, alice, "--content", "one")
	p := e.StartStream(t, args...)
	var ev nip01.Event
	p.Next(t, &ev)
	if ev.ID != first.ID {
		t.Fatalf("history %+v", ev)
	}

	second := publishNote(t, e, r.URL, alice, "--content", "two")
	p.Next(t, &ev)
	if ev.ID != second.ID {
		t.Fatalf("live %+v", ev)
	}

	// The relay restarts; the stream reconnects and goes on.
	r.Restart(t)
	third := publishNote(t, e, r.URL, alice, "--content", "three")
	p.Next(t, &ev)
	if ev.ID != third.ID {
		t.Fatalf("after restart %+v", ev)
	}
	p.Quiet(t, time.Second)
	if code := p.ExitCode(); code != 0 {
		t.Fatalf("exit %d\n%s", code, p.Stderr())
	}

	// A restart with the same --state prints only what's new.
	fourth := publishNote(t, e, r.URL, alice, "--content", "four")
	p = e.StartStream(t, args...)
	p.Next(t, &ev)
	if ev.ID != fourth.ID {
		t.Fatalf("resumed with %q, want only the new note", ev.Content)
	}
	p.Quiet(t, time.Second)
}

func TestFindFollow_Errors(t *testing.T) {
	e := NewEnv(t)
	dead := DeadRelayURL(t)
	e.Run(t, "find", "-f", "--kinds", "1", "-s", dead, "-o", filepath.Join(e.Dir, "x.json"), "--json").ExpectErr(t, "usage")
	e.Run(t, "find", "--kinds", "1", "-s", dead, "--state", filepath.Join(e.Dir, "s"), "--json").ExpectErr(t, "usage")
	db := e.WriteFile("local.db", "")
	e.Run(t, "find", "-f", "--kinds", "1", "-s", db, "--json").ExpectErr(t, "usage")
	e.Run(t, "find", "-f", "--kinds", "1", "-s", dead, "--timeout", "2s", "--json").ExpectErr(t, "network")
	bad := e.WriteFile("bad.state", "not json")
	e.Run(t, "find", "-f", "--kinds", "1", "-s", dead, "--state", bad, "--json").ExpectErr(t, "invalid_input")
}
