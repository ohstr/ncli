package client

import (
	"context"
	"errors"
	"time"

	"github.com/ohstr/nmilat/nip01"
	relaystore "github.com/ohstr/nmilat/relay"
)

// TargetRead is one target's own answer to a read, kept apart from every
// other target's -- unlike QueryTargets, which merges and dedupes, so a
// caller can't tell which relay held what.
type TargetRead struct {
	Target     string
	Events     []*nip01.Event
	Restricted bool
	Err        error
	Elapsed    time.Duration
}

// Label names a target the way a user wrote it: the relay as given, or the
// local store's path.
func (ds *FlowSpec) Label() string {
	if ds.Type == FlOW_LOCAL {
		return ds.Path
	}
	if ds.relayURI != nil {
		return ds.relayURI.String()
	}
	return ds.Relay
}

// ReadTarget reads filters from one target. timeout bounds a remote read; 0
// waits indefinitely. authSigner logs in (NIP-42) when a relay asks; nil
// stays anonymous.
func ReadTarget(ctx context.Context, target *FlowSpec, filters *nip01.SubscriptionFilterGroup, timeout time.Duration, authSigner Signer) TargetRead {
	res := TargetRead{Target: target.Label()}
	start := time.Now()
	switch target.Type {
	case FlOW_LOCAL:
		res.Events, res.Err = relaystore.ReadEventsFromStore(ctx, target.Path, filters)
	case FlOW_REMOTE:
		res.Events, res.Restricted, res.Err = readEventsWithFallback(ctx, timeout, target.relayURI, target.relayFallbackURI, filters, authSigner)
	default:
		res.Err = errors.New("unknown target type")
	}
	res.Elapsed = time.Since(start)
	return res
}
