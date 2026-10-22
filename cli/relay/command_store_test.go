package relay

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/relay/internal/zaptest"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/relay"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

// openTestStore opens path the way the relay does under cfg.
func openTestStore(t *testing.T, cfg RelayConfig, path string) *relay.EventStore {
	t.Helper()
	cfg.RelayNotesDb = path
	store, err := openRelayStore(&cfg)
	require.NoError(t, err)
	t.Cleanup(store.Close)
	return store
}

func zapAndFollow(t *testing.T) []*nip01.Event {
	sender, provider := zaptest.NewKey(t, 1), zaptest.NewKey(t, 2)
	follows := &nip01.Event{PubKey: sender.Pub, CreatedAt: 100, Kind: 3, Tags: [][]string{{"p", strings.Repeat("a", 64)}}}
	require.NoError(t, follows.Sign(sender.Priv))
	return []*nip01.Event{
		zaptest.Receipt(t, sender, provider, strings.Repeat("a", 64), 21_000, uint64(time.Now().Unix())),
		follows,
	}
}

func TestOpenRelayStore_IndexesFollowConfig(t *testing.T) {
	for _, c := range []struct {
		name        string
		cfg         RelayConfig
		zaps, stats bool
	}{
		{"no cache block", RelayConfig{}, false, false},
		{"zaps", cacheOn(false, false, false, true), true, false},
		{"stats", cacheOn(false, false, true, false), false, true},
	} {
		store := openTestStore(t, c.cfg, filepath.Join(t.TempDir(), "notes.db"))
		require.NoError(t, store.InsertEvents(context.Background(), zapAndFollow(t)))
		z, s := store.ZapIndexState(), store.SocialIndexState()
		require.Equal(t, c.zaps, z.Enabled, c.name)
		require.Equal(t, c.zaps, z.HasData, c.name)
		require.Equal(t, c.stats, s.Enabled, c.name)
		require.Equal(t, c.stats, s.HasData, c.name)
	}
}

func TestOpenRelayStore_WarnsOnLeftoverData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.db")
	store := openTestStore(t, cacheOn(false, false, true, true), path)
	require.NoError(t, store.InsertEvents(context.Background(), zapAndFollow(t)))
	store.Close()

	var buf bytes.Buffer
	prev := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = prev })

	store = openTestStore(t, RelayConfig{}, path)
	require.Contains(t, buf.String(), "ncli relay index drop zaps")
	require.Contains(t, buf.String(), "ncli relay index drop stats")
	require.True(t, store.ZapIndexState().HasData, "turning a feature off keeps its data")

	buf.Reset()
	require.NoError(t, store.ClearZapIndex(context.Background()))
	require.NoError(t, store.ClearSocialIndex(context.Background()))
	store.Close()
	openTestStore(t, RelayConfig{}, path)
	require.NotContains(t, buf.String(), "relay index drop")
}

func TestNewServer_WarnsOnIndexMismatch(t *testing.T) {
	prev, prevLog := config, log.Logger
	t.Cleanup(func() { config, log.Logger = prev, prevLog })
	var buf bytes.Buffer
	log.Logger = zerolog.New(&buf)

	store := openTestStore(t, RelayConfig{}, filepath.Join(t.TempDir(), "notes.db"))
	config = cacheOn(false, false, false, true)
	config.Nip11.PrivKey = strings.Repeat("1", 64)
	s := NewServer(store, nil)
	t.Cleanup(s.Stop)
	require.Contains(t, buf.String(), "opened with other indexes")
}
