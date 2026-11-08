package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ohstr/ncli/cli/relay/adminplane"
	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/nip86"
	"github.com/ohstr/nmilat/nip98"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/relay/ratelimit"
)

// defaultInviteTTL mirrors the relay SDK's own fallback, used when an operator
// configures none and a caller names a claim code explicitly (the generated-code
// path lets the SDK apply its default itself).
const defaultInviteTTL = 24 * time.Hour

// defaultInviteMaxTTL caps how far in the future createclaimwithlimits will
// honour an explicit expires_at when membership.inviteMaxTTL is unset -- 30
// days. An out-of-range expiry is refused, never clamped.
const defaultInviteMaxTTL = 720 * time.Hour

// Method names for the two PROPOSED NIP-86 additions. They are not yet in the
// nmilat nip86 package's own constant set (the pinned rc.47), so they are named
// here; move them to nip86.Method* once upstream adopts them.
const (
	nip86MethodCreateClaimWithLimits = "createclaimwithlimits"
	nip86MethodGetClaim              = "getclaim"
)

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

// nip86Enabled reports whether to serve the management API. The moderation
// methods work on any relay; the membership ones only when membership is on
// (membershipOn).
func nip86Enabled() bool {
	return config.Nip86 != nil && config.Nip86.Enabled
}

func membershipOn() bool {
	return config.Membership != nil && config.Membership.Enabled
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

// membershipInviteMaxTTL is the ceiling createclaimwithlimits enforces on an
// explicit absolute expiry, or the built-in default when unset. Startup
// validation (command.go) rejects an unparseable or non-positive value, so a
// bad config never reaches here.
func membershipInviteMaxTTL() time.Duration {
	if config.Membership != nil && config.Membership.InviteMaxTTL != "" {
		if d, err := time.ParseDuration(config.Membership.InviteMaxTTL); err == nil && d > 0 {
			return d
		}
	}
	return defaultInviteMaxTTL
}

// membershipInviteAllowNoExpiry reports whether createclaimwithlimits may mint
// a never-expiring code (expires_at:0). Off by default: an invite that never
// expires is a standing liability, so an operator opts in explicitly.
func membershipInviteAllowNoExpiry() bool {
	return config.Membership != nil && config.Membership.InviteAllowNoExpiry
}

// memberClaims reports whether a plain member (not an admin) may mint an
// invite via createclaim: resolveTaskGrants gives members createclaim when it
// is on (unless membership.members.can is set). Unset, it follows
// memberClaimsDefault: off on a members-only relay, on otherwise. initConfig
// normally resolves it already; the fallback covers a config built without it.
func (c *RelayConfig) memberClaims() bool {
	if c.Membership == nil || c.Membership.MemberClaims == nil {
		return c.memberClaimsDefault()
	}
	return *c.Membership.MemberClaims
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
		// Already a member: nothing to do. Joining again would replace the
		// record and drop the roles it holds.
		if rec, err := admin.getMember(pubkey); err != nil {
			return nil, err
		} else if rec != nil {
			return true, nil
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

	// createclaim has two shapes that must both keep working:
	//   - [code]  (spec form): store the caller's code with config defaults and
	//     answer `true`. A client-chosen code must meet the min-strength bar.
	//   - [] / [""] (legacy): the relay generates the code and returns the full
	//     InviteClaim record -- released bzzapp depends on this, so it is left
	//     exactly as it was.
	router.Handle(nip86.MethodCreateClaim, func(_ context.Context, _ string, req nip86.Request) (any, error) {
		code := ""
		if len(req.Params) > 0 {
			var err error
			if code, err = req.StringParam(0); err != nil {
				return nil, err
			}
		}
		if code != "" {
			if err := validateClaimStrength(code); err != nil {
				return nil, err
			}
		}
		claim, err := admin.issueInviteCode(code, membershipInviteTTL(), membershipInviteMaxUses(), nil)
		if err != nil {
			return nil, err
		}
		if code != "" { // spec form: answer true
			return true, nil
		}
		return claim, nil // legacy generate form: full record
	})

	// createclaimwithlimits is a PROPOSED NIP-86 addition: a caller-named code
	// carrying an explicit expiry and/or use cap, validated against the relay's
	// policy and never clamped. Always `true` on success; always an error (and
	// no code created) on a bad claim, unknown field, wrong type, duplicate, or
	// out-of-policy limit. Admin-only (see the Visible TODO below).
	router.Handle(nip86MethodCreateClaimWithLimits, func(_ context.Context, _ string, req nip86.Request) (any, error) {
		code, err := req.StringParam(0)
		if err != nil {
			return nil, err
		}
		if code == "" {
			return nil, badInput("invite code is required")
		}
		if err := validateClaimStrength(code); err != nil {
			return nil, err
		}

		// A strict decode: an unknown key, or expires_at/max_uses given as
		// anything but a JSON integer, is a caller error, never silently
		// dropped. An absent params[1] (no limits object at all) means
		// all-defaults.
		var lim inviteLimits
		if len(req.Params) > 1 {
			dec := json.NewDecoder(bytes.NewReader(req.Params[1]))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&lim); err != nil {
				return nil, badInput("invalid limits object: %v", err)
			}
		}

		policy := invitePolicy{
			defaultTTL:     membershipInviteTTL(),
			maxTTL:         membershipInviteMaxTTL(),
			allowNoExpiry:  membershipInviteAllowNoExpiry(),
			defaultMaxUses: membershipInviteMaxUses(),
		}
		expiresAt, maxUses, err := resolveInviteLimits(time.Now(), lim, policy)
		if err != nil {
			return nil, err
		}
		if _, err := admin.issueInviteWithLimits(code, expiresAt, maxUses, nil); err != nil {
			return nil, err
		}
		return true, nil
	})

	// listclaims answers a plain list of LIVE code strings (not expired, not
	// used up). The spec says expired/used-up codes SHOULD NOT be listed here;
	// a caller that needs full records (incl. dead ones) uses getclaim or the
	// REST route instead.
	router.Handle(nip86.MethodListClaims, func(context.Context, string, nip86.Request) (any, error) {
		return admin.liveInviteCodes()
	})

	// getclaim is a PROPOSED NIP-86 addition: the state of any stored code,
	// including expired/used-up ones, until it is deleted or pruned. The output
	// field is `claim` (the spec's name), not the record's `code`. Admin-only.
	router.Handle(nip86MethodGetClaim, func(_ context.Context, _ string, req nip86.Request) (any, error) {
		code, err := req.StringParam(0)
		if err != nil {
			return nil, err
		}
		claim, err := admin.getInvite(code)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"claim":      claim.Code,
			"expires_at": claim.ExpiresAt,
			"max_uses":   claim.MaxUses,
			"uses":       claim.Uses,
		}, nil
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
func newNip86Handler(wsHandler *relay.SessionHandler, store *relay.EventStore, rooms *room.Manager, moderation *relayModeration) (*nip86.Handler, error) {
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

	router := nip86.NewRouter()
	if membershipOn() {
		router = newNip86Router(membershipAdmin{ws: wsHandler, store: store, rooms: rooms})
	}
	registerModerationMethods(router, moderation, store)

	cfg := nip86.Config{
		Router:         router,
		AllowedPubkeys: admins,
		AllowedOrigins: origins,
	}

	// Who may call what is the task model (tasks.go): a NIP-86 method is a
	// task, held by the admins (every method) and by whatever the config
	// grants members and members' NIP-43 roles (memberClaims maps to members
	// holding createclaim). NIP-98 still checks the signature, payload and URL
	// for everyone. Authorize admits a non-admin key holding at least one
	// method; Visible then confines every caller to the methods it holds, so
	// supportedmethods answers each caller with its own set (the spec allows
	// this) and a method it doesn't hold is refused as unknown. Membership
	// off, nobody but the admins holds a method: Authorize stays nil and only
	// AllowedPubkeys pass, as before.
	auth := newTaskAuthority(&config, wsHandler.Membership(), moderation)
	// Every registered method, read before Visible is set; supportedmethods
	// then filters this list with one membership lookup per call rather
	// than one per method.
	router.Handle(nip86.MethodSupportedMethods, auth.supportedMethods(router.MethodsFor("")))
	router.Visible = auth.nip86Visible
	if membershipOn() {
		cfg.Authorize = auth.mayUseNip86
	}

	return nip86.NewHandler(cfg), nil
}

// nip86Limit is what limitNip86 needs besides the limiter: who the admins
// are, the CORS allowlist the NIP-86 handler answers with, and the replay
// guard in front of it.
type nip86Limit struct {
	// perPubkey is whether limits.http.perPubkey is set.
	perPubkey bool
	// admins are never limited: the relay key and nip86.admins.
	admins []string
	// origins is nip86.allowedOrigins.
	origins []string
	// replay is the guard ServeOnce consults, read here without recording.
	replay *adminplane.ReplayGuard
}

// limitNip86 counts each NIP-86 POST against limits.http. A request with
// no Authorization is charged per IP. One with a header already used (the
// replay guard, peeked, not recorded) is charged per IP only, so a replay
// never spends its signer's budget, and next refuses it. Any other header
// is NIP-98 verified first, as the handler verifies it: a failure is
// charged per IP, then a 401; an admin (the relay key, nip86.admins) is
// not limited at all, so a flood sharing its IP can't lock it out; any
// other signer is charged per IP and, when a per-pubkey limit is set, per
// signer. A refusal is a 429 with Retry-After, and a refusal or 401 carries
// the CORS headers the NIP-86 handler would. A nil limiter counts nothing.
func limitNip86(limiter *ratelimit.Limiter, opts nip86Limit, next http.Handler) http.Handler {
	if limiter == nil {
		return next
	}
	admins := make(map[string]bool, len(opts.admins))
	for _, a := range opts.admins {
		admins[a] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		ip := limiter.ClientIP(r)
		refuse := func(wait time.Duration) {
			nip86CORS(w, r, opts.origins)
			ratelimit.Refuse(w, ratelimit.HTTP, wait)
		}
		auth := r.Header.Get("Authorization")
		if auth == "" || (opts.replay != nil && opts.replay.Used(auth, time.Now())) {
			if wait, ok := limiter.Allow(ratelimit.HTTP, ip, ""); !ok {
				refuse(wait)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, nip86.MaxBodyBytes))
		if err != nil {
			nip86CORS(w, r, opts.origins)
			http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		signer, err := nip98.VerifyAnyPubkey(r, nip98.Options{Body: body, RequirePayload: true, NormalizeRootPath: true})
		if err != nil {
			if wait, ok := limiter.Allow(ratelimit.HTTP, ip, ""); !ok {
				refuse(wait)
				return
			}
			// 401 is NIP-86's only status for a failed authentication.
			nip86CORS(w, r, opts.origins)
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		if admins[signer] {
			next.ServeHTTP(w, r)
			return
		}
		pubkey := ""
		if opts.perPubkey {
			pubkey = signer
		}
		if wait, ok := limiter.Allow(ratelimit.HTTP, ip, pubkey); !ok {
			refuse(wait)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// nip86CORS sets the CORS headers nmilat's nip86.Handler answers with, for
// a response limitNip86 writes before it: Access-Control-Allow-Origin from
// nip86.allowedOrigins (any origin when empty), and Vary: Origin when it
// names one. Without them a browser hides the 429 or 401 from the page.
func nip86CORS(w http.ResponseWriter, r *http.Request, origins []string) {
	allowed := ""
	if len(origins) == 0 {
		allowed = "*"
	}
	origin := r.Header.Get("Origin")
	for _, candidate := range origins {
		if candidate == "*" {
			allowed = "*"
			break
		}
		if origin != "" && strings.EqualFold(candidate, origin) {
			allowed = origin
			break
		}
	}
	if allowed == "" {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", allowed)
	if allowed != "*" {
		w.Header().Add("Vary", "Origin")
	}
}
