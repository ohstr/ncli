package bunker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/nmilat/nip46"
	"sigs.k8s.io/yaml"
)

const sessionsFileName = "sessions.yaml"

// Decision is Store.Decide's verdict for one incoming request.
type Decision int

const (
	Ask Decision = iota
	Allow
	Deny
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Deny:
		return "deny"
	default:
		return "ask"
	}
}

// sensitiveKinds are never covered by an any-kind grant -- only a grant that
// names one of them explicitly. Metadata (0), contacts (3), and deletion
// (5): a broad "always allow sign_event" click must never start silently
// covering identity or contact-list rewrites too.
var sensitiveKinds = map[int]bool{0: true, 3: true, 5: true}

// Grant is one remembered permission for a client app (identified by its
// pubkey) to skip the approval prompt for future matching requests. It's
// the product of three independent axes: scope (Method, and for
// sign_event, optionally one specific Kind -- nil means "any kind"),
// duration (ExpiresAt, nil means no time limit), and budget
// (RemainingUses, nil means unlimited). Verdict is Allow or Deny; a Grant
// is never stored for Ask.
type Grant struct {
	Method        string     `json:"method" yaml:"method"`
	Verdict       Decision   `json:"verdict" yaml:"verdict"`
	Kind          *int       `json:"kind,omitempty" yaml:"kind,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
	RemainingUses *int       `json:"remaining_uses,omitempty" yaml:"remaining_uses,omitempty"`
	CreatedAt     time.Time  `json:"created_at" yaml:"created_at"`
}

// expired reports whether g's validity window has passed as of now -- a
// purely time-based check; budget exhaustion (RemainingUses reaching zero)
// is handled separately by resolveGrant, since that mutates state rather
// than just observing it.
func (g *Grant) expired(now time.Time) bool {
	return g.ExpiresAt != nil && !now.Before(*g.ExpiresAt)
}

// matches reports whether g covers a request for method/kind -- an exact
// match if g.Kind names a specific kind, or (for a nil/"any kind" grant) a
// match for every kind except the always-sensitive ones, which only an
// exact-kind grant may cover. The sensitive-kind exclusion only applies to
// sign_event -- kind is meaningless for every other method (Handle always
// passes 0 for them), so an any-scope "connect"/"ping"/... grant must
// match unconditionally, not get blocked because 0 happens to also be
// kind 0 (metadata)'s value.
func (g *Grant) matches(method string, kind int, exactOnly bool) bool {
	if g.Method != method {
		return false
	}
	if g.Kind != nil {
		return *g.Kind == kind
	}
	if exactOnly {
		return false
	}
	if method == nip46.MethodSignEvent && sensitiveKinds[kind] {
		return false
	}
	return true
}

// Session is one paired app, keyed by its pubkey, plus whatever it's been
// remembered to have (Grants, possibly empty -- see Store.Pair). A session
// with no grants still belongs in the Trusted Apps list: it completed the
// NIP-46 handshake and is actively talking to this signer, it just hasn't
// had any specific permission remembered for it yet (every request still
// prompts).
type Session struct {
	Pubkey string `json:"pubkey" yaml:"pubkey"`
	// AppName/AppURL are the client's own self-reported nip46.Metadata
	// (Name/Url), captured once at pairing time -- never verified, and
	// only ever populated for a nostrconnect:// pairing (the only
	// direction that carries any app metadata at the protocol level at
	// all; a bunker:// pairing always leaves these blank -- see
	// Store.Pair's own doc comment). Not kept fresh afterward: if the app
	// changes its own metadata later, this still shows whatever it said
	// the moment it first paired.
	AppName string `json:"app_name,omitempty" yaml:"app_name,omitempty"`
	AppURL  string `json:"app_url,omitempty" yaml:"app_url,omitempty"`
	// Nickname is the user's own label for this app (SessionsTable's "n"
	// shortcut/Store.SetName, `sessions rename` on the CLI side) --
	// unlike AppName, which is the app's own self-reported, never-
	// verified claim, this is something the operator typed in
	// themselves specifically so they can tell two same-named or
	// unnamed (bunker://) apps apart. labelFor prefers this over AppName
	// over a raw shortHex(Pubkey) wherever a trusted app is displayed to
	// a human, so setting it changes what every one of those views shows
	// at once. Empty means "not set" -- falls through to the rest of
	// labelFor's own chain.
	Nickname string `json:"nickname,omitempty" yaml:"nickname,omitempty"`
	// PairedAt is when this Session was first created (Store.Pair or
	// Store.Remember, whichever ran first) -- "Trusted Since" in
	// board.go's SessionsTable. Zero for a session predating this field
	// (an old sessions.yaml written before it existed); formatElapsed's
	// own zero-value handling covers that, not this type.
	PairedAt time.Time `json:"paired_at" yaml:"paired_at"`
	Grants   []Grant   `json:"grants,omitempty" yaml:"grants,omitempty"`
}

type sessionsFile struct {
	Sessions []Session `json:"sessions,omitempty" yaml:"sessions,omitempty"`
}

// Store is the daemon's remembered-permission engine: per-app grants,
// loaded fully into memory at startup and persisted to a single YAML file
// on every change -- the same Load/Save-whole-file convention as
// client/vault.go/client/prefs.go, not bbolt, since this dataset is
// config-shaped (a human's list of trusted apps, realistically dozens of
// rows): an in-memory map gives O(1) lookups on Decide's hot path with no
// disk I/O for the common (non-budget-limited) case, and a full-file
// rewrite on the rare write is cheap.
type Store struct {
	path  string
	mu    sync.Mutex
	byPub map[string]*Session
	now   func() time.Time
}

// SessionsPath returns the OS-appropriate path to sessions.yaml, under
// ncli's shared bunker state directory.
func SessionsPath() string {
	return filepath.Join(common.AppConfigDir(), "bunker", sessionsFileName)
}

// LoadStore reads path (creating no file yet if it doesn't exist -- an
// empty Store, not an error, matching prefs.yaml's "missing means empty"
// convention) into memory.
func LoadStore(path string) (*Store, error) {
	s := &Store{path: path, byPub: map[string]*Session{}, now: time.Now}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	} else if err != nil {
		return nil, err
	}

	var sf sessionsFile
	if err := yaml.UnmarshalStrict(data, &sf); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	for i := range sf.Sessions {
		sess := sf.Sessions[i]
		s.byPub[sess.Pubkey] = &sess
	}
	return s, nil
}

// saveLocked writes every session to s.path -- caller must hold s.mu.
// Every known pubkey is persisted, including one with zero current grants
// (Store.Pair's paired-but-nothing-remembered-yet state): Trusted Apps is
// meant to survive a daemon restart the same way a remembered grant
// already does, not silently forget a pairing just because no permission
// happens to be attached to it right now.
func (s *Store) saveLocked() error {
	sf := sessionsFile{}
	for _, sess := range s.byPub {
		sf.Sessions = append(sf.Sessions, *sess)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(&sf)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

// Decide is the hot path: every incoming NIP-46 request from pubkey
// consults it exactly once. kind is only meaningful for method ==
// "sign_event" (pass 0 otherwise -- no grant is ever stored with a non-nil
// Kind for any other method, so it never affects matching). An exact-kind
// grant is checked before an any-kind one, and consuming a budget-limited
// grant (or letting a time-limited one lapse) happens atomically under the
// same lock so concurrent requests can never over-spend a "next N uses"
// grant past zero.
func (s *Store) Decide(pubkey, method string, kind int) Decision {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.byPub[pubkey]
	if !ok {
		return Ask
	}

	now := s.now()
	s.pruneSessionLocked(sess, now)

	if idx := indexOfMatch(sess.Grants, method, kind, true); idx >= 0 {
		return s.resolveGrantLocked(sess, idx)
	}
	if idx := indexOfMatch(sess.Grants, method, kind, false); idx >= 0 {
		return s.resolveGrantLocked(sess, idx)
	}
	return Ask
}

func indexOfMatch(grants []Grant, method string, kind int, exactOnly bool) int {
	for i := range grants {
		if grants[i].matches(method, kind, exactOnly) {
			return i
		}
	}
	return -1
}

// resolveGrantLocked returns idx's verdict, decrementing/removing it if
// budget-limited. Caller must hold s.mu.
func (s *Store) resolveGrantLocked(sess *Session, idx int) Decision {
	g := &sess.Grants[idx]
	verdict := g.Verdict

	if g.RemainingUses != nil {
		remaining := *g.RemainingUses - 1
		if remaining <= 0 {
			sess.Grants = append(sess.Grants[:idx], sess.Grants[idx+1:]...)
		} else {
			g.RemainingUses = &remaining
		}
		s.saveLocked()
	}

	return verdict
}

// pruneSessionLocked drops every expired grant from sess. Caller must hold
// s.mu; does not persist by itself -- callers that mutate state
// (resolveGrantLocked, Prune) save afterward.
func (s *Store) pruneSessionLocked(sess *Session, now time.Time) bool {
	kept := sess.Grants[:0]
	changed := false
	for _, g := range sess.Grants {
		if g.expired(now) {
			changed = true
			continue
		}
		kept = append(kept, g)
	}
	sess.Grants = kept
	return changed
}

// Prune drops every expired grant across every session and persists the
// result -- called periodically (see queue.go's sweep goroutine) so a
// grant that lapses is cleaned up even if that app never reconnects to
// trigger Decide's own lazy prune. Unlike grants, a session itself is
// never dropped just because its last grant expired: it stays listed in
// Trusted Apps (with an empty Grants, same as a freshly-paired app that
// was never granted anything) until a human explicitly revokes it --
// see Store.Revoke.
func (s *Store) Prune() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	changed := false
	for _, sess := range s.byPub {
		if s.pruneSessionLocked(sess, now) {
			changed = true
		}
	}
	if changed {
		s.saveLocked()
	}
}

// Pair registers pubkey as a known/trusted app -- called once its
// "connect" request actually completes (handler.go's execute, or
// daemon.go's InitiateNostrconnect for the signer-speaks-first direction),
// independent of whether any specific permission was ever remembered for
// it via Remember. Without this, an app that's only ever been
// "Approve Once"-d never shows up in the Trusted Apps list at all, even
// though it successfully paired and is actively exchanging requests with
// the signer -- see board.go's SessionsTable ("TRUSTED APPS"). A no-op if
// pubkey is already known, whether from an earlier pairing or because it
// already holds a remembered grant -- appName/appURL are NOT backfilled
// onto an already-known session even if this call happens to carry them
// and the existing one doesn't (e.g. a bunker:// pairing, no metadata at
// all, followed later by a nostrconnect:// reconnect that does carry
// some): the same one-shot-at-creation-only rule Session.AppName's own
// doc comment describes, just also applying across repeated Pair calls,
// not only after the session already exists some other way.
func (s *Store) Pair(pubkey, appName, appURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.byPub[pubkey]; ok {
		return nil
	}
	s.byPub[pubkey] = &Session{Pubkey: pubkey, AppName: appName, AppURL: appURL, PairedAt: s.now()}
	return s.saveLocked()
}

// Remember persists grant for pubkey, appending to any existing grants for
// that app. Replaces an existing grant with the identical (Method, Kind)
// scope, if any, rather than accumulating duplicates that would otherwise
// shadow each other unpredictably.
func (s *Store) Remember(pubkey string, grant Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.byPub[pubkey]
	if !ok {
		// Reached in practice essentially only for a pubkey Pair never
		// saw first -- shouldn't happen given "connect" is always a
		// NIP-46 session's first request, but this is still the
		// session-creation path structurally, so PairedAt is set here
		// too rather than leaving it a real Pair call would always set.
		sess = &Session{Pubkey: pubkey, PairedAt: s.now()}
		s.byPub[pubkey] = sess
	}

	replaced := false
	for i := range sess.Grants {
		if sameScope(sess.Grants[i], grant) {
			sess.Grants[i] = grant
			replaced = true
			break
		}
	}
	if !replaced {
		sess.Grants = append(sess.Grants, grant)
	}

	return s.saveLocked()
}

func sameScope(a, b Grant) bool {
	if a.Method != b.Method {
		return false
	}
	if (a.Kind == nil) != (b.Kind == nil) {
		return false
	}
	return a.Kind == nil || *a.Kind == *b.Kind
}

// Revoke removes every grant for pubkey, reporting false if it had none.
func (s *Store) Revoke(pubkey string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.byPub[pubkey]; !ok {
		return false, nil
	}
	delete(s.byPub, pubkey)
	return true, s.saveLocked()
}

// RevokeGrant removes exactly one grant -- identified by (method, kind),
// the same scope sameScope/Remember already key off of -- from pubkey's
// session, leaving every other grant (and the session itself) untouched.
// Reports false if pubkey isn't known or has no grant with that exact
// scope. This is board.go's per-grant "Manage Grants" overlay and
// `ncli bunker sessions revoke-grant`'s own primitive -- the surgical
// counterpart to Revoke's all-or-nothing wipe.
func (s *Store) RevokeGrant(pubkey, method string, kind *int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.byPub[pubkey]
	if !ok {
		return false, nil
	}
	target := Grant{Method: method, Kind: kind}
	for i := range sess.Grants {
		if sameScope(sess.Grants[i], target) {
			sess.Grants = append(sess.Grants[:i], sess.Grants[i+1:]...)
			return true, s.saveLocked()
		}
	}
	return false, nil
}

// SetName sets pubkey's Nickname, reporting false if pubkey isn't a known
// session -- no auto-creation, unlike Remember: there's no legitimate
// path that renames a pubkey Pair/Remember hasn't already created. name
// == "" clears it, reverting that app's display back to labelFor's own
// self-reported-name/pubkey fallback.
func (s *Store) SetName(pubkey, name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.byPub[pubkey]
	if !ok {
		return false, nil
	}
	sess.Nickname = name
	return true, s.saveLocked()
}

// labelFor is how a human should see a trusted app identified, in every
// TUI table/dialog, CLI text output, and daemon log line in this
// package: the user's own Nickname if they've set one, else the app's
// self-reported name (+URL, via summarizeApp) if it gave one at
// pairing, else its shortened pubkey. Takes an already-fetched Session
// rather than a Store lookup so it works equally from board.go's
// tables, which only ever reach a Session through BunkerClient/IPC --
// including, after `ncli bunker attach`, a daemon running in a
// different process where a *Store isn't reachable at all.
func labelFor(s Session) string {
	if s.Nickname != "" {
		return s.Nickname
	}
	if name := summarizeApp(s); name != "-" {
		return name
	}
	return shortHex(s.Pubkey)
}

// Label is Store's own in-process counterpart to labelFor, for the
// handful of call sites (daemon.go's own log lines) that hold a *Store
// directly and only have a raw pubkey, not an already-fetched Session.
// Falls back to shortHex(pubkey) if pubkey isn't a known session at all.
func (s *Store) Label(pubkey string) string {
	s.mu.Lock()
	sess, ok := s.byPub[pubkey]
	s.mu.Unlock()
	if !ok {
		return shortHex(pubkey)
	}
	return labelFor(*sess)
}

// List returns a snapshot of every session, sorted by pubkey for a stable
// display order.
func (s *Store) List() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Session, 0, len(s.byPub))
	for _, sess := range s.byPub {
		out = append(out, *sess)
	}
	return out
}

// Grant constructors -- the fixed set of choices board.go's step-2 dialog
// offers, plus GrantForever for programmatic/CLI callers. now is the
// grant's CreatedAt. "Approve/Reject once" never reaches these: a one-off
// decision answers the single pending request directly and is never
// persisted to the Store at all.
func newGrant(method string, verdict Decision, kind *int, now time.Time) Grant {
	return Grant{Method: method, Verdict: verdict, Kind: kind, CreatedAt: now}
}

func GrantForDuration(method string, kind *int, dur time.Duration, now time.Time) Grant {
	exp := now.Add(dur)
	g := newGrant(method, Allow, kind, now)
	g.ExpiresAt = &exp
	return g
}

func GrantForUses(method string, kind *int, uses int, now time.Time) Grant {
	g := newGrant(method, Allow, kind, now)
	g.RemainingUses = &uses
	return g
}

func GrantForever(method string, kind *int, now time.Time) Grant {
	return newGrant(method, Allow, kind, now)
}

func DenyAlways(method string, now time.Time) Grant {
	return newGrant(method, Deny, nil, now)
}
