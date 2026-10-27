package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ohstr/nmilat/nip01"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/ohstr/nmilat/wire"
)

// ErrFollowLocal is --follow given a local .db target, which has no live
// feed.
var ErrFollowLocal = errors.New("--follow needs relay targets, not a local .db file")

// maxSeen bounds the ids a FollowState remembers.
const maxSeen = 5000

// FollowState is what a follow has delivered, so a restart resumes where it
// stopped: recently seen ids and, per relay, the newest created_at seen.
// Delivery across a crash is at least once: an id seen in the last second
// may come again; nothing is skipped.
type FollowState struct {
	path string
	mu   sync.Mutex

	seen  map[string]bool
	order []string
	since map[string]int64
	dirty bool
}

type followStateFile struct {
	Seen  []string         `json:"seen"`
	Since map[string]int64 `json:"since"`
}

// LoadFollowState reads path; a missing file is an empty state. path ""
// keeps the state in memory only.
func LoadFollowState(path string) (*FollowState, error) {
	s := &FollowState{path: path, seen: map[string]bool{}, since: map[string]int64{}}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f followStateFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("state file %s: %w", path, err)
	}
	for _, id := range f.Seen {
		s.add(id)
	}
	for k, v := range f.Since {
		s.since[k] = v
	}
	return s, nil
}

func (s *FollowState) add(id string) {
	if s.seen[id] {
		return
	}
	s.seen[id] = true
	s.order = append(s.order, id)
	if len(s.order) > maxSeen {
		delete(s.seen, s.order[0])
		s.order = s.order[1:]
	}
}

// Seen reports whether id was delivered.
func (s *FollowState) Seen(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[id]
}

// Mark records id as delivered from relay. It reports false if id was
// already delivered.
func (s *FollowState) Mark(id, relay string, createdAt int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[id] {
		return false
	}
	s.add(id)
	if createdAt > s.since[relay] {
		s.since[relay] = createdAt
	}
	s.dirty = true
	return true
}

// Since is the newest created_at delivered from relay, 0 if none.
func (s *FollowState) Since(relay string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.since[relay]
}

// Save writes the state atomically if it changed.
func (s *FollowState) Save() error {
	s.mu.Lock()
	if s.path == "" || !s.dirty {
		s.mu.Unlock()
		return nil
	}
	f := followStateFile{Seen: append([]string(nil), s.order...), Since: map[string]int64{}}
	for k, v := range s.since {
		f.Since[k] = v
	}
	s.dirty = false
	s.mu.Unlock()

	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".ncli-state-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// FollowOptions configures Follow.
type FollowOptions struct {
	// AuthSigner logs in (NIP-42) when a relay asks; nil stays anonymous.
	AuthSigner Signer
	// State dedupes across relays and restarts; nil keeps it in memory.
	State *FollowState
	// Overlap moves a resumed subscription's since this far before the
	// newest event seen, for events published late. Default 10 minutes.
	Overlap time.Duration
	// DialTimeout bounds one connection attempt. Default 30s.
	DialTimeout time.Duration
	// Emit receives each new event once, serialized. An error stops Follow.
	Emit func(ev *nip01.Event, relay string) error
	// Warn receives a relay's recoverable failure (it is retried).
	Warn func(relay string, err error)
}

// followBackoff bounds the wait between reconnects.
var followBackoff = [2]time.Duration{time.Second, 30 * time.Second}

// Follow subscribes to filters on every relay target and emits each new
// event once, until ctx is done (then it returns nil). A relay that drops
// is redialed with backoff. If no relay can be reached at the start it
// returns ErrNoReachableTargets; a refused login is returned as is.
func Follow(ctx context.Context, targets *TargetsSpec, filters *nip01.SubscriptionFilterGroup, opts FollowOptions) error {
	if opts.State == nil {
		opts.State, _ = LoadFollowState("")
	}
	if opts.Overlap == 0 {
		opts.Overlap = 10 * time.Minute
	}
	if opts.DialTimeout == 0 {
		opts.DialTimeout = 30 * time.Second
	}
	if opts.Warn == nil {
		opts.Warn = func(string, error) {}
	}
	var remotes []*FlowSpec
	for _, t := range targets.Relays {
		if t.Type != FlOW_REMOTE {
			return ErrFollowLocal
		}
		remotes = append(remotes, t)
	}
	if len(remotes) == 0 {
		return ErrNoReachableTargets
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	f := &follower{opts: opts, filters: filters}

	// Save the state at most once a second, and on the way out.
	saveDone := make(chan struct{})
	go func() {
		defer close(saveDone)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				if err := opts.State.Save(); err != nil {
					opts.Warn("", fmt.Errorf("saving state: %w", err))
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	connected := make(chan bool, len(remotes))
	errs := make(chan error, len(remotes))
	var wg sync.WaitGroup
	for _, t := range remotes {
		wg.Add(1)
		go func(t *FlowSpec) {
			defer wg.Done()
			if err := f.run(ctx, t, connected); err != nil {
				errs <- err
				cancel()
			}
		}(t)
	}

	// Fail fast only if every relay is down at the start.
	failed := 0
	for failed < len(remotes) {
		ok := <-connected
		if ok {
			break
		}
		failed++
	}
	if failed == len(remotes) {
		cancel()
	}
	wg.Wait()
	cancel()
	<-saveDone
	saveErr := opts.State.Save()

	select {
	case err := <-errs:
		return err
	default:
	}
	if failed == len(remotes) {
		return ErrNoReachableTargets
	}
	return saveErr
}

type follower struct {
	opts    FollowOptions
	filters *nip01.SubscriptionFilterGroup
	emitMu  sync.Mutex
}

// run follows one relay until ctx is done. It reports the first connection
// attempt on connected. A login refusal or an Emit error ends it.
func (f *follower) run(ctx context.Context, t *FlowSpec, connected chan<- bool) error {
	relay := t.Label()
	first := true
	wait := followBackoff[0]
	for ctx.Err() == nil {
		// The connection lives as long as connCtx, so the dial timeout
		// cancels it only while dialing.
		connCtx, cancelConn := context.WithCancel(ctx)
		timer := time.AfterFunc(f.opts.DialTimeout, cancelConn)
		sess, err := dialRelay(connCtx, t.relayURI, t.relayFallbackURI, nil, f.opts.AuthSigner)
		timer.Stop()
		if first {
			connected <- err == nil
			first = false
		}
		if err == nil {
			wait = followBackoff[0]
			err = f.stream(connCtx, sess, relay)
			sess.Close()
		}
		cancelConn()
		var le *LoginError
		if errors.As(err, &le) || errors.Is(err, errEmit) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		f.opts.Warn(relay, err)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil
		}
		if wait *= 2; wait > followBackoff[1] {
			wait = followBackoff[1]
		}
	}
	return nil
}

var errEmit = errors.New("emit failed")

// resumeFilters is f.filters with since moved up to where relay left off.
func (f *follower) resumeFilters(relay string) *nip01.SubscriptionFilterGroup {
	last := f.opts.State.Since(relay)
	if last == 0 {
		return f.filters
	}
	since := last - int64(f.opts.Overlap.Seconds())
	if since < 0 {
		since = 0
	}
	out := nip01.NewSubscriptionFilterGroup()
	for _, flt := range f.filters.GetAll() {
		c := *flt
		if int64(c.Since) < since {
			c.Since = uint64(since)
		}
		out.Add(&c)
	}
	return out
}

// stream runs one live subscription on sess until it drops. As for a
// one-shot read, a login only matters once the relay refuses the REQ for
// want of one.
func (f *follower) stream(ctx context.Context, sess *relaySession, relay string) error {
	subID := uuid.NewString()
	subscribe := func() error {
		if !sess.conn.SubscribeWithID(subID, f.resumeFilters(relay)) {
			return relayclient.ErrConnectionClosed
		}
		return nil
	}
	if err := subscribe(); err != nil {
		return err
	}
	retried := false
	for {
		select {
		case msg, ok := <-sess.conn.Read():
			if !ok {
				return relayclient.ErrConnectionClosed
			}
			if sess.observe(msg) {
				continue
			}
			switch m := msg.(type) {
			case *wire.EventSubscriptionResponse:
				if m.SubscriptionID != subID || m.Event == nil || m.Event.Verify(nip01.WithoutPowCheck()) != nil {
					continue
				}
				if err := f.emit(m.Event, relay); err != nil {
					return err
				}
			case *wire.ClosedSubscriptionResponse:
				if m.SubscriptionID != subID {
					continue
				}
				if refusedForLogin(m.Message) && !retried {
					if err := sess.awaitLogin(ctx, m.Message, nil); err != nil {
						return err
					}
					retried = true
					if err := subscribe(); err != nil {
						return err
					}
					continue
				}
				if refusedForLogin(m.Message) || strings.HasPrefix(m.Message, restrictedPrefix) {
					return &LoginError{Relay: relay, Reason: LoginRelayRefused, Detail: m.Message}
				}
				return fmt.Errorf("relay closed the subscription: %s", m.Message)
			}
		case err := <-sess.conn.Errors():
			return err
		case <-sess.conn.Closed():
			return relayclient.ErrConnectionClosed
		case <-ctx.Done():
			return nil
		}
	}
}

func (f *follower) emit(ev *nip01.Event, relay string) error {
	f.emitMu.Lock()
	defer f.emitMu.Unlock()
	if !f.opts.State.Mark(ev.ID, relay, int64(ev.CreatedAt)) {
		return nil
	}
	if err := f.opts.Emit(ev, relay); err != nil {
		return fmt.Errorf("%w: %w", errEmit, err)
	}
	return nil
}
