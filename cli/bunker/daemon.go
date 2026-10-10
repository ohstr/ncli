// Package bunker implements `ncli bunker`: a NIP-46 remote signer ("bunker").
// The daemon (this file) holds the unlocked identity key and runs nmilat's
// NIP-46 relay signer (nip46/bunker); handler.go decides each request
// against a remembered-permission policy (policy.go) and a human approval
// queue (queue.go). See board.go/command.go for the TUI and
// CLI surface, and ipc_server.go/ipc_client.go/spawn_unix.go for how a
// terminal attaches to (or backgrounds) a running daemon.
package bunker

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/nip46/bunker"
	relayclient "github.com/ohstr/nmilat/relay/client"
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
	// NostrconnectConfirmTimeout overrides how long a nostrconnect://
	// pairing waits for the paired app's first request before reporting
	// itself unconfirmed. Zero means nostrconnectConfirmTimeout. Only
	// tests set this, to avoid spending the real wait on a client that
	// deliberately stays silent.
	NostrconnectConfirmTimeout time.Duration
}

// Daemon runs the signer (nmilat's nip46/bunker server, through Handler)
// and keeps ncli's history, log and profile around it. Safe for
// concurrent use; Run blocks until ctx is cancelled, so callers drive its
// lifetime the same way every other ncli command does its own graceful
// shutdown -- signal.NotifyContext at the top, ctx cancellation flowing
// down (see cli/bunker/command.go).
type Daemon struct {
	cfg     DaemonConfig
	handler *Handler

	pairMu   sync.Mutex
	pairWait map[string]chan struct{} // by client pubkey -- closed on that client's first request, so InitiateNostrconnect can tell a pairing the app actually picked up from one it only sent

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
		pairWait:    map[string]chan struct{}{},
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
	d.handler.OnDecision = d.decided
	d.handler.Logf = d.log
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
// Request" column is documented as derived from history. Delegates
// straight to recordHistory so persistence/compaction/activity-log
// behavior stays identical to every other resolution.
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

// The kind:0 content document lives in cli/common as ProfileMetadata, shared
// with "ncli profile" -- this only reads the display fields from it.

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
	meta, err := common.ParseProfileMetadata(ev.Content)
	if err != nil {
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
	srv, err := d.handler.server()
	if err != nil {
		return fmt.Errorf("bunker: %w", err)
	}

	go d.cfg.Queue.Run(ctx, 30*time.Second, d.cfg.Store)
	go d.fetchProfile(ctx)

	return srv.Run(ctx)
}

// RelayStatus is whether one configured relay currently has a live
// connection -- the "is this thing actually working" signal board.go's
// IdentityBar shows next to the identity itself. Connecting is true only
// until that relay's first dial attempt resolves.
type RelayStatus = bunker.RelayStatus

// RelayStatuses reports live/dead for every configured relay, in
// configured order. A relay the signer hasn't started dialing yet reads as
// Connecting.
func (d *Daemon) RelayStatuses() []RelayStatus {
	live := map[string]RelayStatus{}
	if srv, err := d.handler.server(); err == nil {
		for _, st := range srv.RelayStatuses() {
			live[st.URL] = st
		}
	}
	statuses := make([]RelayStatus, 0, len(d.cfg.Relays))
	for _, raw := range d.cfg.Relays {
		st, ok := live[raw]
		if !ok {
			st = RelayStatus{URL: raw, Connecting: true}
		}
		st.URL = raw
		statuses = append(statuses, st)
	}
	return statuses
}

// decided logs every request (method, sender and id only -- never content
// or key material: a headless daemon's log file is not a redaction layer)
// and confirms a nostrconnect pairing on the app's first request.
func (d *Daemon) decided(dec bunker.Decision) {
	d.notifyFirstRequest(dec.Call.Client)
	d.log("request method=%s from=%s id=%s", dec.Call.Method, d.cfg.Store.Label(dec.Call.Client), dec.Call.ID)
}

// awaitFirstRequest blocks until pubkey sends this daemon any NIP-46
// request, or timeout elapses. It exists because the nostrconnect:// flow
// has no acknowledgement: the signer sends its connect response and the
// spec gives the client nothing to send back. A client that accepted the
// pairing does start using it immediately though -- nostr-tools fires
// switch_relays the moment fromURI resolves -- so the first request from
// that pubkey is the closest thing to a delivery receipt available.
//
// Reports whether one arrived. False means "unconfirmed", never "failed":
// the secret has already gone out and the client may well have taken it.
func (d *Daemon) awaitFirstRequest(ctx context.Context, pubkey string, timeout time.Duration) bool {
	ch := make(chan struct{})
	d.pairMu.Lock()
	d.pairWait[pubkey] = ch
	d.pairMu.Unlock()
	defer func() {
		d.pairMu.Lock()
		delete(d.pairWait, pubkey)
		d.pairMu.Unlock()
	}()

	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	case <-ctx.Done():
		return false
	}
}

// notifyFirstRequest releases whichever awaitFirstRequest is watching
// pubkey, if any. Closing (rather than sending) makes it idempotent.
func (d *Daemon) notifyFirstRequest(pubkey string) {
	d.pairMu.Lock()
	ch, ok := d.pairWait[pubkey]
	if ok {
		delete(d.pairWait, pubkey)
	}
	d.pairMu.Unlock()
	if ok {
		close(ch)
	}
}

// NewBunkerPairing generates a fresh single-use secret and arms the
// handler to expect it on the next "connect" request -- the bunker://
// flow, where the client speaks first. Returns the bunker:// URI to
// display/share.
func (d *Daemon) NewBunkerPairing() (string, error) {
	return d.NewBunkerPairingWithGrants(nil)
}

// NewBunkerPairingWithGrants is NewBunkerPairing plus `ncli bunker connect
// --grants <file>`'s own hook: spec is applied to whichever pubkey ends up
// presenting the freshly generated secret, since the bunker:// direction
// never knows the app's pubkey until it actually connects. nil spec is
// exactly NewBunkerPairing's own behavior.
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

// nostrconnectConfirmTimeout bounds how long InitiateNostrconnect waits
// for the paired app to make its first request. Only a reporting nicety --
// the pairing is already registered and usable when this elapses (see
// awaitFirstRequest).
const nostrconnectConfirmTimeout = 30 * time.Second

// InitiateNostrconnect implements the nostrconnect:// flow: the signer
// answers the URI with a connect response carrying its secret, on the
// relays the URI names (bunker.Server.AcceptNostrconnectSchema). The client
// sends nothing back -- see awaitFirstRequest for how this reports whether
// the app actually picked the pairing up.
func (d *Daemon) InitiateNostrconnect(ctx context.Context, schema *nip46.NostrconnectSchema) error {
	return d.InitiateNostrconnectWithGrants(ctx, schema, nil)
}

// InitiateNostrconnectWithGrants is InitiateNostrconnect plus `ncli bunker
// connect <uri> --grants <file>`'s own hook. The app's pubkey is already
// known here, so spec is applied directly alongside Store.Pair.
func (d *Daemon) InitiateNostrconnectWithGrants(ctx context.Context, schema *nip46.NostrconnectSchema, spec *GrantSpec) error {
	srv, err := d.handler.server()
	if err != nil {
		return fmt.Errorf("nostrconnect: %w", err)
	}
	if err := srv.AcceptNostrconnectSchema(ctx, schema); err != nil {
		if errors.Is(err, bunker.ErrNoRelay) {
			return fmt.Errorf("nostrconnect: none of the URI's relays could be reached (%v): %w", err, ErrNoRelayReachable)
		}
		return fmt.Errorf("nostrconnect: %w", err)
	}

	// Register the pairing in Trusted Apps. The name/URL are the app's own
	// self-reported identity -- unauthenticated, and per NIP-46 a display
	// hint only, never an input to an authorization decision.
	var appName, appURL string
	if schema.Metadata != nil {
		appName, appURL = schema.Metadata.Name, schema.Metadata.Url
	}
	_ = d.cfg.Store.Pair(schema.ClientPublickey, appName, appURL)

	// Grants the URI itself asked for, then whatever --grants staged. The
	// spec file is the operator's own instruction, so it is applied second
	// and wins where the two overlap.
	now := time.Now()
	for _, g := range parsePerms(schema.Perms, now) {
		_ = d.cfg.Store.Remember(schema.ClientPublickey, g)
	}
	if spec != nil {
		for _, g := range spec.Resolve(now) {
			_ = d.cfg.Store.Remember(schema.ClientPublickey, g)
		}
		if spec.Nickname != "" {
			_, _ = d.cfg.Store.SetName(schema.ClientPublickey, spec.Nickname)
		}
	}

	confirmTimeout := d.cfg.NostrconnectConfirmTimeout
	if confirmTimeout <= 0 {
		confirmTimeout = nostrconnectConfirmTimeout
	}
	label := d.cfg.Store.Label(schema.ClientPublickey)
	if d.awaitFirstRequest(ctx, schema.ClientPublickey, confirmTimeout) {
		d.log("paired with %s via nostrconnect", label)
	} else {
		d.log("sent the nostrconnect pairing to %s; waiting for its first request", label)
	}
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
