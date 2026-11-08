package relay

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip09"
	"github.com/ohstr/nmilat/nip43"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/utils"
	"github.com/rs/zerolog/log"
)

// membershipAdmin is the transport-free core behind every way of
// administering NIP-43 membership: the /admin/membership REST routes and the
// NIP-86 methods both call these, so the two surfaces cannot drift apart.
//
// Every write goes through ws.Membership() -- the same MembershipService each
// live Session consults -- never the store directly, or the running relay's
// in-memory cache desyncs from what an admin just wrote.
type membershipAdmin struct {
	ws    *relay.SessionHandler
	store *relay.EventStore
	// rooms is nil unless huddle audio is enabled. Held so removing a member
	// can end its live calls.
	rooms *room.Manager
}

// adminInputError marks a failure caused by what the caller sent rather than by
// the relay. The REST surface answers it with 400 and NIP-86 reports it in the
// response's error field; neither should call it a 500.
type adminInputError struct{ msg string }

func (e adminInputError) Error() string { return e.msg }

// errNotFound is a referenced thing (an invite code) that doesn't exist.
var errNotFound = errors.New("not found")

func badInput(format string, args ...any) error {
	return adminInputError{msg: fmt.Sprintf(format, args...)}
}

// isBadInput reports whether err came from the caller's input.
func isBadInput(err error) bool {
	var target adminInputError
	return errors.As(err, &target)
}

// checkPubkey rejects a malformed pubkey, so a typo never becomes a "member"
// that can never authenticate as itself.
func checkPubkey(pubkey string) error {
	if err := utils.Validate32Key(pubkey); err != nil {
		return badInput("invalid pubkey: %v", err)
	}
	return nil
}

/////////////////////////////////////////////////////////////////////
// Members
/////////////////////////////////////////////////////////////////////

func (a membershipAdmin) listMembers() ([]*relay.MemberRecord, error) {
	records, err := a.ws.Membership().List()
	if err != nil {
		return nil, err
	}
	if records == nil {
		records = []*relay.MemberRecord{}
	}
	return records, nil
}

// getMember returns (nil, nil) when pubkey is not a member: absence is a normal
// outcome, and each transport says so in its own vocabulary.
func (a membershipAdmin) getMember(pubkey string) (*relay.MemberRecord, error) {
	if err := checkPubkey(pubkey); err != nil {
		return nil, err
	}
	return a.ws.Membership().Get(pubkey)
}

func (a membershipAdmin) addMember(ctx context.Context, pubkey string, roles []string) (*relay.MemberRecord, error) {
	if err := checkPubkey(pubkey); err != nil {
		return nil, err
	}
	if err := a.ws.Membership().Join(pubkey, canonicalRoles(roles)); err != nil {
		return nil, err
	}
	if config.Membership.PublishAddRemoveEvents {
		publishMembershipAdminEvent(ctx, a.store, nip43.NewAddUser(config.Nip11.PubKey, pubkey))
	}
	return a.ws.Membership().Get(pubkey)
}

func (a membershipAdmin) removeMember(ctx context.Context, pubkey string) error {
	if err := checkPubkey(pubkey); err != nil {
		return err
	}
	if err := a.ws.Membership().Leave(pubkey); err != nil {
		return err
	}
	if config.Membership.PublishAddRemoveEvents {
		publishMembershipAdminEvent(ctx, a.store, nip43.NewRemoveUser(config.Nip11.PubKey, pubkey))
	}
	a.evictFromHuddles(pubkey)
	return nil
}

// evictFromHuddles ends pubkey's live calls. The huddle door checks admission
// once, at join, so without this a removed member keeps hearing the room until
// it chooses to reconnect.
func (a membershipAdmin) evictFromHuddles(pubkey string) {
	if a.rooms == nil {
		return
	}
	const revoked = `{"type":"error","code":"join_rejected","message":"membership revoked"}`
	if n := a.rooms.EvictPubkey(pubkey, revoked); n > 0 {
		log.Info().Str("pubkey", pubkey).Int("peers", n).Msg("removed member evicted from live huddles")
	}
}

/////////////////////////////////////////////////////////////////////
// Invites
/////////////////////////////////////////////////////////////////////

func (a membershipAdmin) issueInvite(ttl time.Duration, maxUses int, roles []string) (*relay.InviteClaim, error) {
	if maxUses < 0 {
		return nil, badInput("max_uses must be >= 0")
	}
	return a.ws.Membership().IssueInvite(ttl, maxUses, canonicalRoles(roles))
}

// issueInviteCode issues an invite under a caller-chosen code, which is what
// NIP-86's createclaim passes; an empty code falls back to issueInvite and lets
// the relay generate one, as the REST route does.
func (a membershipAdmin) issueInviteCode(code string, ttl time.Duration, maxUses int, roles []string) (*relay.InviteClaim, error) {
	if code == "" {
		return a.issueInvite(ttl, maxUses, roles)
	}
	if maxUses < 0 {
		return nil, badInput("max_uses must be >= 0")
	}

	// Refuse to overwrite a live code: silently replacing it would reset the
	// use count and expiry of an invite already handed out.
	existing, err := a.store.GetInviteClaim(code)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, badInput("that invite code already exists")
	}

	now := time.Now()
	claim := &relay.InviteClaim{
		Code:      code,
		CreatedAt: now.Unix(),
		ExpiresAt: now.Add(ttl).Unix(),
		MaxUses:   maxUses,
		Roles:     canonicalRoles(roles),
	}
	if err := a.store.PutInviteClaim(claim); err != nil {
		return nil, err
	}
	return claim, nil
}

// issueInviteWithLimits stores a caller-named invite with an already-resolved
// absolute expiry (unix seconds; 0 = never) and max_uses, as
// createclaimwithlimits needs. Unlike issueInviteCode it does not derive expiry
// from a TTL: the shared validator (resolveInviteLimits) has already turned the
// requested limits into verbatim absolute values, so this persists them as-is
// rather than re-deriving and risking a silent clamp.
func (a membershipAdmin) issueInviteWithLimits(code string, expiresAt int64, maxUses int, roles []string) (*relay.InviteClaim, error) {
	if code == "" {
		return nil, badInput("invite code is required")
	}
	// Refuse to overwrite an existing code: silently replacing it would reset
	// the use count and expiry of an invite already handed out.
	existing, err := a.store.GetInviteClaim(code)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, badInput("that invite code already exists")
	}

	claim := &relay.InviteClaim{
		Code:      code,
		CreatedAt: time.Now().Unix(),
		ExpiresAt: expiresAt,
		MaxUses:   maxUses,
		Roles:     canonicalRoles(roles),
	}
	if err := a.store.PutInviteClaim(claim); err != nil {
		return nil, err
	}
	return claim, nil
}

// issueInviteGenerated mints a relay-generated invite code carrying an
// already-resolved absolute expiry (unix seconds; 0 = never) and max_uses. It
// is the generated-code counterpart to issueInviteWithLimits: the REST route
// (handleInviteCreate) uses it when the caller sent an absolute expires_at --
// what `ncli relay invites create --expires-at/--no-expiry` sends -- rather
// than a ttl. Unlike the relay SDK's IssueInvite it does not derive the expiry
// from a TTL, so it can store expires_at:0 (never) verbatim without the SDK's
// ttl<=0 default kicking in.
func (a membershipAdmin) issueInviteGenerated(expiresAt int64, maxUses int, roles []string) (*relay.InviteClaim, error) {
	if maxUses < 0 {
		return nil, badInput("max_uses must be >= 0")
	}
	claim := &relay.InviteClaim{
		Code:      nip43.NewClaim(),
		CreatedAt: time.Now().Unix(),
		ExpiresAt: expiresAt,
		MaxUses:   maxUses,
		Roles:     canonicalRoles(roles),
	}
	if err := a.store.PutInviteClaim(claim); err != nil {
		return nil, err
	}
	return claim, nil
}

func (a membershipAdmin) listInvites() ([]*relay.InviteClaim, error) {
	claims, err := a.store.ListInviteClaims()
	if err != nil {
		return nil, err
	}
	if claims == nil {
		claims = []*relay.InviteClaim{}
	}
	return claims, nil
}

// liveInviteCodes returns the codes of invites still usable right now -- not
// expired and not used up -- as bare strings, which is what NIP-86 listclaims
// answers. The expiry/exhaustion test mirrors the store's own ConsumeInviteClaim
// so a code this hides is exactly a code a join would reject.
func (a membershipAdmin) liveInviteCodes() ([]string, error) {
	claims, err := a.store.ListInviteClaims()
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	codes := make([]string, 0, len(claims))
	for _, c := range claims {
		if c.ExpiresAt > 0 && now > c.ExpiresAt {
			continue // expired
		}
		if c.MaxUses > 0 && c.Uses >= c.MaxUses {
			continue // used up
		}
		codes = append(codes, c.Code)
	}
	return codes, nil
}

// getInvite returns the stored claim for code, including expired/used-up ones,
// as NIP-86 getclaim needs. An unknown code is errNotFound, which each surface
// renders in its own vocabulary.
func (a membershipAdmin) getInvite(code string) (*relay.InviteClaim, error) {
	if code == "" {
		return nil, badInput("invite code is required")
	}
	claim, err := a.store.GetInviteClaim(code)
	if err != nil {
		return nil, err
	}
	if claim == nil {
		return nil, errNotFound
	}
	return claim, nil
}

func (a membershipAdmin) revokeInvite(code string) error {
	if code == "" {
		return badInput("invite code is required")
	}
	// The store's delete is a no-op for an unknown code; say so instead of
	// reporting "revoked" for a code that never existed (a typo).
	claim, err := a.store.GetInviteClaim(code)
	if err != nil {
		return err
	}
	if claim == nil {
		return errNotFound
	}
	return a.store.DeleteInviteClaim(code)
}

// minClaimLength is the floor on a client-chosen invite code: a code shorter
// than this is too guessable to hand bearer-admission power. NIP-86's proposed
// text asks clients for >=128 bits of randomness; 16 chars is the practical
// minimum this relay enforces on top of that recommendation.
const minClaimLength = 16

// validateClaimStrength rejects a client-chosen invite code that is too short
// or contains a character outside [A-Za-z0-9_-]. The generated-code path
// (createclaim with no code) does not go through this: the relay makes those
// strong itself.
func validateClaimStrength(code string) error {
	if len(code) < minClaimLength {
		return badInput("invite code must be at least %d characters", minClaimLength)
	}
	for _, r := range code {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return badInput("invite code may only contain letters, digits, '_' or '-'")
		}
	}
	return nil
}

// invitePolicy is the relay's configured ceiling/defaults for per-invite
// expiry and uses, resolved from membership config. Shared by the NIP-86
// createclaimwithlimits method and the `ncli relay invites create` flags so
// both enforce identical semantics.
type invitePolicy struct {
	defaultTTL     time.Duration // applied when expires_at is absent
	maxTTL         time.Duration // ceiling on an explicit future expires_at
	allowNoExpiry  bool          // whether expires_at:0 (never) is permitted
	defaultMaxUses int           // applied when max_uses is absent
}

// inviteLimits are the per-invite limits a caller requested. A nil field means
// "absent -> use the relay's default"; this is why the fields are pointers and
// not plain values (0 is a meaningful value for expires_at -- "never").
type inviteLimits struct {
	ExpiresAt *int64 `json:"expires_at"`
	MaxUses   *int   `json:"max_uses"`
}

// resolveInviteLimits is the one shared validator for section G. It turns the
// requested limits into resolved absolute values against policy, or returns a
// badInput. It NEVER clamps: a request outside policy is an error.
//
//   - expires_at absent      -> now + policy.defaultTTL
//   - expires_at == 0        -> never (0), only if policy.allowNoExpiry
//   - expires_at > 0         -> must be in the future AND <= now + policy.maxTTL
//   - max_uses absent        -> policy.defaultMaxUses
//   - max_uses present       -> must be >= 1
func resolveInviteLimits(now time.Time, lim inviteLimits, policy invitePolicy) (expiresAt int64, maxUses int, err error) {
	switch {
	case lim.ExpiresAt == nil:
		expiresAt = now.Add(policy.defaultTTL).Unix()
	case *lim.ExpiresAt == 0:
		if !policy.allowNoExpiry {
			return 0, 0, badInput("expires_at 0 (never expires) is not allowed by this relay")
		}
		expiresAt = 0
	default:
		v := *lim.ExpiresAt
		if v <= now.Unix() {
			return 0, 0, badInput("expires_at must be in the future")
		}
		if max := now.Add(policy.maxTTL).Unix(); v > max {
			return 0, 0, badInput("expires_at is later than this relay allows (at most %s from now)", policy.maxTTL)
		}
		expiresAt = v
	}

	switch {
	case lim.MaxUses == nil:
		maxUses = policy.defaultMaxUses
	case *lim.MaxUses < 1:
		return 0, 0, badInput("max_uses must be >= 1")
	default:
		maxUses = *lim.MaxUses
	}

	return expiresAt, maxUses, nil
}

/////////////////////////////////////////////////////////////////////
// Roles
/////////////////////////////////////////////////////////////////////

func (a membershipAdmin) listRoles(ctx context.Context) ([]roleJSON, error) {
	events, err := a.store.QueryEvents(ctx, &nip01.SubscriptionFilter{
		Kinds:   []int{nip43.KindRoleDefinition},
		Authors: []string{config.Nip11.Self},
	})
	if err != nil {
		return nil, err
	}

	roles := make([]roleJSON, 0, len(events))
	for _, ev := range events {
		role, err := nip43.ParseRole(ev)
		if err != nil {
			log.Warn().Err(err).Str("event_id", ev.ID).Msg("skipping malformed role-definition event")
			continue
		}
		roles = append(roles, roleJSON{ID: role.ID, Label: role.Label, Description: role.Description, Color: role.Color, Order: role.Order})
	}
	return roles, nil
}

// putRole creates or edits a role. Kind 33534 is parameterized-replaceable, so
// publishing the same id again is the edit.
func (a membershipAdmin) putRole(ctx context.Context, role roleJSON) error {
	if canonicalRole(role.ID) == "" {
		return badInput("id is required")
	}
	if role.Color != nil && (*role.Color < 0 || *role.Color > 360) {
		return badInput("color must be an integer 0-360")
	}
	if config.Nip11.PrivKey == "" {
		return errors.New("the relay has no nip11.privkey configured, so it cannot sign a role definition")
	}

	ev := nip43.NewRoleDefinition(nip43.RoleParams{
		SelfPubkey:  config.Nip11.PubKey,
		ID:          canonicalRole(role.ID),
		Label:       role.Label,
		Description: role.Description,
		Color:       role.Color,
		Order:       role.Order,
	})
	if err := ev.Sign(config.Nip11.PrivKey); err != nil {
		return fmt.Errorf("failed to sign role definition: %w", err)
	}
	return a.store.InsertEvents(ctx, []*nip01.Event{ev})
}

// deleteRole retracts a role definition with a NIP-09 deletion signed by the
// relay's own key, which is the only signer its events will accept.
func (a membershipAdmin) deleteRole(ctx context.Context, id string) error {
	if id == "" {
		return badInput("id is required")
	}
	if config.Nip11.PrivKey == "" {
		return errors.New("the relay has no nip11.privkey configured, so it cannot sign a deletion")
	}

	events, err := a.store.QueryEvents(ctx, &nip01.SubscriptionFilter{
		Kinds:   []int{nip43.KindRoleDefinition},
		Authors: []string{config.Nip11.Self},
	})
	if err != nil {
		return err
	}

	tags := [][]string{}
	for _, ev := range events {
		role, err := nip43.ParseRole(ev)
		if err != nil || !strings.EqualFold(role.ID, id) {
			continue
		}
		tags = append(tags, []string{"e", ev.ID})
	}
	if len(tags) == 0 {
		return badInput("no such role: %s", id)
	}

	del := nip01.NewUnsignedEvent(nip09.KindDeletion, config.Nip11.PubKey, "", tags...)
	if err := del.Sign(config.Nip11.PrivKey); err != nil {
		return fmt.Errorf("failed to sign role deletion: %w", err)
	}
	return a.store.InsertEvents(ctx, []*nip01.Event{del})
}

// setMemberRoles assigns or unassigns one role on a member, re-reading the
// record first so a concurrent change to its other roles is not clobbered.
func (a membershipAdmin) setMemberRoles(ctx context.Context, pubkey, roleID string, assign bool) (*relay.MemberRecord, error) {
	if err := checkPubkey(pubkey); err != nil {
		return nil, err
	}
	if canonicalRole(roleID) == "" {
		return nil, badInput("role id is required")
	}

	rec, err := a.ws.Membership().Get(pubkey)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, badInput("pubkey is not a member of this relay")
	}

	roleID = canonicalRole(roleID)
	roles := make([]string, 0, len(rec.Roles)+1)
	found := false
	for _, existing := range rec.Roles {
		// EqualFold: a record written before role ids were canonical may
		// still hold "Host"; unassigning "host" must remove it.
		if strings.EqualFold(existing, roleID) {
			found = true
			if !assign {
				continue
			}
		}
		roles = append(roles, existing)
	}
	if assign && !found {
		roles = append(roles, roleID)
	}

	if err := a.ws.Membership().Join(pubkey, roles); err != nil {
		return nil, err
	}
	return a.ws.Membership().Get(pubkey)
}

// canonicalRole is a NIP-43 role id as this relay stores it: trimmed and
// lowercase. Config keys are lowercased on load, so membership.roles can
// only ever name a lowercase id; storing every id the same way keeps
// assign, unassign and the tasks a role carries in agreement.
func canonicalRole(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// canonicalRoles canonicalises each id and drops empties and duplicates.
func canonicalRoles(roles []string) []string {
	if roles == nil {
		return nil
	}
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		if r = canonicalRole(r); r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}
