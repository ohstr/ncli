package client

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/ohstr/nmilat/nip01"
)

func newInspectStoreTestEvent(i int) *nip01.Event {
	return &nip01.Event{
		ID:        fmt.Sprintf("%064x", i),
		PubKey:    fmt.Sprintf("%064x", 1),
		CreatedAt: 1,
		Kind:      1,
		Tags:      [][]string{},
		Content:   "inspect store test event",
	}
}

func TestInspectStoreInsertRoundTrip(t *testing.T) {
	store, err := NewInspectStore()
	if err != nil {
		t.Fatalf("NewInspectStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	event := newInspectStoreTestEvent(1)

	if err := store.Insert(ctx, event); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	pes, err := store.store.FindEvents(ctx, &nip01.SubscriptionFilter{IDs: []string{event.ID}})
	if err != nil {
		t.Fatalf("FindEvents: %v", err)
	}
	if len(pes) != 1 {
		t.Fatalf("expected exactly 1 persisted match for %s, got %d", event.ID, len(pes))
	}
}

// TestInspectStoreInsertToleratesDuplicateEvent is a regression guard: an
// inspect session commonly queries overlapping relays, so the exact same
// event ID routinely arrives twice, from two different targets. That must
// not surface as an error -- see Insert's comment.
func TestInspectStoreInsertToleratesDuplicateEvent(t *testing.T) {
	store, err := NewInspectStore()
	if err != nil {
		t.Fatalf("NewInspectStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	event := newInspectStoreTestEvent(1)

	if err := store.Insert(ctx, event); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	if err := store.Insert(ctx, event); err != nil {
		t.Fatalf("expected a duplicate Insert to be tolerated, got: %v", err)
	}
}

func TestInspectStoreCloseRemovesTempDir(t *testing.T) {
	store, err := NewInspectStore()
	if err != nil {
		t.Fatalf("NewInspectStore: %v", err)
	}

	dir := store.dir
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("expected temp dir %s to exist before Close: %v", dir, err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected temp dir %s to be removed after Close, stat err=%v", dir, err)
	}
}

// TestInspectStoreConcurrentInsert mirrors
// stream_write_concurrency_test.go's style: concurrent Insert calls from
// multiple goroutines (as handleEvent's per-source concurrency produces in
// production) must all land, with no races -- run with -race.
func TestInspectStoreConcurrentInsert(t *testing.T) {
	store, err := NewInspectStore()
	if err != nil {
		t.Fatalf("NewInspectStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	const numEvents = 200
	const numWorkers = 8

	var wg sync.WaitGroup
	eventsPerWorker := numEvents / numWorkers
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < eventsPerWorker; i++ {
				id := worker*eventsPerWorker + i
				if err := store.Insert(ctx, newInspectStoreTestEvent(id)); err != nil {
					t.Errorf("Insert(%d): %v", id, err)
				}
			}
		}(w)
	}
	wg.Wait()

	for _, id := range []int{0, numEvents / 2, numEvents - 1} {
		event := newInspectStoreTestEvent(id)
		pes, err := store.store.FindEvents(ctx, &nip01.SubscriptionFilter{IDs: []string{event.ID}})
		if err != nil {
			t.Fatalf("FindEvents(%s): %v", event.ID, err)
		}
		if len(pes) != 1 {
			t.Errorf("event %s: expected exactly 1 persisted match, got %d", event.ID, len(pes))
		}
	}
}
