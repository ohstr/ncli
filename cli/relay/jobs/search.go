package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
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
	eventsBucket   = []byte{1}
)

// searchBacklog is how far behind the live indexer may fall before the
// relay shows its catching up as a job.
var searchBacklog int64 = 1000

// searchBatch is how many profiles a rebuild sends per request.
const searchBatch = 500

// SearchIndex is the Meilisearch profile index. While search is on, a
// relay.SearchIndexer keeps it in step with the store; a rebuild builds a
// new index beside it and swaps it in.
type SearchIndex struct {
	Store     *relay.EventStore
	Indexer   *relay.SearchIndexer
	Host      string
	Key       string
	IndexName string
	// Disabled explains why search is off or read-only ("" when on).
	Disabled string

	mu      sync.Mutex
	checked time.Time
	cached  searchCheck
}

type searchCheck struct {
	docs     int64
	identity string
	err      error
}

func (s *SearchIndex) Name() string      { return "search" }
func (s *SearchIndex) WritesStore() bool { return false }

func (s *SearchIndex) client(name string) *meilisearch.MeiliClient {
	return meilisearch.NewMeiliClient(s.Host, s.Key, name)
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
	case s.stored(keyFingerprint) != scoringFingerprint():
		out.State, out.Action, out.Reason = StateNeedsRebuild, KindRebuild, "the scoring settings changed"
	case out.Behind >= searchBacklog || out.Queued >= searchBacklog:
		out.State, out.Action = StateBehind, KindCatchUp
		out.Reason = fmt.Sprintf("%s events and %s profiles to index", Count(out.Behind), Count(out.Queued))
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
		n, err := s.buildInto(ctx, next, from, progress)
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

// buildInto writes every stored profile to target, reporting progress over
// the events up to head.
func (s *SearchIndex) buildInto(ctx context.Context, target search.Writer, head uint64, progress func(Progress)) (int64, error) {
	var sent int64
	var batch []*search.ProfileDocument
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := target.Upsert(ctx, batch); err != nil {
			return err
		}
		sent += int64(len(batch))
		batch = batch[:0]
		return nil
	}
	err := scanProfiles(ctx, s.Store, func(ev *nip01.Event, pos uint64) error {
		if doc := relay.SearchDocument(s.Store, ev); doc != nil {
			batch = append(batch, doc)
		}
		if len(batch) >= searchBatch {
			if err := flush(); err != nil {
				return err
			}
			if progress != nil {
				progress(Progress{Phase: "build", Done: int64(pos), Total: int64(max(head, pos)), Applied: sent})
			}
		}
		return nil
	})
	if err == nil {
		err = flush()
	}
	if progress != nil {
		progress(Progress{Phase: "swap", Done: int64(head), Total: int64(head), Applied: sent})
	}
	return sent, err
}

// scanProfiles walks every stored event in read transactions of a thousand,
// calling fn outside them for each profile.
func scanProfiles(ctx context.Context, store *relay.EventStore, fn func(*nip01.Event, uint64) error) error {
	const batch = 1000
	var after uint64
	for {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		var found []*nip01.Event
		var at []uint64
		n := 0
		err := store.Db().View(func(tx *bolt.Tx) error {
			b := tx.Bucket(eventsBucket)
			if b == nil {
				return nil
			}
			c := b.Cursor()
			for k, v := c.Seek(binary.BigEndian.AppendUint64(nil, after+1)); k != nil && n < batch; k, v = c.Next() {
				n++
				after = binary.BigEndian.Uint64(k)
				var head struct {
					Kind int `json:"kind"`
				}
				if json.Unmarshal(v, &head) != nil || head.Kind != 0 {
					continue
				}
				var ev nip01.Event
				if json.Unmarshal(v, &ev) == nil {
					found = append(found, &ev)
					at = append(at, after)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		for i, ev := range found {
			if err := fn(ev, at[i]); err != nil {
				return err
			}
		}
		if n < batch {
			return nil
		}
	}
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
