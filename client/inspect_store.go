package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
)

// InspectStore is the durable, session-scoped record of every event an
// Inspector run receives -- decoupled from whatever bounded window the TUI
// keeps for display. It's a normal relay.EventStore (the same one used for
// local apply targets) pointed at a fresh temp directory, so it gets the
// store's existing batched-write behavior for free instead of needing any
// new concurrency machinery. The directory is removed on Close, so nothing
// outlives the inspect session.
type InspectStore struct {
	store *relay.EventStore
	dir   string
}

// NewInspectStore creates a temp-backed EventStore for one Inspector run.
func NewInspectStore() (*InspectStore, error) {
	dir, err := os.MkdirTemp("", "ncli-inspect-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create inspect temp dir: %w", err)
	}

	store, err := relay.NewEventStore(filepath.Join(dir, "inspect.db"), &nip11.Limitation{})
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("failed to open inspect temp store: %w", err)
	}

	return &InspectStore{store: store, dir: dir}, nil
}

// Insert records a single received event. Safe to call concurrently from
// multiple sources' goroutines -- the underlying EventStore batches
// concurrent inserts into a single bbolt transaction.
//
// Seeing the same event ID more than once is the normal case here, not a
// failure: inspect sessions commonly query overlapping relays, so the same
// note routinely arrives from two different targets. relay.ErrEventDuplicated
// just means the store already has it -- exactly as retained as if this call
// were the first -- so it's swallowed rather than surfaced as an error.
func (s *InspectStore) Insert(ctx context.Context, event *nip01.Event) error {
	err := s.store.InsertEvents(ctx, []*nip01.Event{event})
	if errors.Is(err, relay.ErrEventDuplicated) {
		return nil
	}
	return err
}

// Close shuts down the temp store and removes its backing directory.
func (s *InspectStore) Close() error {
	s.store.Close()
	return os.RemoveAll(s.dir)
}
