package client

import "testing"

func TestResolveRelayURL_ExplicitWss(t *testing.T) {
	primary, fallback, err := ResolveRelayURL("wss://relay.ohstr.com")
	if err != nil {
		t.Fatalf("ResolveRelayURL() error = %v", err)
	}
	if primary.String() != "wss://relay.ohstr.com" {
		t.Fatalf("primary = %q, want wss://relay.ohstr.com", primary.String())
	}
	if fallback != nil {
		t.Fatalf("fallback = %v, want nil for an explicit scheme", fallback)
	}
}

func TestResolveRelayURL_ExplicitWs(t *testing.T) {
	primary, fallback, err := ResolveRelayURL("ws://localhost:5500")
	if err != nil {
		t.Fatalf("ResolveRelayURL() error = %v", err)
	}
	if primary.String() != "ws://localhost:5500" {
		t.Fatalf("primary = %q, want ws://localhost:5500", primary.String())
	}
	if fallback != nil {
		t.Fatalf("fallback = %v, want nil for an explicit scheme", fallback)
	}
}

func TestResolveRelayURL_UnsupportedScheme(t *testing.T) {
	if _, _, err := ResolveRelayURL("https://example.com"); err == nil {
		t.Fatal("ResolveRelayURL(https://...) error = nil, want an error")
	}
}

func TestResolveRelayURL_EmptyHost(t *testing.T) {
	if _, _, err := ResolveRelayURL("wss://"); err == nil {
		t.Fatal("ResolveRelayURL(wss://) error = nil, want an error (empty host)")
	}
}

func TestResolveRelayURL_SchemelessHost(t *testing.T) {
	primary, fallback, err := ResolveRelayURL("relay.primal.net")
	if err != nil {
		t.Fatalf("ResolveRelayURL() error = %v", err)
	}
	if primary.String() != "wss://relay.primal.net" {
		t.Fatalf("primary = %q, want wss://relay.primal.net", primary.String())
	}
	if fallback == nil || fallback.String() != "ws://relay.primal.net" {
		t.Fatalf("fallback = %v, want ws://relay.primal.net", fallback)
	}
}

func TestResolveRelayURL_SchemelessHostPort(t *testing.T) {
	primary, fallback, err := ResolveRelayURL("localhost:4869")
	if err != nil {
		t.Fatalf("ResolveRelayURL() error = %v", err)
	}
	if primary.String() != "wss://localhost:4869" {
		t.Fatalf("primary = %q, want wss://localhost:4869", primary.String())
	}
	if fallback == nil || fallback.String() != "ws://localhost:4869" {
		t.Fatalf("fallback = %v, want ws://localhost:4869", fallback)
	}
}

func TestResolveRelayURL_SchemelessIP(t *testing.T) {
	primary, fallback, err := ResolveRelayURL("192.168.1.5:7000")
	if err != nil {
		t.Fatalf("ResolveRelayURL() error = %v", err)
	}
	if primary.String() != "wss://192.168.1.5:7000" {
		t.Fatalf("primary = %q, want wss://192.168.1.5:7000", primary.String())
	}
	if fallback == nil {
		t.Fatal("fallback = nil, want a ws:// fallback for a schemeless IP")
	}
}

func TestResolveRelayURL_SchemelessGarbageRejected(t *testing.T) {
	if _, _, err := ResolveRelayURL("not-a-relay-url"); err == nil {
		t.Fatal("ResolveRelayURL(no dot, no scheme) error = nil, want an error")
	}
}

func TestResolveRelayURL_SchemelessPathRejected(t *testing.T) {
	if _, _, err := ResolveRelayURL("../testdata/notes.db"); err == nil {
		t.Fatal("ResolveRelayURL(path with slash) error = nil, want an error")
	}
}

func TestLooksLikeRelayHost(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"relay.primal.net", true},
		{"localhost", true},
		{"localhost:4869", true},
		{"192.168.1.5", true},
		{"192.168.1.5:7000", true},
		{"not-a-relay-url", false},
		{"", false},
		{"../notes.db", false},
		{`foo\bar`, false},
	}
	for _, c := range cases {
		if got := looksLikeRelayHost(c.in); got != c.want {
			t.Errorf("looksLikeRelayHost(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
