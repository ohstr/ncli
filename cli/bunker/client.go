package bunker

import (
	"context"
	"errors"
	"time"

	"github.com/ohstr/nmilat/nip46"
)

// StatusInfo is a running daemon's summary -- what `ncli bunker status`
// and the TUI header show. IdentityName/IdentityNip05 are empty until (and
// unless) Daemon.fetchProfile resolves the identity's own kind:0 metadata
// from one of its relays -- callers fall back to a shortened IdentityPub
// until then.
type StatusInfo struct {
	IdentityPub   string `json:"identity_pub"`
	IdentityName  string `json:"identity_name,omitempty"`
	IdentityNip05 string `json:"identity_nip05,omitempty"`
	// VaultLabel is this identity's vault entry label, if it has one --
	// see DaemonConfig.VaultLabel's own doc comment for when that is.
	VaultLabel    string        `json:"vault_label,omitempty"`
	Relays        []string      `json:"relays"`
	RelayStatuses []RelayStatus `json:"relay_statuses"`
	PendingCount  int           `json:"pending_count"`
	SessionCount  int           `json:"session_count"`
	StartedAt     time.Time     `json:"started_at"`
}

// BunkerClient is what a TUI (board.go) or a one-shot CLI command
// (command.go: status/sessions/connect) needs from a running daemon,
// whether that daemon is a separate process reached over the Unix control
// socket (ipcClient, the Linux/macOS attach path) or the same process
// (localClient -- the Windows path, per the platform-scope decision, and
// what tests drive directly). board.go polls ListPending/ListSessions on
// a ticker (the same decoupled-render pattern client/tui/eventtable.go
// already uses) rather than requiring a push-event wire protocol -- one
// fewer moving part, and consistent with the rest of this codebase's TUI
// panels.
type BunkerClient interface {
	// Status reports the daemon's identity, relays, and current counts.
	Status() (StatusInfo, error)
	// ListPending returns every currently-pending approval request.
	ListPending() ([]Pending, error)
	// Approve/Reject resolve one pending request by ID. remember, if
	// non-nil, is also persisted to the policy Store against that
	// request's client app -- nil means "decide this one request only,
	// never persisted" (the plan's "Approve/Reject Once").
	Approve(id string, remember *Grant) error
	Reject(id string, remember *Grant) error
	// ListSessions/Revoke/SetName manage remembered per-app permissions.
	ListSessions() ([]Session, error)
	Revoke(pubkey string) (bool, error)
	// RevokeGrant removes exactly one grant (identified by method and,
	// for sign_event, kind -- nil kind means the any-kind grant for that
	// method, not "every kind") from pubkey's session, leaving the rest
	// untouched -- see Store.RevokeGrant's own doc comment. Reports false
	// if there was no such grant.
	RevokeGrant(pubkey, method string, kind *int) (bool, error)
	// SetGrant persists grant against pubkey directly, with no pending
	// request to resolve alongside it -- unlike Approve/Reject's remember
	// parameter. board.go's "Manage Grants" overlay uses this to re-scope
	// (extend the duration of, widen the budget of, ...) an existing
	// grant on demand; Store.Remember's own same-scope replacement is
	// what makes that just work without a separate revoke-first step.
	SetGrant(pubkey string, grant Grant) error
	// SetName sets (or, with name == "", clears) pubkey's user-assigned
	// nickname -- see Store.SetName's own doc comment.
	SetName(pubkey, name string) (bool, error)
	// History returns the daemon's most recent resolved requests, most
	// recent first -- board.go's HistoryTable ("REQUEST HISTORY") polls
	// this, complementing ListPending (only currently-unresolved
	// requests) with what actually happened to the ones that aren't
	// anymore.
	History() ([]HistoryEntry, error)
	// Connect starts a pairing: uri == "" generates and returns a fresh
	// bunker:// URI (the client-speaks-first flow); a nostrconnect://
	// URI instead initiates the signer-speaks-first handshake, blocking
	// until the client confirms or the attempt times out. spec (nil for
	// none -- every caller before `ncli bunker connect --grants` did)
	// is `--grants <file>`'s own already-loaded-and-validated GrantSpec
	// (grantspec.go), resolved into concrete Grants only once the app
	// this pairing is for actually connects -- see
	// Daemon.NewBunkerPairingWithGrants/InitiateNostrconnectWithGrants and
	// GrantSpec.Resolve's own doc comment on why that timing matters.
	Connect(uri string, spec *GrantSpec) (string, error)
	// Logs returns the daemon's recent activity-log lines -- board.go's
	// DaemonLogWatcher polls this to feed the Logger panel, the only way
	// that activity (relay connects, every request's method/from/id, a
	// rejected/mismatched pairing attempt, ...) reaches a TUI attached to
	// a separately-running background daemon over IPC.
	Logs() (LogSnapshot, error)
	// Stop gracefully shuts down the daemon.
	Stop() error
	// Close releases this client's own resources (e.g. its socket
	// connection) without affecting the daemon itself.
	Close() error
}

// ErrGrantWithoutPending is returned by Approve/Reject when remember is
// non-nil but id doesn't (or no longer does) name a pending request --
// there's no app pubkey left to record the grant against.
var ErrGrantWithoutPending = errors.New("bunker: no pending request to attribute the remembered grant to")

// localClient drives a *Daemon directly, in the same process -- no
// socket, no serialization. This is the Windows path (per the platform-
// scope decision: no background/attach there, so the TUI just talks to
// the daemon it's already running alongside) and the straightforward path
// for tests.
type localClient struct {
	daemon    *Daemon
	startedAt time.Time
	cancel    context.CancelFunc
}

// newLocalClient wraps daemon, whose Run(ctx) the caller has already
// started (or is about to -- cancel stops it, wired to Stop()).
func newLocalClient(daemon *Daemon, startedAt time.Time, cancel context.CancelFunc) *localClient {
	return &localClient{daemon: daemon, startedAt: startedAt, cancel: cancel}
}

func (c *localClient) Status() (StatusInfo, error) {
	name, nip05 := c.daemon.Profile()
	return StatusInfo{
		IdentityPub:   c.daemon.cfg.IdentityPub,
		IdentityName:  name,
		IdentityNip05: nip05,
		VaultLabel:    c.daemon.cfg.VaultLabel,
		Relays:        c.daemon.cfg.Relays,
		RelayStatuses: c.daemon.RelayStatuses(),
		PendingCount:  len(c.daemon.cfg.Queue.List()),
		SessionCount:  len(c.daemon.cfg.Store.List()),
		StartedAt:     c.startedAt,
	}, nil
}

func (c *localClient) ListPending() ([]Pending, error) {
	return c.daemon.cfg.Queue.List(), nil
}

func (c *localClient) resolve(id string, verdict Decision, remember *Grant) error {
	if remember != nil {
		pending, ok := c.daemon.cfg.Queue.Get(id)
		if !ok {
			return ErrGrantWithoutPending
		}
		if err := c.daemon.cfg.Store.Remember(pending.ClientKey, *remember); err != nil {
			return err
		}
	}
	return c.daemon.cfg.Queue.Resolve(id, verdict, remember != nil)
}

func (c *localClient) Approve(id string, remember *Grant) error {
	return c.resolve(id, Allow, remember)
}

func (c *localClient) Reject(id string, remember *Grant) error {
	return c.resolve(id, Deny, remember)
}

func (c *localClient) ListSessions() ([]Session, error) {
	return c.daemon.cfg.Store.List(), nil
}

func (c *localClient) History() ([]HistoryEntry, error) {
	return c.daemon.History(), nil
}

func (c *localClient) Revoke(pubkey string) (bool, error) {
	return c.daemon.cfg.Store.Revoke(pubkey)
}

func (c *localClient) RevokeGrant(pubkey, method string, kind *int) (bool, error) {
	return c.daemon.cfg.Store.RevokeGrant(pubkey, method, kind)
}

func (c *localClient) SetGrant(pubkey string, grant Grant) error {
	return c.daemon.cfg.Store.Remember(pubkey, grant)
}

func (c *localClient) SetName(pubkey, name string) (bool, error) {
	return c.daemon.cfg.Store.SetName(pubkey, name)
}

func (c *localClient) Connect(uri string, spec *GrantSpec) (string, error) {
	if uri == "" {
		return c.daemon.NewBunkerPairingWithGrants(spec)
	}
	schema, err := nip46.ParseNostrconnect(uri)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := c.daemon.InitiateNostrconnectWithGrants(ctx, schema, spec); err != nil {
		return "", err
	}
	return "paired", nil
}

func (c *localClient) Logs() (LogSnapshot, error) {
	return c.daemon.RecentLogs(), nil
}

func (c *localClient) Stop() error {
	if c.cancel != nil {
		c.cancel()
	}
	return nil
}

func (c *localClient) Close() error { return nil }
