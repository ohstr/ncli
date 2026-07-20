package client

import (
	"bufio"
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip09"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/wire"
	"github.com/ohstr/nmilat/relay"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"
)

const (
	streamFlowBufferSize = 10240 // 10240

	workerFailureThreshold = 2
	workerCoolDownTime     = time.Second * 10 // 10
	workerDelayTime        = time.Second * 5  // 5

	// maxEventHistorySize bounds each destination's pending-ack map. It's a
	// defensive cap, not a load-bearing one: entries are removed as soon as
	// their ACK is processed. But a pending entry stays alive from the
	// moment deliverToSubscriber registers it until handleFlow processes its
	// ACK, so under sustained backlog it can simultaneously occupy space in
	// incomingEvents (up to streamFlowBufferSize, awaiting the destination's
	// write loop) *and* incoming (up to another streamFlowBufferSize,
	// awaiting handleFlow) -- a combined worst case of 2*streamFlowBufferSize
	// (20480). The cap must stay comfortably above that, or it'll evict
	// still-live entries and their eventual ACK will hit the "unexpected
	// ack" fallback instead of being counted.
	maxEventHistorySize = 24000

	// localWriteConcurrency is the fallback worker count for a
	// LocalSubscription destination when its FlowSpec doesn't set
	// WriteConcurrency. Submitting more than one insert at a time lets the
	// store's own task batcher (EventStore.handleTasks) coalesce them into
	// a single bbolt transaction instead of committing one event per flush;
	// a single in-flight task defeats that batching entirely.
	localWriteConcurrency = 32
)

type FlowContext struct {
	stat    tui.FlowStat
	filters *nip01.SubscriptionFilterGroup
	trusted bool
	worker  *StreamWorker

	// strictPow gates NIP-13 enforcement for this flow's handleEvent calls:
	// false (the default, set by Stream.Sync from the --strict-pow global
	// flag) accepts an event regardless of a missing/insufficient PoW nonce;
	// true rejects it, which is what every event.Verify() call used to do
	// unconditionally before this flag existed.
	strictPow bool

	incoming       chan wire.SubscriptionResponse
	incomingEvents chan *nip01.Event
	errors         chan error

	// pauseMu guards pauseCh/pauser: open() recreates both on every
	// (re)connect cycle, from the flow's own goroutine, while other flows'
	// broadcastEvents goroutines read the current pauseCh concurrently via
	// paused() -- a plain field read/write here would race.
	pauseMu sync.RWMutex
	pauseCh chan interface{}
	pauser  sync.Once

	closeCh chan interface{}
	closer  sync.Once

	lastUpdate uint64

	recovery *RecoveryManager

	// pending tracks events handed to this destination that are awaiting an
	// ACK, so handleFlow can correlate an OkSubscriptionResponse back to the
	// event without depending on any other flow's state.
	pending *StreamEventHistory
}

func NewFlowContext(filters *nip01.SubscriptionFilterGroup, stat tui.FlowStat, trusted bool, recovery *RecoveryManager) *FlowContext {
	return &FlowContext{
		stat:           stat,
		trusted:        trusted,
		filters:        filters.Copy(),
		incoming:       make(chan wire.SubscriptionResponse, streamFlowBufferSize), // flow buffer
		incomingEvents: make(chan *nip01.Event, streamFlowBufferSize),
		errors:         make(chan error),
		closeCh:        make(chan interface{}),
		recovery:       recovery,
		pending:        newStreamEventHistory(),
	}
}

func (fc *FlowContext) readEvent() <-chan *nip01.Event {
	return fc.incomingEvents
}

func (fc *FlowContext) reload() {
	fc.filters.ResetSince(fc.lastUpdate)
	fc.stat.IncreaseRetries(fc.worker.NextRetry())
	go fc.worker.Retry()
}

func (fc *FlowContext) pause() {
	fc.pauseMu.Lock()
	defer fc.pauseMu.Unlock()
	fc.pauser.Do(func() {
		close(fc.pauseCh)
	})
}

func (fc *FlowContext) open() {
	fc.pauseMu.Lock()
	defer fc.pauseMu.Unlock()
	fc.pauseCh = make(chan interface{})
	fc.pauser = sync.Once{}
}

// paused returns the current generation's pause signal channel, safe to call
// concurrently with open() (which recreates it every (re)connect cycle).
func (fc *FlowContext) paused() <-chan interface{} {
	fc.pauseMu.RLock()
	defer fc.pauseMu.RUnlock()
	return fc.pauseCh
}

func (fc *FlowContext) close() {
	fc.closer.Do(func() {
		close(fc.closeCh)
	})
}

func (fc *FlowContext) receive(response wire.SubscriptionResponse) {
	select {
	case fc.incoming <- response:
	case <-fc.closed():
		return
	}
}

func (fc *FlowContext) closed() <-chan interface{} {
	return fc.closeCh
}

// sendError forwards a fatal/packet error to handleFlow via fc.errors,
// giving up instead of blocking forever if there's no reader left.
// fc.errors is unbuffered with exactly one reader (handleFlow), but
// handleFlow's lifecycle isn't tied to whichever Read/Write goroutine is
// trying to report an error to it: handleFlow can return on its own (e.g.
// via the ClosedSubscriptionResponse reload path, or after its own
// fatal-error drain window elapses) while that goroutine keeps running.
// An unconditional send here would then block forever, leaking the
// goroutine and permanently killing retries for that destination.
func (fc *FlowContext) sendError(ctx context.Context, err error) {
	select {
	case fc.errors <- err:
	case <-fc.closed():
	case <-ctx.Done():
	}
}

func (fc *FlowContext) setWorker(worker *StreamWorker) {
	fc.worker = worker
}

// StreamEventHistory is an O(1) LRU: a map for lookup plus a doubly-linked
// list (most-recent at the front) for eviction order. list.Element.Value
// holds the *nip01.Event directly -- nip01.Event already carries its own ID,
// so wrapping it in a separate struct just to keep a second copy of that ID
// around would be a pure extra allocation on every add() for no benefit.
type StreamEventHistory struct {
	data  map[string]*list.Element // eventID -> element (Value is *nip01.Event)
	order *list.List
	mu    sync.RWMutex
}

func newStreamEventHistory() *StreamEventHistory {
	return &StreamEventHistory{
		data:  make(map[string]*list.Element),
		order: list.New(),
	}
}

func (seh *StreamEventHistory) get(eventID string) (*nip01.Event, bool) {
	seh.mu.RLock()
	defer seh.mu.RUnlock()
	elem, ok := seh.data[eventID]
	if !ok {
		return nil, false
	}
	return elem.Value.(*nip01.Event), true
}

func (seh *StreamEventHistory) add(event *nip01.Event) {
	seh.mu.Lock()
	defer seh.mu.Unlock()

	if elem, exists := seh.data[event.ID]; exists {
		elem.Value = event
		seh.order.MoveToFront(elem)
		return
	}

	elem := seh.order.PushFront(event)
	seh.data[event.ID] = elem

	if seh.order.Len() > maxEventHistorySize {
		oldest := seh.order.Back()
		seh.order.Remove(oldest)
		delete(seh.data, oldest.Value.(*nip01.Event).ID)
	}
}

func (seh *StreamEventHistory) delete(eventID string) {
	seh.mu.Lock()
	defer seh.mu.Unlock()
	if elem, ok := seh.data[eventID]; ok {
		seh.order.Remove(elem)
		delete(seh.data, eventID)
	}
}

type StreamChannel struct {
	logger *tui.FlowLogger

	// retain is set only by Inspector (never by Stream/Sync): when non-nil,
	// handleEvent hands every received event to it instead of the
	// aggregating logger.LogEvent call, so inspect sessions keep the full
	// event (for the selectable event table + temp store) instead of just
	// a coalesced ID. Nil for every other module, which keeps their
	// behavior byte-identical to before this field existed.
	retain func(event *nip01.Event, attr tui.FlowAttr)

	incoming chan *nip01.Event

	subscribers   map[int]*FlowContext
	subscribersMu sync.RWMutex

	// subscribersSnapshot mirrors subscribers as a plain slice, rebuilt only
	// by addSubscriber/removeSubscriber (rare: stream setup/teardown), so
	// the per-event hot path in broadcastEvents/handleEvent can read it via
	// a single lock-free atomic load instead of taking subscribersMu and
	// allocating a fresh slice from the map on every single event. Each
	// store is a brand-new slice whose backing array is never mutated after
	// publication, so concurrent Loads never race with a Store.
	subscribersSnapshot atomic.Pointer[[]*FlowContext]

	timeouts *TimeoutSpec
}

func NewStreamChannel(numReaders int, timeouts *TimeoutSpec) *StreamChannel {
	sc := &StreamChannel{
		incoming:    make(chan *nip01.Event, streamFlowBufferSize*numReaders), // main buffer
		subscribers: make(map[int]*FlowContext),
		logger:      &tui.FlowLogger{},
		timeouts:    timeouts,
	}
	empty := []*FlowContext{}
	sc.subscribersSnapshot.Store(&empty)

	return sc
}

// snapshotSubscribers rebuilds subscribersSnapshot from subscribers. Must be
// called with subscribersMu held for writing.
func (sc *StreamChannel) snapshotSubscribers() {
	snapshot := make([]*FlowContext, 0, len(sc.subscribers))
	for _, subscriber := range sc.subscribers {
		snapshot = append(snapshot, subscriber)
	}
	sc.subscribersSnapshot.Store(&snapshot)
}

func (sc *StreamChannel) addSubscriber(fc *FlowContext) {
	sc.subscribersMu.Lock()
	defer sc.subscribersMu.Unlock()
	sc.subscribers[fc.stat.GetAttributes().Index] = fc
	sc.snapshotSubscribers()
}

func (sc *StreamChannel) removeSubscriber(index int) {
	sc.subscribersMu.Lock()
	defer sc.subscribersMu.Unlock()
	delete(sc.subscribers, index)
	sc.snapshotSubscribers()
}

// deliverToSubscriber hands an event to a single destination's flow, and
// registers it as pending an ACK unless the subscriber is paused/closing (in
// which case it's dropped, matching prior behavior).
//
// pending.add must happen before the channel send, not after: once the send
// succeeds, the destination's write loop can read the event and generate its
// ACK immediately, and handleFlow could process that ACK before this
// goroutine gets scheduled again -- a race that would hit the "unexpected
// ack" fallback for a perfectly legitimate event.
func deliverToSubscriber(ctx context.Context, subscriber *FlowContext, event *nip01.Event) {
	subscriber.pending.add(event)

	select {
	case subscriber.incomingEvents <- event:
	case <-subscriber.paused():
		subscriber.pending.delete(event.ID) // never actually delivered
	case <-ctx.Done():
		subscriber.pending.delete(event.ID) // never actually delivered
	}
}

func (sc *StreamChannel) broadcastEvents(ctx context.Context) {
	for {
		select {
		case event := <-sc.incoming:
			subscribers := *sc.subscribersSnapshot.Load()

			switch len(subscribers) {
			case 0:
				continue
			case 1:
				// No aliasing risk with a single destination: skip the copy.
				deliverToSubscriber(ctx, subscribers[0], event)
			default:
				// Fan out concurrently so one slow/stuck destination can't
				// head-of-line-block delivery to the others. ctx and event
				// are passed as explicit params (not captured) so each
				// goroutine's argument frame is copied directly rather than
				// requiring a heap-allocated closure environment.
				//
				// Only subscribers[1:] get their own goroutine; subscribers[0]
				// is delivered inline in this goroutine instead of a
				// dedicated one. This goroutine already has to wait for
				// every delivery (via wg.Wait()) before it can move on to
				// the next incoming event, so handling one of them itself
				// costs nothing extra -- it just saves a goroutine spawn per
				// event, and doesn't change the head-of-line-blocking
				// guarantee for the other N-1 subscribers, which still run
				// fully independently of subscribers[0]'s delivery.
				var wg sync.WaitGroup
				wg.Add(len(subscribers) - 1)
				for _, subscriber := range subscribers[1:] {
					go func(ctx context.Context, subscriber *FlowContext, event *nip01.Event) {
						defer wg.Done()
						deliverToSubscriber(ctx, subscriber, event.Copy())
					}(ctx, subscriber, event)
				}
				deliverToSubscriber(ctx, subscribers[0], event.Copy())
				wg.Wait()
			}

		case <-ctx.Done():
			return
		}
	}
}

func (sc *StreamChannel) handleEvent(ctx context.Context, fc *FlowContext, event *nip01.Event) {

	if !fc.trusted {
		var opts []nip01.VerifyOption
		if !fc.strictPow {
			opts = append(opts, nip01.WithoutPowCheck())
		}
		if err := event.Verify(opts...); err != nil {
			sc.logger.Error(fmt.Errorf("%w (id=%s)", err, event.ID), fc.stat.GetAttributes())
			fc.stat.IncreaseFailures()
			return
		}
	}

	hasSubscribers := len(*sc.subscribersSnapshot.Load()) > 0

	if hasSubscribers {
		select {
		case sc.incoming <- event: // broadcast event
		case <-ctx.Done():
		}
	} else if sc.retain != nil {
		sc.retain(event, fc.stat.GetAttributes()) // Inspector: keep the full event, not just its ID
	} else {
		sc.logger.LogEvent(event.ID, fc.stat.GetAttributes()) // log it in case there is no destinations
	}

	fc.stat.AddEvent(event.Kind, event.PubKey)
	fc.lastUpdate = uint64(time.Now().Unix()) // event.CreatedAt

}

func (sc *StreamChannel) handleFlow(ctx context.Context, fc *FlowContext) {

	defer fc.stat.ResetEOSECounter()

	fc.open()
	defer fc.pause()

	for {
		select {

		case res := <-fc.incoming:
			switch o := res.(type) {

			case *wire.EventSubscriptionResponse:
				sc.handleEvent(ctx, fc, o.Event)

			case *wire.OkSubscriptionResponse:
				if !o.Accepted {
					// Failures counts every rejection, whether or not it
					// ends up permanently lost -- most get absorbed by
					// recovery and retried successfully, so this alone
					// isn't a sign of trouble. IncreaseLost (below) is the
					// one that matters: it only fires when recovery *also*
					// couldn't take custody of the event, meaning nothing
					// will ever retry it -- genuine, unrecoverable loss.
					sc.logger.Error(fmt.Errorf("%s: %s", o.Message, o.EventID), fc.stat.GetAttributes())
					fc.stat.IncreaseFailures()

					event, found := fc.pending.get(o.EventID)
					fc.pending.delete(o.EventID)

					if fc.recovery != nil && found {
						if err := fc.recovery.SaveFailedEvent(event, fc.stat.GetAttributes().Name, errors.New(o.Message)); err != nil {
							sc.logger.Error(fmt.Errorf("failed to save rejected event to recovery: %w", err), fc.stat.GetAttributes())
							fc.stat.IncreaseLost()
						}
					} else {
						fc.stat.IncreaseLost()
					}

				} else if isDuplicatedEvent(o.Message) {
					fc.pending.delete(o.EventID)
					fc.stat.IncSynced()

				} else if event, ok := fc.pending.get(o.EventID); ok {
					fc.pending.delete(o.EventID)
					sc.logger.LogEvent(o.EventID, fc.stat.GetAttributes())
					fc.stat.AddEvent(event.Kind, event.PubKey)

				} else if o.Message == "" || isEphemeralAck(o.Message) {
					// accepted=true with no message and no pending match: in
					// practice this is a relay re-sending (or sending late) an
					// OK it already gave us for an event we've moved past, not
					// a correlation failure -- a real problem always comes
					// with relay-supplied text explaining what's wrong.
					//
					// Ephemeral events (kind 20000-29999) are never persisted,
					// so relays commonly ack them with an explanatory
					// "ephemeral: ..." message instead of an empty one -- same
					// non-issue, just with text attached. Silently dropped
					// rather than warned on.

				} else {
					sc.logger.Warn(fmt.Sprintf("unexpected ack for event %s (accepted=%v, message=%q)", o.EventID, o.Accepted, o.Message), fc.stat.GetAttributes())
				}

			case *wire.EOSESubscriptionResponse:
				sc.logger.Success(fmt.Sprintf("EOSE received [purple]%d", fc.stat.EOSECount()), fc.stat.GetAttributes())
				fc.stat.ResetEOSECounter()

			case *wire.NoticeSubscriptionResponse:
				sc.logger.Warn(fmt.Sprintf("notice: %s", o.Message), fc.stat.GetAttributes())

			case *wire.ClosedSubscriptionResponse:
				sc.logger.Error(fmt.Errorf("%s: retry", o.Message), fc.stat.GetAttributes())
				fc.reload()
				return
			}

		case err := <-fc.errors:

			if ok := wire.IsPacketError(err); ok {
				sc.logger.Error(err, fc.stat.GetAttributes())
				fc.stat.IncreaseFailures()
			} else {
				// Connection error or fatal error.
				// DRAIN WINDOW: Consume potential concurrent errors from the same failure (e.g. Read+Write failing together)
				// to prevent spawning multiple retries for the same event.
				sc.logger.Error(fmt.Errorf("retrying: %w", err), fc.stat.GetAttributes())

				drainTimeout := time.After(500 * time.Millisecond)
				keepDraining := true
				for keepDraining {
					select {
					case extraErr := <-fc.errors:
						sc.logger.Warn(fmt.Sprintf("suppressed concurrent error: %v", extraErr), fc.stat.GetAttributes())
					case <-drainTimeout:
						keepDraining = false
					}
				}

				fc.reload()
				return
			}

		case <-fc.closed():
			sc.logger.Error(fmt.Errorf("closed: %s", fc.stat.GetAttributes().Name), fc.stat.GetAttributes())
			return

		case <-ctx.Done():
			sc.logger.Error(fmt.Errorf("closed: %s", fc.stat.GetAttributes().Name), fc.stat.GetAttributes())
			return
		}

	}

}

type Stream struct {
	sources      map[int]ClientSubscription
	destinations map[int]ClientSubscription

	sc       *StreamChannel
	filters  *nip01.SubscriptionFilterGroup
	recovery *RecoveryManager

	// strictPow is applied to every flow's FlowContext in Sync -- see
	// FlowContext.strictPow.
	strictPow bool

	// getConnectionConfig helper to convert TimeoutSpec to relayclient.ConnectionConfig
	cancel context.CancelFunc
}

// getConnectionConfig helper to convert TimeoutSpec to relayclient.ConnectionConfig
func (sc *StreamChannel) getConnectionConfig() *relayclient.ConnectionConfig {
	cfg := relayclient.DefaultConnectionConfig()
	if sc.timeouts == nil {
		return cfg
	}
	if sc.timeouts.Handshake != nil {
		if d, err := time.ParseDuration(*sc.timeouts.Handshake); err == nil {
			cfg.HandshakeTimeout = d
		}
	}
	if sc.timeouts.Ping != nil {
		if d, err := time.ParseDuration(*sc.timeouts.Ping); err == nil {
			cfg.PingInterval = d
		}
	}
	if sc.timeouts.Pong != nil {
		if d, err := time.ParseDuration(*sc.timeouts.Pong); err == nil {
			cfg.PongTimeout = d
		}
	}
	if sc.timeouts.Write != nil {
		if d, err := time.ParseDuration(*sc.timeouts.Write); err == nil {
			cfg.WriteTimeout = d
		}
	}
	return cfg
}

// defaultRecoveryStorePath derives a stable recovery-store location under the
// OS temp directory from this stream's destination endpoints, so re-running
// the same `to:` topology reuses the same store (and its pending entries)
// across restarts instead of starting fresh each time.
func defaultRecoveryStorePath(spec *StreamSpec) string {
	ids := make([]string, 0, len(spec.To))
	for _, f := range spec.To {
		if f.Relay != "" {
			ids = append(ids, "relay:"+f.Relay)
		} else {
			ids = append(ids, "path:"+f.Path)
		}
	}
	sort.Strings(ids)

	sum := sha256.Sum256([]byte(strings.Join(ids, "|")))
	return filepath.Join(os.TempDir(), "ncli", "recovery", fmt.Sprintf("%x.db", sum[:8]))
}

func NewStream(spec *StreamSpec, strictPow bool) (*Stream, error) {
	s := &Stream{
		sources:      make(map[int]ClientSubscription),
		destinations: make(map[int]ClientSubscription),
		filters:      spec.filters,
		strictPow:    strictPow,

		sc: NewStreamChannel(len(spec.From), spec.Timeouts),
	}

	// Recovery is always on: rejected/failed-to-send events are persisted and
	// retried in the background rather than silently dropped. `spec.Recovery`
	// is purely optional tuning (store path override, retry knobs).
	maxRetries := 10
	retryInterval := 10 * time.Second
	storePath := ""

	if spec.Recovery != nil {
		if spec.Recovery.MaxRetries > 0 {
			maxRetries = spec.Recovery.MaxRetries
		}
		if d, err := time.ParseDuration(spec.Recovery.RetryInterval); err == nil && d > 0 {
			retryInterval = d
		}
		storePath = spec.Recovery.StorePath
	}

	if storePath == "" {
		storePath = defaultRecoveryStorePath(spec)
	}

	// Recovery is a best-effort reliability layer, not a hard dependency: if
	// its store can't be opened (e.g. another instance already holds the
	// bbolt file lock on the same auto-derived path -- two streams sharing a
	// destination topology, or a stale lock), the stream must still start.
	// Rejected/failed events just fall back to being counted as lost, as
	// they always were before recovery existed.
	rm, err := NewRecoveryManager(storePath, maxRetries, retryInterval)
	if err != nil {
		log.Warn().Err(err).Str("path", storePath).Msg("recovery store unavailable; continuing without recovery")
	} else {
		s.recovery = rm
	}

	if err := s.addFlow(&s.sources, spec.From); err != nil {
		return nil, fmt.Errorf("error while processing source flow: %w", err)
	}

	if err := s.addFlow(&s.destinations, spec.To); err != nil {
		return nil, fmt.Errorf("error while processing destination flow: %w", err)
	}

	return s, nil
}

func (s *Stream) addFlow(way *map[int]ClientSubscription, specs []*FlowSpec) error {
	var nextIndex int
	for _, spec := range specs {
		if spec.killed {
			continue
		}

		nextIndex++
		switch spec.Type {
		case FlOW_LOCAL:
			if err := ensureLocalStoreDir(spec); err != nil {
				return fmt.Errorf("failed to create directory for local store: %w", err)
			}
			store, err := relay.NewEventStore(spec.Path, &nip11.Limitation{})
			if err != nil {
				return err
			}
			(*way)[nextIndex] = NewLocalSubscription(store, spec.Trusted, spec.WriteConcurrency)

		case FlOW_REMOTE:
			(*way)[nextIndex] = NewRemoteSubscription(spec.relayURI, spec.relayFallbackURI, spec.Trusted, s.recovery)

		case FLOW_FILE:
			file, err := os.Open(spec.Path)
			if err != nil {
				return fmt.Errorf("failed to open flow file %s: %w", spec.Path, err)
			}
			defer file.Close()

			scanner := bufio.NewScanner(file)
			firstLine := true
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}

				if !firstLine {
					nextIndex++
				}
				firstLine = false

				if fs, err := flowSpecFromString(line); err == nil {
					switch fs.Type {
					case FlOW_REMOTE:
						(*way)[nextIndex] = NewRemoteSubscription(fs.relayURI, fs.relayFallbackURI, spec.Trusted, s.recovery)
					case FlOW_LOCAL:
						store, err := relay.NewEventStore(fs.Path, &nip11.Limitation{})
						if err != nil {
							return fmt.Errorf("failed to open local store from file %s: %w", line, err)
						}
						(*way)[nextIndex] = NewLocalSubscription(store, spec.Trusted, spec.WriteConcurrency)
					}
				} else {
					log.Warn().Str("line", line).Msg("skipping invalid flow entry in file")
					if nextIndex > 0 { // Prevent underflow if first line is invalid
						nextIndex--
					}
				}
			}

			if err := scanner.Err(); err != nil {
				return fmt.Errorf("error reading flow file %s: %w", spec.Path, err)
			}

		}
	}

	return nil
}

func (s *Stream) Sync(parent context.Context) (tui.FlowMetricsSlice, tui.FlowMetricsSlice) {

	var ctx context.Context
	ctx, s.cancel = context.WithCancel(parent)

	upStats := make([]tui.FlowStat, 0)
	downStats := make([]tui.FlowStat, 0)

	indexWidth := len(strconv.Itoa(max(len(s.sources), len(s.destinations))))
	s.sc.logger.SetIndexWidth(indexWidth)

	for flowID, flow := range s.sources {
		flowID, flow := flowID, flow
		stat := tui.NewInboundMetrics(flowID, flow.Name(), func() {
			flow.Close()
			delete(s.sources, flowID)
		})
		stat.SetIndexWidth(indexWidth)
		downStats = append(downStats, stat)

		fc := NewFlowContext(s.filters, stat, flow.IsTrusted(), s.recovery)
		fc.strictPow = s.strictPow
		go func() {
			flow.Run(ctx, flow.Read, s.sc, fc)
		}()
	}

	for flowID, flow := range s.destinations {
		flowID, flow := flowID, flow
		stat := tui.NewOutboundMetrics(flowID, flow.Name(), func() {
			s.sc.removeSubscriber(flowID)
			flow.Close()
			delete(s.destinations, flowID)
		})
		stat.SetIndexWidth(indexWidth)
		upStats = append(upStats, stat)

		fc := NewFlowContext(s.filters, stat, flow.IsTrusted(), s.recovery)
		fc.strictPow = s.strictPow
		go func() {
			flow.Run(ctx, flow.Write, s.sc, fc)
		}()

		s.sc.addSubscriber(fc)
	}

	go s.sc.broadcastEvents(ctx)

	if s.recovery != nil {
		s.recovery.Start(ctx)
	}

	return upStats, downStats

}

func (s *Stream) Spec() Spec {
	spec := &StreamSpec{
		From:    make([]*FlowSpec, 0, len(s.sources)),
		To:      make([]*FlowSpec, 0, len(s.destinations)),
		Filters: make([]*FilterSpec, 0, s.filters.Size()),
	}

	flowSpec := func(flow ClientSubscription) *FlowSpec {
		switch f := flow.(type) {
		case *RemoteSubscription:
			return &FlowSpec{
				Type:    FlOW_REMOTE,
				Trusted: f.trusted,
				Relay:   f.relay.String(),
			}

		case *LocalSubscription:
			return &FlowSpec{
				Type:    FlOW_LOCAL,
				Trusted: f.trusted,
				Path:    f.store.Name(),
				Ensure:  EnsureExists,
			}
		}
		return nil
	}

	for _, flow := range s.sources {
		if fs := flowSpec(flow); fs != nil {
			spec.From = append(spec.From, fs)
		}
	}

	for _, flow := range s.destinations {
		if fs := flowSpec(flow); fs != nil {
			spec.To = append(spec.To, fs)
		}
	}

	for _, f := range s.filters.Copy().GetAll() {
		f.Since = uint64(time.Now().Unix())
		spec.Filters = append(spec.Filters, NewFilterSpec(f))
	}

	return spec
}

func (s *Stream) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	for _, flow := range s.sources {
		flow.Close()
	}
	for _, flow := range s.destinations {
		flow.Close()
	}
	if s.recovery != nil {
		s.recovery.Stop()
	}
}

//////

type ClientSubscription interface {
	Read(context.Context)
	Write(context.Context)
	Name() string
	IsTrusted() bool
	Close()
	Run(context.Context, func(context.Context), *StreamChannel, *FlowContext)
	Stat() tui.FlowStat
}

type ClientSubscriptionContext struct {
	worker  *StreamWorker
	trusted bool
	sc      *StreamChannel
	fc      *FlowContext
}

func (csc *ClientSubscriptionContext) IsTrusted() bool {
	return csc.trusted
}

func (csc *ClientSubscriptionContext) Run(ctx context.Context, job func(context.Context), sc *StreamChannel, fc *FlowContext) {
	fc.setWorker(csc.worker)
	csc.fc = fc
	csc.sc = sc
	csc.worker.SetJob(job, ctx).Run()
}

func (csc *ClientSubscriptionContext) Stat() tui.FlowStat {
	return csc.fc.stat
}

type LocalSubscription struct {
	store *relay.EventStore
	query *relay.StoreQuery
	*ClientSubscriptionContext

	writeConcurrency int

	// deleteBarrier orders a kind:5 deletion event against this
	// destination's own in-flight inserts: a normal insert takes RLock (many
	// can run concurrently), a deletion takes Lock (drains in-flight
	// inserts, blocks new ones, runs alone, then releases). Without this,
	// concurrent workers submitting to the same store give the deletion and
	// its target event no ordering relative to each other, and a deletion
	// that commits before its target is a permanent no-op (no tombstone is
	// kept for events not yet seen). Acquired only in Write's dispatch loop,
	// synchronously and in read order -- see the comment there.
	deleteBarrier sync.RWMutex
}

func NewLocalSubscription(store *relay.EventStore, trusted bool, writeConcurrency int) *LocalSubscription {
	if writeConcurrency <= 0 {
		writeConcurrency = localWriteConcurrency
	}

	return &LocalSubscription{
		store:            store,
		writeConcurrency: writeConcurrency,
		ClientSubscriptionContext: &ClientSubscriptionContext{
			worker:  NewStreamWorker(),
			trusted: trusted,
		},
	}
}

func (ls *LocalSubscription) Read(parent context.Context) {

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	go func() {
		ls.sc.handleFlow(ctx, ls.fc)
	}()

	if ls.query == nil {
		if query, err := relay.NewStoreQuery(ls.store, ls.fc.filters); err != nil {
			ls.fc.sendError(ctx, fmt.Errorf("failed to create query: %w", err))
			return
		} else {
			ls.query = query
		}
	}

	var wg sync.WaitGroup
	sub, events, subErrors, eose := relay.NewSubscription(uuid.NewString(), ls.query)

	go func() {
		sub.Start(ctx, &wg)
	}()

	for {
		select {

		case pe := <-events:
			event, err := ls.store.FindEvent(pe.Evsid)
			if err != nil {
				if !errors.Is(err, relay.ErrEventNotFound) {
					ls.fc.sendError(ctx, err)
				}
				// else: event was deleted concurrently, skip it
			} else {
				ls.fc.receive(&wire.EventSubscriptionResponse{Event: event})
			}
			wg.Done()

		case <-eose:
			ls.fc.receive(&wire.EOSESubscriptionResponse{})

		case err := <-subErrors:
			ls.fc.sendError(ctx, err)
			if ok := wire.IsPacketError(err); !ok {
				return
			}

		case <-ls.fc.closed():
			return

		case <-ctx.Done():
			return
		}

	}

}

// Write pumps events from the destination's channel into the local store
// using a pool of ls.writeConcurrency workers rather than one event at a
// time. A single in-flight insert per destination would mean the store's
// task batcher never sees more than one task per flush, forcing every event
// to pay for its own bbolt commit; running workers concurrently lets
// multiple inserts queue up together so they actually get batched.
//
// Each worker still submits and awaits its own single-event task, so
// per-event duplicate/error reporting and ACK correlation (fc.pending, is
// keyed by event ID) is untouched -- only the serialization between events
// is removed. Workers may complete out of order; that's fine, since
// handleFlow resolves ACKs by ID lookup, not by arrival order.
func (ls *LocalSubscription) Write(parent context.Context) {

	g, gctx := errgroup.WithContext(parent)
	g.SetLimit(ls.writeConcurrency)

	// handleFlow must run on parent, not gctx: errgroup cancels gctx the
	// instant any worker returns an error, which can happen well before
	// Write's own g.Wait() below returns. If handleFlow watched gctx it
	// could exit and stop listening on fc.errors before the final send
	// below ever runs, blocking that send forever. handleFlow returns on
	// its own once it actually processes a fatal error (see its <-fc.errors
	// case), so tying it to parent instead doesn't change when it exits in
	// that case -- it just keeps it alive long enough to receive the send.
	go func() {
		ls.sc.handleFlow(parent, ls.fc)
	}()

	// This single loop is the only place deleteBarrier is acquired: doing it
	// here, synchronously and in the exact order events are read off the
	// channel, is what makes the barrier actually order a deletion after
	// the insert it targets. Acquiring it inside each spawned goroutine
	// instead would not: with g.SetLimit workers racing to dequeue and run,
	// a deletion's goroutine could reach the lock before the target
	// event's goroutine even starts, since goroutine scheduling order isn't
	// tied to read order. Acquiring the lock here, before g.Go hands the
	// actual (slow, concurrent) store work off to a worker, fixes that: a
	// deletion's Lock() cannot be granted until every earlier-read event's
	// RLock -- acquired in this same sequential loop before the deletion
	// was even dequeued -- has been released, which only happens once that
	// event's write has actually completed.
	//
	// g.Go blocks here once writeConcurrency workers are active, same
	// backpressure as before; the lock is held a little longer while
	// blocked (nothing runs it yet), which is conservative but harmless.
dispatch:
	for {
		select {
		case ev := <-ls.fc.readEvent():
			isDelete := nip09.IsDeletionKind(ev.Kind)
			if isDelete {
				ls.deleteBarrier.Lock()
			} else {
				ls.deleteBarrier.RLock()
			}
			g.Go(func() error {
				if isDelete {
					defer ls.deleteBarrier.Unlock()
				} else {
					defer ls.deleteBarrier.RUnlock()
				}
				// parent, not gctx: gctx is cancelled the instant any
				// worker's writeOneSafe returns an error, and both
				// EventStore.Execute and writeOne's own select race a
				// ctx.Done() case against the task's real outcome. If this
				// call used gctx, a sibling worker's genuine store failure
				// would cancel gctx out from under every other in-flight
				// write, making EventStore.Execute short-circuit without
				// ever enqueueing the task (never persisted) or making
				// writeOne's select nondeterministically discard an
				// already-completed write -- either way silently dropping a
				// perfectly good, unrelated event with no ACK, no stat, and
				// no recovery entry. parent is only cancelled on genuine
				// upstream shutdown, which is the only case a write should
				// ever be aborted out from under it.
				return ls.writeOneSafe(parent, ev)
			})

		case <-ls.fc.closed():
			break dispatch

		case <-gctx.Done():
			break dispatch
		}
	}

	if err := g.Wait(); err != nil {
		// errgroup.Wait cancels gctx unconditionally right before returning
		// (even on success), so by this point gctx.Done() is always ready --
		// racing this send against it could nondeterministically drop a
		// real error. parent is only cancelled on genuine upstream
		// shutdown, so it's the correct case to race against instead.
		select {
		case ls.fc.errors <- err:
		case <-ls.fc.closed():
		case <-parent.Done():
		}
	}
}

// writeOneSafe wraps writeOne with panic recovery so one malformed or
// edge-case event can't take down this destination's entire persistent
// worker pool -- only that one event's outcome is lost, and the worker loop
// continues. Recovering here (per event) rather than once around the
// worker's outer loop matters: recovering there would silently and
// permanently retire that worker slot on any panic.
//
// This can't just defer utils.RecoverPanic like relay/session.go's
// executeStoreTask does: that helper only logs, it can't set a named return,
// so a recovered panic would fall through as err == nil -- errgroup would
// treat the event as a successful write when it never reached
// ls.fc.receive(...), silently dropping it (no ACK, no stat, no recovery
// entry). Recovering inline here lets the panic become a real error instead.
func (ls *LocalSubscription) writeOneSafe(ctx context.Context, ev *nip01.Event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			stack := make([]byte, 1<<13)
			n := runtime.Stack(stack, false)
			log.Error().Msgf("recovered from panic writing event %s: %v\nStack trace:\n%s\n", ev.ID, r, stack[:n])
			err = fmt.Errorf("panic writing event %s: %v", ev.ID, r)
		}
	}()
	return ls.writeOne(ctx, ev)
}

// writeOne inserts a single event and reports its outcome, returning a
// non-nil error only for a genuine (non-duplicate) store failure -- the
// caller treats that as fatal for the whole Write(). deleteBarrier is
// acquired by the caller (Write's dispatch loop), not here -- see its
// comment there for why.
func (ls *LocalSubscription) writeOne(ctx context.Context, ev *nip01.Event) error {
	task := relay.NewEventInsertTask([]*nip01.Event{ev})
	ls.store.Execute(ctx, task)

	select {
	case <-task.Completed():
		ls.fc.receive(&wire.OkSubscriptionResponse{
			EventID:  ev.ID,
			Accepted: true,
		})
		return nil

	case err := <-task.Errors():
		if errors.Is(err, relay.ErrEventDuplicated) {
			ls.fc.receive(&wire.OkSubscriptionResponse{
				EventID:  ev.ID,
				Accepted: true,
				Message:  fmt.Sprintf("duplicate: %s", err.Error()),
			})
			return nil
		}
		return err

	case <-ls.fc.closed():
		return nil
	case <-ctx.Done():
		return nil
	}
}

func (ls *LocalSubscription) Name() string {
	return filepath.Base(ls.store.Name())
}

func (ls *LocalSubscription) Close() {
	if ls.fc != nil {
		ls.fc.close()
	}
	ls.store.Close()
}

//////////

type RemoteSubscription struct {
	relay         *url.URL
	relayFallback *url.URL
	*ClientSubscriptionContext
	recovery *RecoveryManager
}

// NewRemoteSubscription connects to url, retrying against fallback (may be
// nil) if url fails to connect -- see connectRelayWithFallback.
func NewRemoteSubscription(url *url.URL, fallback *url.URL, trusted bool, recovery *RecoveryManager) *RemoteSubscription {

	return &RemoteSubscription{
		relay:         url,
		relayFallback: fallback,
		recovery:      recovery,
		ClientSubscriptionContext: &ClientSubscriptionContext{
			worker:  NewStreamWorker(),
			trusted: trusted,
		},
	}
}

func (rs *RemoteSubscription) Read(parent context.Context) {

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	go func() {
		rs.sc.handleFlow(ctx, rs.fc)
	}()

	conn, err := connectRelayWithFallback(ctx, rs.relay, rs.relayFallback, rs.sc.getConnectionConfig())
	if err != nil {
		rs.fc.sendError(ctx, err)
		log.Error().
			Err(err).
			Str("relay", rs.relay.String()).
			Msg("failed to open read connection")
		return
	}
	defer conn.Close()

	// SubscribeWithID, not Subscribe: this loop reads every message type off
	// the raw conn.Read() channel itself (via fc.receive), so it must not
	// use Subscribe's own typed per-subscription channel — that channel
	// would go undrained and eventually block the connection's read loop
	// once its buffer fills.
	conn.SubscribeWithID(uuid.NewString(), rs.fc.filters)

	// Create a channel to signal connection death to the main loop
	connDead := make(chan struct{})

	go func() {
		defer close(connDead)
		select {
		case err := <-conn.Errors():
			rs.fc.sendError(ctx, err)
			log.Warn().
				Err(err).
				Str("relay", rs.relay.String()).
				Msg("read connection error")
		case <-rs.fc.closed():
			return
		case <-ctx.Done():
			return
		}
	}()

	for {
		select {
		case ev := <-conn.Read():
			rs.fc.receive(ev)

		case <-connDead:
			return

		case <-rs.fc.closed():
			return

		case <-ctx.Done():
			return
		}
	}

}

func (rs *RemoteSubscription) Write(parent context.Context) {

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	go func() {
		rs.sc.handleFlow(ctx, rs.fc)
	}()

	conn, err := connectRelayWithFallback(ctx, rs.relay, rs.relayFallback, rs.sc.getConnectionConfig())
	if err != nil {
		rs.fc.sendError(ctx, err)
		log.Error().
			Err(err).
			Str("relay", rs.relay.String()).
			Msg("failed to open write connection")
		return
	}
	defer conn.Close()

	// Create a channel to signal connection death to the main loop
	connDead := make(chan struct{})

	go func() {
		defer close(connDead)
		for {
			select {
			case res := <-conn.Read(): // Write connection also reads ACKs
				rs.fc.receive(res)

			case err := <-conn.Errors():
				rs.fc.sendError(ctx, err)
				log.Warn().
					Err(err).
					Str("relay", rs.relay.String()).
					Msg("write connection error")
				return

			case <-rs.fc.closed():
				return

			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case ev := <-rs.fc.readEvent():
			// Counted on ack, not here -- see the OkSubscriptionResponse
			// handling in handleFlow. Counting at send time too would
			// double-count every successfully acked event (once optimistically
			// here, once for real on its ack), which is exactly what LocalSubscription's
			// Write avoids by only ever counting through the synthetic ack it
			// sends itself.
			if ok := conn.Send(ev); !ok {
				log.Warn().
					Str("relay", rs.relay.String()).
					Str("event_id", ev.ID).
					Msg("send interrupted; connection closed")

				// Try to save to recovery; IncreaseLost only fires below if
				// that also fails -- see the OkSubscriptionResponse handling
				// in handleFlow for the full Lost-vs-Failures distinction.
				if rs.recovery != nil {
					if err := rs.recovery.SaveFailedEvent(ev, rs.relay.String(), relayclient.ErrConnectionClosed); err != nil {
						log.Error().Err(err).Msg("failed to save event to recovery")
						rs.fc.stat.IncreaseLost()
					}
				} else {
					rs.fc.stat.IncreaseLost()
				}

				rs.fc.sendError(ctx, relayclient.ErrConnectionClosed)
				return
			}

		case <-connDead:
			return

		case <-rs.fc.closed():
			return

		case <-ctx.Done():
			return
		}
	}

}

func (rs *RemoteSubscription) Name() string {
	return rs.relay.String()
}

func (rs *RemoteSubscription) Close() {
	if rs.fc != nil {
		rs.fc.close()
	}
}

////////

type StreamWorker struct {
	task         func()
	failureCount int
}

func NewStreamWorker() *StreamWorker {
	return &StreamWorker{}
}

func (sw *StreamWorker) SetJob(task func(context.Context), ctx context.Context) *StreamWorker {
	sw.task = func() {
		task(ctx)
	}
	return sw
}

func (sw *StreamWorker) Run() {
	sw.task()
}

func (sw *StreamWorker) NextRetry() time.Time {

	if sw.failureCount+1 >= workerFailureThreshold {
		return time.Now().Add(workerDelayTime + workerCoolDownTime)
	} else {
		return time.Now().Add(workerDelayTime)
	}
}

func (sw *StreamWorker) Retry() {

	<-time.After(workerDelayTime)
	sw.failureCount++

	if sw.failureCount >= workerFailureThreshold {
		<-time.After(workerCoolDownTime)
		sw.failureCount = 0
	}
	go func() {
		sw.Run()
	}()

}

func isDuplicatedEvent(msg string) bool {
	prefix := "duplicate:"
	if len(msg) < len(prefix) {
		return false
	}

	return strings.EqualFold(msg[:len(prefix)], prefix)
}

func isEphemeralAck(msg string) bool {
	prefix := "ephemeral:"
	if len(msg) < len(prefix) {
		return false
	}

	return strings.EqualFold(msg[:len(prefix)], prefix)
}
