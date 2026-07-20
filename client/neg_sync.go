package client

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/gdamore/tcell/v2"
	"github.com/google/uuid"
	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip13"
	"github.com/ohstr/nmilat/nip77"
	"github.com/ohstr/nmilat/wire"
	"github.com/ohstr/nmilat/relay"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/rs/zerolog/log"
)

var syncAttr = tui.FlowAttr{
	Index:     1,
	Name:      "sync",
	FlagColor: tcell.ColorPurple,
}

// SyncModule implements Module for NIP-77 Negentropy reconciliation.
type SyncModule struct {
	spec   *SyncSpec
	conn   *relayclient.Connection
	store  *relay.EventStore
	logger *tui.FlowLogger
	cancel context.CancelFunc

	// strictPow gates NIP-13 enforcement on pulled events -- see hasValidPow
	// and pullEvents. false (the default) accepts a pulled event regardless
	// of a missing/insufficient PoW nonce; true drops it instead of
	// inserting it into the local store, unless the remote flow is Trusted.
	strictPow bool
}

func NewSyncModule(spec *SyncSpec, logger *tui.FlowLogger, strictPow bool) (*SyncModule, error) {
	if logger == nil {
		logger = &tui.FlowLogger{}
	}
	return &SyncModule{
		spec:      spec,
		logger:    logger,
		strictPow: strictPow,
	}, nil
}

// hasValidPow reports whether ev's proof-of-work is acceptable per NIP-13:
// true if it claims none (no nonce tag -- nothing to enforce), or if it does
// claim one and nip13.ValidatePow confirms the event's ID actually meets its
// declared difficulty.
func hasValidPow(ev *nip01.Event) bool {
	if len(ev.GetTag(nip13.POWTagName)) == 0 {
		return true
	}
	fields := nip13.Fields{ID: ev.ID, PubKey: ev.PubKey, CreatedAt: ev.CreatedAt, Kind: ev.Kind, Tags: ev.Tags, Content: ev.Content}
	_, _, err := nip13.ValidatePow(fields)
	return err == nil
}

func (s *SyncModule) Run(ctx context.Context) (*tui.FlowLogger, error) {
	childCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	go s.execute(childCtx)

	return s.logger, nil
}

func (s *SyncModule) execute(ctx context.Context) {

	// 1. Open local store
	local := s.spec.GetLocal()
	s.logger.Info(fmt.Sprintf("Opening local store: %s", local.Path), syncAttr)

	s.logger.Trace("Attempting to open local store", syncAttr)
	if err := ensureLocalStoreDir(local); err != nil {
		s.logger.Error(fmt.Errorf("failed to create directory for local store: %w", err), syncAttr)
		return
	}
	store, err := relay.NewEventStore(local.Path, &nip11.Limitation{})
	if err != nil {
		s.logger.Error(fmt.Errorf("failed to open store: %w", err), syncAttr)
		return
	}
	s.logger.Debug(fmt.Sprintf("Local store opened: %s", local.Path), syncAttr)
	s.store = store
	defer store.Close()

	// 2. Load local items via QueryNip77Items
	s.logger.Info("Loading local items...", syncAttr)

	var allItems []nip77.Item
	for i, f := range s.spec.Filters {
		s.logger.Trace(fmt.Sprintf("Querying filter %d/%d", i+1, len(s.spec.Filters)), syncAttr)
		items, err := store.QueryNip77Items(ctx, &f.SubscriptionFilter)
		if err != nil {
			s.logger.Error(fmt.Errorf("failed to query items: %w", err), syncAttr)
			return
		}
		s.logger.Debug(fmt.Sprintf("Filter %d yielded %d items", i+1, len(items)), syncAttr)
		allItems = append(allItems, items...)
	}

	// Sort ascending by Timestamp, then ID (NIP-77 requirement)
	sort.Slice(allItems, func(i, j int) bool {
		return allItems[i].Compare(allItems[j]) < 0
	})

	s.logger.Info(fmt.Sprintf("Loaded %d local items", len(allItems)), syncAttr)

	// 3. Build client-side Negentropy
	neg := nip77.New(allItems)
	initMsg := neg.Initiate()
	s.logger.Trace("Encoding initial message", syncAttr)
	initHex, err := initMsg.ToHex()
	if err != nil {
		s.logger.Error(fmt.Errorf("failed to encode initial message: %w", err), syncAttr)
		return
	}
	s.logger.Debug(fmt.Sprintf("Initial message encoded (%d bytes)", len(initHex)/2), syncAttr)

	// 4. Connect to relay
	remote := s.spec.GetRemote()
	s.logger.Info(fmt.Sprintf("Connecting to %s", remote.Relay), syncAttr)

	var cfg *relayclient.ConnectionConfig
	if s.spec.Timeouts != nil {
		cfg = &relayclient.ConnectionConfig{}
	}

	conn, err := connectRelayWithFallback(ctx, remote.relayURI, remote.relayFallbackURI, cfg)
	if err != nil {
		s.logger.Error(fmt.Errorf("failed to connect: %w", err), syncAttr)
		return
	}
	s.conn = conn
	defer conn.Close()

	s.logger.Success("Connected", syncAttr)

	// 5. NEG-OPEN
	subID := uuid.NewString()

	// Use the first filter for NEG-OPEN (NIP-77 uses a single filter)
	var negFilter *nip01.SubscriptionFilter
	if len(s.spec.Filters) > 0 {
		negFilter = &s.spec.Filters[0].SubscriptionFilter
	}

	negOpen := &wire.NegOpenPacket{
		SubscriptionID: subID,
		Filter:         negFilter,
		Message:        initHex,
	}

	select {
	case conn.Outgoing() <- negOpen:
		s.logger.Trace("NEG-OPEN packet sent to outgoing channel", syncAttr)
	case <-ctx.Done():
		return
	}

	s.logger.Info("NEG-OPEN sent, starting reconciliation", syncAttr)

	// 6. Reconciliation loop
	var haveIDs []string // IDs we have that they need
	var needIDs []string // IDs we need that they have

	for round := 0; round < s.spec.MaxReconcileRounds; round++ {
		var resp wire.SubscriptionResponse
		select {
		case resp = <-conn.Read():
		case err := <-conn.Errors():
			s.logger.Error(fmt.Errorf("connection error: %w", err), syncAttr)
			return
		case <-ctx.Done():
			return
		}

		switch r := resp.(type) {
		case *wire.NegMsgResponse:
			s.logger.Trace(fmt.Sprintf("Received NEG-MSG (%d bytes)", len(r.Message)/2), syncAttr)
			theirMsg, err := nip77.FromHex(r.Message)
			if err != nil {
				s.logger.Error(fmt.Errorf("failed to decode NEG-MSG: %w", err), syncAttr)
				return
			}

			// Debug: log each incoming range
			if s.logger.Enabled(tui.LogLevelTrace) {
				for i, rng := range theirMsg.Ranges {
					s.logger.Trace(fmt.Sprintf("  relay range[%d]: mode=%s payload=%d bytes ts=%d prefix=%d",
						i, negModeName(rng.Mode), len(rng.Payload), rng.UpperBound.Timestamp, len(rng.UpperBound.IDPrefix)), syncAttr)
				}
			}

			s.logger.Debug(fmt.Sprintf("Reconciling round %d", round+1), syncAttr)
			responseMsg, roundHave, roundNeed, err := neg.Reconcile(theirMsg)
			if err != nil {
				s.logger.Error(fmt.Errorf("reconciliation failed: %w", err), syncAttr)
				return
			}

			// Debug: log each outgoing range
			if s.logger.Enabled(tui.LogLevelTrace) {
				for i, rng := range responseMsg.Ranges {
					s.logger.Trace(fmt.Sprintf("  client range[%d]: mode=%s payload=%d bytes",
						i, negModeName(rng.Mode), len(rng.Payload)), syncAttr)
				}
			}

			haveIDs = append(haveIDs, roundHave...)
			needIDs = append(needIDs, roundNeed...)

			// Calculate progress percentage based on Skip ranges
			totalItems := len(allItems)
			reconciledItems := 0
			currIdx := 0
			for _, r := range responseMsg.Ranges {
				start := currIdx
				for currIdx < len(allItems) && nip77.ItemIsBeforeBound(allItems[currIdx], r.UpperBound) {
					currIdx++
				}
				if r.Mode == 0 { // Skip
					reconciledItems += (currIdx - start)
				}
			}

			progress := 0.0
			if totalItems > 0 {
				progress = float64(reconciledItems) / float64(totalItems) * 100
			}

			s.logger.Info(fmt.Sprintf("Round %d: have=%d, need=%d (%.0f%% reconciled)", round+1, len(roundHave), len(roundNeed), progress), syncAttr)

			// Check convergence
			if nip77.IsComplete(responseMsg) {
				s.logger.Success(fmt.Sprintf("Reconciliation complete after %d rounds (100%%)", round+1), syncAttr)
				goto reconcileDone
			}

			// Send next round
			respHex, err := responseMsg.ToHex()
			if err != nil {
				s.logger.Error(fmt.Errorf("failed to encode response: %w", err), syncAttr)
				return
			}

			s.logger.Trace("Sending next round NEG-MSG", syncAttr)
			select {
			case conn.Outgoing() <- &wire.NegMsgPacket{SubscriptionID: subID, Message: respHex}:
				s.logger.Debug(fmt.Sprintf("NEG-MSG sent for round %d", round+1), syncAttr)
			case <-ctx.Done():
				return
			}

		case *wire.NegErrResponse:
			s.logger.Error(fmt.Errorf("relay NEG-ERR: %s", r.Code), syncAttr)
			return

		case *wire.NoticeSubscriptionResponse:
			s.logger.Warn(fmt.Sprintf("notice: %s", r.Message), syncAttr)

		default:
			s.logger.Debug(fmt.Sprintf("Ignoring unexpected response type: %T", resp), syncAttr)
			log.Debug().Msgf("sync: ignoring unexpected response type: %T", resp)
		}
	}

	s.logger.Warn(fmt.Sprintf("Max reconciliation rounds reached (%d)", s.spec.MaxReconcileRounds), syncAttr)

reconcileDone:

	// 7. Close negentropy session
	select {
	case conn.Outgoing() <- &wire.NegClosePacket{SubscriptionID: subID}:
	case <-ctx.Done():
		return
	}

	s.logger.Info(fmt.Sprintf("Summary: %d to pull, %d to push", len(needIDs), len(haveIDs)), syncAttr)

	// 8. Pull phase (direction=down or both)
	if s.spec.Direction == SyncDirectionDown || s.spec.Direction == SyncDirectionBoth {
		s.pullEvents(ctx, conn, store, needIDs)
	}

	// 9. Push phase (direction=up or both)
	if s.spec.Direction == SyncDirectionUp || s.spec.Direction == SyncDirectionBoth {
		s.pushEvents(ctx, conn, store, haveIDs)
	}

	s.logger.Success("Sync complete", syncAttr)
}

func (s *SyncModule) pullEvents(ctx context.Context, conn *relayclient.Connection, store *relay.EventStore, needIDs []string) {
	if len(needIDs) == 0 {
		s.logger.Info("Pull: nothing to pull", syncAttr)
		return
	}

	s.logger.Info(fmt.Sprintf("Pulling %d events...", len(needIDs)), syncAttr)

	pulled := 0

	// Trusted mirrors stream's handleEvent semantics: a Trusted remote is
	// never second-guessed regardless of --strict-pow.
	enforcePow := s.strictPow && !s.spec.GetRemote().Trusted

	// Batch requests
	for i := 0; i < len(needIDs); i += s.spec.PullBatchSize {
		end := i + s.spec.PullBatchSize
		if end > len(needIDs) {
			end = len(needIDs)
		}
		batch := needIDs[i:end]
		s.logger.Trace(fmt.Sprintf("Pulling batch %d IDs (%d-%d)", len(batch), i, end), syncAttr)

		fg := nip01.NewSubscriptionFilterGroup()
		fg.Add(&nip01.SubscriptionFilter{IDs: batch})

		reqSubID := uuid.NewString()
		s.logger.Debug(fmt.Sprintf("Sending pull request sub_id=%s", reqSubID), syncAttr)
		conn.SubscribeWithID(reqSubID, fg)

		var batchEvents []*nip01.Event

		// Read events until EOSE
	batchLoop:
		for {
			select {
			case resp := <-conn.Read():
				switch r := resp.(type) {
				case *wire.EventSubscriptionResponse:
					if r.Event != nil {
						if enforcePow && !hasValidPow(r.Event) {
							s.logger.Warn(fmt.Sprintf("dropping pulled event %s: pow check failed", r.Event.ID), syncAttr)
							continue
						}
						batchEvents = append(batchEvents, r.Event)
					}
				case *wire.EOSESubscriptionResponse:
					break batchLoop
				}
			case err := <-conn.Errors():
				s.logger.Error(fmt.Errorf("pull error: %w", err), syncAttr)
				return
			case <-ctx.Done():
				return
			}
		}

		conn.CloseSubscription(reqSubID)

		// Batch insert
		if len(batchEvents) > 0 {
			s.logger.Trace(fmt.Sprintf("Inserting %d events into local store", len(batchEvents)), syncAttr)
			if err := store.InsertEvents(ctx, batchEvents); err != nil {
				if errors.Is(err, relay.ErrEventDuplicated) {
					s.logger.Debug(fmt.Sprintf("Inserted/verified %d events (some were already present)", len(batchEvents)), syncAttr)
					pulled += len(batchEvents)
				} else {
					s.logger.Error(fmt.Errorf("insert failed: %w", err), syncAttr)
				}
			} else {
				s.logger.Debug(fmt.Sprintf("Inserted %d events", len(batchEvents)), syncAttr)
				pulled += len(batchEvents)
			}
		}
	}

	s.logger.Success(fmt.Sprintf("Pulled %d events", pulled), syncAttr)
}

func (s *SyncModule) pushEvents(ctx context.Context, conn *relayclient.Connection, store *relay.EventStore, haveIDs []string) {
	if len(haveIDs) == 0 {
		s.logger.Info("Push: nothing to push", syncAttr)
		return
	}

	s.logger.Info(fmt.Sprintf("Pushing %d events...", len(haveIDs)), syncAttr)

	pushed := 0

	// Batch lookup by IDs and publish
	for i := 0; i < len(haveIDs); i += s.spec.PullBatchSize {
		end := i + s.spec.PullBatchSize
		if end > len(haveIDs) {
			end = len(haveIDs)
		}
		batch := haveIDs[i:end]
		s.logger.Trace(fmt.Sprintf("Pushing batch %d IDs (%d-%d)", len(batch), i, end), syncAttr)

		fg := nip01.NewSubscriptionFilterGroup()
		fg.Add(&nip01.SubscriptionFilter{IDs: batch})

		s.logger.Trace("Reading events from store for push", syncAttr)
		events, err := readEventsFromStoreByIDs(ctx, store, fg)
		if err != nil {
			s.logger.Warn(fmt.Sprintf("Failed to read batch: %v", err), syncAttr)
			continue
		}
		s.logger.Debug(fmt.Sprintf("Found %d/%d events in store to push", len(events), len(batch)), syncAttr)

		traceEnabled := s.logger.Enabled(tui.LogLevelTrace)
		for _, ev := range events {
			if traceEnabled {
				s.logger.Trace(fmt.Sprintf("Publishing event %s", ev.ID[:8]), syncAttr)
			}
			if conn.Send(ev) {
				pushed++
			}
		}
	}

	s.logger.Success(fmt.Sprintf("Pushed %d events", pushed), syncAttr)
}

// readEventsFromStoreByIDs reads events from the store matching the given filter group.
func readEventsFromStoreByIDs(ctx context.Context, store *relay.EventStore, fg *nip01.SubscriptionFilterGroup) ([]*nip01.Event, error) {
	query, err := relay.NewStoreQuery(store, fg)
	if err != nil {
		return nil, err
	}

	var events []*nip01.Event
	var wg sync.WaitGroup
	sub, eventsCh, errorsCh, eose := relay.NewSubscription(uuid.NewString(), query)
	go sub.Start(ctx, &wg)

	for {
		select {
		case pe := <-eventsCh:
			event, err := store.FindEvent(pe.Evsid)
			if err != nil {
				return nil, err
			}
			events = append(events, event)
			wg.Done()

		case err := <-errorsCh:
			return nil, err

		case <-eose:
			return events, nil
		}
	}
}

func (s *SyncModule) Spec() Spec {
	return s.spec
}

func (s *SyncModule) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.conn != nil {
		s.conn.Close()
	}
	if s.store != nil {
		s.store.Close()
	}
}

// idBytesToHex converts raw ID bytes to hex. Unused placeholder removed.
func idBytesToHex(idBytes []byte) string {
	return hex.EncodeToString(idBytes)
}

var negModeNames = [...]string{"Skip", "Fingerprint", "IdList"}

func negModeName(mode int) string {
	if mode < 0 || mode >= len(negModeNames) {
		return ""
	}
	return negModeNames[mode]
}
