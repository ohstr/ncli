package client

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
	"github.com/rs/zerolog/log"
)

type TargetFlow struct {
	fc  *FlowContext
	sub ClientSubscription
}

type Inspector struct {
	sc            *StreamChannel
	metrics       tui.FlowMetricsSlice
	subscriptions map[int]ClientSubscription
	filters       *nip01.SubscriptionFilterGroup
	store         *InspectStore
}

func (i *Inspector) Spec() Spec {
	spec := &InspectSpec{
		Targets: make([]*FlowSpec, 0, len(i.subscriptions)),
		Filters: make([]*FilterSpec, 0, i.filters.Size()),
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

	for _, flow := range i.subscriptions {
		if fs := flowSpec(flow); fs != nil {
			spec.Targets = append(spec.Targets, fs)
		}
	}

	for _, f := range i.filters.Copy().GetAll() {
		f.Since = uint64(time.Now().Unix())
		spec.Filters = append(spec.Filters, NewFilterSpec(f))
	}

	return spec
}

// NewInspector runs a read-only inspect session. events, if non-nil, is the
// already-constructed (not yet Init'd) TUI event table this session feeds --
// it must be wired into sc.retain here, before any target goroutine starts
// below, or early events could race past a nil retain and fall back to the
// aggregating logger instead.
func NewInspector(ctx context.Context, spec *InspectSpec, events *tui.EventTable) (*Inspector, error) {

	store, err := NewInspectStore()
	if err != nil {
		return nil, fmt.Errorf("failed to create inspect session store: %w", err)
	}

	i := &Inspector{
		sc:            NewStreamChannel(0, nil),
		metrics:       []tui.FlowStat{},
		subscriptions: make(map[int]ClientSubscription, len(spec.Targets)),
		filters:       spec.filters,
		store:         store,
	}

	if events != nil {
		i.sc.retain = func(event *nip01.Event, attr tui.FlowAttr) {
			if err := i.store.Insert(ctx, event); err != nil {
				i.sc.logger.Error(fmt.Errorf("failed to retain event %s: %w", event.ID, err), attr)
			}
			events.Push(event, attr)
		}
	}

	targets := make([]*TargetFlow, 0, len(spec.Targets))

	for index, target := range spec.Targets {
		var subID = index + 1
		var subscription ClientSubscription

		switch target.Type {
		case FlOW_LOCAL:
			if err := ensureLocalStoreDir(target); err != nil {
				return nil, fmt.Errorf("failed to create directory for local store: %w", err)
			}
			se, err := relay.NewEventStore(target.Path, &nip11.Limitation{})
			if err != nil {
				return nil, err
			}
			subscription = NewLocalSubscription(se, target.Trusted, target.WriteConcurrency)

		case FlOW_REMOTE:
			subscription = NewRemoteSubscription(target.relayURI, target.relayFallbackURI, target.Trusted, nil)
		}

		i.subscriptions[subID] = subscription
		// Inbound, not Outbound: every inspect target is a source being
		// read from, never a merged-stream destination, so (unlike Stream's
		// destinations) each row's Kinds/Pubkeys diversity is genuinely
		// distinct per target and worth showing -- that's exactly what
		// NewInboundMetrics tracks. It drops "Synced" instead (an
		// ack-from-publish concept Inspector never reaches, since it never
		// publishes), which would otherwise just sit at 0 for every row.
		stat := tui.NewInboundMetrics(subID, subscription.Name(), func() {
			i.CloseBySubID(subID)
			delete(i.subscriptions, subID)
		})
		i.metrics = append(i.metrics, stat)
		fc := NewFlowContext(spec.filters, stat, target.Trusted, nil)

		targets = append(targets, &TargetFlow{
			fc:  fc,
			sub: subscription,
		})
	}

	indexWidth := len(strconv.Itoa(len(targets)))
	i.sc.logger.SetIndexWidth(indexWidth)
	for _, stat := range i.metrics {
		stat.SetIndexWidth(indexWidth)
	}
	if events != nil {
		events.SetIndexWidth(indexWidth)
	}

	for _, target := range targets {
		go target.sub.Run(ctx, target.sub.Read, i.sc, target.fc)
	}

	return i, nil
}

func (i *Inspector) CloseBySubID(subID int) {
	if sub, ok := i.subscriptions[subID]; ok {
		sub.Close()
		delete(i.subscriptions, subID)
	}
}

func (i *Inspector) Close() {
	for _, sub := range i.subscriptions {
		sub.Close()
	}
	if i.store != nil {
		if err := i.store.Close(); err != nil {
			log.Warn().Err(err).Msg("failed to clean up inspect session store")
		}
	}
}
