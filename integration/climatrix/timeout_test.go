package climatrix

import (
	"testing"
	"time"
)

// --timeout bounds the whole per-target wait, connection handshake
// included: a relay that accepts the TCP connection and then says nothing
// must not hold the command past it.
func TestTimeoutCoversHandshake(t *testing.T) {
	url := blackhole(t)
	e := NewEnv(t)
	bin(t) // build outside the timed window
	start := time.Now()
	e.Run(t, "find", "--kinds", "1", "-s", url, "--timeout", "1s", "--json").ExpectErr(t, "network")
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("--timeout 1s against a silent relay took %s", took.Round(100*time.Millisecond))
	}
}
