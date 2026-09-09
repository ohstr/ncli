package client

import (
	"testing"
	"time"

	relayclient "github.com/ohstr/nmilat/relay/client"
)

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

// TestTimeoutSpecConnectionConfig is a regression guard for a real bug this
// caught: SyncModule.execute (client/neg_sync.go) used to build a
// *relayclient.ConnectionConfig by hand as `&relayclient.ConnectionConfig{}`
// whenever spec.Timeouts was non-nil -- an all-zero struct, silently
// discarding every configured handshake/ping/pong/write value, since
// relayclient.NewConnection backfills any zero field with its own
// defaults regardless of whether the whole config is nil or just
// zero-valued. A `sync.yaml` with an explicit `timeouts:` block therefore
// had no effect at all. Fixed by giving TimeoutSpec a single
// ConnectionConfig method (this test) that both StreamChannel's and
// SyncModule's connection setup now share, instead of each hand-rolling
// (or, in sync's case, failing to hand-roll) the string-to-duration
// conversion.
func TestTimeoutSpecConnectionConfig(t *testing.T) {
	defaults := relayclient.DefaultConnectionConfig()

	t.Run("nil TimeoutSpec uses relayclient's own defaults", func(t *testing.T) {
		var ts *TimeoutSpec
		got := ts.ConnectionConfig()
		if *got != *defaults {
			t.Errorf("ConnectionConfig() = %+v, want relayclient defaults %+v", got, defaults)
		}
	})

	t.Run("empty TimeoutSpec (no fields set) uses relayclient's own defaults", func(t *testing.T) {
		got := (&TimeoutSpec{}).ConnectionConfig()
		if *got != *defaults {
			t.Errorf("ConnectionConfig() = %+v, want relayclient defaults %+v", got, defaults)
		}
	})

	t.Run("every configured value actually takes effect", func(t *testing.T) {
		handshake, ping, pong, write := "3s", "7s", "11s", "13s"
		ts := &TimeoutSpec{Handshake: &handshake, Ping: &ping, Pong: &pong, Write: &write}

		got := ts.ConnectionConfig()

		want := relayclient.ConnectionConfig{
			HandshakeTimeout: 3 * time.Second,
			PingInterval:     7 * time.Second,
			PongTimeout:      11 * time.Second,
			WriteTimeout:     13 * time.Second,
		}
		if *got != want {
			t.Errorf("ConnectionConfig() = %+v, want %+v -- a configured `timeouts:` block must actually change the connection config, not silently fall back to defaults", got, want)
		}
	})

	t.Run("an unparseable duration string falls back to that field's default, not zero", func(t *testing.T) {
		bogus := "not-a-duration"
		got := (&TimeoutSpec{Pong: &bogus}).ConnectionConfig()
		if got.PongTimeout != defaults.PongTimeout {
			t.Errorf("PongTimeout = %v for an unparseable value, want the default %v (never a zero/immediate timeout)", got.PongTimeout, defaults.PongTimeout)
		}
	})
}
