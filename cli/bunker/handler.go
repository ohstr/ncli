package bunker

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/go-flokicoin/crypto/schnorr"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip04"
	"github.com/ohstr/nmilat/nip44"
	"github.com/ohstr/nmilat/nip46"
)

// PairingSecretTTL is how long a bunker:// pairing secret (SetPendingSecret)
// stays valid with nobody connecting -- the same 5-minute window
// DefaultPendingTTL already gives a pending decision, reused here so there's
// one consistent "how long do I have" mental model across the whole bunker
// flow rather than two different unexplained timeouts. board.go's
// showBunkerURI shows a live countdown against this same constant.
const PairingSecretTTL = DefaultPendingTTL

// Handler dispatches one parsed NIP-46 request at a time against the
// signer's identity, the remembered-permission Store, and the pending
// approval Queue. It has no relay/transport knowledge of its own --
// Handle returns the signed response event for the caller (daemon.go) to
// publish, so this whole dispatch path is testable without any network.
// Handle may block for as long as Queue's TTL while a human decides, so
// callers must invoke it from its own per-request goroutine rather than a
// shared read loop.
type Handler struct {
	IdentityPriv string
	IdentityPub  string
	Store        *Store
	Queue        *Queue
	Relays       []string

	// OnSigned, if set, fires from execute() right after a sign_event
	// request's event is actually signed -- daemon.go wires this to
	// recordSignedEvent so the signed JSON reaches that request's own
	// HistoryEntry, for board.go's HistoryTable to show/copy. Nil is a
	// valid no-op (e.g. in tests that construct a Handler directly).
	OnSigned func(requestID string, event *nip01.Event)

	mu                     sync.Mutex
	pendingSecret          string    // see SetPendingSecret
	pendingSecretExpiresAt time.Time // zero while pendingSecret == ""

	// pendingGrantSpec is what to remember (via GrantSpec.Resolve, then
	// Store.Remember/SetName) for whichever pubkey actually presents
	// pendingSecret -- see SetPendingGrants. nil (the common case, an
	// unscripted pairing) means nothing extra happens beyond the usual
	// Store.Pair.
	pendingGrantSpec *GrantSpec

	// pairingSecretTTL overrides PairingSecretTTL when non-zero -- test-only
	// seam so an integration test can prove a secret really does stop
	// working once its TTL elapses (real elapsed time, through the actual
	// wire protocol) without a test sleeping the real 5-minute default.
	pairingSecretTTL time.Duration
}

// SetPendingSecret records the secret this daemon currently expects on an
// incoming bunker://-flow "connect" request (the signer displayed a
// bunker:// URI carrying this secret and is waiting for a client to
// present it back), armed for PairingSecretTTL (or pairingSecretTTL, if
// that test-only override is set). Pass "" to stop expecting one -- Handle
// already clears it itself on a successful single-use match, so callers
// only need this to arm a freshly generated pairing or to cancel one.
func (h *Handler) SetPendingSecret(secret string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pendingSecret = secret
	// Arming a secret (or explicitly cancelling one with "") always starts
	// a fresh pairing attempt -- a spec left over from a previous one
	// (the operator generated a URI, set grants for it, then generated a
	// new one without using the first) must not silently attach to
	// whoever happens to complete this different attempt. Callers that
	// want grants for THIS attempt call SetPendingGrants after this, not
	// before.
	h.pendingGrantSpec = nil
	if secret == "" {
		h.pendingSecretExpiresAt = time.Time{}
		return
	}
	ttl := PairingSecretTTL
	if h.pairingSecretTTL > 0 {
		ttl = h.pairingSecretTTL
	}
	h.pendingSecretExpiresAt = time.Now().Add(ttl)
}

// SetPendingGrants records spec to resolve (via GrantSpec.Resolve, then
// Store.Remember/SetName) for whichever pubkey ends up presenting the
// secret currently armed by SetPendingSecret -- `ncli bunker connect
// --grants <file>`'s own hook into the bunker:// flow, where (unlike
// nostrconnect://) the app's pubkey isn't known until it actually
// connects, so there's nothing to resolve spec against yet at Connect
// time. Must be called after SetPendingSecret, which clears this as its
// own side effect (see that method's doc comment) -- calling this first
// would just have the next SetPendingSecret wipe it out again.
func (h *Handler) SetPendingGrants(spec *GrantSpec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pendingGrantSpec = spec
}

func (h *Handler) takePendingSecretIfMatches(given string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.pendingSecret == "" {
		return false
	}
	if time.Now().After(h.pendingSecretExpiresAt) {
		h.pendingSecret = "" // stale -- a client presenting an expired secret shouldn't revive it
		return false
	}
	if subtle.ConstantTimeCompare([]byte(given), []byte(h.pendingSecret)) != 1 {
		return false
	}
	h.pendingSecret = "" // single-use
	return true
}

// takePendingGrantSpec returns (and clears) whatever SetPendingGrants last
// recorded -- called exactly once, from execute()'s own MethodConnect
// case, right after a pairing attempt actually succeeds.
func (h *Handler) takePendingGrantSpec() *GrantSpec {
	h.mu.Lock()
	defer h.mu.Unlock()
	spec := h.pendingGrantSpec
	h.pendingGrantSpec = nil
	return spec
}

// hasPendingGrantSpec peeks (without consuming) whether a GrantSpec is
// currently staged -- Handle's own MethodConnect case uses this to decide
// whether to skip the usual Ask/Queue step for "connect" itself; the
// actual consuming read happens later, in execute(), via
// takePendingGrantSpec.
func (h *Handler) hasPendingGrantSpec() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pendingGrantSpec != nil
}

// Handle dispatches req, blocking on a human decision if neither an
// existing grant nor an explicit deny already resolves it. The returned
// event is signed and ready to publish; a nil return means req couldn't
// even be turned into an error response (a key/encoding failure, not a
// normal rejection) and should just be logged, not published.
func (h *Handler) Handle(req *nip46.RequestEvent, encryption string) *nip01.Event {
	peer := req.PubKey

	if req.Method == nip46.MethodConnect {
		given := ""
		if len(req.Params) > 1 {
			given = req.Params[1]
		}
		if !h.takePendingSecretIfMatches(given) {
			return h.errorResponse(peer, req.RequestID, "no matching pairing in progress", encryption)
		}
		// `ncli bunker connect --grants <file>` staging a GrantSpec for
		// this exact secret already IS the human's (or agent's) approval
		// decision for this pairing attempt -- without this, the
		// unattended case that flag exists for would still block on
		// Queue.Add below, exactly like an unscripted pairing's own
		// "connect" request does (see
		// TestHandle_Connect_GoesThroughApprovalQueueWhenNoGrant), with
		// no human present to ever click it. An unscripted pairing (no
		// spec staged) is untouched: it still asks, same as always.
		if h.hasPendingGrantSpec() {
			return h.execute(req, peer, encryption, nil)
		}
	}

	kind := 0
	var signEvt *nip01.Event
	if req.Method == nip46.MethodSignEvent {
		var err error
		signEvt, err = parseSignEventParams(req.Params)
		if err != nil {
			return h.errorResponse(peer, req.RequestID, "invalid params: "+err.Error(), encryption)
		}
		if signEvt.PubKey != "" && signEvt.PubKey != h.IdentityPub {
			return h.errorResponse(peer, req.RequestID, "event pubkey does not match signer identity", encryption)
		}
		kind = signEvt.Kind
	}

	switch h.Store.Decide(peer, req.Method, kind) {
	case Deny:
		return h.errorResponse(peer, req.RequestID, "rejected", encryption)
	case Ask:
		pending := Pending{ID: req.RequestID, ClientKey: peer, Method: req.Method, Kind: kind, Params: req.Params, Event: signEvt}
		verdict, err := h.Queue.Add(pending)
		if err != nil {
			return h.errorResponse(peer, req.RequestID, "signer busy: "+err.Error(), encryption)
		}
		if verdict != Allow {
			return h.errorResponse(peer, req.RequestID, "rejected", encryption)
		}
	}

	return h.execute(req, peer, encryption, signEvt)
}

func (h *Handler) execute(req *nip46.RequestEvent, peer, encryption string, signEvt *nip01.Event) *nip01.Event {
	switch req.Method {
	case nip46.MethodConnect:
		// Best-effort: a disk hiccup registering this pairing shouldn't
		// fail the handshake response itself -- see Store.Pair's own doc
		// comment. "", "" for app name/URL: bunker:// (this direction --
		// the client speaks first) carries no app-metadata field at the
		// NIP-46 protocol level at all, unlike nostrconnect:// (see
		// daemon.go's InitiateNostrconnect) -- there's nothing here to
		// pass even in principle, not just nothing supplied this time.
		_ = h.Store.Pair(peer, "", "")
		// Apply whatever `ncli bunker connect --grants <file>` armed
		// alongside this pairing's own secret (see SetPendingGrants) --
		// nil (the common, unscripted case) makes this a no-op. Resolved
		// against time.Now() here, not whenever the spec was loaded or
		// the bunker:// URI was generated -- see GrantSpec.Resolve's own
		// doc comment. Also best-effort, for the same reason as Pair
		// above: this is the handshake's own success response, not the
		// place to surface a disk-write failure.
		if spec := h.takePendingGrantSpec(); spec != nil {
			for _, g := range spec.Resolve(time.Now()) {
				_ = h.Store.Remember(peer, g)
			}
			if spec.Nickname != "" {
				_, _ = h.Store.SetName(peer, spec.Nickname)
			}
		}
		return h.okResponse(peer, req.RequestID, "ack", encryption)

	case nip46.MethodPing:
		return h.okResponse(peer, req.RequestID, "pong", encryption)

	case nip46.MethodGetPublicKey:
		return h.okResponse(peer, req.RequestID, h.IdentityPub, encryption)

	case nip46.MethodGetRelays:
		return h.okResponse(peer, req.RequestID, h.relaysJSON(), encryption)

	case nip46.MethodSignEvent:
		if err := signEvt.Sign(h.IdentityPriv); err != nil {
			return h.errorResponse(peer, req.RequestID, "sign failed: "+err.Error(), encryption)
		}
		if h.OnSigned != nil {
			h.OnSigned(req.RequestID, signEvt)
		}
		out, err := json.Marshal(signEvt)
		if err != nil {
			return h.errorResponse(peer, req.RequestID, "encode failed", encryption)
		}
		return h.okResponse(peer, req.RequestID, string(out), encryption)

	case nip46.MethodNIP04Encrypt, nip46.MethodNIP04Decrypt, nip46.MethodNIP44Encrypt, nip46.MethodNIP44Decrypt:
		result, err := h.crypto(req.Method, req.Params)
		if err != nil {
			return h.errorResponse(peer, req.RequestID, err.Error(), encryption)
		}
		return h.okResponse(peer, req.RequestID, result, encryption)

	default:
		return h.errorResponse(peer, req.RequestID, "unsupported method: "+req.Method, encryption)
	}
}

func (h *Handler) relaysJSON() string {
	m := make(map[string]map[string]bool, len(h.Relays))
	for _, r := range h.Relays {
		m[r] = map[string]bool{"read": true, "write": true}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func (h *Handler) crypto(method string, params []string) (string, error) {
	if len(params) < 2 {
		return "", errors.New("expected [pubkey, text] params")
	}
	peerPub, text := params[0], params[1]

	switch method {
	case nip46.MethodNIP04Encrypt:
		return nip04.Encrypt(text, h.IdentityPriv, peerPub)
	case nip46.MethodNIP04Decrypt:
		return nip04.Decrypt(text, peerPub, h.IdentityPriv)
	case nip46.MethodNIP44Encrypt:
		key, err := h.nip44ConversationKey(peerPub)
		if err != nil {
			return "", err
		}
		return nip44.Encrypt(text, key)
	case nip46.MethodNIP44Decrypt:
		key, err := h.nip44ConversationKey(peerPub)
		if err != nil {
			return "", err
		}
		return nip44.Decrypt(text, key)
	default:
		return "", fmt.Errorf("unsupported method %q", method)
	}
}

// nip44ConversationKey mirrors nip46's own (unexported) deriveKeys +
// GenerateConversationKey pairing -- duplicated rather than imported since
// nip46 doesn't export that pair, and it's a handful of lines of pure key
// parsing, not protocol logic worth a cross-module change for.
func (h *Handler) nip44ConversationKey(peerPubkeyHex string) ([]byte, error) {
	privBytes, err := hex.DecodeString(h.IdentityPriv)
	if err != nil {
		return nil, fmt.Errorf("invalid identity private key: %w", err)
	}
	priv, _ := btcec.PrivKeyFromBytes(privBytes)

	pubBytes, err := hex.DecodeString(peerPubkeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid peer public key: %w", err)
	}
	pub, err := schnorr.ParsePubKey(pubBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid peer public key: %w", err)
	}

	return nip44.GenerateConversationKey(priv, pub)
}

func parseSignEventParams(params []string) (*nip01.Event, error) {
	if len(params) < 1 {
		return nil, errors.New("sign_event requires an event param")
	}
	var ev nip01.Event
	if err := json.Unmarshal([]byte(params[0]), &ev); err != nil {
		return nil, fmt.Errorf("malformed event JSON: %w", err)
	}
	return &ev, nil
}

func (h *Handler) okResponse(peer, requestID, result, encryption string) *nip01.Event {
	ev, err := nip46.NewResponseEvent(h.IdentityPriv, peer, requestID, result, encryption)
	if err != nil {
		return nil
	}
	if err := ev.Sign(h.IdentityPriv); err != nil {
		return nil
	}
	return ev
}

func (h *Handler) errorResponse(peer, requestID, msg, encryption string) *nip01.Event {
	ev, err := nip46.NewErrorResponseEvent(h.IdentityPriv, peer, requestID, msg, encryption)
	if err != nil {
		return nil
	}
	if err := ev.Sign(h.IdentityPriv); err != nil {
		return nil
	}
	return ev
}
