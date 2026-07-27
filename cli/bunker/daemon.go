// Package bunker implements `ncli bunker`: a NIP-46 remote signer ("bunker").
// The daemon (this file) holds the unlocked identity key, listens on one or
// more relays for kind:24133 requests addressed to it, and dispatches them
// (handler.go) against a remembered-permission policy (policy.go) and a
// human approval queue (queue.go). See board.go/command.go for the TUI and
// CLI surface, and ipc_server.go/ipc_client.go/spawn_unix.go for how a
// terminal attaches to (or backgrounds) a running daemon.
package bunker

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip04"
	"github.com/ohstr/nmilat/nip44"
	"github.com/ohstr/nmilat/nip46"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/ohstr/nmilat/utils"
)

// DaemonConfig is everything Daemon needs to start listening. Store/Queue
// are constructed by the caller (command.go) so they can also be handed to
// an IPC server / TUI board that observes the same live state Daemon
// mutates.
type DaemonConfig struct {
	IdentityPriv string
	IdentityPub  string
	// VaultLabel is the identity's vault entry label, if the resolved
	// pubkey happens to be one (see ResolveSignerKey's own doc comment --
	// this covers more than just `--identity <label>` input) -- empty
	// otherwise (a raw nsec never saved to the vault). Display-only:
	// board.go's formatIdentity/identityLabel show it alongside
	// name/nip05 when present, nothing in the signing/policy path reads
	// it.
	VaultLabel string
	Relays     []string // configured at startup; ResolveRelayURL-style bare hosts are NOT accepted here -- command.go resolves them to ws(s):// URLs first
	Store      *Store
	Queue      *Queue
	OnLog      func(format string, args ...any) // activity-feed hook for board.go/ipc_server.go; never passed request/response content or key material -- see the security notes below
	// EventLog, if set, durably records every Added/Resolved/signed
	// request (see recordAdded/recordHistory/recordSignedEvent) so
	// Request History survives a crash or restart instead of resetting
	// to empty -- nil is a valid no-op (e.g. in tests that don't care
	// about persistence). command.go's real daemon-startup paths always
	// set this via LoadEventLog(EventLogPath()).
	EventLog *EventLog
	// InitialHistory seeds historyTail at construction -- the reconstruction
	// LoadEventLog's own second return value gives command.go on startup.
	// Assigned directly rather than replayed through recordHistory: these
	// entries are already durably on disk, so replaying them would just
	// re-append duplicates.
	InitialHistory []HistoryEntry
}

// Daemon owns the live relay connections and dispatches incoming NIP-46
// requests through a Handler. Safe for concurrent use; Run blocks until
// ctx is cancelled, so callers drive its lifetime the same way every other
// ncli command does its own graceful shutdown -- signal.NotifyContext at
// the top, ctx cancellation flowing down (see cli/bunker/command.go).
type Daemon struct {
	cfg     DaemonConfig
	handler *Handler

	mu        sync.Mutex
	conns     map[string]*relayclient.Connection // by relay URL string
	attempted map[string]bool                    // by relay URL string -- true once runRelay's first dial for it has resolved (succeeded or failed) at least once; see RelayStatuses
	wg        sync.WaitGroup

	outMu   sync.Mutex
	outWait map[string]chan *nip46.Response // by NIP-46 request id, for a request THIS daemon sent (nostrconnect's signer-speaks-first flow) awaiting the client's response

	profileMu    sync.RWMutex
	profileName  string // display_name, falling back to name, from the identity's own kind:0 -- empty until fetchProfile resolves one
	profileNip05 string

	logMu    sync.Mutex
	logTail  []string // bounded recent activity-log lines, oldest first -- see RecentLogs
	logTotal int      // count of lines ever logged, including ones since rotated out of logTail

	historyMu   sync.Mutex
	historyTail []HistoryEntry // bounded resolved-request history, oldest first internally -- see History
}

// maxLogTail bounds Daemon's own in-memory activity-log tail (RecentLogs).
// Generous relative to board.go's DaemonLogWatcher poll cadence
// (renderInterval, currently 2s) -- rotating this many lines out between
// two polls would need an implausible activity burst, so a poller falling
// behind and silently missing some lines is not a realistic concern.
const maxLogTail = 500

// maxHistoryTail bounds Daemon's own in-memory resolved-request history
// (History) -- generous enough for a TUI glance-list/CLI listing to stay
// useful without growing unboundedly over a long-running daemon's
// lifetime. Also the compaction target LoadEventLog trims its own durable
// copy to, so the in-memory tail and the on-disk log stay the same
// bounded size.
const maxHistoryTail = 200

// NewDaemon builds a Daemon. cfg.Store/cfg.Queue must be non-nil (the
// caller owns their lifetime, e.g. to also hand them to an IPC server).
func NewDaemon(cfg DaemonConfig) *Daemon {
	d := &Daemon{
		cfg:         cfg,
		conns:       map[string]*relayclient.Connection{},
		attempted:   map[string]bool{},
		outWait:     map[string]chan *nip46.Response{},
		historyTail: cfg.InitialHistory,
	}
	d.handler = &Handler{
		IdentityPriv: cfg.IdentityPriv,
		IdentityPub:  cfg.IdentityPub,
		Store:        cfg.Store,
		Queue:        cfg.Queue,
		Relays:       cfg.Relays,
	}
	// Claims Queue's own OnResolved/OnAdded hooks -- see ResolvedEvent's
	// doc comment. Nothing else in this codebase sets either (OnResolved
	// existed, unwired, before request history did; OnAdded has never had
	// a subscriber before EventLog), so there's no competing owner to
	// clobber.
	cfg.Queue.OnResolved(d.recordHistory)
	cfg.Queue.OnAdded(d.recordAdded)
	d.handler.OnSigned = d.recordSignedEvent
	d.handler.OnAutoApproved = d.recordAutoApproved
	return d
}

// log records one activity-log line both to cfg.OnLog (a spawned
// background daemon writes this to daemon.log on disk -- see
// runDaemonProcess) and to this Daemon's own in-memory tail (RecentLogs),
// which is what actually makes it back to a TUI attached over IPC. Before
// RecentLogs existed, a spawned daemon's activity (relay connects, every
// request's method/from/id, a rejected/mismatched pairing attempt, ...)
// only ever reached that on-disk file -- board.go's Logger panel had
// nothing feeding it, despite looking like a live activity feed.
func (d *Daemon) log(format string, args ...any) {
	line := fmt.Sprintf("%s "+format, append([]any{time.Now().Format("15:04:05")}, args...)...)
	d.logMu.Lock()
	d.logTail = append(d.logTail, line)
	d.logTotal++
	if len(d.logTail) > maxLogTail {
		d.logTail = d.logTail[len(d.logTail)-maxLogTail:]
	}
	d.logMu.Unlock()

	if d.cfg.OnLog != nil {
		d.cfg.OnLog(format, args...)
	}
}

// LogSnapshot is Daemon.RecentLogs' return shape: Lines is the current
// bounded tail (oldest first); Total is the absolute count of lines ever
// logged, letting a poller (board.go's DaemonLogWatcher) tell how many of
// the lines it already showed are still present at the front of Lines,
// rather than re-showing or skipping entries once maxLogTail starts
// dropping the oldest ones.
type LogSnapshot struct {
	Lines []string `json:"lines"`
	Total int      `json:"total"`
}

// RecentLogs returns the daemon's most recent activity-log lines.
func (d *Daemon) RecentLogs() LogSnapshot {
	d.logMu.Lock()
	defer d.logMu.Unlock()
	lines := make([]string, len(d.logTail))
	copy(lines, d.logTail)
	return LogSnapshot{Lines: lines, Total: d.logTotal}
}

// HistoryEntry is one resolved request -- Daemon's own bounded record of
// what happened to a Pending once Queue settled it (a human's Resolve, or
// sweepExpired's own auto-reject), independent of the policy Store's
// remembered-grant bookkeeping: a one-off Approve/Reject Once never
// touches the Store at all, but still belongs here. See board.go's
// HistoryTable ("REQUEST HISTORY") for where this is actually shown.
type HistoryEntry struct {
	ID         string    `json:"id"`
	ClientKey  string    `json:"client_key"`
	Method     string    `json:"method"`
	Kind       int       `json:"kind,omitempty"` // only meaningful for Method == sign_event, same as Pending.Kind
	CreatedAt  time.Time `json:"created_at"`
	ResolvedAt time.Time `json:"resolved_at"`
	Verdict    Decision  `json:"verdict"`
	// Remembered is true if this decision also created/updated a Store
	// grant (an "Always ..." choice), not just a one-off Approve/Reject
	// Once -- see ResolvedEvent.Remembered.
	Remembered bool `json:"remembered"`
	// AutoApproved is true if this request was allowed instantly by an
	// already-standing grant (or, for "connect", a pending GrantSpec) --
	// never went through a human decision or Queue.Add at all. See
	// ResolvedEvent.AutoApproved/recordAutoApproved. Always false, never
	// true at the same time as Expired -- an auto-approved request is
	// never in the queue to expire.
	AutoApproved bool `json:"auto_approved,omitempty"`
	// Expired is true if nobody answered in time and the queue's own TTL
	// sweep auto-rejected it, rather than a human deciding.
	Expired bool `json:"expired"`
	// Event is the sign_event target -- unsigned the moment this entry is
	// recorded (recordHistory copies it straight from Pending.Event,
	// whatever the verdict), then overwritten with the real signed event
	// shortly after if this was actually approved and Handler.execute
	// signed it -- see recordSignedEvent. So a rejected or expired
	// sign_event still has its (never-signed) Event here, for board.go's
	// HistoryTable to show what was actually being asked for even though
	// nothing was ever signed; check Event.Sig != "" to tell the two
	// apart. Nil for every method but sign_event, which never has one.
	Event *nip01.Event `json:"event,omitempty"`
}

// recordAdded is Queue's own OnAdded hook (wired in NewDaemon),
// durably recording p the instant it actually starts waiting on a human
// decision -- see EventLog.AppendAdded. A no-op if EventLog isn't
// configured (e.g. most tests). Best-effort like every other persistence
// call here: a write failure is logged, never lets a disk hiccup block
// live signing.
func (d *Daemon) recordAdded(p Pending) {
	if d.cfg.EventLog == nil {
		return
	}
	if err := d.cfg.EventLog.AppendAdded(p); err != nil {
		d.log("failed to persist pending request %s to the event log: %v", p.ID, err)
	}
}

// recordHistory is Queue's own OnResolved hook (wired in NewDaemon),
// appending one HistoryEntry per resolved request to the bounded tail --
// oldest dropped once maxHistoryTail is exceeded, the same trim-from-
// front convention Daemon.log already uses for RecentLogs. Also durably
// persists h (EventLog.AppendResolved, best-effort -- same reasoning as
// recordAdded) before the in-memory update, and logs a one-line
// activity-feed summary, the resolution-side counterpart to
// handleIncoming's own "request method=... from=... id=..." line logged
// on arrival -- that line alone never said how a request was decided.
func (d *Daemon) recordHistory(ev ResolvedEvent) {
	h := HistoryEntry{
		ID:           ev.Pending.ID,
		ClientKey:    ev.Pending.ClientKey,
		Method:       ev.Pending.Method,
		Kind:         ev.Pending.Kind,
		CreatedAt:    ev.Pending.CreatedAt,
		ResolvedAt:   time.Now(),
		Verdict:      ev.Verdict,
		Remembered:   ev.Remembered,
		Expired:      ev.Expired,
		AutoApproved: ev.AutoApproved,
		// Unsigned for now (nil for anything but sign_event) -- see
		// HistoryEntry.Event's own doc comment for why this is set here
		// unconditionally rather than only for an eventual approval.
		Event: ev.Pending.Event,
	}

	if d.cfg.EventLog != nil {
		if err := d.cfg.EventLog.AppendResolved(h); err != nil {
			d.log("failed to persist resolved request %s to the event log: %v", h.ID, err)
		}
	}

	d.historyMu.Lock()
	d.historyTail = append(d.historyTail, h)
	if len(d.historyTail) > maxHistoryTail {
		d.historyTail = d.historyTail[len(d.historyTail)-maxHistoryTail:]
	}
	d.historyMu.Unlock()

	// A long-lived daemon never restarts, so LoadEventLog's own one-time
	// startup compact never gets another chance to run -- without this,
	// events.wal would grow for as long as the process stays up instead of
	// staying bounded like historyTail already is. CompactDue is a cheap
	// counter check, so it's fine to ask on every resolution; the actual
	// rewrite (which needs a fresh copy of historyTail, taken under its own
	// lock) only happens the rare time the answer is yes.
	if d.cfg.EventLog != nil && d.cfg.EventLog.CompactDue() {
		d.historyMu.Lock()
		tail := make([]HistoryEntry, len(d.historyTail))
		copy(tail, d.historyTail)
		d.historyMu.Unlock()
		if err := d.cfg.EventLog.compact(tail); err != nil {
			d.log("failed to compact event log: %v", err)
		}
	}

	outcome := "rejected"
	switch {
	case h.AutoApproved:
		outcome = "auto-approved (existing grant)"
	case h.Expired:
		outcome = "expired (no response)"
	case h.Verdict == Allow:
		outcome = "approved"
	}
	d.log("request resolved method=%s from=%s id=%s verdict=%s", h.Method, d.cfg.Store.Label(h.ClientKey), h.ID, outcome)
}

// recordAutoApproved is Handler's own OnAutoApproved hook (wired in
// NewDaemon), for a request that was allowed instantly by an
// already-standing grant (or, for "connect", a pending GrantSpec) and so
// never touched Queue.Add/Queue.OnResolved at all -- without this,
// Request History only ever showed requests that needed a human decision,
// even though skills/ncli-bunker/SKILL.md's own "Trusted Apps > Last
// Request" column is documented as derived from history (see followup
// issue in integration/agent-eval/followup/issues.md). Delegates straight
// to recordHistory so persistence/compaction/activity-log behavior stays
// identical to every other resolution.
func (d *Daemon) recordAutoApproved(p Pending) {
	d.recordHistory(ResolvedEvent{Pending: p, Verdict: Allow, AutoApproved: true})
}

// recordSignedEvent is Handler's own OnSigned hook (wired in NewDaemon),
// attaching the just-signed event to its request's HistoryEntry so
// board.go's HistoryTable can show/copy the signed JSON. Searched from the
// end since the entry it's after is always the most recently resolved one
// -- recordHistory (Resolve's onResolved) is guaranteed to have already run
// and created it by the time this fires (see queue.go's Resolve, which
// deliberately calls onResolved before close(e.done) for exactly this
// ordering). A miss (entry already rotated out of historyTail, or this
// request somehow isn't in it at all) is silently ignored: there's no
// history row left to attach to, and nothing else depends on this
// succeeding.
func (d *Daemon) recordSignedEvent(requestID string, event *nip01.Event) {
	found := false
	d.historyMu.Lock()
	for i := len(d.historyTail) - 1; i >= 0; i-- {
		if d.historyTail[i].ID == requestID {
			d.historyTail[i].Event = event
			found = true
			break
		}
	}
	d.historyMu.Unlock()

	if !found || d.cfg.EventLog == nil {
		return
	}
	if err := d.cfg.EventLog.AppendSigned(requestID, event); err != nil {
		d.log("failed to persist signed event for request %s to the event log: %v", requestID, err)
	}
}

// History returns the daemon's most recent resolved requests, most
// recent first -- unlike RecentLogs/ListPending (both oldest-first),
// recency rather than arrival order is what a history view is for.
func (d *Daemon) History() []HistoryEntry {
	d.historyMu.Lock()
	defer d.historyMu.Unlock()
	out := make([]HistoryEntry, len(d.historyTail))
	for i, h := range d.historyTail {
		out[len(out)-1-i] = h
	}
	return out
}

// Profile returns the identity's display name (display_name, falling back
// to name) and nip05 as resolved by fetchProfile, or two empty strings if
// that hasn't found one (yet, or ever -- a signer with no published
// profile is a normal, permanent state, not an error).
func (d *Daemon) Profile() (name, nip05 string) {
	d.profileMu.RLock()
	defer d.profileMu.RUnlock()
	return d.profileName, d.profileNip05
}

func (d *Daemon) setProfile(name, nip05 string) {
	d.profileMu.Lock()
	d.profileName, d.profileNip05 = name, nip05
	d.profileMu.Unlock()
}

// profileMetadata is the subset of a kind:0 event's JSON content this
// cares about for display purposes -- everything else a real profile
// carries (picture, about, banner, lud16, ...) is irrelevant here.
type profileMetadata struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Nip05       string `json:"nip05"`
}

// fetchProfile is a best-effort, one-shot lookup of the signing identity's
// own kind:0 metadata, run once at startup so the TUI can show a human
// name instead of a bare pubkey -- see board.go's IdentityBar. It tries
// each configured relay in turn until one answers, dialing its own
// short-lived connection per relay rather than reusing Daemon's own
// long-lived ones (those are already busy consuming their NIP-46 request
// subscription via SubscribeWithID+Events; see serveConn's doc comment on
// not mixing consumption styles on one Connection). Gives up silently on
// error or timeout -- a shortened npub is a perfectly good fallback, not
// worth surfacing a dialog over.
func (d *Daemon) fetchProfile(ctx context.Context) {
	for _, raw := range d.cfg.Relays {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if d.fetchProfileFrom(ctx, u) {
			return
		}
	}
}

// fetchProfileFrom queries one relay for the identity's newest kind:0 and
// applies it if found, returning whether it succeeded (so fetchProfile
// knows whether to try the next relay).
func (d *Daemon) fetchProfileFrom(ctx context.Context, u *url.URL) bool {
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := relayclient.Connect(dialCtx, u)
	if err != nil {
		return false
	}
	defer conn.Close()

	_, events, done := conn.Subscribe(nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds:   []int{0},
		Authors: []string{d.cfg.IdentityPub},
		Limit:   1,
	}))

	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	var latest *nip01.Event
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return d.applyProfile(latest)
			}
			if latest == nil || ev.Event.CreatedAt > latest.CreatedAt {
				latest = ev.Event
			}
		case <-done:
			return d.applyProfile(latest)
		case <-timeout.C:
			return d.applyProfile(latest)
		case <-ctx.Done():
			return false
		}
	}
}

func (d *Daemon) applyProfile(ev *nip01.Event) bool {
	if ev == nil {
		return false
	}
	var meta profileMetadata
	if err := json.Unmarshal([]byte(ev.Content), &meta); err != nil {
		return false
	}
	name := meta.DisplayName
	if name == "" {
		name = meta.Name
	}
	if name == "" && meta.Nip05 == "" {
		return false
	}
	d.setProfile(name, meta.Nip05)
	return true
}

// Run dials every configured relay, listens for kind:24133 requests
// addressed to the daemon's identity, and dispatches each one (in its own
// goroutine -- Handler.Handle can block for as long as a human takes to
// decide) until ctx is cancelled. It also drives the queue/policy sweep
// (queue.go's Queue.Run). Blocks until every relay goroutine has exited.
func (d *Daemon) Run(ctx context.Context) error {
	if len(d.cfg.Relays) == 0 {
		return errors.New("bunker: no relays configured")
	}

	go d.cfg.Queue.Run(ctx, 30*time.Second, d.cfg.Store)
	go d.fetchProfile(ctx)

	for _, raw := range d.cfg.Relays {
		u, err := url.Parse(raw)
		if err != nil {
			d.log("skipping invalid relay %q: %v", raw, err)
			continue
		}
		d.wg.Add(1)
		go func(u *url.URL) {
			defer d.wg.Done()
			d.runRelay(ctx, u)
		}(u)
	}

	d.wg.Wait()
	return nil
}

// requestFilter is the kind:24133 / #p:<me> filter every relay connection
// subscribes on -- every NIP-46 request or response addressed to this
// identity, regardless of which client sent it.
func (d *Daemon) requestFilter() *nip01.SubscriptionFilterGroup {
	return nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds: []int{nip46.KindRequest},
		Tags:  map[string][]string{"p": {d.cfg.IdentityPub}},
	})
}

// runRelay dials u, serves it until the connection drops or ctx is
// cancelled, then reconnects with exponential backoff (capped at 30s) --
// unless ctx is already done, in which case it returns instead of
// reconnecting.
func (d *Daemon) runRelay(ctx context.Context, u *url.URL) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}

		conn, err := relayclient.Connect(ctx, u)
		d.markAttempted(u.String())
		if err != nil {
			d.log("relay %s: connect failed: %v", u, err)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		d.log("relay %s: connected", u)

		d.registerConn(u.String(), conn)
		d.serveConn(ctx, conn)
		d.unregisterConn(u.String())
	}
}

// RelayStatus is whether one configured relay currently has a live
// connection -- the "is this thing actually working" signal board.go's
// IdentityBar shows next to the identity itself. A relay flips between
// connected/disconnected over the daemon's lifetime as runRelay's own
// backoff-and-reconnect loop runs; this always reflects the current
// instant, not a sticky history.
type RelayStatus struct {
	URL       string `json:"url"`
	Connected bool   `json:"connected"`
	// Connecting is true only for the brief window between the daemon
	// starting and this relay's very first dial attempt resolving (either
	// way) -- never true again afterward, even while a later reconnect
	// attempt is itself in flight. Exists so a caller (board.go's
	// AlertBar/IdentityBar) can tell "still trying for the first time,
	// no verdict yet" apart from "tried and it's actually down" instead
	// of both looking identical (Connected == false) the instant the
	// daemon starts, before its relay goroutines have even had a chance
	// to run -- see runRelay/markAttempted.
	Connecting bool `json:"connecting,omitempty"`
}

// RelayStatuses reports live/dead for every configured relay, in
// configured order. d.conns (registerConn/unregisterConn, driven by
// runRelay) is the single source of truth for what's currently connected;
// d.attempted (markAttempted) is the source for Connecting.
func (d *Daemon) RelayStatuses() []RelayStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	statuses := make([]RelayStatus, 0, len(d.cfg.Relays))
	for _, raw := range d.cfg.Relays {
		_, connected := d.conns[raw]
		statuses = append(statuses, RelayStatus{
			URL:        raw,
			Connected:  connected,
			Connecting: !connected && !d.attempted[raw],
		})
	}
	return statuses
}

func (d *Daemon) registerConn(key string, conn *relayclient.Connection) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.conns[key] = conn
}

// markAttempted records that key's first dial attempt (runRelay's own
// relayclient.Connect call) has resolved, successfully or not -- see
// RelayStatus.Connecting's own doc comment for why this matters.
func (d *Daemon) markAttempted(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attempted[key] = true
}

func (d *Daemon) unregisterConn(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.conns, key)
}

// serveConn subscribes on conn (using SubscribeWithID + Events, not
// Subscribe -- Subscribe's channel closes at EOSE, which would silently
// stop delivery of new live requests the moment the relay finishes
// replaying any stored backlog; SubscribeWithID's subscription is never
// registered for that EOSE-triggered auto-close, so Events keeps streaming
// indefinitely, which is what a signer that must listen forever needs) and
// dispatches events until conn drops or ctx is cancelled. Responses are
// sent fire-and-forget (Connection.Send, not Publish -- Publish reads from
// the same shared incoming channel Events() already drains, and running
// both concurrently on one Connection races for the same messages); a
// client that gets no timely response is expected to resend with the same
// request id, which Queue's de-dup (or an already-persisted grant)
// resolves immediately without re-prompting a human twice.
func (d *Daemon) serveConn(ctx context.Context, conn *relayclient.Connection) {
	defer conn.Close()

	subID := uuid.NewString()
	conn.SubscribeWithID(subID, d.requestFilter())
	events := conn.Events(subID)

	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-conn.Errors():
			if !ok {
				return
			}
			d.log("relay %s: %v", conn.Relay(), err)
			if errors.Is(err, relayclient.ErrConnectionClosed) {
				return
			}
		case ev, ok := <-events:
			if !ok {
				return
			}
			go d.handleIncoming(conn, ev.Event)
		}
	}
}

// handleIncoming verifies, decrypts, and dispatches one raw event received
// on conn. Never logs decrypted content or key material -- only the
// method/kind/pubkey/request-id shape already visible in Pending/log
// lines, per the plan's logging-safety requirement (a headless daemon's
// log file is not a redaction layer).
//
// parseRequestEventFallback re-attempts decrypting ev as a NIP-46 request
// using whichever encryption scheme ParseRequestEvent (nip46) did NOT
// actually attempt (tried is exactly what handleIncoming computed nip46
// would have used -- the request's "encryption" tag verbatim, or
// nip46.EncryptionNIP04 if absent). Defaults to NIP-44V2 unless tried was
// already an exact match for it: nip46's tag-reading is a raw string
// comparison against its own "nip04"/"nip44_v2" constants with no
// normalization, so a tag of "" (missing), "nip44" (the common real-world
// convention -- no "_v2" suffix), or anything else unrecognized all still
// mean "nip46 did NOT actually try NIP-44V2," not "so it must have been
// NIP-04." The narrower case -- tried was already exactly nip44_v2 and
// still failed (corrupt content, wrong key, ...) -- falls back to NIP-04
// as the only other scheme, mostly so genuinely bad data still gets a
// clean try-and-fail rather than an unreachable branch.
//
// Duplicates a handful of lines of decrypt/unmarshal logic nip46 keeps
// unexported, the same reason handler.go's nip44ConversationKey
// duplicates its key-derivation pair. Returns the scheme that actually
// decrypted successfully alongside the parsed request, so handleIncoming's
// response goes back encrypted the way the client can actually read it --
// not whatever nip46 originally guessed.
func (d *Daemon) parseRequestEventFallback(ev *nip01.Event, tried string) (*nip46.RequestEvent, string, error) {
	fallback := nip46.EncryptionNIP44V2
	if tried == nip46.EncryptionNIP44V2 {
		fallback = nip46.EncryptionNIP04
	}

	var plaintext string
	var err error
	if fallback == nip46.EncryptionNIP44V2 {
		var key []byte
		if key, err = d.handler.nip44ConversationKey(ev.PubKey); err == nil {
			plaintext, err = nip44.Decrypt(ev.Content, key)
		}
	} else {
		plaintext, err = nip04.Decrypt(ev.Content, ev.PubKey, d.cfg.IdentityPriv)
	}
	if err != nil {
		return nil, "", fmt.Errorf("fallback decrypt (%s): %w", fallback, err)
	}

	var req nip46.Request
	if err := json.Unmarshal([]byte(plaintext), &req); err != nil {
		return nil, "", fmt.Errorf("fallback unmarshal (%s): %w", fallback, err)
	}
	return &nip46.RequestEvent{Event: ev, Request: req}, fallback, nil
}

func (d *Daemon) handleIncoming(conn *relayclient.Connection, ev *nip01.Event) {
	if err := ev.Verify(); err != nil {
		d.log("dropped unverifiable event %s: %v", shortHex(ev.ID), err)
		return
	}

	encryption := nip46.EncryptionNIP04
	if enc, err := utils.FindUniqueEventTagValue(ev.Tags, "encryption"); err == nil && enc != "" {
		encryption = enc
	}

	req, err := nip46.ParseRequestEvent(ev, d.cfg.IdentityPriv)
	if err != nil {
		// Some real-world clients send NIP-44 ciphertext without tagging
		// the event "encryption" (or, less commonly, the reverse) --
		// ParseRequestEvent picks exactly one scheme from the tag's
		// presence/absence and never retries (see its own doc comment),
		// so a tag/ciphertext mismatch otherwise drops a perfectly valid
		// request. Try the scheme it didn't, once, before giving up.
		fallbackReq, fallbackEncryption, fallbackErr := d.parseRequestEventFallback(ev, encryption)
		if fallbackErr != nil {
			d.log("dropped unparseable request from %s: %v", d.cfg.Store.Label(ev.PubKey), err)
			return
		}
		req, encryption = fallbackReq, fallbackEncryption
	}

	// A NIP-46 request and response share one event kind, distinguished
	// only by which JSON fields the decrypted content has (see nip46.go's
	// KindRequest doc comment) -- ParseRequestEvent still "succeeds" on a
	// response's shape, just with an empty Method, since the JSON simply
	// doesn't have a "method" key. Route those to whichever earlier
	// sendRequestAndAwait call is still waiting on this request id (the
	// nostrconnect signer-speaks-first flow's confirmation), rather than
	// treating an empty method as a request to Handler.
	if req.Method == "" {
		if resp, err := nip46.ParseResponseEvent(ev, d.cfg.IdentityPriv); err == nil {
			d.deliverResponse(resp)
		}
		return
	}

	d.log("request method=%s from=%s id=%s", req.Method, d.cfg.Store.Label(ev.PubKey), req.RequestID)

	resp := d.handler.Handle(req, encryption)
	if resp == nil {
		d.log("failed to build a response for request %s", req.RequestID)
		return
	}
	if !conn.Send(resp) {
		d.log("relay %s: connection closed, dropped response to %s", conn.Relay(), req.RequestID)
	}
}

// sendRequestAndAwait sends a NIP-46 request this daemon itself
// originates (only ever "connect", for the nostrconnect signer-speaks-
// first flow) and waits up to timeout for the matching response.
func (d *Daemon) sendRequestAndAwait(conn *relayclient.Connection, recipientPub, method string, params []string, encryption string, timeout time.Duration) (*nip46.Response, error) {
	ev, reqID, err := nip46.NewRequestEvent(d.cfg.IdentityPriv, recipientPub, method, params, encryption)
	if err != nil {
		return nil, err
	}
	if err := ev.Sign(d.cfg.IdentityPriv); err != nil {
		return nil, err
	}

	ch := make(chan *nip46.Response, 1)
	d.outMu.Lock()
	d.outWait[reqID] = ch
	d.outMu.Unlock()
	defer func() {
		d.outMu.Lock()
		delete(d.outWait, reqID)
		d.outMu.Unlock()
	}()

	if !conn.Send(ev) {
		return nil, relayclient.ErrConnectionClosed
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timed out waiting for a response to %s", method)
	}
}

func (d *Daemon) deliverResponse(resp *nip46.ResponseEvent) {
	d.outMu.Lock()
	ch, ok := d.outWait[resp.RequestID]
	d.outMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- &resp.Response:
	default:
	}
}

// connectionFor returns the daemon's existing connection to relayURL, or
// dials and registers a new one (serving it under the same lifetime as
// every other relay connection) if it isn't already connected -- used by
// InitiateNostrconnect when the nostrconnect:// URI's relay isn't one of
// the daemon's own configured relays.
func (d *Daemon) connectionFor(ctx context.Context, relayURL string) (*relayclient.Connection, error) {
	d.mu.Lock()
	conn, ok := d.conns[relayURL]
	d.mu.Unlock()
	if ok {
		return conn, nil
	}

	u, err := url.Parse(relayURL)
	if err != nil {
		return nil, fmt.Errorf("invalid relay %q: %w", relayURL, err)
	}

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.runRelay(ctx, u)
	}()

	// runRelay registers the connection asynchronously; poll briefly
	// rather than adding a second signaling path just for this one-time
	// ad-hoc case.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		conn, ok := d.conns[relayURL]
		d.mu.Unlock()
		if ok {
			return conn, nil
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("timed out connecting to relay %s", relayURL)
}

// NewBunkerPairing generates a fresh single-use secret and arms the
// handler to expect it on the next "connect" request -- the bunker://
// flow, where the client speaks first. Returns the bunker:// URI to
// display/share.
func (d *Daemon) NewBunkerPairing() (string, error) {
	return d.NewBunkerPairingWithGrants(nil)
}

// NewBunkerPairingWithGrants is NewBunkerPairing plus `ncli bunker connect
// --grants <file>`'s own hook: spec is resolved and applied to whichever
// pubkey ends up presenting the freshly generated secret (handler.go's
// execute, on that "connect" request's own success), not to any pubkey
// known now -- the whole reason this is a separate arm-then-apply step
// instead of an immediate GrantSpec.Resolve/Store.Remember call is that
// the bunker:// direction never knows the app's pubkey until it actually
// connects. nil spec is exactly NewBunkerPairing's own behavior.
func (d *Daemon) NewBunkerPairingWithGrants(spec *GrantSpec) (string, error) {
	secret, err := NewSecret()
	if err != nil {
		return "", err
	}
	d.handler.SetPendingSecret(secret)
	if spec != nil {
		d.handler.SetPendingGrants(spec)
	}
	return BunkerURI(d.cfg.IdentityPub, secret, d.cfg.Relays), nil
}

// InitiateNostrconnect implements the nostrconnect:// flow's signer-
// speaks-first handshake: send a "connect" request to the URI's client
// pubkey carrying its secret, and wait for the client's response to echo
// that same secret back (constant-time compared, never `==`) as
// confirmation.
func (d *Daemon) InitiateNostrconnect(ctx context.Context, schema *nip46.NostrconnectSchema) error {
	return d.InitiateNostrconnectWithGrants(ctx, schema, nil)
}

// InitiateNostrconnectWithGrants is InitiateNostrconnect plus `ncli bunker
// connect <uri> --grants <file>`'s own hook. Unlike the bunker:// direction
// (NewBunkerPairingWithGrants), the app's pubkey is already known here --
// schema.ClientPublickey, straight from the nostrconnect:// URI it
// generated -- so spec is resolved and applied directly, right alongside
// the existing Store.Pair call below, rather than staged on the handler
// for a later request to consume.
func (d *Daemon) InitiateNostrconnectWithGrants(ctx context.Context, schema *nip46.NostrconnectSchema, spec *GrantSpec) error {
	conn, err := d.connectionFor(ctx, schema.Relay.String())
	if err != nil {
		return fmt.Errorf("nostrconnect: %w", err)
	}

	resp, err := d.sendRequestAndAwait(conn, schema.ClientPublickey, nip46.MethodConnect,
		[]string{d.cfg.IdentityPub, schema.Secret}, nip46.EncryptionNIP44V2, 60*time.Second)
	if err != nil {
		return fmt.Errorf("nostrconnect: %w", err)
	}
	if resp.Error != "" {
		return fmt.Errorf("nostrconnect: client rejected pairing: %s", resp.Error)
	}
	if subtle.ConstantTimeCompare([]byte(resp.Result), []byte(schema.Secret)) != 1 {
		return errors.New("nostrconnect: secret confirmation mismatch")
	}

	// Register the pairing in Trusted Apps -- this direction never goes
	// through Handler.Handle (the signer sent the request itself and is
	// just awaiting the client's echo above), so it's the one path
	// handler.go's own Store.Pair call on MethodConnect never covers.
	// schema.Metadata's Name/Url are this app's own self-reported
	// identity, straight from the nostrconnect:// URI it generated --
	// ParseNostrconnect (uri.go) requires the metadata query param, so
	// this is normally non-nil, but nil is handled defensively anyway
	// (an empty Store.Pair name/URL, same as the bunker:// direction,
	// rather than a nil-pointer panic on a technicality of that
	// requirement).
	var appName, appURL string
	if schema.Metadata != nil {
		appName, appURL = schema.Metadata.Name, schema.Metadata.Url
	}
	_ = d.cfg.Store.Pair(schema.ClientPublickey, appName, appURL)
	// Resolved against time.Now() here, not whenever spec was loaded --
	// see GrantSpec.Resolve's own doc comment. Unlike the bunker://
	// direction, there's no gap to worry about in practice (this call
	// blocks on the client's own echo response immediately above), but
	// resolving at the same point in both directions keeps one mental
	// model instead of two.
	if spec != nil {
		for _, g := range spec.Resolve(time.Now()) {
			_ = d.cfg.Store.Remember(schema.ClientPublickey, g)
		}
		// The spec's own nickname wins over the app's self-reported name
		// -- same priority labelFor already gives Nickname over AppName
		// for every other pairing, just applied proactively here instead
		// of waiting for a human to set it later via 'n'/`sessions rename`.
		if spec.Nickname != "" {
			_, _ = d.cfg.Store.SetName(schema.ClientPublickey, spec.Nickname)
		}
	}
	d.log("paired with %s via nostrconnect", d.cfg.Store.Label(schema.ClientPublickey))
	return nil
}

// shortHex renders a hex id/pubkey as its first 8 characters -- the same
// truncation convention client/tui/eventtable.go's shortHex already uses,
// duplicated here rather than imported since client/tui depends on this
// package's sibling client package, not the other way around.
func shortHex(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}
