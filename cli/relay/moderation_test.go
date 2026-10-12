package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/nip86"
	"github.com/stretchr/testify/require"
)

const otherPrivKey = "2222222222222222222222222222222222222222222222222222222222222222"

func newModeration(t *testing.T) (*relayModeration, *nip86.Router, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relay.db.moderation.json")
	m, err := loadModeration(path, testPubKey)
	require.NoError(t, err)
	_, store := newTestWSHandler(t)
	router := nip86.NewRouter()
	registerModerationMethods(router, m, store)
	return m, router, path
}

func signedBy(t *testing.T, priv string, kind int) *nip01.Event {
	t.Helper()
	ev := nip01.NewEvent(kind, "x")
	require.NoError(t, ev.Sign(priv))
	return ev
}

func TestModeration_BanPubkey(t *testing.T) {
	m, router, _ := newModeration(t)
	ev := signedBy(t, otherPrivKey, 1)

	require.Empty(t, m.eventPolicy(ev))
	resp := call(t, router, nip86.MethodBanPubkey, `"`+ev.PubKey+`"`, `"spam"`)
	require.Empty(t, resp.Error)
	require.Equal(t, "blocked: pubkey is banned from this relay", m.eventPolicy(ev))

	listed := call(t, router, nip86.MethodListBannedPubkeys)
	require.Equal(t, []reasonEntry{{Pubkey: ev.PubKey, Reason: "spam"}}, listed.Result)

	require.NotEmpty(t, call(t, router, nip86.MethodBanPubkey, `"`+testPubKey+`"`).Error, "the relay's own key can't be banned")
	require.NotEmpty(t, call(t, router, nip86.MethodBanPubkey, `"nope"`).Error)

	require.Empty(t, call(t, router, nip86.MethodUnbanPubkey, `"`+ev.PubKey+`"`).Error)
	require.Empty(t, m.eventPolicy(ev))
}

func TestModeration_BanEventDeletesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.json")
	m, err := loadModeration(path, testPubKey)
	require.NoError(t, err)
	_, store := newTestWSHandler(t)
	router := nip86.NewRouter()
	registerModerationMethods(router, m, store)

	ev := signedBy(t, otherPrivKey, 1)
	require.NoError(t, store.InsertEvents(context.Background(), []*nip01.Event{ev}))

	require.Empty(t, call(t, router, methodBanEvent, `"`+ev.ID+`"`, `"illegal"`).Error)
	found, err := store.QueryEvents(context.Background(), &nip01.SubscriptionFilter{IDs: []string{ev.ID}})
	require.NoError(t, err)
	require.Empty(t, found, "a banned event must be deleted")
	require.Equal(t, "blocked: event is banned from this relay", m.eventPolicy(ev))
	require.Equal(t, []reasonEntry{{ID: ev.ID, Reason: "illegal"}}, call(t, router, methodListBannedEvents).Result)

	require.Empty(t, call(t, router, methodAllowEvent, `"`+ev.ID+`"`).Error)
	require.Empty(t, m.eventPolicy(ev))
	require.Equal(t, []reasonEntry{}, call(t, router, methodListEventsNeedingModeration).Result)
	require.NotEmpty(t, call(t, router, methodBanEvent, `"short"`).Error)
}

func TestModeration_Kinds(t *testing.T) {
	m, router, _ := newModeration(t)
	note, reaction := signedBy(t, otherPrivKey, 1), signedBy(t, otherPrivKey, 7)

	require.Empty(t, call(t, router, methodDisallowKind, `7`).Error)
	require.Empty(t, m.eventPolicy(note))
	require.Equal(t, "blocked: kind 7 is not allowed on this relay", m.eventPolicy(reaction))

	// An allowlist refuses every kind it doesn't name; allowing a
	// disallowed kind moves it across.
	require.Empty(t, call(t, router, methodAllowKind, `7`).Error)
	require.Equal(t, []int{7}, call(t, router, methodListAllowedKinds).Result)
	require.Equal(t, []int{}, call(t, router, methodListDisallowedKinds).Result)
	require.Empty(t, m.eventPolicy(reaction))
	require.NotEmpty(t, m.eventPolicy(note))

	// The relay's own events are never refused.
	require.Empty(t, m.eventPolicy(signedBy(t, testPrivKey, 1)))
	require.NotEmpty(t, call(t, router, methodAllowKind, `70000`).Error)
}

func TestModeration_IPGate(t *testing.T) {
	m, router, _ := newModeration(t)
	require.Empty(t, call(t, router, methodBlockIP, `"192.0.2.7"`, `"abuse"`).Error)
	require.Equal(t, []reasonEntry{{IP: "192.0.2.7", Reason: "abuse"}}, call(t, router, methodListBlockedIPs).Result)

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	gate := m.ipGate(next)
	serve := func(addr, contentType string) int {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
		r.RemoteAddr = addr
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		return w.Code
	}
	require.Equal(t, http.StatusForbidden, serve("192.0.2.7:4000", ""))
	require.Equal(t, http.StatusTeapot, serve("192.0.2.8:4000", ""))
	require.Equal(t, http.StatusTeapot, serve("192.0.2.7:4000", nip86.ContentType), "an admin must not lock themselves out")

	require.Empty(t, call(t, router, methodUnblockIP, `"192.0.2.7"`).Error)
	require.Equal(t, http.StatusTeapot, serve("192.0.2.7:4000", ""))
	require.NotEmpty(t, call(t, router, methodBlockIP, `"not-an-ip"`).Error)
}

func TestModeration_Nip11AndPersistence(t *testing.T) {
	m, router, path := newModeration(t)
	changed := 0
	m.onNip11Change = func() { changed++ }

	require.Empty(t, call(t, router, methodChangeRelayName, `"Community"`).Error)
	require.Empty(t, call(t, router, methodChangeRelayDescription, `"Our place"`).Error)
	require.Empty(t, call(t, router, methodChangeRelayIcon, `"https://relay.example/icon.png"`).Error)
	require.Equal(t, 3, changed)
	require.Empty(t, call(t, router, nip86.MethodBanPubkey, `"`+testMember+`"`).Error)

	reloaded, err := loadModeration(path, testPubKey)
	require.NoError(t, err)
	md := nip11.Metadata{Name: "from config", Description: "from config"}
	reloaded.applyNip11(&md)
	require.Equal(t, "Community", md.Name)
	require.Equal(t, "Our place", md.Description)
	require.Equal(t, "https://relay.example/icon.png", md.Icon)
	require.NotEmpty(t, reloaded.eventPolicy(&nip01.Event{PubKey: testMember, ID: strings.Repeat("0", 64)}), "a ban survives a restart")
}

// Without membership, NIP-86 serves only the moderation methods.
func TestNip86Handler_WithoutMembership(t *testing.T) {
	prev := config
	t.Cleanup(func() { config = prev })
	config = RelayConfig{Nip11: nip11.Metadata{PubKey: testPubKey}, Nip86: &Nip86Config{Enabled: true}}

	m, _, _ := newModeration(t)
	wsHandler, store := newTestWSHandler(t)
	h, err := newNip86Handler(wsHandler, store, nil, m)
	require.NoError(t, err)
	require.NotNil(t, h)
}
