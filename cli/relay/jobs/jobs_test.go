package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/relay/internal/zaptest"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/utils"
	"github.com/rs/zerolog"
)

func testKey(t *testing.T, i int) (priv, pub string) {
	t.Helper()
	sum := sha256.Sum256([]byte(fmt.Sprintf("jobs-test-key-%d", i)))
	priv = hex.EncodeToString(sum[:])
	pub, err := utils.GetPublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func signed(t *testing.T, i, kind int, createdAt uint64, tags ...[]string) *nip01.Event {
	t.Helper()
	priv, pub := testKey(t, i)
	ev := &nip01.Event{PubKey: pub, Kind: kind, CreatedAt: createdAt, Tags: tags, Content: fmt.Sprintf(`{"name":"user%d"}`, i)}
	if err := ev.Sign(priv); err != nil {
		t.Fatal(err)
	}
	return ev
}

func newStore(t *testing.T, maxLimit int) *relay.EventStore {
	t.Helper()
	store, err := relay.NewEventStore(filepath.Join(t.TempDir(), "notes.db"), &nip11.Limitation{MaxLimit: maxLimit}, relay.WithEventStoreIndexes(relay.DerivedIndexes{Zaps: true, Social: true}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func insert(t *testing.T, store *relay.EventStore, events ...*nip01.Event) {
	t.Helper()
	if err := store.InsertEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
}

// A store query returns at most max_limit events; a rebuild must still see
// every profile, including several stored in the same second.
func TestEachProfilePagesPastMaxLimit(t *testing.T) {
	store := newStore(t, 3)
	var events []*nip01.Event
	for i := 0; i < 10; i++ {
		createdAt := uint64(100)
		switch {
		case i >= 8:
			createdAt = 300
		case i >= 5:
			createdAt = 200
		}
		events = append(events, signed(t, i, 0, createdAt))
	}
	events = append(events, signed(t, 99, 1, 250)) // not a profile
	insert(t, store, events...)

	seen := map[string]int{}
	n, err := eachProfile(context.Background(), store, 0, func(ev *nip01.Event) { seen[ev.ID]++ })
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 || len(seen) != 10 {
		t.Fatalf("scanned %d, distinct %d; want 10 and 10", n, len(seen))
	}
	for id, c := range seen {
		if c != 1 {
			t.Errorf("profile %s visited %d times", id[:8], c)
		}
	}

	since := map[string]int{}
	if _, err := eachProfile(context.Background(), store, 200, func(ev *nip01.Event) { since[ev.ID]++ }); err != nil {
		t.Fatal(err)
	}
	if len(since) != 5 {
		t.Errorf("profiles since 200 = %d, want 5", len(since))
	}
}

// fakeIndex blocks a rebuild until released.
type fakeIndex struct {
	name    string
	why     string
	release chan struct{}
	err     error
	mu      sync.Mutex
	drops   int
}

func (f *fakeIndex) Name() string          { return f.name }
func (f *fakeIndex) Available(Kind) string { return f.why }
func (f *fakeIndex) State() (string, bool) {
	if f.why != "" {
		return IndexDisabled, false
	}
	return IndexReady, true
}
func (f *fakeIndex) Estimate() int64     { return 5 }
func (f *fakeIndex) ScanEstimate() int64 { return 42 }
func (f *fakeIndex) Rebuild(ctx context.Context, _ Options, progress Progress) error {
	progress(7, 3)
	select {
	case <-f.release:
		return f.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (f *fakeIndex) Drop(context.Context) error {
	f.mu.Lock()
	f.drops++
	f.mu.Unlock()
	return nil
}

func waitState(t *testing.T, m *Manager, id string, want State) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		j, err := m.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if j.State == want {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s state %s, want %s", id, j.State, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStartIsIdempotentAndExclusive(t *testing.T) {
	ix := &fakeIndex{name: "search", release: make(chan struct{})}
	finished := make(chan Job, 4)
	m := New("", zerolog.Nop(), ix)
	m.OnFinish = func(j Job) { finished <- j }

	first, started, err := m.Start("search", KindRebuild, Options{Fresh: true})
	if err != nil || !started || first.State != StateRunning || first.Total != 42 {
		t.Fatalf("first start = %+v, %v, %v", first, started, err)
	}
	again, started, err := m.Start("search", KindRebuild, Options{Fresh: true})
	if err != nil || started || again.ID != first.ID {
		t.Fatalf("repeat start = %+v, started=%v, err=%v; want the running job", again, started, err)
	}
	if _, _, err := m.Start("search", KindRebuild, Options{}); !errors.Is(err, ErrBusy) {
		t.Errorf("rebuild with other options err = %v, want ErrBusy", err)
	}
	if _, _, err := m.Start("search", KindDrop, Options{}); !errors.Is(err, ErrBusy) {
		t.Errorf("drop during rebuild err = %v, want ErrBusy", err)
	}
	if m.Running() != 1 {
		t.Errorf("running = %d, want 1", m.Running())
	}

	close(ix.release)
	j := waitState(t, m, first.ID, StateSucceeded)
	if j.Scanned != 7 || j.Indexed != 3 || j.Total != 7 || j.FinishedAt == 0 {
		t.Errorf("finished job = %+v", j)
	}
	if got := <-finished; got.ID != first.ID {
		t.Errorf("OnFinish got %s", got.ID)
	}
	if info := m.Indexes(); len(info) != 1 || info[0].Running != nil || info[0].Last == nil || info[0].Last.ID != first.ID {
		t.Errorf("indexes = %+v", info)
	}
}

func TestCancel(t *testing.T) {
	ix := &fakeIndex{name: "zaps", release: make(chan struct{})}
	m := New("", zerolog.Nop(), ix)
	j, _, err := m.Start("zaps", KindRebuild, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := m.Cancel(ctx, j.ID)
	if err != nil || got.State != StateCancelled {
		t.Fatalf("cancel = %+v, %v", got, err)
	}
	// Cancelling a finished job is a no-op that reports it.
	if again, err := m.Cancel(ctx, j.ID); err != nil || again.State != StateCancelled {
		t.Errorf("second cancel = %+v, %v", again, err)
	}
	if _, err := m.Cancel(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown cancel err = %v", err)
	}
}

func TestFailureAndUnavailable(t *testing.T) {
	broken := &fakeIndex{name: "search", release: make(chan struct{}), err: errors.New("meilisearch down")}
	off := &fakeIndex{name: "zaps", why: "zaps are off"}
	m := New("", zerolog.Nop(), broken, off)

	j, _, _ := m.Start("search", KindRebuild, Options{})
	close(broken.release)
	if got := waitState(t, m, j.ID, StateFailed); got.Error != "meilisearch down" {
		t.Errorf("failed job error = %q", got.Error)
	}
	if _, _, err := m.Start("zaps", KindRebuild, Options{}); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "zaps are off") {
		t.Errorf("unavailable err = %v", err)
	}
	if _, _, err := m.Start("nope", KindRebuild, Options{}); !errors.Is(err, ErrUnknownIndex) {
		t.Errorf("unknown index err = %v", err)
	}
}

func TestHistoryPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.db.jobs.json")
	ix := &fakeIndex{name: "zaps", release: make(chan struct{})}
	close(ix.release)
	m := New(path, zerolog.Nop(), ix)
	var last Job
	for i := 0; i < keptJobs+3; i++ {
		j, _, err := m.Start("zaps", KindDrop, Options{})
		if err != nil {
			t.Fatal(err)
		}
		last = waitState(t, m, j.ID, StateSucceeded)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("history file: %v %v", fi, err)
	}

	reopened := New(path, zerolog.Nop(), ix)
	list := reopened.List()
	if len(list) != keptJobs || list[0].ID != last.ID {
		t.Fatalf("reloaded %d jobs; want %d, newest %s", len(list), keptJobs, last.ID)
	}

	// A job recorded as running died with the old process.
	if err := os.WriteFile(path, []byte(`[{"id":"x","index":"zaps","kind":"rebuild","state":"running","started_at":1}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := New(path, zerolog.Nop(), ix).Get("x")
	if err != nil || j.State != StateFailed || j.Error == "" {
		t.Errorf("stale running job = %+v, %v", j, err)
	}
}

func TestZapIndexRebuild(t *testing.T) {
	store := newStore(t, 500)
	_, receiver := testKey(t, 1)
	insert(t, store,
		zaptest.Receipt(t, zaptest.NewKey(t, 2), zaptest.NewKey(t, 4), receiver, 21000, 100),
		signed(t, 3, 1, 101),
	)
	m := New("", zerolog.Nop(), &ZapIndex{Store: store})
	j, _, err := m.Start("zaps", KindRebuild, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := waitState(t, m, j.ID, StateSucceeded)
	if got.Scanned != 2 || got.Indexed != 1 {
		t.Errorf("zap rebuild = %+v, want scanned 2 indexed 1", got)
	}
	top, err := store.TopZapped(context.Background(), 0, 1000, relay.CoinSats, 10)
	if err != nil || len(top) != 1 || top[0].Pubkey != receiver || top[0].Total != 21000 {
		t.Errorf("top zapped = %+v, %v", top, err)
	}
}

func TestZapIndexEstimateCountsZapReceipts(t *testing.T) {
	store := newStore(t, 500)
	insert(t, store, signed(t, 2, 1, 100), signed(t, 3, 1, 101), signed(t, 4, 0, 102))
	if got := (&ZapIndex{Store: store}).Estimate(); got != 0 {
		t.Errorf("estimate with no zap receipts = %d, want 0", got)
	}

	_, receiver := testKey(t, 1)
	insert(t, store,
		signed(t, 5, 9735, 103, []string{"p", receiver}, []string{"amount", "21000"}),
		signed(t, 6, 9735, 104, []string{"p", receiver}, []string{"amount", "1000"}),
	)
	ix := &ZapIndex{Store: store}
	if got := ix.Estimate(); got != 2 {
		t.Errorf("estimate = %d, want the 2 zap receipts", got)
	}
	if got := ix.ScanEstimate(); got != 5 {
		t.Errorf("scan estimate = %d, want all 5 stored events", got)
	}
}

func TestZapIndexUnavailableWhenDisabled(t *testing.T) {
	m := New("", zerolog.Nop(), &ZapIndex{Store: newStore(t, 500), Disabled: "top-zapped is off"})
	if _, _, err := m.Start("zaps", KindRebuild, Options{}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("rebuild of a disabled zap index = %v, want ErrUnavailable", err)
	}
	info := m.Indexes()[0]
	if info.Available || info.Unavailable != "top-zapped is off" || info.Estimate != 0 {
		t.Errorf("disabled zap index = %+v", info)
	}
}
