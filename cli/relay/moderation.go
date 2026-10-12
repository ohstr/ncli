package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip86"
	"github.com/ohstr/nmilat/relay"
)

// NIP-86 methods nmilat's nip86 package has no constant for.
const (
	methodBanEvent                    = "banevent"
	methodAllowEvent                  = "allowevent"
	methodListBannedEvents            = "listbannedevents"
	methodListEventsNeedingModeration = "listeventsneedingmoderation"
	methodChangeRelayName             = "changerelayname"
	methodChangeRelayDescription      = "changerelaydescription"
	methodChangeRelayIcon             = "changerelayicon"
	methodAllowKind                   = "allowkind"
	methodDisallowKind                = "disallowkind"
	methodListAllowedKinds            = "listallowedkinds"
	methodListDisallowedKinds         = "listdisallowedkinds"
	methodBlockIP                     = "blockip"
	methodUnblockIP                   = "unblockip"
	methodListBlockedIPs              = "listblockedips"
)

// moderationState is what NIP-86 moderation changes, saved as JSON next to
// the relay's store so it survives a restart.
type moderationState struct {
	BannedPubkeys   map[string]string `json:"banned_pubkeys,omitempty"`
	BannedEvents    map[string]string `json:"banned_events,omitempty"`
	AllowedKinds    []int             `json:"allowed_kinds,omitempty"`
	DisallowedKinds []int             `json:"disallowed_kinds,omitempty"`
	BlockedIPs      map[string]string `json:"blocked_ips,omitempty"`
	Name            *string           `json:"name,omitempty"`
	Description     *string           `json:"description,omitempty"`
	Icon            *string           `json:"icon,omitempty"`
}

// relayModeration holds the relay's NIP-86 moderation state: bans, kind
// rules, blocked IPs and NIP-11 overrides. Every change is saved before it
// takes effect.
type relayModeration struct {
	mu    sync.RWMutex
	path  string
	self  string
	state moderationState

	// onNip11Change runs after the name, description or icon changes.
	onNip11Change func()
}

// moderationPath is where a relay keeps its moderation state: beside its
// store, so each relay context has its own.
func moderationPath(store string) string {
	return store + ".moderation.json"
}

// loadModeration reads path, or starts empty when it doesn't exist. self,
// the relay's own pubkey, is never refused.
func loadModeration(path, self string) (*relayModeration, error) {
	m := &relayModeration{path: path, self: strings.ToLower(self)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &m.state); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// save writes the state atomically. Called with mu held.
func (m *relayModeration) save() error {
	data, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// update applies fn to a copy of the state, saves it, then keeps it: a
// failed save changes nothing.
func (m *relayModeration) update(fn func(*moderationState)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.state
	next := cloneState(m.state)
	fn(&next)
	m.state = next
	if err := m.save(); err != nil {
		m.state = prev
		return err
	}
	return nil
}

func cloneState(s moderationState) moderationState {
	c := s
	c.BannedPubkeys = cloneMap(s.BannedPubkeys)
	c.BannedEvents = cloneMap(s.BannedEvents)
	c.BlockedIPs = cloneMap(s.BlockedIPs)
	c.AllowedKinds = slices.Clone(s.AllowedKinds)
	c.DisallowedKinds = slices.Clone(s.DisallowedKinds)
	return c
}

func cloneMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// eventPolicy is the relay's relay.EventPolicy: it refuses events from a
// banned pubkey, a banned event, and kinds the kind rules exclude. The
// relay's own events are never refused.
func (m *relayModeration) eventPolicy(ev *nip01.Event) string {
	if m == nil || ev == nil {
		return ""
	}
	pubkey := strings.ToLower(ev.PubKey)
	if m.self != "" && pubkey == m.self {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, banned := m.state.BannedPubkeys[pubkey]; banned {
		return "blocked: pubkey is banned from this relay"
	}
	if _, banned := m.state.BannedEvents[strings.ToLower(ev.ID)]; banned {
		return "blocked: event is banned from this relay"
	}
	if slices.Contains(m.state.DisallowedKinds, ev.Kind) ||
		(len(m.state.AllowedKinds) > 0 && !slices.Contains(m.state.AllowedKinds, ev.Kind)) {
		return fmt.Sprintf("blocked: kind %d is not allowed on this relay", ev.Kind)
	}
	return ""
}

// ipBlocked reports whether r comes from a blocked IP. Only the connection's
// own address counts: a forwarded-for header is set by the client, so it
// can't be trusted without knowing the proxy in front.
func (m *relayModeration) ipBlocked(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, blocked := m.state.BlockedIPs[ip.String()]
	return blocked
}

// ipGate refuses every request from a blocked IP except NIP-86 management
// calls, so an admin can never lock themselves out.
func (m *relayModeration) ipGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !nip86.IsManagementRequest(r) && r.Method != http.MethodOptions && m.ipBlocked(r) {
			http.Error(w, "blocked: your IP is blocked from this relay", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// applyNip11 writes the name, description and icon overrides into md.
func (m *relayModeration) applyNip11(md *nip11.Metadata) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.state.Name != nil {
		md.Name = *m.state.Name
	}
	if m.state.Description != nil {
		md.Description = *m.state.Description
	}
	if m.state.Icon != nil {
		md.Icon = *m.state.Icon
	}
}

// reasonEntry is one row of a NIP-86 list method's result.
type reasonEntry struct {
	Pubkey string `json:"pubkey,omitempty"`
	ID     string `json:"id,omitempty"`
	IP     string `json:"ip,omitempty"`
	Reason string `json:"reason"`
}

// optionalReason reads a method's optional reason parameter.
func optionalReason(req nip86.Request, i int) (string, error) {
	if len(req.Params) <= i {
		return "", nil
	}
	return req.StringParam(i)
}

func sortedEntries(m map[string]string, row func(key, reason string) reasonEntry) []reasonEntry {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]reasonEntry, 0, len(keys))
	for _, k := range keys {
		out = append(out, row(k, m[k]))
	}
	return out
}

// registerModerationMethods adds NIP-86's moderation methods to router:
// pubkey and event bans, kind rules, IP blocks and the relay's NIP-11 name,
// description and icon. banevent also deletes the stored event.
func registerModerationMethods(router *nip86.Router, m *relayModeration, store *relay.EventStore) {
	ok := func(err error) (any, error) {
		if err != nil {
			return nil, err
		}
		return true, nil
	}

	router.Handle(nip86.MethodBanPubkey, func(_ context.Context, _ string, req nip86.Request) (any, error) {
		raw, err := req.StringParam(0)
		if err != nil {
			return nil, err
		}
		pubkey, err := nip19.ParsePublicKey(raw)
		if err != nil {
			return nil, err
		}
		if pubkey == m.self {
			return nil, errors.New("the relay's own key can't be banned")
		}
		reason, err := optionalReason(req, 1)
		if err != nil {
			return nil, err
		}
		return ok(m.update(func(s *moderationState) { s.BannedPubkeys[pubkey] = reason }))
	})
	router.Handle(nip86.MethodUnbanPubkey, func(_ context.Context, _ string, req nip86.Request) (any, error) {
		raw, err := req.StringParam(0)
		if err != nil {
			return nil, err
		}
		pubkey, err := nip19.ParsePublicKey(raw)
		if err != nil {
			return nil, err
		}
		return ok(m.update(func(s *moderationState) { delete(s.BannedPubkeys, pubkey) }))
	})
	router.Handle(nip86.MethodListBannedPubkeys, func(context.Context, string, nip86.Request) (any, error) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return sortedEntries(m.state.BannedPubkeys, func(k, r string) reasonEntry { return reasonEntry{Pubkey: k, Reason: r} }), nil
	})

	router.Handle(methodBanEvent, func(ctx context.Context, _ string, req nip86.Request) (any, error) {
		id, err := eventIDParam(req)
		if err != nil {
			return nil, err
		}
		reason, err := optionalReason(req, 1)
		if err != nil {
			return nil, err
		}
		if err := m.update(func(s *moderationState) { s.BannedEvents[id] = reason }); err != nil {
			return nil, err
		}
		pes, err := store.FindEvents(ctx, &nip01.SubscriptionFilter{IDs: []string{id}})
		if err == nil && len(pes) > 0 {
			err = store.DeleteAll(pes)
		}
		return ok(err)
	})
	router.Handle(methodAllowEvent, func(_ context.Context, _ string, req nip86.Request) (any, error) {
		id, err := eventIDParam(req)
		if err != nil {
			return nil, err
		}
		return ok(m.update(func(s *moderationState) { delete(s.BannedEvents, id) }))
	})
	router.Handle(methodListBannedEvents, func(context.Context, string, nip86.Request) (any, error) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return sortedEntries(m.state.BannedEvents, func(k, r string) reasonEntry { return reasonEntry{ID: k, Reason: r} }), nil
	})
	// ncli keeps no review queue: nothing is ever waiting.
	router.Handle(methodListEventsNeedingModeration, func(context.Context, string, nip86.Request) (any, error) {
		return []reasonEntry{}, nil
	})

	setText := func(field func(*moderationState) **string) nip86.Method {
		return func(_ context.Context, _ string, req nip86.Request) (any, error) {
			v, err := req.StringParam(0)
			if err != nil {
				return nil, err
			}
			if err := m.update(func(s *moderationState) { *field(s) = &v }); err != nil {
				return nil, err
			}
			if m.onNip11Change != nil {
				m.onNip11Change()
			}
			return true, nil
		}
	}
	router.Handle(methodChangeRelayName, setText(func(s *moderationState) **string { return &s.Name }))
	router.Handle(methodChangeRelayDescription, setText(func(s *moderationState) **string { return &s.Description }))
	router.Handle(methodChangeRelayIcon, setText(func(s *moderationState) **string { return &s.Icon }))

	router.Handle(methodAllowKind, func(_ context.Context, _ string, req nip86.Request) (any, error) {
		kind, err := kindParam(req)
		if err != nil {
			return nil, err
		}
		return ok(m.update(func(s *moderationState) {
			s.DisallowedKinds = slices.DeleteFunc(s.DisallowedKinds, func(k int) bool { return k == kind })
			if !slices.Contains(s.AllowedKinds, kind) {
				s.AllowedKinds = append(s.AllowedKinds, kind)
				slices.Sort(s.AllowedKinds)
			}
		}))
	})
	router.Handle(methodDisallowKind, func(_ context.Context, _ string, req nip86.Request) (any, error) {
		kind, err := kindParam(req)
		if err != nil {
			return nil, err
		}
		return ok(m.update(func(s *moderationState) {
			s.AllowedKinds = slices.DeleteFunc(s.AllowedKinds, func(k int) bool { return k == kind })
			if !slices.Contains(s.DisallowedKinds, kind) {
				s.DisallowedKinds = append(s.DisallowedKinds, kind)
				slices.Sort(s.DisallowedKinds)
			}
		}))
	})
	router.Handle(methodListAllowedKinds, func(context.Context, string, nip86.Request) (any, error) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return append([]int{}, m.state.AllowedKinds...), nil
	})
	router.Handle(methodListDisallowedKinds, func(context.Context, string, nip86.Request) (any, error) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return append([]int{}, m.state.DisallowedKinds...), nil
	})

	router.Handle(methodBlockIP, func(_ context.Context, _ string, req nip86.Request) (any, error) {
		ip, err := ipParam(req)
		if err != nil {
			return nil, err
		}
		reason, err := optionalReason(req, 1)
		if err != nil {
			return nil, err
		}
		return ok(m.update(func(s *moderationState) { s.BlockedIPs[ip] = reason }))
	})
	router.Handle(methodUnblockIP, func(_ context.Context, _ string, req nip86.Request) (any, error) {
		ip, err := ipParam(req)
		if err != nil {
			return nil, err
		}
		return ok(m.update(func(s *moderationState) { delete(s.BlockedIPs, ip) }))
	})
	router.Handle(methodListBlockedIPs, func(context.Context, string, nip86.Request) (any, error) {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return sortedEntries(m.state.BlockedIPs, func(k, r string) reasonEntry { return reasonEntry{IP: k, Reason: r} }), nil
	})
}

func eventIDParam(req nip86.Request) (string, error) {
	id, err := req.StringParam(0)
	if err != nil {
		return "", err
	}
	id = strings.ToLower(id)
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		return "", fmt.Errorf("%w: param 0 is not a hex event id", nip86.ErrWrongParamType)
	}
	return id, nil
}

func kindParam(req nip86.Request) (int, error) {
	kind, err := req.IntParam(0)
	if err != nil {
		return 0, err
	}
	if kind < 0 || kind > 65535 {
		return 0, fmt.Errorf("%w: kind %d is out of range", nip86.ErrWrongParamType, kind)
	}
	return kind, nil
}

func ipParam(req nip86.Request) (string, error) {
	raw, err := req.StringParam(0)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return "", fmt.Errorf("%w: param 0 is not an IP address", nip86.ErrWrongParamType)
	}
	return ip.String(), nil
}
