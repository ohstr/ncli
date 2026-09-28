package client

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/ohstr/nmilat/nip01"
)

// fixtureEventsPath holds a mixed-kind set of real, signed events -- see
// testdata/README.md -- so a test can work against a realistic corpus
// without reaching a relay.
const fixtureEventsPath = "../testdata/events.json"

// fixtureEventKinds is the fixture's exact composition. Asserted so a
// regenerated fixture that silently drops a kind fails here rather than
// quietly weakening whatever test relies on that kind being present.
var fixtureEventKinds = map[int]int{
	0:     40,  // metadata -- ncli profile
	1:     100, // text notes
	3:     5,   // contact lists -- following count
	6:     30,  // reposts
	7:     40,  // reactions
	1984:  20,  // reports
	9735:  24,  // zap receipts
	10002: 30,  // relay lists (NIP-65)
	10063: 30,  // blossom servers (BUD-03)
	30023: 20,  // long-form articles
}

func loadFixtureEvents(t *testing.T) []*nip01.Event {
	t.Helper()
	raw, err := os.ReadFile(fixtureEventsPath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", fixtureEventsPath, err)
	}
	var events []*nip01.Event
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatalf("failed to parse %s: %v", fixtureEventsPath, err)
	}
	return events
}

// TestFixtureEventsAreVerifiable guards the checked-in fixture: every event
// must still parse and cryptographically verify, so a corrupted or
// hand-edited events.json fails here rather than surfacing as a confusing
// failure in whichever test consumes it. No network -- Verify covers
// format, schnorr signature, the id-to-content binding, and NIP-13 for the
// few events that carry a nonce tag.
func TestFixtureEventsAreVerifiable(t *testing.T) {
	events := loadFixtureEvents(t)

	want := 0
	for _, n := range fixtureEventKinds {
		want += n
	}
	if len(events) != want {
		t.Fatalf("fixture holds %d events, want %d", len(events), want)
	}

	seen := make(map[string]bool, len(events))
	gotKinds := make(map[int]int, len(fixtureEventKinds))
	for i, ev := range events {
		gotKinds[ev.Kind]++
		if seen[ev.ID] {
			t.Errorf("event %d: duplicate id %s", i, ev.ID[:8])
		}
		seen[ev.ID] = true
		if err := ev.Verify(); err != nil {
			t.Errorf("event %d (kind %d, %s) failed verification: %v", i, ev.Kind, ev.ID[:8], err)
		}
	}

	for kind, wantN := range fixtureEventKinds {
		if gotKinds[kind] != wantN {
			t.Errorf("kind %d: fixture has %d event(s), want %d", kind, gotKinds[kind], wantN)
		}
	}
	for kind, gotN := range gotKinds {
		if _, ok := fixtureEventKinds[kind]; !ok {
			t.Errorf("kind %d: %d unexpected event(s) not in the documented mix", kind, gotN)
		}
	}
}
