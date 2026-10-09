package bunker

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/nip46/bunker"
)

// PairingSecretTTL is how long a bunker:// pairing secret (SetPendingSecret)
// stays valid with nobody connecting -- the same 5-minute window
// DefaultPendingTTL already gives a pending decision, reused here so there's
// one consistent "how long do I have" mental model across the whole bunker
// flow rather than two different unexplained timeouts. board.go's
// showBunkerURI shows a live countdown against this same constant.
const PairingSecretTTL = DefaultPendingTTL

// Handler is ncli's policy on top of nmilat's NIP-46 signer
// (nip46/bunker): it decides each request against the remembered-permission
// Store and the human approval Queue, and records pairings. The signer
// itself (dispatch, crypto, pairing secrets, responses) is bunker.Server;
// Handle answers one request through it without any relay, so the whole
// path is testable without a network. Handle may block for as long as
// Queue's TTL while a human decides.
type Handler struct {
	IdentityPriv string
	IdentityPub  string
	Store        *Store
	Queue        *Queue
	Relays       []string

	// OnSigned, if set, fires right after a sign_event request's event is
	// signed -- daemon.go wires this to recordSignedEvent so the signed
	// JSON reaches that request's own HistoryEntry. Nil is a no-op.
	OnSigned func(requestID string, event *nip01.Event)

	// OnAutoApproved, if set, fires for a request allowed instantly by a
	// standing grant (or, for "connect", a pending GrantSpec), which never
	// touches Queue.Add and so never reaches Queue.OnResolved. daemon.go
	// wires this to recordAutoApproved so such requests still get a
	// HistoryEntry. Fired before the request is carried out, so a
	// sign_event's HistoryEntry exists by the time OnSigned attaches the
	// signed event to it. Nil is a no-op.
	OnAutoApproved func(Pending)

	// OnDecision, if set, also receives every request's outcome --
	// daemon.go logs it and confirms nostrconnect pairings with it.
	OnDecision func(bunker.Decision)
	// Logf receives the signer's operational messages.
	Logf func(format string, args ...any)

	once   sync.Once
	srv    *bunker.Server
	srvErr error

	mu sync.Mutex
	// pendingGrantSpec is what to remember for whichever pubkey presents
	// the armed secret -- see SetPendingGrants.
	pendingGrantSpec *GrantSpec

	// pairingSecretTTL overrides PairingSecretTTL when non-zero -- a
	// test-only seam.
	pairingSecretTTL time.Duration
}

// server builds the bunker.Server on first use, from the fields set by
// then.
func (h *Handler) server() (*bunker.Server, error) {
	h.once.Do(func() {
		key, err := nip46.NewLocalKey(h.IdentityPriv)
		if err != nil {
			h.srvErr = err
			return
		}
		h.srv, h.srvErr = bunker.NewServer(bunker.ServerConfig{
			Key:        key,
			Relays:     h.Relays,
			Policy:     h,
			OnDecision: h.decided,
			Logf: func(format string, args ...any) {
				if h.Logf != nil {
					h.Logf(format, args...)
				}
			},
		})
	})
	return h.srv, h.srvErr
}

// SetPendingSecret arms secret as the one the next bunker://-flow
// "connect" must present, for PairingSecretTTL. "" cancels. It also drops
// any GrantSpec staged for a previous attempt: callers that want grants
// for this one call SetPendingGrants after this, not before.
func (h *Handler) SetPendingSecret(secret string) {
	h.mu.Lock()
	h.pendingGrantSpec = nil
	ttl := PairingSecretTTL
	if h.pairingSecretTTL != 0 {
		ttl = h.pairingSecretTTL
	}
	h.mu.Unlock()
	if srv, err := h.server(); err == nil {
		srv.ArmSecret(secret, ttl)
	}
}

// SetPendingGrants records spec to apply to whichever pubkey presents the
// secret SetPendingSecret armed -- `ncli bunker connect --grants <file>`'s
// hook into the bunker:// flow, where the app's pubkey isn't known until
// it connects.
func (h *Handler) SetPendingGrants(spec *GrantSpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pendingGrantSpec = spec
}

func (h *Handler) takePendingGrantSpec() *GrantSpec {
	h.mu.Lock()
	defer h.mu.Unlock()
	spec := h.pendingGrantSpec
	h.pendingGrantSpec = nil
	return spec
}

func (h *Handler) hasPendingGrantSpec() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pendingGrantSpec != nil
}

// Handle answers one request. The returned event is signed and ready to
// publish; nil means no response could be built at all.
func (h *Handler) Handle(req *nip46.RequestEvent, encryption string) *nip01.Event {
	srv, err := h.server()
	if err != nil {
		return nil
	}
	resp, err := srv.Handle(context.Background(), req, encryption)
	if err != nil {
		return nil
	}
	return resp
}

// Authorize implements bunker.Policy. A connect only reaches it after the
// server matched the armed secret.
func (h *Handler) Authorize(_ context.Context, call *bunker.Call) error {
	peer := call.Client

	if call.Method == nip46.MethodConnect && h.hasPendingGrantSpec() {
		// `ncli bunker connect --grants <file>` staging a GrantSpec for
		// this secret IS the approval decision for this pairing; without
		// it the unattended case would block on the queue with nobody to
		// click. Params are omitted: they carry the pairing secret.
		if h.OnAutoApproved != nil {
			h.OnAutoApproved(Pending{ID: call.ID, ClientKey: peer, Method: call.Method, CreatedAt: time.Now()})
		}
		h.pair(call)
		return nil
	}

	// switch_relays and logout manage the client's own session and expose
	// nothing, and a compliant client fires switch_relays after every
	// connect -- asking a human about them would be noise. Answered for a
	// paired client, refused for anyone else; not recorded in History.
	if call.Method == nip46.MethodSwitchRelays || call.Method == nip46.MethodLogout {
		if !h.Store.IsPaired(peer) {
			return nip46.Deny("not paired")
		}
		return nil
	}

	kind := 0
	if call.Event != nil {
		kind = call.Event.Kind
	}
	switch h.Store.Decide(peer, call.Method, kind) {
	case Deny:
		return nip46.Deny("rejected")
	case Ask:
		pending := Pending{ID: call.ID, ClientKey: peer, Method: call.Method, Kind: kind, Params: call.Params, Event: call.Event}
		verdict, err := h.Queue.Add(pending)
		if err != nil {
			return nip46.Deny("signer busy: " + err.Error())
		}
		if verdict != Allow {
			return nip46.Deny("rejected")
		}
	case Allow:
		if h.OnAutoApproved != nil {
			h.OnAutoApproved(Pending{ID: call.ID, ClientKey: peer, Method: call.Method, Kind: kind, Params: call.Params, Event: call.Event, CreatedAt: time.Now()})
		}
	}
	if call.Method == nip46.MethodConnect {
		h.pair(call)
	}
	return nil
}

// pair registers an approved connect: the app's self-reported name/url (a
// display hint only), the perms it asked for (still ruled on by
// Store.Decide), then any staged GrantSpec. Best-effort: a disk hiccup
// shouldn't fail the handshake itself.
func (h *Handler) pair(call *bunker.Call) {
	peer := call.Client
	var name, url string
	if call.Metadata != nil {
		name, url = call.Metadata.Name, call.Metadata.Url
	}
	_ = h.Store.Pair(peer, name, url)
	now := time.Now()
	for _, g := range parsePerms(call.Perms, now) {
		_ = h.Store.Remember(peer, g)
	}
	if spec := h.takePendingGrantSpec(); spec != nil {
		for _, g := range spec.Resolve(now) {
			_ = h.Store.Remember(peer, g)
		}
		if spec.Nickname != "" {
			_, _ = h.Store.SetName(peer, spec.Nickname)
		}
	}
}

// decided runs after each request: it hands a signed event to OnSigned
// and ends a session on logout, once the response is settled.
func (h *Handler) decided(d bunker.Decision) {
	if d.Allowed() {
		switch d.Call.Method {
		case nip46.MethodSignEvent:
			if h.OnSigned != nil {
				var ev nip01.Event
				if err := json.Unmarshal([]byte(d.Result), &ev); err == nil {
					h.OnSigned(d.Call.ID, &ev)
				}
			}
		case nip46.MethodLogout:
			// A client that logs out and comes back has to pair again.
			_, _ = h.Store.Revoke(d.Call.Client)
		}
	}
	if h.OnDecision != nil {
		h.OnDecision(d)
	}
}
