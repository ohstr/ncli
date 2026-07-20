package client

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nip01"
)

func newEventExportTestEvent(i int) *nip01.Event {
	return &nip01.Event{
		ID:        fmt.Sprintf("%064x", i),
		PubKey:    fmt.Sprintf("%064x", 1),
		CreatedAt: 1,
		Kind:      1,
		Tags:      [][]string{},
		Content:   fmt.Sprintf("event export test event %d", i),
	}
}

func newEventExportTestWriter(t *testing.T) *EventSessionWriter {
	t.Helper()
	specPath := filepath.Join(t.TempDir(), "inspect.yaml")
	return NewEventSessionWriter(specPath)
}

func TestEventSessionWriterCreatesFileOnFirstSave(t *testing.T) {
	w := newEventExportTestWriter(t)
	ev1 := newEventExportTestEvent(1)

	path, err := w.Save(ev1)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	base := filepath.Base(path)
	if !strings.HasPrefix(base, "inspect-events-") || !strings.HasSuffix(base, ".json") {
		t.Fatalf("expected filename like inspect-events-<ts>.json, got %s", base)
	}

	loaded, err := LoadEvents(path)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != ev1.ID {
		t.Fatalf("expected [%s], got %+v", ev1.ID, loaded)
	}
}

func TestEventSessionWriterAppendsAcrossSaves(t *testing.T) {
	w := newEventExportTestWriter(t)
	ev1 := newEventExportTestEvent(1)
	ev2 := newEventExportTestEvent(2)

	path1, err := w.Save(ev1)
	if err != nil {
		t.Fatalf("Save(ev1): %v", err)
	}

	path2, err := w.Save(ev2)
	if err != nil {
		t.Fatalf("Save(ev2): %v", err)
	}

	if path1 != path2 {
		t.Fatalf("expected the same file across saves in one session, got %s then %s", path1, path2)
	}

	loaded, err := LoadEvents(path1)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected both events present after two saves, got %d: %+v", len(loaded), loaded)
	}
}

func TestEventSessionWriterDedupesByID(t *testing.T) {
	w := newEventExportTestWriter(t)
	ev1 := newEventExportTestEvent(1)

	path, err := w.Save(ev1)
	if err != nil {
		t.Fatalf("Save (first): %v", err)
	}
	if _, err := w.Save(ev1); err != nil {
		t.Fatalf("Save (duplicate): %v", err)
	}

	loaded, err := LoadEvents(path)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected saving the same event twice to de-dupe, got %d entries", len(loaded))
	}
}

func TestEventSessionWriterSaveWithNoEventsIsANoop(t *testing.T) {
	w := newEventExportTestWriter(t)

	path, err := w.Save()
	if err != nil {
		t.Fatalf("Save(): %v", err)
	}
	if path != "" {
		t.Fatalf("expected no path from an empty Save, got %q", path)
	}
}
