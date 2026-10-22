package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ohstr/ncli/cli/relay/internal/zaptest"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// openAt opens path with ix, closing it at the end of the test.
func openAt(t *testing.T, path string, ix relay.DerivedIndexes) *relay.EventStore {
	t.Helper()
	store, err := relay.NewEventStore(path, &nip11.Limitation{MaxLimit: 500}, relay.WithEventStoreIndexes(ix))
	require.NoError(t, err)
	t.Cleanup(store.Close)
	return store
}

var bothIndexes = relay.DerivedIndexes{Zaps: true, Social: true}

// zapAndFollow is a zap receipt and a contact list, one entry in each index.
func zapAndFollow(t *testing.T) []*nip01.Event {
	_, receiver := testKey(t, 1)
	return []*nip01.Event{
		zaptest.Receipt(t, zaptest.NewKey(t, 2), zaptest.NewKey(t, 4), receiver, 21000, 100),
		signed(t, 3, 3, 101, []string{"p", receiver}),
	}
}

func TestStoreIndexStates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.db")
	store := openAt(t, path, bothIndexes)
	insert(t, store, zapAndFollow(t)...)
	zaps, stats := &ZapIndex{Store: store}, &StatsIndex{Store: store}
	for _, ix := range []Index{zaps, stats} {
		state, has := ix.State()
		require.Equal(t, IndexReady, state, ix.Name())
		require.True(t, has, ix.Name())
	}

	require.NoError(t, zaps.Drop(context.Background()))
	state, has := zaps.State()
	require.Equal(t, IndexStale, state, "an enabled index is stale once dropped")
	require.False(t, has)

	store.Close()
	store = openAt(t, path, relay.DerivedIndexes{})
	state, has = (&StatsIndex{Store: store}).State()
	require.Equal(t, IndexDisabled, state)
	require.True(t, has, "turning stats off keeps the data")
}

func TestIndexesReportBuilding(t *testing.T) {
	ix := &fakeIndex{name: "zaps", release: make(chan struct{})}
	m := New("", zerolog.Nop(), ix)
	_, _, err := m.Start("zaps", KindRebuild, Options{})
	require.NoError(t, err)
	t.Cleanup(func() { close(ix.release) })
	require.Equal(t, IndexBuilding, m.Indexes()[0].State)
}

func TestDropDisabledIndexWithData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.db")
	on := openAt(t, path, bothIndexes)
	insert(t, on, zapAndFollow(t)...)
	on.Close()
	store := openAt(t, path, relay.DerivedIndexes{})

	m := New("", zerolog.Nop(),
		&ZapIndex{Store: store, Disabled: "zaps off"},
		&StatsIndex{Store: store, Disabled: "stats off"},
	)
	for _, info := range m.Indexes() {
		require.False(t, info.Available, info.Name)
		require.Equal(t, IndexDisabled, info.State, info.Name)
		require.True(t, info.HasData, info.Name)
		require.Positive(t, info.Estimate, info.Name)
		require.Zero(t, info.ScanEstimate, info.Name)

		_, _, err := m.Start(info.Name, KindRebuild, Options{})
		require.ErrorIs(t, err, ErrUnavailable, info.Name)
		_, _, err = m.Estimate(info.Name)
		require.ErrorIs(t, err, ErrUnavailable, "a dry run is a rebuild's")

		j, started, err := m.Start(info.Name, KindDrop, Options{})
		require.NoError(t, err, info.Name)
		require.True(t, started)
		waitState(t, m, j.ID, StateSucceeded)
	}
	for _, info := range m.Indexes() {
		require.False(t, info.HasData, info.Name)
		require.Zero(t, info.Estimate, info.Name)
	}
}

func TestDropBusyDuringRebuild(t *testing.T) {
	store := openAt(t, filepath.Join(t.TempDir(), "notes.db"), bothIndexes)
	insert(t, store, zapAndFollow(t)...)
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := store.RebuildZapIndex(context.Background(), func(int, int) {
			close(started)
			<-release
		})
		done <- err
	}()
	<-started
	err := (&ZapIndex{Store: store}).Drop(context.Background())
	close(release)
	require.NoError(t, <-done)
	require.True(t, errors.Is(err, ErrBusy), "drop during a rebuild = %v", err)
}

func TestSearchDropStillNeedsSearch(t *testing.T) {
	m := New("", zerolog.Nop(), &SearchIndex{Disabled: "search is off"})
	_, _, err := m.Start("search", KindDrop, Options{})
	require.ErrorIs(t, err, ErrUnavailable)
	info := m.Indexes()[0]
	require.Equal(t, IndexDisabled, info.State)
	require.False(t, info.HasData)
}

func TestIndexInfoJSONIsAdditive(t *testing.T) {
	m := New("", zerolog.Nop(), &fakeIndex{name: "zaps"})
	raw, err := json.Marshal(m.Indexes()[0])
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	for _, k := range []string{"name", "available", "estimate", "scan_estimate", "state", "has_data"} {
		require.Contains(t, got, k)
	}
	require.Equal(t, true, got["available"])
	require.Equal(t, IndexReady, got["state"])
}
