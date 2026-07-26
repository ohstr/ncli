package bunker

import (
	"net/url"
	"strings"
	"testing"

	"github.com/ohstr/nmilat/nip46"
)

func TestNewSecret_UniqueAndHex(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("NewSecret() produced the same value twice")
	}
	if len(a) != secretByteLen*2 {
		t.Errorf("len(secret) = %d, want %d (hex-encoded)", len(a), secretByteLen*2)
	}
}

func TestBunkerURI_RoundTripsAsURL(t *testing.T) {
	uri := BunkerURI("abc123", "s3cr3t", []string{"wss://relay.one", "wss://relay.two"})

	u, err := url.ParseRequestURI(uri)
	if err != nil {
		t.Fatalf("BunkerURI produced an unparseable URI: %v", err)
	}
	if u.Scheme != "bunker" {
		t.Errorf("scheme = %q, want bunker", u.Scheme)
	}
	if u.Host != "abc123" {
		t.Errorf("host = %q, want abc123", u.Host)
	}
	q := u.Query()
	if q.Get("secret") != "s3cr3t" {
		t.Errorf("secret = %q, want s3cr3t", q.Get("secret"))
	}
	if relays := q["relay"]; len(relays) != 2 || relays[0] != "wss://relay.one" || relays[1] != "wss://relay.two" {
		t.Errorf("relays = %v, want both relays preserved", relays)
	}
}

func TestBunkerURI_NoSecret(t *testing.T) {
	uri := BunkerURI("abc123", "", []string{"wss://relay.one"})
	u, err := url.ParseRequestURI(uri)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Has("secret") {
		t.Error("empty secret should not appear as a query param at all")
	}
}

// Sanity check that nip46.ParseNostrconnect (consumed directly, not
// wrapped) parses the shape ncli will actually receive from another
// client's nostrconnect:// string -- confirms the integration point uri.go
// documents, without re-testing nip46 itself.
func TestParseNostrconnect_Integration(t *testing.T) {
	uri := "nostrconnect://" + strings.Repeat("b", 64) +
		"?relay=" + url.QueryEscape("wss://relay.example") +
		"&secret=abc&metadata=" + url.QueryEscape(`{"name":"Test App"}`)

	parsed, err := nip46.ParseNostrconnect(uri)
	if err != nil {
		t.Fatalf("ParseNostrconnect() error = %v", err)
	}
	if parsed.Secret != "abc" {
		t.Errorf("Secret = %q, want abc", parsed.Secret)
	}
	if parsed.Metadata.Name != "Test App" {
		t.Errorf("Metadata.Name = %q, want %q", parsed.Metadata.Name, "Test App")
	}
}
