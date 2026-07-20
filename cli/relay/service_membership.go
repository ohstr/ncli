package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip43"
	"github.com/ohstr/nmilat/relay"
	"github.com/ohstr/nmilat/utils"
	"github.com/rs/zerolog/log"
)

// maxAdminBodyBytes bounds every membership admin endpoint's request body --
// these are small, fixed-shape JSON payloads (a pubkey, a handful of role
// ids, invite parameters), so an oversized body is never legitimate input,
// only an abusive or mistaken caller.
const maxAdminBodyBytes = 1 << 20 // 1 MiB

// registerMembershipAdminRoutes adds the NIP-43 membership admin surface
// (/admin/membership/...) onto mux, backing `ncli relay members/invites/
// roles`. Every write goes through wsHandler.Membership() (the same
// MembershipService instance every live Session consults), never store
// writes directly -- see NIP43_ADMIN_UX.md's "Precedent to follow exactly"
// for why: a direct store write here would desync the running relay's
// in-memory membership cache from what admin commands just wrote.
func registerMembershipAdminRoutes(mux *http.ServeMux, wsHandler *relay.SessionHandler, store *relay.EventStore, adminAuth func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /admin/membership/members", adminAuth(requireMembership(handleMembersList(wsHandler))))
	mux.HandleFunc("GET /admin/membership/members/{pubkey}", adminAuth(requireMembership(handleMemberShow(wsHandler))))
	mux.HandleFunc("POST /admin/membership/members", adminAuth(requireMembership(handleMemberAdd(wsHandler, store))))
	mux.HandleFunc("DELETE /admin/membership/members/{pubkey}", adminAuth(requireMembership(handleMemberRemove(wsHandler, store))))

	mux.HandleFunc("POST /admin/membership/invites", adminAuth(requireMembership(handleInviteCreate(wsHandler))))
	mux.HandleFunc("GET /admin/membership/invites", adminAuth(requireMembership(handleInviteList(store))))
	mux.HandleFunc("DELETE /admin/membership/invites/{code}", adminAuth(requireMembership(handleInviteRevoke(store))))

	mux.HandleFunc("GET /admin/membership/roles", adminAuth(requireMembership(handleRolesList(store))))
	mux.HandleFunc("POST /admin/membership/roles", adminAuth(requireMembership(handleRoleCreate(store))))
}

// requireMembership wraps next so every membership admin handler rejects
// with a clear, distinguishable error (rather than a confusing "member not
// found" or an internal error further down) when NIP-43 membership isn't
// configured on this relay at all. 501 (not 409/400) because this is
// neither a transient conflict nor a malformed request -- retrying without
// an operator enabling membership.enabled will never succeed.
func requireMembership(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if config.Membership == nil || !config.Membership.Enabled {
			http.Error(w, "NIP-43 membership is not enabled on this relay (set membership.enabled: true)", http.StatusNotImplemented)
			return
		}
		next.ServeHTTP(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error().Err(err).Msg("failed to encode admin membership response")
	}
}

// decodeAdminBody JSON-decodes r's body into v, capped at maxAdminBodyBytes.
// Returns false (and has already written a 400 response) on any decode
// failure -- callers should return immediately.
func decodeAdminBody(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// validatePubkey reports whether pubkey is well-formed 64-char hex,
// writing a 400 response and returning false otherwise -- so a malformed
// pubkey never silently becomes a "member" that can never actually
// authenticate as itself, and a mistyped lookup gets a clear reason instead
// of an ambiguous "not found".
func validatePubkey(w http.ResponseWriter, pubkey string) bool {
	if err := utils.Validate32Key(pubkey); err != nil {
		http.Error(w, "invalid pubkey: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

/////////////////////////////////////////////////////////////////////
// Members
/////////////////////////////////////////////////////////////////////

func handleMembersList(wsHandler *relay.SessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		records, err := wsHandler.Membership().List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if records == nil {
			records = []*relay.MemberRecord{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"members": records})
	}
}

func handleMemberShow(wsHandler *relay.SessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pubkey := r.PathValue("pubkey")
		if !validatePubkey(w, pubkey) {
			return
		}
		rec, err := wsHandler.Membership().Get(pubkey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if rec == nil {
			http.Error(w, "pubkey is not a member of this relay", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, rec)
	}
}

func handleMemberAdd(wsHandler *relay.SessionHandler, store *relay.EventStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Pubkey string   `json:"pubkey"`
			Roles  []string `json:"roles,omitempty"`
		}
		if !decodeAdminBody(w, r, &body) {
			return
		}
		if !validatePubkey(w, body.Pubkey) {
			return
		}

		if err := wsHandler.Membership().Join(body.Pubkey, body.Roles); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if config.Membership.PublishAddRemoveEvents {
			publishMembershipAdminEvent(r.Context(), store, nip43.NewAddUser(config.Nip11.PubKey, body.Pubkey))
		}

		rec, err := wsHandler.Membership().Get(body.Pubkey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, rec)
	}
}

func handleMemberRemove(wsHandler *relay.SessionHandler, store *relay.EventStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pubkey := r.PathValue("pubkey")
		if !validatePubkey(w, pubkey) {
			return
		}

		if err := wsHandler.Membership().Leave(pubkey); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if config.Membership.PublishAddRemoveEvents {
			publishMembershipAdminEvent(r.Context(), store, nip43.NewRemoveUser(config.Nip11.PubKey, pubkey))
		}

		writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
	}
}

// publishMembershipAdminEvent signs ev with the relay's own key and inserts
// it directly, mirroring relay/membership.go's publishSelfSigned for the
// self-service join/leave path -- kept as a fire-and-log side effect, not
// something that fails the admin request itself: the authoritative
// membership state (wsHandler.Membership()) was already committed by the
// time this runs.
func publishMembershipAdminEvent(ctx context.Context, store *relay.EventStore, ev *nip01.Event) {
	if config.Nip11.PrivKey == "" {
		return
	}
	if err := ev.Sign(config.Nip11.PrivKey); err != nil {
		log.Error().Err(err).Int("kind", ev.Kind).Msg("failed to sign membership add/remove event")
		return
	}
	if err := store.InsertEvents(ctx, []*nip01.Event{ev}); err != nil {
		log.Error().Err(err).Int("kind", ev.Kind).Msg("failed to publish membership add/remove event")
	}
}

/////////////////////////////////////////////////////////////////////
// Invites
/////////////////////////////////////////////////////////////////////

func handleInviteCreate(wsHandler *relay.SessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			TTL     string   `json:"ttl,omitempty"`
			MaxUses int      `json:"max_uses,omitempty"`
			Roles   []string `json:"roles,omitempty"`
		}
		if !decodeAdminBody(w, r, &body) {
			return
		}

		var ttl time.Duration
		if body.TTL != "" {
			parsed, err := time.ParseDuration(body.TTL)
			if err != nil {
				http.Error(w, "invalid ttl: "+err.Error(), http.StatusBadRequest)
				return
			}
			ttl = parsed
		}
		if body.MaxUses < 0 {
			http.Error(w, "max_uses must be >= 0", http.StatusBadRequest)
			return
		}

		claim, err := wsHandler.Membership().IssueInvite(ttl, body.MaxUses, body.Roles)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, claim)
	}
}

func handleInviteList(store *relay.EventStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, err := store.ListInviteClaims()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if claims == nil {
			claims = []*relay.InviteClaim{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"invites": claims})
	}
}

func handleInviteRevoke(store *relay.EventStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.PathValue("code")
		if code == "" {
			http.Error(w, "invite code is required", http.StatusBadRequest)
			return
		}
		if err := store.DeleteInviteClaim(code); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
	}
}

/////////////////////////////////////////////////////////////////////
// Roles
/////////////////////////////////////////////////////////////////////

// roleJSON is the wire shape for a role in admin responses -- nip43.Role
// itself carries no JSON tags (it's a pure parse result, not meant to be
// (de)serialized directly), so this is the boundary type between it and
// the admin HTTP API.
type roleJSON struct {
	ID          string `json:"id"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Color       *int   `json:"color,omitempty"`
	Order       *int   `json:"order,omitempty"`
}

func handleRolesList(store *relay.EventStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		events, err := store.QueryEvents(r.Context(), &nip01.SubscriptionFilter{
			Kinds:   []int{nip43.KindRoleDefinition},
			Authors: []string{config.Nip11.Self},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
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
		writeJSON(w, http.StatusOK, map[string]interface{}{"roles": roles})
	}
}

func handleRoleCreate(store *relay.EventStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body roleJSON
		if !decodeAdminBody(w, r, &body) {
			return
		}
		if body.ID == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		if body.Color != nil && (*body.Color < 0 || *body.Color > 360) {
			http.Error(w, "color must be an integer 0-360", http.StatusBadRequest)
			return
		}

		ev := nip43.NewRoleDefinition(nip43.RoleParams{
			SelfPubkey:  config.Nip11.PubKey,
			ID:          body.ID,
			Label:       body.Label,
			Description: body.Description,
			Color:       body.Color,
			Order:       body.Order,
		})
		if err := ev.Sign(config.Nip11.PrivKey); err != nil {
			http.Error(w, "failed to sign role definition: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := store.InsertEvents(r.Context(), []*nip01.Event{ev}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, body)
	}
}
