package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ohstr/ncli/cli/common/meilisearch"
	nmconfig "github.com/ohstr/nmilat/config"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/search"
	bolt "go.etcd.io/bbolt"
)

// searchState is ncli's own bucket in the store: which Meilisearch index
// this relay last built, and with what scoring.
var (
	searchState    = []byte("ncli/search")
	keyIdentity    = []byte("identity")
	keyFingerprint = []byte("fingerprint")
	keySettings    = []byte("settings")
)

// searchBacklog is how far behind the live indexer may fall before the
// relay shows its catching up as a job.
var searchBacklog int64 = 1000

// SearchIndex is the Meilisearch profile index. While search is on, a
// relay.SearchIndexer keeps it in step with the store; a rebuild builds a
// new index beside it and swaps it in.
type SearchIndex struct {
	Store     *relay.EventStore
	Indexer   *relay.SearchIndexer
	Host      string
	Key       string
	IndexName string
	// Client, when set, is the client the indexer writes with; indexes the
	// rebuild makes share its options and last task.
	Client *meilisearch.MeiliClient
	// BatchSize is the profiles a rebuild sends per request
	// (relay.DefaultSearchBatch when 0), and MaxInFlight how many requests
	// it keeps enqueued before waiting for the oldest
	// (relay.DefaultSearchInFlight when 0).
	BatchSize   int
	MaxInFlight int
	// Disabled explains why search is off or read-only ("" when on).
	Disabled string

	mu      sync.Mutex
	checked time.Time
	cached  searchCheck
	queue   queueWatch
}

type searchCheck struct {
	docs     int64
	identity string
	err      error
}

func (s *SearchIndex) Name() string      { return "search" }
func (s *SearchIndex) WritesStore() bool { return false }

func (s *SearchIndex) client(name string) *meilisearch.MeiliClient {
	if s.Client != nil {
		return s.Client.ForIndex(name)
	}
	return meilisearch.NewMeiliClient(s.Host, s.Key, name)
}

func (s *SearchIndex) batching() (batch, inFlight int) {
	batch, inFlight = s.BatchSize, s.MaxInFlight
	if batch <= 0 {
		batch = relay.DefaultSearchBatch
	}
	if inFlight <= 0 {
		inFlight = relay.DefaultSearchInFlight
	}
	return batch, inFlight
}

// check asks Meilisearch for the live index's size and identity, at most
// every two seconds.
func (s *SearchIndex) check(fresh bool) searchCheck {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !fresh && time.Since(s.checked) < 2*time.Second {
		return s.cached
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c := s.client(s.IndexName)
	var out searchCheck
	out.identity, out.err = c.Identity(ctx)
	if out.err == nil && out.identity != "" {
		out.docs, out.err = c.CountDocuments(ctx)
	}
	s.cached, s.checked = out, time.Now()
	return out
}

func (s *SearchIndex) stored(key []byte) string {
	var v string
	_ = s.Store.Db().View(func(tx *bolt.Tx) error {
		if b := tx.Bucket(searchState); b != nil {
			v = string(b.Get(key))
		}
		return nil
	})
	return v
}

func (s *SearchIndex) remember(identity string) error {
	return s.Store.Db().Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(searchState)
		if err != nil {
			return err
		}
		if err := b.Put(keyIdentity, []byte(identity)); err != nil {
			return err
		}
		if err := b.Put(keySettings, []byte(s.client(s.IndexName).SettingsFingerprint())); err != nil {
			return err
		}
		return b.Put(keyFingerprint, []byte(scoringFingerprint()))
	})
}

// scoringFingerprint identifies the weights a profile's search score uses.
func scoringFingerprint() string {
	c := nmconfig.Get().Search
	raw, _ := json.Marshal([]any{c.Scoring, c.Verification, c.Social})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

func (s *SearchIndex) Status() Status {
	out := Status{Name: s.Name(), Enabled: s.Disabled == "", Unavailable: s.Disabled, Head: int64(s.Store.Head())}
	if !out.Enabled {
		out.State = StateDisabled
		if fs, err := s.Store.FeedStatus(relay.SearchFeed.Name); err == nil {
			out.HasData = true
			out.Queued = fs.Queued
			out.Hint = "the relay still tracks profiles for search; stop with ncli relay index drop search"
		}
		return out
	}
	st := s.Indexer.Status()
	out.Behind, out.Queued = int64(st.Behind), st.Queued
	s.mu.Lock()
	s.queue.add(queueSample{at: time.Now(), queued: st.Queued, applied: st.Upserted + st.Deleted})
	out.Warning = s.queue.warning()
	s.mu.Unlock()
	if s.Client != nil {
		out.LastTask = s.Client.LastTask()
	}
	chk := s.check(false)
	out.Entries, out.HasData = chk.docs, chk.docs > 0
	switch {
	case chk.err != nil:
		out.State, out.Error = StateUnreachable, chk.err.Error()
		out.Reason = "Meilisearch can't be reached; profiles wait in the store until it is"
		return out
	case st.LastError != "":
		out.State, out.Error = StateUnreachable, st.LastError
		out.Reason = "the last write to Meilisearch failed; it is retried"
		return out
	}
	known := s.stored(keyIdentity)
	switch {
	case chk.identity == "":
		out.State, out.Action, out.Reason = StateNeedsRebuild, KindRebuild, "the Meilisearch index doesn't exist"
	case known == "":
		out.State, out.Action, out.Reason = StateNeedsRebuild, KindRebuild, "not built by this relay yet"
	case known != chk.identity:
		out.State, out.Action, out.Reason = StateNeedsRebuild, KindRebuild, "the Meilisearch index was replaced"
	case s.stored(keySettings) != s.client(s.IndexName).SettingsFingerprint():
		out.State, out.Action, out.Reason = StateNeedsRebuild, KindRebuild, "the index settings changed"
	case s.stored(keyFingerprint) != scoringFingerprint():
		out.State, out.Action, out.Reason = StateNeedsRebuild, KindRebuild, "the scoring settings changed"
	case out.Behind >= searchBacklog || out.Queued >= searchBacklog:
		out.State, out.Action = StateBehind, KindCatchUp
		out.Reason = fmt.Sprintf("%s events (any kind) to look through and %s profiles to index", Count(out.Behind), Count(out.Queued))
	default:
		out.State = StateReady
	}
	return out
}

func (s *SearchIndex) Run(ctx context.Context, kind Kind, progress func(Progress)) (Result, error) {
	switch kind {
	case KindRebuild:
		return s.rebuild(ctx, progress)
	case KindCatchUp:
		return s.follow(ctx, progress)
	}
	return Result{}, ErrNothingToDo
}

// follow reports the live indexer's progress until it has caught up with
// what was stored when it started.
func (s *SearchIndex) follow(ctx context.Context, progress func(Progress)) (Result, error) {
	start := s.Indexer.Status()
	target := s.Store.Head()
	res := Result{EntriesBefore: s.check(true).docs}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		st := s.Indexer.Status()
		phase := "index"
		if st.LastError != "" {
			phase = "waiting for Meilisearch: " + st.LastError
		}
		if progress != nil {
			progress(Progress{
				Phase: phase, Done: int64(st.Watermark - min(st.Watermark, start.Watermark)),
				Total:   int64(target - min(target, start.Watermark)),
				Applied: st.Upserted - start.Upserted + st.Deleted - start.Deleted,
			})
		}
		if st.Watermark >= target && st.Queued < searchBacklog {
			res.Applied = st.Upserted - start.Upserted + st.Deleted - start.Deleted
			res.EntriesAfter = s.check(true).docs
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, context.Cause(ctx)
		case <-tick.C:
		}
	}
}

// rebuild builds every stored profile into "<index>__next" with live
// indexing paused, swaps it in, and has the indexer resume from where the
// build began, so nothing stored meanwhile is missed.
func (s *SearchIndex) rebuild(ctx context.Context, progress func(Progress)) (Result, error) {
	res := Result{EntriesBefore: s.check(true).docs}
	err := s.Indexer.Exclusive(func() error {
		from := s.Store.Head()
		next := s.client(s.IndexName + "__next")
		if err := next.RemoveIndex(ctx); err != nil {
			return err
		}
		if err := next.Initialize(ctx); err != nil {
			return err
		}
		n, err := s.buildInto(ctx, next, progress)
		if err != nil {
			_ = next.RemoveIndex(context.Background())
			return err
		}
		res.Applied = n
		live := s.client(s.IndexName)
		if err := live.Initialize(ctx); err != nil {
			return err
		}
		if err := live.SwapWith(ctx, next.IndexName()); err != nil {
			return err
		}
		if err := next.RemoveIndex(ctx); err != nil {
			return err
		}
		if err := s.Store.SetFeedWatermark(relay.SearchFeed.Name, from); err != nil {
			return err
		}
		identity, err := live.Identity(ctx)
		if err != nil {
			return err
		}
		return s.remember(identity)
	})
	if err != nil {
		return res, err
	}
	res.EntriesAfter = s.check(true).docs
	return res, nil
}

// buildInto writes every stored profile to target, keeping up to
// MaxInFlight requests of BatchSize profiles enqueued so Meilisearch merges
// them, and reports progress as profiles applied out of the stored kind 0
// count.
func (s *SearchIndex) buildInto(ctx context.Context, target search.Enqueuer, progress func(Progress)) (int64, error) {
	size, inFlight := s.batching()
	total := int64(s.Store.Counts().Kind(0))
	type sent struct {
		pending    search.Pending
		read, docs int64
	}
	var queue []sent
	var batch []*search.ProfileDocument
	var read, done, applied int64
	report := func(phase string) {
		if progress != nil {
			progress(Progress{Phase: phase, Done: done, Total: max(total, done), Applied: applied})
		}
	}
	settle := func() error {
		oldest := queue[0]
		if err := oldest.pending.Wait(ctx); err != nil {
			return err
		}
		queue = queue[1:]
		done, applied = done+oldest.read, applied+oldest.docs
		report("build")
		return nil
	}
	send := func() error {
		if read == 0 {
			return nil
		}
		for len(queue) >= inFlight {
			if err := settle(); err != nil {
				return err
			}
		}
		p, err := target.EnqueueUpsert(ctx, batch)
		if err != nil {
			return err
		}
		queue = append(queue, sent{pending: p, read: read, docs: int64(len(batch))})
		batch, read = make([]*search.ProfileDocument, 0, size), 0
		return nil
	}
	report("build")
	err := s.Store.EachProfile(ctx, func(ev *nip01.Event) error {
		read++
		if doc := relay.SearchDocument(s.Store, ev); doc != nil {
			batch = append(batch, doc)
		}
		if len(batch) >= size {
			return send()
		}
		return nil
	})
	if err == nil {
		err = send()
	}
	for err == nil && len(queue) > 0 {
		err = settle()
	}
	report("swap")
	return applied, err
}

// Drop forgets search on a relay where it is off: the profiles queued for
// it, and the Meilisearch index when one is configured.
func (s *SearchIndex) Drop(ctx context.Context, _ func(Progress)) (Result, error) {
	var res Result
	if s.Host != "" {
		c := s.client(s.IndexName)
		if n, err := c.CountDocuments(ctx); err == nil {
			res.EntriesBefore = n
		}
		if err := c.RemoveIndex(ctx); err != nil {
			return res, fmt.Errorf("remove the Meilisearch index: %w", err)
		}
	}
	err := s.Store.DropFeed(relay.SearchFeed.Name)
	if errors.Is(err, relay.ErrUnknownFeed) {
		if s.Host == "" {
			return res, ErrNothingToDo
		}
		err = nil
	}
	if err == nil {
		err = s.Store.Db().Update(func(tx *bolt.Tx) error {
			if tx.Bucket(searchState) == nil {
				return nil
			}
			return tx.DeleteBucket(searchState)
		})
	}
	return res, err
}

func (s *SearchIndex) Cancelled(Kind) {}
