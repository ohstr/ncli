package jobs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/relay/internal/zaptest"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/relay"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var bothIndexes = relay.DerivedIndexes{Zaps: true, Social: true}

// zapAndFollow is a zap receipt and a contact list, one entry in each index.
func zapAndFollow(t *testing.T, at uint64) []*nip01.Event {
	_, receiver := testKey(t, 1)
	return []*nip01.Event{
		zaptest.Receipt(t, zaptest.NewKey(t, 2), zaptest.NewKey(t, 4), receiver, 21000, at),
		signed(t, 3, 3, at+1, []string{"p", receiver}),
	}
}

func runToEnd(t *testing.T, m *Manager, index string, kind Kind) Job {
	t.Helper()
	j, err := m.Start(index, kind, TriggerAdmin, "")
	require.NoError(t, err)
	return waitEnded(t, m, j.ID)
}

func TestStoreIndex_CatchUpAfterTheFeatureWasOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.db")
	store := openAt(t, path, bothIndexes)
	insert(t, store, zapAndFollow(t, 100)...)
	store.Close()

	store = openAt(t, path, relay.DerivedIndexes{})
	insert(t, store, zaptest.Receipt(t, zaptest.NewKey(t, 5), zaptest.NewKey(t, 4), signed(t, 1, 1, 1).PubKey, 5000, 200))
	off := NewZapIndex(store, "zap ranking is off")
	st := off.Status()
	assert.Equal(t, StateDisabled, st.State)
	assert.Equal(t, int64(1), st.Entries)
	store.Close()

	store = openAt(t, path, bothIndexes)
	zaps := NewZapIndex(store, "")
	st = zaps.Status()
	assert.Equal(t, StateBehind, st.State)
	assert.Equal(t, KindCatchUp, st.Action)
	assert.NotZero(t, st.Behind)

	m := New("", zerolog.Nop(), zaps)
	done := runToEnd(t, m, "zaps", KindCatchUp)
	assert.Equal(t, StateSucceeded, done.State, done.Error)
	assert.Equal(t, int64(1), done.EntriesBefore)
	assert.Equal(t, int64(2), done.EntriesAfter)
	assert.Contains(t, done.Summary, "1 → 2 entries")
	st = zaps.Status()
	assert.Equal(t, StateReady, st.State)
	assert.Empty(t, st.Action)
}

func TestStoreIndex_DropFreesADisabledIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.db")
	store := openAt(t, path, bothIndexes)
	insert(t, store, zapAndFollow(t, 100)...)
	store.Close()

	store = openAt(t, path, relay.DerivedIndexes{})
	zaps, stats := NewZapIndex(store, "zaps off"), NewStatsIndex(store, "stats off")
	m := New("", zerolog.Nop(), zaps, stats)
	for _, ix := range []*StoreIndex{zaps, stats} {
		st := ix.Status()
		require.True(t, st.HasData, ix.Name())
		assert.Contains(t, st.Hint, "ncli relay index drop "+ix.Name())
		done := runToEnd(t, m, ix.Name(), KindDrop)
		assert.Equal(t, StateSucceeded, done.State, done.Error)
		assert.Equal(t, int64(1), done.EntriesBefore, ix.Name())
		assert.Equal(t, ix.Name()+": dropped 1 entries", done.Summary)
		st = ix.Status()
		assert.False(t, st.HasData)
		assert.Zero(t, st.Retired, "freed, not just hidden")
		_, err := m.Start(ix.Name(), KindDrop, TriggerAdmin, "")
		assert.ErrorIs(t, err, ErrNothingToDo, "a second drop has nothing to do")
	}
}

func TestStoreIndex_CancelledRebuildIsDiscarded(t *testing.T) {
	store := openAt(t, filepath.Join(t.TempDir(), "notes.db"), bothIndexes)
	var evs []*nip01.Event
	for i := range 3000 {
		evs = append(evs, signed(t, 1000+i%50, 1, uint64(100+i)))
	}
	insert(t, store, append(evs, zapAndFollow(t, 5000)...)...)
	zaps := NewZapIndex(store, "")
	m := New("", zerolog.Nop(), zaps)

	j, err := m.Start("zaps", KindRebuild, TriggerAdmin, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { r := m.RunningJob("zaps"); return r == nil || r.Done > 0 }, 5*time.Second, time.Millisecond)
	_, err = m.Cancel(j.ID)
	require.NoError(t, err)
	done := waitEnded(t, m, j.ID)
	if done.State == StateCancelled {
		st, err := store.IndexStatus(relay.IndexZaps)
		require.NoError(t, err)
		assert.Nil(t, st.Build, "nothing left to resume")
		assert.Equal(t, int64(1), st.Entries, "the live index is untouched")
	}
}

func TestStoreIndex_StatusNamesTheResumeAsARebuild(t *testing.T) {
	store := openAt(t, filepath.Join(t.TempDir(), "notes.db"), bothIndexes)
	insert(t, store, zapAndFollow(t, 100)...)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := store.RebuildIndex(ctx, relay.IndexZaps, func(relay.IndexProgress) { cancel() })
	require.ErrorIs(t, err, context.Canceled)
	st := NewZapIndex(store, "").Status()
	assert.Equal(t, KindRebuild, st.Action)
	assert.Contains(t, st.Reason, "resuming")
}
