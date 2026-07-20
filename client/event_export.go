package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ohstr/nmilat/nip01"
)

// EventSessionWriter accumulates events a user saves during one inspect
// session into a single JSON file. The path is computed once, lazily, on
// the first Save (mirroring Client.save's dir/base derivation, but with a
// distinct "-events-" suffix so it can never collide with a spec-save's own
// timestamped file); every later Save in the same session reuses that path
// and merges in, de-duplicated by event ID, instead of creating a new file
// each time.
type EventSessionWriter struct {
	mu       sync.Mutex
	specPath string
	path     string
}

func NewEventSessionWriter(specPath string) *EventSessionWriter {
	return &EventSessionWriter{specPath: specPath}
}

// Save appends events to this session's output file, returning its path.
// Read-modify-write against the whole file is fine here: saves are
// human-paced (one key press per event), not a hot loop.
func (w *EventSessionWriter) Save(events ...*nip01.Event) (string, error) {
	if len(events) == 0 {
		return "", nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.path == "" {
		w.path = w.newPath()
	}

	existing, err := loadExistingEventsIfPresent(w.path)
	if err != nil {
		return "", err
	}

	merged := mergeEventsByID(existing, events)

	out, err := json.MarshalIndent(merged, "", "    ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal events: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(w.path), os.ModePerm); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %w", filepath.Dir(w.path), err)
	}

	if err := os.WriteFile(w.path, out, 0644); err != nil {
		return "", fmt.Errorf("failed to write %s: %w", w.path, err)
	}

	return w.path, nil
}

func (w *EventSessionWriter) newPath() string {
	dir := filepath.Dir(w.specPath)
	ext := filepath.Ext(w.specPath)
	base := filepath.Base(w.specPath)
	name := base[:len(base)-len(ext)]

	return filepath.Join(dir, fmt.Sprintf("%s-events-%d.json", name, time.Now().Unix()))
}

func loadExistingEventsIfPresent(path string) ([]*nip01.Event, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	return LoadEvents(path)
}

func mergeEventsByID(existing, incoming []*nip01.Event) []*nip01.Event {
	seen := make(map[string]bool, len(existing)+len(incoming))
	merged := make([]*nip01.Event, 0, len(existing)+len(incoming))

	for _, ev := range existing {
		if !seen[ev.ID] {
			seen[ev.ID] = true
			merged = append(merged, ev)
		}
	}
	for _, ev := range incoming {
		if !seen[ev.ID] {
			seen[ev.ID] = true
			merged = append(merged, ev)
		}
	}
	return merged
}
