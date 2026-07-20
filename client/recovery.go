package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/rs/zerolog/log"
	bolt "go.etcd.io/bbolt"
)

var (
	BUCKET_RECOVERY = []byte("recovery_meta")
)

type RetryMeta struct {
	EventID     string    `json:"id"`
	Destination string    `json:"dest"`
	Attempts    int       `json:"attempts"`
	LastAttempt time.Time `json:"last_attempt"`
	NextRetry   time.Time `json:"next_retry"`
	FailReason  string    `json:"reason"`
}

type saveRequest struct {
	event       *nip01.Event
	destination string
	reason      error
}

type RecoveryManager struct {
	store         *relay.EventStore
	metaDB        *bolt.DB // share the same DB instance
	maxRetries    int
	retryInterval time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	retryQueue chan *RetryMeta
	saveQueue  chan *saveRequest

	conns   map[string]*relayclient.Connection
	connsMu sync.Mutex
}

func NewRecoveryManager(path string, maxRetries int, retryInterval time.Duration) (*RecoveryManager, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory for recovery store: %w", err)
	}

	// reuse relay.EventStore for the heavy lifting of event storage
	// We use a lenient limitation for the local recovery store
	store, err := relay.NewEventStore(path, &nip11.Limitation{
		MaxLimit:         100000,
		MaxIndexableTags: 100,
		MaxMessageLength: 1024 * 1024,
	})
	if err != nil {
		return nil, err
	}

	rm := &RecoveryManager{
		store:         store,
		metaDB:        store.Db(),
		maxRetries:    maxRetries,
		retryInterval: retryInterval,
		retryQueue:    make(chan *RetryMeta, 100),
		saveQueue:     make(chan *saveRequest, 10240), // Buffer for async saves matches stream buffer
		conns:         make(map[string]*relayclient.Connection),
	}

	// Initialize meta bucket
	err = rm.metaDB.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(BUCKET_RECOVERY)
		return err
	})
	if err != nil {
		store.Close()
		return nil, err
	}

	return rm, nil
}

func (rm *RecoveryManager) Start(ctx context.Context) {
	rm.ctx, rm.cancel = context.WithCancel(ctx)
	rm.wg.Add(2) // One for retryLoop, one for saveWorker
	go rm.retryLoop()
	go rm.saveWorker()
}

func (rm *RecoveryManager) Stop() {
	if rm.cancel != nil {
		rm.cancel()
	}
	rm.wg.Wait() // retryLoop/saveWorker exited; saveWorker drained saveQueue to disk

	// Connections pooled before rm.ctx was cancelled are dead (their
	// background goroutines were tied to rm.ctx's lifetime); drop them so
	// the final flush below dials fresh ones bound to its own context.
	rm.closeConnections()

	// Last chance to deliver everything still pending before the store
	// closes: ignore NextRetry backoff since we're exiting now, not later.
	// Bounded so an unreachable destination can't hang shutdown; anything
	// left over stays durably on disk for the next run to pick up.
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
	rm.flushPending(flushCtx)
	flushCancel()

	rm.store.Close()
	rm.closeConnections()
}

func (rm *RecoveryManager) closeConnections() {
	rm.connsMu.Lock()
	defer rm.connsMu.Unlock()
	for _, conn := range rm.conns {
		conn.Close()
	}
	rm.conns = make(map[string]*relayclient.Connection)
}

// flushPending attempts immediate delivery of every currently-pending
// recovery entry, ignoring each entry's NextRetry backoff, bounded by ctx.
func (rm *RecoveryManager) flushPending(ctx context.Context) {
	rm.retryBatchGroups(ctx, rm.collectPending(true))
}

func (rm *RecoveryManager) SaveFailedEvent(event *nip01.Event, destination string, reason error) error {
	// Queue the save request asynchronously (non-blocking)
	select {
	case rm.saveQueue <- &saveRequest{
		event:       event,
		destination: destination,
		reason:      reason,
	}:
		return nil
	default:
		// Queue is full, log but don't block
		log.Warn().
			Str("event_id", event.ID).
			Str("destination", destination).
			Msg("recovery save queue full, dropping event")
		return errors.New("recovery save queue full")
	}
}

// saveWorker processes save requests asynchronously
func (rm *RecoveryManager) saveWorker() {
	defer rm.wg.Done()

	for {
		select {
		case <-rm.ctx.Done():
			// Drain remaining requests before exiting
			rm.drainSaveQueue()
			return

		case req := <-rm.saveQueue:
			if err := rm.saveEventSync(rm.ctx, req.event, req.destination, req.reason); err != nil {
				log.Error().
					Err(err).
					Str("event_id", req.event.ID).
					Str("destination", req.destination).
					Msg("failed to save event to recovery in background")
			}
		}
	}
}

// drainSaveQueue processes remaining save requests during shutdown. rm.ctx
// is already cancelled by the time this runs, so saves use their own bounded
// context rather than inheriting a context that's already Done.
func (rm *RecoveryManager) drainSaveQueue() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		select {
		case req := <-rm.saveQueue:
			if err := rm.saveEventSync(ctx, req.event, req.destination, req.reason); err != nil {
				log.Error().
					Err(err).
					Str("event_id", req.event.ID).
					Msg("failed to save event during shutdown")
			}
		default:
			return
		}
	}
}

// saveEventSync is the synchronous version of save (used by worker)
func (rm *RecoveryManager) saveEventSync(ctx context.Context, event *nip01.Event, destination string, reason error) error {
	// 1. Save event to EventStore
	insertTask := relay.NewEventInsertTask([]*nip01.Event{event})

	// Use parent context with longer timeout
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	rm.store.Execute(execCtx, insertTask)

	select {
	case <-insertTask.Completed():
		// success
	case err := <-insertTask.Errors():
		if err != relay.ErrEventDuplicated {
			return fmt.Errorf("failed to save event to recovery store: %w", err)
		}
		// duplicated is fine, we just update metadata
	case <-execCtx.Done():
		return execCtx.Err()
	}

	// 2. Save Metadata
	meta := &RetryMeta{
		EventID:     event.ID,
		Destination: destination,
		Attempts:    0,
		LastAttempt: time.Now(),
		NextRetry:   time.Now().Add(rm.retryInterval),
		FailReason:  reason.Error(),
	}

	return rm.saveMeta(meta)
}

func (rm *RecoveryManager) saveMeta(meta *RetryMeta) error {
	return rm.metaDB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(BUCKET_RECOVERY)
		key := []byte(fmt.Sprintf("%s:%s", meta.EventID, meta.Destination)) // Composite key? Or just random ID?
		// Actually, we might fail sending same event to multiple relays.
		// So key should probably include destination.

		data, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		return b.Put(key, data)
	})
}

func (rm *RecoveryManager) deleteMeta(eventID, destination string) error {
	return rm.metaDB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(BUCKET_RECOVERY)
		key := []byte(fmt.Sprintf("%s:%s", eventID, destination))
		return b.Delete(key)
	})
}

func (rm *RecoveryManager) retryLoop() {
	defer rm.wg.Done()

	ticker := time.NewTicker(rm.retryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-rm.ctx.Done():
			return
		case <-ticker.C:
			rm.processRetries()
		}
	}
}

// collectPending scans the recovery bucket for entries. With force=false
// (the periodic retry-loop path) it only returns entries due per their
// NextRetry backoff; with force=true (the exit-time flush) it returns
// everything, since there won't be a "next tick" to wait for.
func (rm *RecoveryManager) collectPending(force bool) []*RetryMeta {
	var result []*RetryMeta
	now := time.Now()

	rm.metaDB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(BUCKET_RECOVERY)
		c := b.Cursor()

		for k, v := c.First(); k != nil; k, v = c.Next() {
			var meta RetryMeta
			if err := json.Unmarshal(v, &meta); err != nil {
				continue
			}

			if force || now.After(meta.NextRetry) {
				result = append(result, &meta)
			}
		}
		return nil
	})

	return result
}

// retryBatchGroups groups pending entries by destination and attempts
// delivery for each destination concurrently, bounded by ctx.
func (rm *RecoveryManager) retryBatchGroups(ctx context.Context, toRetry []*RetryMeta) {
	retryGroups := make(map[string][]*RetryMeta)
	for _, meta := range toRetry {
		retryGroups[meta.Destination] = append(retryGroups[meta.Destination], meta)
	}

	var batchWg sync.WaitGroup
	for dest, metas := range retryGroups {
		batchWg.Add(1)
		go func(d string, m []*RetryMeta) {
			defer batchWg.Done()
			rm.processBatch(ctx, d, m)
		}(dest, metas)
	}
	batchWg.Wait()
}

func (rm *RecoveryManager) processRetries() {
	rm.retryBatchGroups(rm.ctx, rm.collectPending(false))
}

func (rm *RecoveryManager) getConnection(ctx context.Context, destination string) (*relayclient.Connection, error) {
	rm.connsMu.Lock()
	if conn, ok := rm.conns[destination]; ok {
		rm.connsMu.Unlock()
		return conn, nil
	}
	rm.connsMu.Unlock()

	// Connect
	u, err := url.Parse(destination)
	if err != nil {
		return nil, err
	}

	// NewConnection uses this context for the lifetime of its background
	// read/write goroutines, not just the dial, so it must not be tied to a
	// short-lived timeout here (that would cancel the connection right after
	// this function returns). The dial itself is bounded by HandshakeTimeout
	// in DefaultConnectionConfig().
	conn, err := relayclient.NewConnection(ctx, u, relayclient.DefaultConnectionConfig())
	if err != nil {
		return nil, err
	}

	// Consume messages to keep connection alive and handle pings
	// REMOVED: Background reader would steal messages from PublishEvent.
	// We rely on PublishEvent to read. If connection is idle, Pings might pile up
	// until we publish again. Ideally we needs a better connection abstraction
	// but for now this prevents the race.

	rm.connsMu.Lock()
	rm.conns[destination] = conn
	rm.connsMu.Unlock()

	return conn, nil
}

func (rm *RecoveryManager) processBatch(ctx context.Context, destination string, metas []*RetryMeta) {
	conn, err := rm.getConnection(ctx, destination)
	if err != nil {
		for _, meta := range metas {
			// If invalid URL, delete
			if _, urlErr := url.Parse(destination); urlErr != nil {
				rm.deleteMeta(meta.EventID, meta.Destination)
				continue
			}
			rm.handleRetryFailure(meta, err)
		}
		return
	}

	// 2. Iterate and send
	for _, meta := range metas {
		if ctx.Err() != nil {
			// Interrupted (e.g. the exit-time flush's deadline passed) —
			// leave remaining entries untouched for the next run instead of
			// burning a retry attempt on an interruption unrelated to the
			// destination.
			return
		}

		// Load event
		event, err := rm.findEvent(meta.EventID)
		if err != nil {
			log.Error().Err(err).Str("id", meta.EventID).Msg("failed to load event for retry, deleting metadata")
			rm.deleteMeta(meta.EventID, meta.Destination)
			continue
		}

		// Use short timeout for individual publish
		pubCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = PublishEvent(pubCtx, conn, event)
		cancel()

		if err != nil {
			if ctx.Err() != nil {
				// Same reasoning as above: the outer context ended this
				// attempt, not the destination, so don't penalize it.
				return
			}

			rm.handleRetryFailure(meta, err)

			// If connection is closed/broken, we should close it and maybe stop batch
			if errors.Is(err, relayclient.ErrConnectionClosed) || strings.Contains(err.Error(), "connection closed") || strings.Contains(err.Error(), "broken pipe") {
				// Close connection to force reconnect next time
				conn.Close()

				rm.connsMu.Lock()
				if rm.conns[destination] == conn {
					delete(rm.conns, destination)
				}
				rm.connsMu.Unlock()

				// The rest of batch will fail or we can try to reconnect immediately?
				// For now let them fail/retry next loop
			}
		} else {
			// Success
			log.Info().Str("id", event.ID).Str("dest", destination).Msg("successfully recovered event")
			rm.deleteMeta(meta.EventID, meta.Destination)
		}
	}
}

func (rm *RecoveryManager) handleRetryFailure(meta *RetryMeta, err error) {
	meta.Attempts++
	meta.LastAttempt = time.Now()
	meta.FailReason = err.Error()

	if meta.Attempts >= rm.maxRetries {
		log.Warn().Str("id", meta.EventID).Msg("max retries reached, dropping event")
		rm.deleteMeta(meta.EventID, meta.Destination)
	} else {
		// Backoff
		meta.NextRetry = time.Now().Add(rm.retryInterval * time.Duration(meta.Attempts))
		rm.saveMeta(meta)
	}
}

func (rm *RecoveryManager) findEvent(id string) (*nip01.Event, error) {
	filter := &nip01.SubscriptionFilter{IDs: []string{id}}
	pes, err := rm.store.FindEvents(context.Background(), filter)
	if err != nil {
		return nil, err
	}
	if len(pes) == 0 {
		return nil, relay.ErrEventNotFound
	}
	return rm.store.FindEvent(pes[0].Evsid)
}
