package jobs

import (
	"context"
	"errors"
	"fmt"

	"github.com/ohstr/ncli/cli/common/meilisearch"
	"github.com/ohstr/nmilat/relay"
)

// Index states, as listed. A running job shows as building, catching_up,
// rescoring or dropping.
const (
	StateDisabled     = "disabled"
	StateEmpty        = "empty"
	StateReady        = "ready"
	StateBehind       = "behind"
	StateNeedsRebuild = "needs_rebuild"
	StateUnreachable  = "unreachable"
	StateBuilding     = "building"
	StateCatchingUp   = "catching_up"
	StateRescoring    = "rescoring"
	StateDropping     = "dropping"
)

// Status describes an index and what it needs next.
type Status struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Unavailable says why a disabled index is off.
	Unavailable string `json:"unavailable,omitempty"`
	State       string `json:"state"`
	// Action is the job the relay runs next on its own; Reason says why.
	Action Kind   `json:"action,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Hint is what an admin may want to run, when the relay won't.
	Hint    string `json:"hint,omitempty"`
	HasData bool   `json:"has_data"`
	// Entries is exact: zap receipts, followed profiles, search documents.
	Entries int64 `json:"entries"`
	// Behind is events stored since the index was last current; Queued is
	// profiles waiting to be looked at again (search).
	Behind  int64 `json:"behind"`
	Queued  int64 `json:"queued,omitempty"`
	Head    int64 `json:"head"`
	BuiltAt int64 `json:"built_at,omitempty"`
	Retired int   `json:"retired,omitempty"`
	// Error is the last failure reaching the index's backend.
	Error string `json:"error,omitempty"`
	// Warning is a problem that needs no action yet (search: the queue
	// outgrowing what is applied).
	Warning string `json:"warning,omitempty"`
	// LastTask is search's last Meilisearch document task: how long its
	// batch took and its slowest step.
	LastTask *meilisearch.TaskStat `json:"last_task,omitempty"`
}

// StoreIndex is a derived index kept in the event store (nmilat's zap or
// social index), maintained only while its feature is on.
type StoreIndex struct {
	// name is how ncli calls it; store is nmilat's name.
	name, store string
	Store       *relay.EventStore
	// Disabled explains why the index is off ("" when on).
	Disabled string
}

// NewZapIndex is the zap ranking index.
func NewZapIndex(store *relay.EventStore, disabled string) *StoreIndex {
	return &StoreIndex{name: "zaps", store: relay.IndexZaps, Store: store, Disabled: disabled}
}

// NewStatsIndex is each profile's zaps and follows, behind social scores
// and NIP-85 assertions.
func NewStatsIndex(store *relay.EventStore, disabled string) *StoreIndex {
	return &StoreIndex{name: "stats", store: relay.IndexSocial, Store: store, Disabled: disabled}
}

func (x *StoreIndex) Name() string      { return x.name }
func (x *StoreIndex) WritesStore() bool { return true }

func (x *StoreIndex) Status() Status {
	st, err := x.Store.IndexStatus(x.store)
	out := Status{Name: x.name, Enabled: x.Disabled == "", Unavailable: x.Disabled}
	if err != nil {
		out.State, out.Error = StateDisabled, err.Error()
		return out
	}
	out.State, out.Reason = st.State, st.Reason
	out.Action = Kind(st.Action)
	if st.Action == relay.ActionResume {
		out.Action = KindRebuild
	}
	out.HasData, out.Entries, out.Head = st.HasData, st.Entries, int64(st.Head)
	out.Behind, out.BuiltAt, out.Retired = int64(st.Behind), st.BuiltAt, st.Retired
	if st.Build != nil {
		out.Behind = int64(st.Build.Head - min(st.Build.Head, st.Build.Mark))
	}
	if !out.Enabled {
		out.State = StateDisabled
		if st.HasData {
			out.Hint = fmt.Sprintf("it still holds %s entries; remove them with ncli relay index drop %s", Count(st.Entries), x.name)
		}
	}
	return out
}

func (x *StoreIndex) Run(ctx context.Context, kind Kind, progress func(Progress)) (Result, error) {
	res, err := x.run(ctx, kind, progress)
	if err != nil || kind != KindRebuild {
		return res, err
	}
	// The generation the rebuild replaced is freed as part of it.
	if _, err := x.run(ctx, KindReap, progress); err != nil && !errors.Is(err, ErrNothingToDo) {
		return res, err
	}
	return res, nil
}

func (x *StoreIndex) run(ctx context.Context, kind Kind, progress func(Progress)) (Result, error) {
	action := map[Kind]string{KindCatchUp: relay.ActionCatchUp, KindRebuild: relay.ActionRebuild, KindRescore: relay.ActionRescore, KindReap: relay.ActionReap}[kind]
	res, err := x.Store.RunIndexAction(ctx, x.store, action, func(p relay.IndexProgress) {
		if progress != nil {
			progress(Progress{Phase: p.Phase, Done: p.Done, Total: p.Total, Applied: p.Applied})
		}
	})
	return Result{Applied: res.Applied, EntriesBefore: res.EntriesBefore, EntriesAfter: res.EntriesAfter}, storeErr(err)
}

func (x *StoreIndex) Drop(ctx context.Context, progress func(Progress)) (Result, error) {
	res, err := x.Store.ClearIndex(x.store)
	if err != nil {
		return Result{}, storeErr(err)
	}
	out := Result{EntriesBefore: res.EntriesBefore}
	// The data is gone from the index at once; free its space now.
	reaped, err := x.Run(ctx, KindReap, progress)
	if err != nil && !errors.Is(err, ErrNothingToDo) {
		return out, err
	}
	out.Applied = reaped.Applied
	return out, nil
}

// Cancelled discards a rebuild an admin stopped, rather than resume it.
func (x *StoreIndex) Cancelled(kind Kind) {
	if kind == KindRebuild {
		_ = x.Store.AbortIndexBuild(x.store)
	}
}

func storeErr(err error) error {
	switch {
	case errors.Is(err, relay.ErrNothingToDo):
		return ErrNothingToDo
	case errors.Is(err, relay.ErrIndexBusy):
		return ErrBusy
	case errors.Is(err, relay.ErrIndexEnabled):
		return ErrEnabled
	case errors.Is(err, relay.ErrIndexDisabled):
		return ErrUnavailable
	}
	return err
}
