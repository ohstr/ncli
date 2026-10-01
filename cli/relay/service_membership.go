package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/relay"
	"github.com/rs/zerolog/log"
)

// maxAdminBodyBytes bounds every membership admin endpoint's request body --
// these are small, fixed-shape JSON payloads (a pubkey, a handful of role
// ids, invite parameters), so an oversized body is never legitimate input,
// only an abusive or mistaken caller.
const maxAdminBodyBytes = 1 << 20 // 1 MiB

// registerMembershipAdminRoutes adds the NIP-43 membership admin surface
// (/admin/membership/...) onto mux, backing `ncli relay members/invites/
// roles`. The handlers are thin: every one of them delegates to
// membershipAdmin, which NIP-86 calls too, so the two transports cannot
// answer the same question differently.
func registerMembershipAdminRoutes(mux *http.ServeMux, wsHandler *relay.SessionHandler, store *relay.EventStore, rooms *room.Manager, adminAuth func(http.HandlerFunc) http.HandlerFunc) {
	admin := membershipAdmin{ws: wsHandler, store: store, rooms: rooms}

	mux.HandleFunc("GET /admin/membership/members", adminAuth(requireMembership(handleMembersList(admin))))
	mux.HandleFunc("GET /admin/membership/members/{pubkey}", adminAuth(requireMembership(handleMemberShow(admin))))
	mux.HandleFunc("POST /admin/membership/members", adminAuth(requireMembership(handleMemberAdd(admin))))
	mux.HandleFunc("DELETE /admin/membership/members/{pubkey}", adminAuth(requireMembership(handleMemberRemove(admin))))

	mux.HandleFunc("POST /admin/membership/invites", adminAuth(requireMembership(handleInviteCreate(admin))))
	mux.HandleFunc("GET /admin/membership/invites", adminAuth(requireMembership(handleInviteList(admin))))
	mux.HandleFunc("DELETE /admin/membership/invites/{code}", adminAuth(requireMembership(handleInviteRevoke(admin))))

	mux.HandleFunc("GET /admin/membership/roles", adminAuth(requireMembership(handleRolesList(admin))))
	mux.HandleFunc("POST /admin/membership/roles", adminAuth(requireMembership(handleRoleCreate(admin))))
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

// writeAdminError maps a core error onto a status code: what the caller sent
// wrong is a 400, anything else is a 500.
func writeAdminError(w http.ResponseWriter, err error) {
	if isBadInput(err) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

/////////////////////////////////////////////////////////////////////
// Members
/////////////////////////////////////////////////////////////////////

func handleMembersList(admin membershipAdmin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		records, err := admin.listMembers()
		if err != nil {
			writeAdminError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"members": records})
	}
}

func handleMemberShow(admin membershipAdmin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec, err := admin.getMember(r.PathValue("pubkey"))
		if err != nil {
			writeAdminError(w, err)
			return
		}
		if rec == nil {
			http.Error(w, "pubkey is not a member of this relay", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, rec)
	}
}

func handleMemberAdd(admin membershipAdmin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Pubkey string   `json:"pubkey"`
			Roles  []string `json:"roles,omitempty"`
		}
		if !decodeAdminBody(w, r, &body) {
			return
		}

		rec, err := admin.addMember(r.Context(), body.Pubkey, body.Roles)
		if err != nil {
			writeAdminError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rec)
	}
}

func handleMemberRemove(admin membershipAdmin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := admin.removeMember(r.Context(), r.PathValue("pubkey")); err != nil {
			writeAdminError(w, err)
			return
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

func handleInviteCreate(admin membershipAdmin) http.HandlerFunc {
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

		claim, err := admin.issueInvite(ttl, body.MaxUses, body.Roles)
		if err != nil {
			writeAdminError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, claim)
	}
}

func handleInviteList(admin membershipAdmin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, err := admin.listInvites()
		if err != nil {
			writeAdminError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"invites": claims})
	}
}

func handleInviteRevoke(admin membershipAdmin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := admin.revokeInvite(r.PathValue("code")); err != nil {
			writeAdminError(w, err)
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

func handleRolesList(admin membershipAdmin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roles, err := admin.listRoles(r.Context())
		if err != nil {
			writeAdminError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"roles": roles})
	}
}

func handleRoleCreate(admin membershipAdmin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body roleJSON
		if !decodeAdminBody(w, r, &body) {
			return
		}
		if err := admin.putRole(r.Context(), body); err != nil {
			writeAdminError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, body)
	}
}
