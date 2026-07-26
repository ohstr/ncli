package bunker

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessions.yaml")
	s, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore() error = %v", err)
	}
	fc := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s.now = fc.Now
	return s, fc
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func kindPtr(k int) *int { return &k }

func TestDecide_NoSessionAsks(t *testing.T) {
	s, _ := newTestStore(t)
	if got := s.Decide("app1", "sign_event", 1); got != Ask {
		t.Errorf("Decide() = %v, want Ask", got)
	}
}

func TestDecide_ExactKindGrant(t *testing.T) {
	s, fc := newTestStore(t)
	if err := s.Remember("app1", GrantForever("sign_event", kindPtr(1), fc.Now())); err != nil {
		t.Fatal(err)
	}

	if got := s.Decide("app1", "sign_event", 1); got != Allow {
		t.Errorf("kind 1 = %v, want Allow", got)
	}
	// A different kind on the same app must still ask -- an exact-kind
	// grant never silently covers any other kind.
	if got := s.Decide("app1", "sign_event", 7); got != Ask {
		t.Errorf("kind 7 = %v, want Ask", got)
	}
}

func TestDecide_AnyKindGrant_ExcludesSensitiveKinds(t *testing.T) {
	s, fc := newTestStore(t)
	if err := s.Remember("app1", GrantForever("sign_event", nil, fc.Now())); err != nil {
		t.Fatal(err)
	}

	for _, k := range []int{1, 7, 30023} {
		if got := s.Decide("app1", "sign_event", k); got != Allow {
			t.Errorf("kind %d = %v, want Allow", k, got)
		}
	}
	for _, k := range []int{0, 3, 5} {
		if got := s.Decide("app1", "sign_event", k); got != Ask {
			t.Errorf("sensitive kind %d = %v, want Ask (any-kind grant must never cover it)", k, got)
		}
	}
}

func TestDecide_ExactGrantOnSensitiveKindIsHonored(t *testing.T) {
	s, fc := newTestStore(t)
	// An explicit, deliberate grant naming kind 0 IS honored -- the
	// sensitive-kind guard only blocks an *any-kind* grant from silently
	// covering it.
	if err := s.Remember("app1", GrantForever("sign_event", kindPtr(0), fc.Now())); err != nil {
		t.Fatal(err)
	}
	if got := s.Decide("app1", "sign_event", 0); got != Allow {
		t.Errorf("explicit kind-0 grant = %v, want Allow", got)
	}
}

func TestDecide_TimeLimitedGrantExpires(t *testing.T) {
	s, fc := newTestStore(t)
	if err := s.Remember("app1", GrantForDuration("sign_event", nil, time.Hour, fc.Now())); err != nil {
		t.Fatal(err)
	}

	if got := s.Decide("app1", "sign_event", 1); got != Allow {
		t.Errorf("before expiry = %v, want Allow", got)
	}

	fc.Advance(2 * time.Hour)
	if got := s.Decide("app1", "sign_event", 1); got != Ask {
		t.Errorf("after expiry = %v, want Ask", got)
	}
}

func TestDecide_BudgetLimitedGrantExhausts(t *testing.T) {
	s, fc := newTestStore(t)
	if err := s.Remember("app1", GrantForUses("sign_event", nil, 3, fc.Now())); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if got := s.Decide("app1", "sign_event", 1); got != Allow {
			t.Fatalf("use %d = %v, want Allow", i, got)
		}
	}
	if got := s.Decide("app1", "sign_event", 1); got != Ask {
		t.Errorf("use 4 = %v, want Ask (budget exhausted)", got)
	}
}

func TestDecide_BudgetLimitedGrant_ConcurrentNeverOverspends(t *testing.T) {
	s, fc := newTestStore(t)
	const budget = 10
	if err := s.Remember("app1", GrantForUses("sign_event", nil, budget, fc.Now())); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < budget*3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.Decide("app1", "sign_event", 1) == Allow {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowed != budget {
		t.Errorf("allowed = %d, want exactly %d (budget must never be over-spent)", allowed, budget)
	}
}

func TestDecide_DenyAlways(t *testing.T) {
	s, fc := newTestStore(t)
	if err := s.Remember("app1", DenyAlways("sign_event", fc.Now())); err != nil {
		t.Fatal(err)
	}
	if got := s.Decide("app1", "sign_event", 1); got != Deny {
		t.Errorf("Decide() = %v, want Deny", got)
	}
}

func TestRevoke(t *testing.T) {
	s, fc := newTestStore(t)
	if err := s.Remember("app1", GrantForever("sign_event", nil, fc.Now())); err != nil {
		t.Fatal(err)
	}

	ok, err := s.Revoke("app1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Revoke() = false, want true")
	}
	if got := s.Decide("app1", "sign_event", 1); got != Ask {
		t.Errorf("after revoke = %v, want Ask", got)
	}

	ok, err = s.Revoke("app1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("second Revoke() = true, want false (nothing left to revoke)")
	}
}

// TestRevokeGrant_LeavesOtherGrantsAlone guards the whole reason
// RevokeGrant exists apart from Revoke: pulling one permission back must
// never touch any other one the same app holds, or the session itself.
func TestRevokeGrant_LeavesOtherGrantsAlone(t *testing.T) {
	s, fc := newTestStore(t)
	kind1 := 1
	if err := s.Remember("app1", GrantForever("sign_event", &kind1, fc.Now())); err != nil {
		t.Fatal(err)
	}
	if err := s.Remember("app1", GrantForever("ping", nil, fc.Now())); err != nil {
		t.Fatal(err)
	}

	ok, err := s.RevokeGrant("app1", "sign_event", &kind1)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("RevokeGrant() = false, want true")
	}

	if got := s.Decide("app1", "sign_event", 1); got != Ask {
		t.Errorf("sign_event kind 1 after RevokeGrant = %v, want Ask", got)
	}
	if got := s.Decide("app1", "ping", 0); got != Allow {
		t.Errorf("ping after RevokeGrant(sign_event) = %v, want Allow (untouched)", got)
	}

	sessions := s.List()
	if len(sessions) != 1 {
		t.Fatalf("List() = %+v, want the session to survive (only one of its grants was revoked)", sessions)
	}
}

// TestRevokeGrant_KindNilMeansAnyKindGrant guards RevokeGrant's own scope
// matching: kind=nil must target the any-kind grant specifically, not
// "any grant regardless of kind" -- an exact-kind grant for the same
// method must survive untouched.
func TestRevokeGrant_KindNilMeansAnyKindGrant(t *testing.T) {
	s, fc := newTestStore(t)
	kind1 := 1
	if err := s.Remember("app1", GrantForever("sign_event", &kind1, fc.Now())); err != nil {
		t.Fatal(err)
	}
	if err := s.Remember("app1", GrantForever("sign_event", nil, fc.Now())); err != nil {
		t.Fatal(err)
	}

	ok, err := s.RevokeGrant("app1", "sign_event", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("RevokeGrant(kind=nil) = false, want true")
	}

	if got := s.Decide("app1", "sign_event", 1); got != Allow {
		t.Errorf("kind 1 after revoking the any-kind grant = %v, want Allow (its own exact-kind grant is untouched)", got)
	}
	if got := s.Decide("app1", "sign_event", 7); got != Ask {
		t.Errorf("kind 7 after revoking the any-kind grant = %v, want Ask", got)
	}
}

// TestRevokeGrant_NoSuchGrantOrSession reports false, not an error, for
// both an unknown pubkey and a known one with no matching scope.
func TestRevokeGrant_NoSuchGrantOrSession(t *testing.T) {
	s, fc := newTestStore(t)
	if err := s.Remember("app1", GrantForever("ping", nil, fc.Now())); err != nil {
		t.Fatal(err)
	}

	ok, err := s.RevokeGrant("app1", "sign_event", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("RevokeGrant() for a scope app1 never had = true, want false")
	}

	ok, err = s.RevokeGrant("never-paired", "ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("RevokeGrant() for an unknown pubkey = true, want false")
	}
}

func TestSetName(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.Pair("app1", "Damus", "https://damus.io"); err != nil {
		t.Fatal(err)
	}

	ok, err := s.SetName("app1", "My Wallet")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("SetName() = false, want true")
	}
	sessions := s.List()
	if len(sessions) != 1 || sessions[0].Nickname != "My Wallet" {
		t.Fatalf("session after SetName = %+v, want Nickname %q", sessions, "My Wallet")
	}

	// Clearing with "" reverts to the self-reported-name fallback --
	// labelFor's own chain, not SetName's problem to enforce, but SetName
	// must still actually blank the field out rather than refusing "".
	ok, err = s.SetName("app1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("SetName(\"\") = false, want true")
	}
	sessions = s.List()
	if len(sessions) != 1 || sessions[0].Nickname != "" {
		t.Fatalf("session after clearing = %+v, want empty Nickname", sessions)
	}

	ok, err = s.SetName("never-paired", "Anything")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("SetName() on unknown pubkey = true, want false")
	}
}

func TestLabelFor(t *testing.T) {
	tests := []struct {
		name string
		s    Session
		want string
	}{
		{
			name: "nickname wins over self-reported name",
			s:    Session{Pubkey: "abc123def456", Nickname: "My Wallet", AppName: "Damus"},
			want: "My Wallet",
		},
		{
			name: "self-reported name wins over pubkey",
			s:    Session{Pubkey: "abc123def456", AppName: "Damus", AppURL: "https://damus.io"},
			want: "Damus (https://damus.io)",
		},
		{
			name: "falls back to shortHex(pubkey) when neither is set",
			s:    Session{Pubkey: "abc123def456"},
			want: shortHex("abc123def456"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := labelFor(tt.s); got != tt.want {
				t.Errorf("labelFor() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStoreLabel(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.Pair("app1", "Damus", "https://damus.io"); err != nil {
		t.Fatal(err)
	}

	if got, want := s.Label("app1"), "Damus (https://damus.io)"; got != want {
		t.Errorf("Label() before SetName = %q, want %q", got, want)
	}

	if _, err := s.SetName("app1", "My Wallet"); err != nil {
		t.Fatal(err)
	}
	if got, want := s.Label("app1"), "My Wallet"; got != want {
		t.Errorf("Label() after SetName = %q, want %q", got, want)
	}

	if got, want := s.Label("never-paired"), shortHex("never-paired"); got != want {
		t.Errorf("Label() on unknown pubkey = %q, want %q", got, want)
	}
}

func TestStore_PersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.yaml")
	s, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	if err := s.Remember("app1", GrantForDuration("sign_event", kindPtr(1), time.Hour, now)); err != nil {
		t.Fatal(err)
	}
	if err := s.Remember("app1", GrantForUses("nip04_encrypt", nil, 5, now)); err != nil {
		t.Fatal(err)
	}
	if err := s.Remember("app2", DenyAlways("sign_event", now)); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.now = func() time.Time { return now }

	if got := reloaded.Decide("app1", "sign_event", 1); got != Allow {
		t.Errorf("app1 sign_event kind 1 after reload = %v, want Allow", got)
	}
	if got := reloaded.Decide("app1", "sign_event", 2); got != Ask {
		t.Errorf("app1 sign_event kind 2 after reload = %v, want Ask", got)
	}
	if got := reloaded.Decide("app2", "sign_event", 1); got != Deny {
		t.Errorf("app2 after reload = %v, want Deny", got)
	}
}

func TestRemember_ReplacesSameScope(t *testing.T) {
	s, fc := newTestStore(t)
	if err := s.Remember("app1", GrantForUses("sign_event", nil, 1, fc.Now())); err != nil {
		t.Fatal(err)
	}
	// Re-granting the same (method, kind) scope should replace, not
	// accumulate -- otherwise the earlier (now exhausted) grant would keep
	// shadowing the new one.
	if err := s.Remember("app1", GrantForever("sign_event", nil, fc.Now())); err != nil {
		t.Fatal(err)
	}

	sessions := s.List()
	if len(sessions) != 1 || len(sessions[0].Grants) != 1 {
		t.Fatalf("List() = %+v, want exactly one session with one grant", sessions)
	}
}

// TestPrune_KeepsSessionButDropsExpiredGrants guards the Trusted Apps fix:
// a session's last grant lapsing must not make the app vanish from the
// list entirely -- it stays (with an empty Grants, same as a freshly
// Store.Pair-ed app) until a human explicitly Revokes it. Prune() used to
// delete the whole session the moment its Grants emptied out; see
// Store.Pair's own doc comment for why that would have silently undone
// this fix on the very next sweep.
func TestPrune_KeepsSessionButDropsExpiredGrants(t *testing.T) {
	s, fc := newTestStore(t)
	if err := s.Remember("app1", GrantForDuration("sign_event", nil, time.Hour, fc.Now())); err != nil {
		t.Fatal(err)
	}

	fc.Advance(2 * time.Hour)
	s.Prune()

	sessions := s.List()
	if len(sessions) != 1 {
		t.Fatalf("List() after Prune() = %+v, want exactly one session (grant gone, pairing kept)", sessions)
	}
	if len(sessions[0].Grants) != 0 {
		t.Errorf("sessions[0].Grants = %+v, want empty (the lapsed grant should be gone)", sessions[0].Grants)
	}
	if got := s.Decide("app1", "sign_event", 1); got != Ask {
		t.Errorf("Decide() after expiry = %v, want Ask", got)
	}
}

// TestPair_RegistersZeroGrantSession guards the Trusted Apps fix itself:
// Store.Pair alone (no Remember call at all -- the "Approve Once every
// time" app) must still make the app show up in List(), with an empty
// Grants (summarizeGrants renders that as "-" in the TUI/CLI).
func TestPair_RegistersZeroGrantSession(t *testing.T) {
	s, fc := newTestStore(t)
	if err := s.Pair("app1", "", ""); err != nil {
		t.Fatal(err)
	}

	sessions := s.List()
	if len(sessions) != 1 || sessions[0].Pubkey != "app1" {
		t.Fatalf("List() = %+v, want exactly one session for app1", sessions)
	}
	if len(sessions[0].Grants) != 0 {
		t.Errorf("Grants = %+v, want empty -- Pair alone remembers no permission", sessions[0].Grants)
	}
	if !sessions[0].PairedAt.Equal(fc.Now()) {
		t.Errorf("PairedAt = %v, want %v (the store's own clock at Pair time)", sessions[0].PairedAt, fc.Now())
	}
	// Pairing alone never resolves a decision -- only Remember does.
	if got := s.Decide("app1", "sign_event", 1); got != Ask {
		t.Errorf("Decide() after Pair-only = %v, want Ask", got)
	}
}

// TestPair_CapturesAppNameAndURL guards the nostrconnect:// direction's
// own reason for Pair's extra params -- daemon.go's InitiateNostrconnect
// threads schema.Metadata.Name/Url straight through.
func TestPair_CapturesAppNameAndURL(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.Pair("app1", "Damus", "https://damus.io"); err != nil {
		t.Fatal(err)
	}

	sessions := s.List()
	if len(sessions) != 1 || sessions[0].AppName != "Damus" || sessions[0].AppURL != "https://damus.io" {
		t.Fatalf("List() = %+v, want AppName=Damus AppURL=https://damus.io", sessions)
	}
}

// TestPair_DoesNotBackfillMetadataOnAlreadyKnownApp guards the
// deliberate one-shot-at-creation-only rule Store.Pair's own doc comment
// describes: a second Pair call for the same pubkey must not overwrite
// (or add) app name/URL on a session that already exists, even if this
// call happens to carry metadata the first one didn't.
func TestPair_DoesNotBackfillMetadataOnAlreadyKnownApp(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.Pair("app1", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Pair("app1", "Damus", "https://damus.io"); err != nil {
		t.Fatal(err)
	}

	sessions := s.List()
	if len(sessions) != 1 || sessions[0].AppName != "" || sessions[0].AppURL != "" {
		t.Fatalf("List() = %+v, want AppName/AppURL still empty (Pair is a no-op on an already-known pubkey)", sessions)
	}
}

// TestPair_NoopIfAlreadyKnown guards against Pair clobbering an existing
// session's remembered grants -- e.g. a client reconnecting after already
// having been granted "Always" for something.
func TestPair_NoopIfAlreadyKnown(t *testing.T) {
	s, fc := newTestStore(t)
	if err := s.Remember("app1", GrantForever("sign_event", nil, fc.Now())); err != nil {
		t.Fatal(err)
	}

	if err := s.Pair("app1", "", ""); err != nil {
		t.Fatal(err)
	}

	sessions := s.List()
	if len(sessions) != 1 || len(sessions[0].Grants) != 1 {
		t.Fatalf("List() = %+v, want the one pre-existing grant untouched", sessions)
	}
}

// TestStore_PairPersistsAcrossReload guards saveLocked's own half of the
// fix: a paired-but-grantless session must survive a reload, the same way
// a remembered grant already does, not require the app to reconnect
// before it reappears in Trusted Apps.
func TestStore_PairPersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.yaml")
	s, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Pair("app1", "", ""); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	sessions := reloaded.List()
	if len(sessions) != 1 || sessions[0].Pubkey != "app1" {
		t.Fatalf("List() after reload = %+v, want the paired app1 session", sessions)
	}
}

// TestStore_SetNamePersistsAcrossReload mirrors
// TestStore_PairPersistsAcrossReload for Nickname: SetName's saveLocked
// call must survive a reload the same way Pair's does.
func TestStore_SetNamePersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.yaml")
	s, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Pair("app1", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetName("app1", "My Wallet"); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	sessions := reloaded.List()
	if len(sessions) != 1 || sessions[0].Nickname != "My Wallet" {
		t.Fatalf("List() after reload = %+v, want Nickname %q", sessions, "My Wallet")
	}
}
