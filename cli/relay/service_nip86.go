package relay

import (
	"context"
	"errors"
	"time"

	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/nip86"
	"github.com/ohstr/nmilat/relay"
)

// defaultInviteTTL mirrors the relay SDK's own fallback, used when an operator
// configures none and a caller names a claim code explicitly (the generated-code
// path lets the SDK apply its default itself).
const defaultInviteTTL = 24 * time.Hour

// nip86Admins is who may call the management API: the relay's own NIP-11 pubkey
// (so an existing operator keeps working unchanged) plus any configured admins.
//
// The configured list is the point of the feature: without it the only admin
// identity is the relay's own key, so administering from an app would mean
// carrying the relay's secret key around.
func nip86Admins() []string {
	admins := make([]string, 0, 4)
	if config.Nip11.PubKey != "" {
		admins = append(admins, config.Nip11.PubKey)
	}
	if config.Nip86 != nil {
		admins = append(admins, config.Nip86.Admins...)
	}
	return admins
}

// nip86Enabled reports whether to serve the management API. It requires
// membership, since every method it exposes administers NIP-43 membership.
func nip86Enabled() bool {
	return config.Nip86 != nil && config.Nip86.Enabled &&
		config.Membership != nil && config.Membership.Enabled
}

// membershipInviteTTL is the configured invite lifetime, or the SDK's default.
func membershipInviteTTL() time.Duration {
	if config.Membership != nil && config.Membership.InviteTTL != "" {
		if d, err := time.ParseDuration(config.Membership.InviteTTL); err == nil && d > 0 {
			return d
		}
	}
	return defaultInviteTTL
}

func membershipInviteMaxUses() int {
	if config.Membership != nil {
		return config.Membership.InviteMaxUses
	}
	return 0
}

// newNip86Router binds the NIP-86 methods onto the same membershipAdmin the
// REST routes use. Pubkey, claim and role methods are all backed by NIP-43
// state that already existed; nothing here keeps state of its own.
func newNip86Router(admin membershipAdmin) *nip86.Router {
	router := nip86.NewRouter()

	router.Handle(nip86.MethodAllowPubkey, func(ctx context.Context, _ string, req nip86.Request) (any, error) {
		pubkey, err := req.StringParam(0)
		if err != nil {
			return nil, err
		}
		if _, err := admin.addMember(ctx, pubkey, nil); err != nil {
			return nil, err
		}
		return true, nil
	})

	router.Handle(nip86.MethodUnallowPubkey, func(ctx context.Context, _ string, req nip86.Request) (any, error) {
		pubkey, err := req.StringParam(0)
		if err != nil {
			return nil, err
		}
		if err := admin.removeMember(ctx, pubkey); err != nil {
			return nil, err
		}
		return true, nil
	})

	router.Handle(nip86.MethodListAllowedPubkeys, func(context.Context, string, nip86.Request) (any, error) {
		records, err := admin.listMembers()
		if err != nil {
			return nil, err
		}
		pubkeys := make([]string, 0, len(records))
		for _, rec := range records {
			pubkeys = append(pubkeys, rec.Pubkey)
		}
		return pubkeys, nil
	})

	// The claim object, not a bare code: the caller needs the expiry and
	// remaining uses to tell a guest what it is getting.
	router.Handle(nip86.MethodCreateClaim, func(_ context.Context, _ string, req nip86.Request) (any, error) {
		code := ""
		if len(req.Params) > 0 {
			var err error
			if code, err = req.StringParam(0); err != nil {
				return nil, err
			}
		}
		return admin.issueInviteCode(code, membershipInviteTTL(), membershipInviteMaxUses(), nil)
	})

	router.Handle(nip86.MethodListClaims, func(context.Context, string, nip86.Request) (any, error) {
		return admin.listInvites()
	})

	router.Handle(nip86.MethodDeleteClaim, func(_ context.Context, _ string, req nip86.Request) (any, error) {
		code, err := req.StringParam(0)
		if err != nil {
			return nil, err
		}
		if err := admin.revokeInvite(code); err != nil {
			return nil, err
		}
		return true, nil
	})

	// create and edit are the same write: kind 33534 is
	// parameterized-replaceable, so publishing an id again replaces it.
	putRole := func(ctx context.Context, _ string, req nip86.Request) (any, error) {
		var role roleJSON
		if err := req.ObjectParam(0, &role); err != nil {
			return nil, err
		}
		if err := admin.putRole(ctx, role); err != nil {
			return nil, err
		}
		return true, nil
	}
	router.Handle(nip86.MethodCreateRole, putRole)
	router.Handle(nip86.MethodEditRole, putRole)

	router.Handle(nip86.MethodDeleteRole, func(ctx context.Context, _ string, req nip86.Request) (any, error) {
		id, err := req.StringParam(0)
		if err != nil {
			return nil, err
		}
		if err := admin.deleteRole(ctx, id); err != nil {
			return nil, err
		}
		return true, nil
	})

	roleAssignment := func(assign bool) nip86.Method {
		return func(ctx context.Context, _ string, req nip86.Request) (any, error) {
			pubkey, err := req.StringParam(0)
			if err != nil {
				return nil, err
			}
			roleID, err := req.StringParam(1)
			if err != nil {
				return nil, err
			}
			if _, err := admin.setMemberRoles(ctx, pubkey, roleID, assign); err != nil {
				return nil, err
			}
			return true, nil
		}
	}
	router.Handle(nip86.MethodAssignRole, roleAssignment(true))
	router.Handle(nip86.MethodUnassignRole, roleAssignment(false))

	return router
}

// newNip86Handler builds the HTTP handler, or nil when the API is off.
func newNip86Handler(wsHandler *relay.SessionHandler, store *relay.EventStore, rooms *room.Manager) (*nip86.Handler, error) {
	if !nip86Enabled() {
		return nil, nil
	}
	admins := nip86Admins()
	if len(admins) == 0 {
		// Fail closed: an enabled endpoint with no admin would otherwise be a
		// management API nobody can use, or worse, one anybody can.
		return nil, errors.New("nip86.enabled requires nip11.pubkey or at least one nip86.admins entry")
	}

	var origins []string
	if config.Nip86 != nil {
		origins = config.Nip86.AllowedOrigins
	}

	admin := membershipAdmin{ws: wsHandler, store: store, rooms: rooms}
	return nip86.NewHandler(nip86.Config{
		Router:         newNip86Router(admin),
		AllowedPubkeys: admins,
		AllowedOrigins: origins,
	}), nil
}
