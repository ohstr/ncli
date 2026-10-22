package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/ohstr/ncli/cli/common/meilisearch"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/search"
	bolt "go.etcd.io/bbolt"
)

// eventsBucket is nmilat's events bucket; its sequence is the highest event
// id assigned, an O(1) upper bound on stored events.
var eventsBucket = []byte{1}

func eventSequence(store *relay.EventStore) int64 {
	var seq uint64
	_ = store.Db().View(func(tx *bolt.Tx) error {
		if b := tx.Bucket(eventsBucket); b != nil {
			seq = b.Sequence()
		}
		return nil
	})
	return int64(seq)
}

// cachedCount keeps a count that costs a walk or a request for a minute,
// failures included, so the dashboard's polling doesn't redo it.
type cachedCount struct {
	mu sync.Mutex
	n  int64
	at time.Time
}

// reset makes the next get count again.
func (c *cachedCount) reset() {
	c.mu.Lock()
	c.at = time.Time{}
	c.mu.Unlock()
}

func (c *cachedCount) get(count func() int64) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at.IsZero() || time.Since(c.at) >= time.Minute {
		c.n, c.at = count(), time.Now()
	}
	return c.n
}

// storeIndexState maps a derived index's state in the store.
func storeIndexState(st relay.IndexState) (string, bool) {
	switch {
	case !st.Enabled:
		return IndexDisabled, st.HasData
	case st.Rebuilding:
		return IndexBuilding, st.HasData
	case !st.Current:
		return IndexStale, st.HasData
	}
	return IndexReady, st.HasData
}

// storeIndexAvailable allows a drop whatever the feature, to remove what a
// disabled index still holds; a rebuild needs the feature.
func storeIndexAvailable(kind Kind, disabled string) string {
	if kind == KindDrop {
		return ""
	}
	return disabled
}

// dropErr reports a drop refused during a rebuild as busy.
func dropErr(err error) error {
	if errors.Is(err, relay.ErrZapRebuildInProgress) || errors.Is(err, relay.ErrSocialRebuildInProgress) {
		return ErrBusy
	}
	return err
}

// ZapIndex is the zap ranking index, kept in the event store only while
// zap ranking is on.
type ZapIndex struct {
	Store *relay.EventStore
	// Disabled explains why the zap index can't be maintained ("" when it can).
	Disabled string
}

func (z *ZapIndex) Name() string               { return "zaps" }
func (z *ZapIndex) Available(kind Kind) string { return storeIndexAvailable(kind, z.Disabled) }
func (z *ZapIndex) State() (string, bool)      { return storeIndexState(z.Store.ZapIndexState()) }

// Estimate counts stored zap receipts (NIP-57 and AltZap), or what a
// disabled index holds.
func (z *ZapIndex) Estimate() int64 {
	if z.Disabled != "" {
		return z.Store.ZapIndexState().Entries
	}
	c := z.Store.Counts()
	return int64(c.Kind(9735) + c.Kind(9521))
}

// ScanEstimate is every stored event: a rebuild reads them all.
func (z *ZapIndex) ScanEstimate() int64 { return eventSequence(z.Store) }

// Rebuild always builds a new index beside the live one and swaps it in;
// Options.Fresh changes nothing here.
func (z *ZapIndex) Rebuild(ctx context.Context, _ Options, progress Progress) error {
	_, err := z.Store.RebuildZapIndex(ctx, progress)
	if errors.Is(err, relay.ErrZapRebuildInProgress) {
		return ErrBusy
	}
	return err
}

func (z *ZapIndex) Drop(ctx context.Context) error { return dropErr(z.Store.ClearZapIndex(ctx)) }

// StatsIndex is each profile's zaps and follows (nmilat's social index),
// behind social scores and NIP-85 assertions. The store keeps it current
// while search or stats is on.
type StatsIndex struct {
	Store *relay.EventStore
	// Disabled explains why the index isn't in use ("" when it is).
	Disabled string

	count cachedCount
}

func (x *StatsIndex) Name() string               { return "stats" }
func (x *StatsIndex) Available(kind Kind) string { return storeIndexAvailable(kind, x.Disabled) }
func (x *StatsIndex) State() (string, bool)      { return storeIndexState(x.Store.SocialIndexState()) }

// Estimate is the pubkeys at least one stored contact list follows.
func (x *StatsIndex) Estimate() int64 {
	return x.count.get(x.Store.CountSocialProfiles)
}

// ScanEstimate is every stored event: a rebuild reads them all.
func (x *StatsIndex) ScanEstimate() int64 { return eventSequence(x.Store) }

// Rebuild builds a new index beside the live one and swaps it in.
func (x *StatsIndex) Rebuild(ctx context.Context, _ Options, progress Progress) error {
	_, err := x.Store.RebuildSocialIndex(ctx, progress)
	if errors.Is(err, relay.ErrSocialRebuildInProgress) {
		return ErrBusy
	}
	return err
}

func (x *StatsIndex) Drop(ctx context.Context) error {
	err := dropErr(x.Store.ClearSocialIndex(ctx))
	x.count.reset()
	return err
}

// SearchIndex is the Meilisearch profile index.
type SearchIndex struct {
	Store     *relay.EventStore
	Host      string
	Key       string
	IndexName string
	// Disabled explains why search can't be maintained ("" when it can).
	Disabled string

	count cachedCount
}

// searchBatch is how many profiles go to Meilisearch per request.
const searchBatch = 500

func (s *SearchIndex) Name() string          { return "search" }
func (s *SearchIndex) Available(Kind) string { return s.Disabled }

// State is disabled when search is off or read-only, else ready: the
// relay can't tell what a Meilisearch index it doesn't write holds.
func (s *SearchIndex) State() (string, bool) {
	if s.Disabled != "" {
		return IndexDisabled, false
	}
	return IndexReady, true
}

// Estimate is the documents in the live Meilisearch index, or -1 when it
// can't be reached.
func (s *SearchIndex) Estimate() int64 {
	if s.Disabled != "" {
		return 0
	}
	return s.count.get(func() int64 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		n, err := meilisearch.NewMeiliClient(s.Host, s.Key, s.IndexName).CountDocuments(ctx)
		if err != nil {
			return -1
		}
		return n
	})
}

// ScanEstimate is the stored profiles a rebuild reads.
func (s *SearchIndex) ScanEstimate() int64 { return int64(s.Store.Counts().Kind(0)) }

func profilesPage(since, until uint64) *nip01.SubscriptionFilterGroup {
	f := nip01.NewSubscriptionFilterGroup()
	filter := nip01.SubscriptionFilter{Kinds: []int{0}, Since: since, Until: until}
	f.Add(&filter)
	return f
}

// eachProfile calls fn once for every stored profile created at or after
// since. A store query returns at most the relay's max_limit events, so it
// pages backwards by created_at. When more profiles share one second than a
// page holds, paging can't get past them, so it falls back to scanning the
// stored events directly for whatever remains.
func eachProfile(ctx context.Context, store *relay.EventStore, since uint64, fn func(*nip01.Event)) (int, error) {
	seen := map[string]bool{}
	visit := func(ev *nip01.Event) {
		if !seen[ev.ID] {
			seen[ev.ID] = true
			fn(ev)
		}
	}
	var until uint64
	for {
		if err := ctx.Err(); err != nil {
			return len(seen), err
		}
		page, err := fetchProfiles(ctx, store, since, until)
		if err != nil {
			return len(seen), err
		}
		if len(page) == 0 {
			return len(seen), nil
		}
		oldest, newest := page[0].CreatedAt, page[0].CreatedAt
		before := len(seen)
		for _, ev := range page {
			oldest = min(oldest, ev.CreatedAt)
			newest = max(newest, ev.CreatedAt)
			visit(ev)
		}
		switch {
		case len(seen) > before && oldest < newest:
			// The oldest second may continue past this page.
			until = oldest
		case len(seen) > before:
			// A page from a single second: there may be more of it than a
			// page can ever return.
			err := scanProfiles(ctx, store, since, oldest, visit)
			return len(seen), err
		default:
			// Nothing new: the oldest second is done.
			if oldest <= since || oldest == 0 {
				return len(seen), nil
			}
			until = oldest - 1
		}
	}
}

// scanProfiles walks every stored event in batched read transactions,
// calling fn for each profile created in [since, until].
func scanProfiles(ctx context.Context, store *relay.EventStore, since, until uint64, fn func(*nip01.Event)) error {
	const batch = 1000
	var last []byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var found []*nip01.Event
		n := 0
		err := store.Db().View(func(tx *bolt.Tx) error {
			b := tx.Bucket(eventsBucket)
			if b == nil {
				return nil
			}
			c := b.Cursor()
			k, v := c.First()
			if last != nil {
				k, v = c.Seek(last)
				if k != nil && bytes.Equal(k, last) {
					k, v = c.Next()
				}
			}
			for ; k != nil && n < batch; k, v = c.Next() {
				n++
				last = append(last[:0], k...)
				var head struct {
					Kind      int    `json:"kind"`
					CreatedAt uint64 `json:"created_at"`
				}
				if json.Unmarshal(v, &head) != nil || head.Kind != 0 || head.CreatedAt < since || head.CreatedAt > until {
					continue
				}
				var ev nip01.Event
				if json.Unmarshal(v, &ev) == nil {
					found = append(found, &ev)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, ev := range found {
			fn(ev)
		}
		if n < batch {
			return nil
		}
	}
}

func fetchProfiles(ctx context.Context, store *relay.EventStore, since, until uint64) ([]*nip01.Event, error) {
	q, err := relay.NewStoreQuery(store, profilesPage(since, until))
	if err != nil {
		return nil, err
	}
	found := make(chan *relay.PotentialEvent)
	var wg sync.WaitGroup
	var fetchErr error
	go func() {
		fetchErr = q.Fetch(ctx, found, &wg, false)
		wg.Wait()
		close(found)
	}()
	var page []*nip01.Event
	for pe := range found {
		if ev, err := store.FindEvent(pe.Evsid); err == nil {
			page = append(page, ev)
		}
		wg.Done()
	}
	// found is closed only after fetchErr is set.
	if fetchErr != nil {
		return nil, fetchErr
	}
	return page, nil
}

func (s *SearchIndex) client(name string) (*meilisearch.MeiliClient, error) {
	c := meilisearch.NewMeiliClient(s.Host, s.Key, name)
	if err := c.Initialize(context.Background()); err != nil {
		return nil, err
	}
	return c, nil
}

// Rebuild indexes every stored profile. Fresh builds "<index>__next" and
// swaps it in, then re-indexes profiles stored during the build, which the
// relay wrote to the index that was swapped out.
func (s *SearchIndex) Rebuild(ctx context.Context, opts Options, progress Progress) error {
	started := uint64(time.Now().Unix())
	if !opts.Fresh {
		live, err := s.client(s.IndexName)
		if err != nil {
			return err
		}
		_, err = s.indexProfiles(ctx, live, 0, progress)
		return err
	}

	next := meilisearch.NewMeiliClient(s.Host, s.Key, s.IndexName+"__next")
	if err := next.RemoveIndex(ctx); err != nil {
		return err
	}
	next, err := s.client(s.IndexName + "__next")
	if err != nil {
		return err
	}
	if _, err := s.indexProfiles(ctx, next, 0, progress); err != nil {
		return err
	}
	live, err := s.client(s.IndexName)
	if err != nil {
		return err
	}
	if err := live.SwapWith(ctx, next.IndexName()); err != nil {
		return err
	}
	if err := next.RemoveIndex(ctx); err != nil {
		return err
	}
	// Catch-up: profiles stored after the build began.
	_, err = s.indexProfiles(ctx, live, started, nil)
	return err
}

func (s *SearchIndex) indexProfiles(ctx context.Context, target *meilisearch.MeiliClient, since uint64, progress Progress) (int, error) {
	indexed := 0
	var batch []*search.ProfileDocument
	var writeErr error
	flush := func() {
		if len(batch) == 0 || writeErr != nil {
			return
		}
		if err := target.BulkIndex(batch); err != nil {
			writeErr = err
			return
		}
		indexed += len(batch)
		batch = batch[:0]
	}

	seen := 0
	scanned, err := eachProfile(ctx, s.Store, since, func(ev *nip01.Event) {
		seen++
		if writeErr != nil {
			return
		}
		doc := search.FromEvent(ev)
		if doc == nil {
			return
		}
		doc.Score += s.storedScore(ev.PubKey)
		batch = append(batch, doc)
		if len(batch) >= searchBatch {
			flush()
			if progress != nil {
				progress(seen, indexed)
			}
		}
	})
	flush()
	if progress != nil {
		progress(scanned, indexed)
	}
	if writeErr != nil {
		return indexed, writeErr
	}
	return indexed, err
}

// storedScore is what a profile earns beyond its kind 0's base score, from
// what the relay already knows (stored check results, zaps and follows),
// weighed with the current scoring config. Nothing is fetched.
func (s *SearchIndex) storedScore(pubkey string) int64 {
	var score int64
	if m, err := s.Store.GetProfileMetrics(pubkey); err == nil && m != nil {
		score += m.CheckScore()
	}
	if social, err := s.Store.ComputeSocialScore(pubkey); err == nil {
		score += social
	}
	return score
}

// Drop deletes every document from the live index.
func (s *SearchIndex) Drop(ctx context.Context) error {
	live, err := s.client(s.IndexName)
	if err != nil {
		return err
	}
	return live.DeleteIndex(ctx)
}
