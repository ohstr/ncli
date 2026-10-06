package relay

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
)

// TestNIP11Document_AdvertisesNIP29Subgroups is the regression test for a
// real-relay repro: curling a running ncli relay's own NIP-11 document
// never showed a "nip29" object, even with 29 correctly listed in
// supported_nips. The cause was this service's own "/" handler building
// its NIP-11 response from nip11.NewHandler(&config.Nip11, ...) directly
// -- which marshals once at startup from the plain config struct, whose
// NIP29 field is never set by anything -- instead of going through
// nmilat's SessionHandler.ServeHTTP, which computes nip29.subgroups
// dynamically but isn't reachable here (see NewServer's comment on why
// not). Fixed by replicating that same RegisteredNIPs() check locally
// before building this service's own handler.
func TestNIP11Document_AdvertisesNIP29Subgroups(t *testing.T) {
	prevConfig := config
	t.Cleanup(func() { config = prevConfig })

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := relay.NewEventStore(dbPath, &nip11.Limitation{MaxLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	config = RelayConfig{
		Nip11: nip11.Metadata{PubKey: strings.Repeat("a", 64), PrivKey: strings.Repeat("1", 64)},
	}

	s := NewServer(store, nil)
	t.Cleanup(s.Stop)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept", nip11.ContentTypeHeader)
	rec := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(rec, req)

	var doc struct {
		SupportedNips []json.RawMessage `json:"supported_nips"`
		NIP29         *struct {
			Subgroups bool `json:"subgroups"`
		} `json:"nip29"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal NIP-11 document (%q): %v", rec.Body.String(), err)
	}

	found29 := false
	for _, raw := range doc.SupportedNips {
		if string(raw) == "29" {
			found29 = true
			break
		}
	}
	if !found29 {
		t.Fatalf("supported_nips = %s, want 29 present (nip29/relayreg should always be linked in)", rec.Body.String())
	}

	if doc.NIP29 == nil || !doc.NIP29.Subgroups {
		t.Errorf(`nip29 = %+v, want {"subgroups": true} -- supported_nips already lists 29, so this document is self-contradictory`, doc.NIP29)
	}
}
