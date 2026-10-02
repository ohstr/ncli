package relay

import (
	"context"
	"errors"
	"fmt"
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
	if err := a.ws.Membership().Join(pubkey, roles); err != nil {
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
	return a.ws.Membership().IssueInvite(ttl, maxUses, roles)
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
		Roles:     roles,
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

func (a membershipAdmin) revokeInvite(code string) error {
	if code == "" {
		return badInput("invite code is required")
	}
	return a.store.DeleteInviteClaim(code)
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
	if role.ID == "" {
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
		ID:          role.ID,
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
		if err != nil || role.ID != id {
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
	if roleID == "" {
		return nil, badInput("role id is required")
	}

	rec, err := a.ws.Membership().Get(pubkey)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, badInput("pubkey is not a member of this relay")
	}

	roles := make([]string, 0, len(rec.Roles)+1)
	found := false
	for _, existing := range rec.Roles {
		if existing == roleID {
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
