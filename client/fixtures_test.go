package client

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/ohstr/nmilat/nip01"
)

// fixtureEventsPath holds 100 real, signed kind:1 events -- see
// testdata/README.md -- so a test can work against a realistic event set
// without reaching a relay.
const fixtureEventsPath = "../testdata/events.json"

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
// failure in whichever test happens to consume it. No network -- Verify
// covers format, schnorr signature and the id-to-content binding, and none
// of these events carry a nonce tag, so the NIP-13 branch never applies.
func TestFixtureEventsAreVerifiable(t *testing.T) {
	events := loadFixtureEvents(t)

	const want = 100
	if len(events) != want {
		t.Fatalf("fixture holds %d events, want %d", len(events), want)
	}

	seen := make(map[string]bool, len(events))
	for i, ev := range events {
		if ev.Kind != 1 {
			t.Errorf("event %d (%s): kind %d, want 1", i, ev.ID[:8], ev.Kind)
		}
		if seen[ev.ID] {
			t.Errorf("event %d: duplicate id %s", i, ev.ID[:8])
		}
		seen[ev.ID] = true
		if err := ev.Verify(); err != nil {
			t.Errorf("event %d (%s) failed verification: %v", i, ev.ID[:8], err)
		}
	}
}
