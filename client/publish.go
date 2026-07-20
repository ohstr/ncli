package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ohstr/nmilat/nip01"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/ohstr/nmilat/wire"
	"github.com/rs/zerolog/log"
)

// PublishEvent sends event over conn and waits for the relay's matching OK
// response -- or a rejection, a connection error/close, ctx's cancellation,
// or a 10s timeout, whichever comes first. conn has no background reader,
// so this is the only reader on it: it must drain everything (Notices
// included) until it sees the matching OK, rather than assuming the next
// message is it. Shared by RecoveryManager's retry queue and
// PublishToTargets (`ncli publish`).
func PublishEvent(ctx context.Context, conn *relayclient.Connection, event *nip01.Event) error {
	if !conn.Send(event) {
		return errors.New("connection closed during send")
	}

	timeout := time.After(10 * time.Second)

	for {
		select {
		case response := <-conn.Read():
			switch r := response.(type) {
			case *wire.OkSubscriptionResponse:
				if r.EventID == event.ID {
					if !r.Accepted {
						return fmt.Errorf("relay rejected event: %s", r.Message)
					}
					return nil
				}
			case *wire.NoticeSubscriptionResponse:
				log.Warn().Str("notice", r.Message).Msg("received notice while publishing")
			}
		case err := <-conn.Errors():
			return err
		case <-conn.Closed():
			return errors.New("connection closed")
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return errors.New("timeout waiting for OK")
		}
	}
}

// PublishResult is one (event, relay) pair's publish outcome.
type PublishResult struct {
	ID       string `json:"id"`
	Relay    string `json:"relay"`
	Accepted bool   `json:"accepted"`
	Error    string `json:"error,omitempty"`
}

// PublishReport summarizes PublishToTargets' attempt to publish every event
// to every relay target -- the full (event, relay) cross product, not just
// one relay per event.
type PublishReport struct {
	Attempted int             `json:"attempted"`
	Succeeded int             `json:"succeeded"`
	Failed    int             `json:"failed"`
	Results   []PublishResult `json:"results"`
}

// AllSucceeded reports whether every (event, relay) pair was accepted.
func (r *PublishReport) AllSucceeded() bool {
	return r.Failed == 0
}

// PublishToTargets sends every event in events to every remote relay in
// targets, waiting for each relay's OK, and reports the outcome of every
// (event, relay) pair. An individual unreachable relay is logged and
// skipped -- matching mergeEventsFromTargets' tolerance for one bad target
// (find/dump/miner check) -- unless every target fails to connect, in which
// case ErrNoReachableTargets is returned rather than silently reporting an
// empty-looking report as if nothing had been asked of it. Local store
// targets aren't supported yet -- rejected up front with a clear error
// rather than silently skipped, since a store target quietly not receiving
// events would be easy to miss.
func PublishToTargets(ctx context.Context, targets *TargetsSpec, events []*nip01.Event) (*PublishReport, error) {
	report := &PublishReport{}
	reached := false

	for _, target := range targets.Relays {
		if target.killed {
			continue
		}
		if target.Type != FlOW_REMOTE {
			return nil, fmt.Errorf("ncli publish only supports remote relay targets for now, not %q (%s)", target.Type, target.Path)
		}

		conn, err := connectRelayWithFallback(ctx, target.relayURI, target.relayFallbackURI, nil)
		if err != nil {
			var connErr *relayclient.ConnectionError
			if errors.As(err, &connErr) {
				log.Error().Err(connErr).Msg("target unreachable, skipping")
				continue
			}
			return nil, err
		}
		reached = true

		for _, event := range events {
			pubCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			pubErr := PublishEvent(pubCtx, conn, event)
			cancel()

			result := PublishResult{ID: event.ID, Relay: target.relayURI.Host}
			report.Attempted++
			if pubErr != nil {
				result.Error = pubErr.Error()
				report.Failed++
			} else {
				result.Accepted = true
				report.Succeeded++
			}
			report.Results = append(report.Results, result)
		}

		conn.Close()
	}

	if !reached && len(targets.Relays) > 0 {
		return nil, ErrNoReachableTargets
	}

	return report, nil
}
