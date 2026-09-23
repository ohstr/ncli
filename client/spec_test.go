package client

import (
	"testing"
	"time"

	relayclient "github.com/ohstr/nmilat/relay/client"
)

// TestTimeoutSpecConnectionConfig is a regression guard: SyncModule.execute
// used to build an all-zero ConnectionConfig whenever spec.Timeouts was
// non-nil, silently discarding every configured value (relayclient
// backfills zero fields with defaults regardless). A configured
// `timeouts:` block had no effect at all until this was fixed.
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
