package relay

import (
	"bytes"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// syncBuffer is a mutex-protected bytes.Buffer -- NewServer starts its
// listener in a background goroutine (service.go's "go s.serve()"), which
// logs independently of the main goroutine's own log calls (NewServer's
// "server config check", and Stop()'s own shutdown logging). A plain
// bytes.Buffer isn't safe for that, so capturing log output across this
// test's NewServer/Stop calls needs real synchronization, not just
// zerolog.SyncWriter (which only protects the Write side, not this test's
// own String() read).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestNewServer_WiresLoggerIntoSDK is the regression test for a gap found
// while verifying nmilat's new "nip11.url is not set" startup warning
// (nmilat#72) actually reaches an operator running `ncli relay`: it
// didn't. NewServer built its SessionConfig without ever setting Logger,
// so every SDK-internal log.Logger.Warn/Error call -- this one included,
// but also the pre-existing membership/groups cache-load-failure logging
// in relay/session.go -- wrote to a zero-value zerolog.Logger and was
// silently dropped, even though this service's own log.Info/Warn calls
// (e.g. "server config check") went through the real, configured
// zerolog/log global logger right next to it.
func TestNewServer_WiresLoggerIntoSDK(t *testing.T) {
	prevConfig := config
	prevLogger := log.Logger
	t.Cleanup(func() {
		config = prevConfig
		log.Logger = prevLogger
	})

	buf := &syncBuffer{}
	log.Logger = zerolog.New(buf)

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := relay.NewEventStore(dbPath, &nip11.Limitation{MaxLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	// nip11.URL deliberately left empty: nip29/relayreg is always linked
	// into this package (cli/relay/command.go's blank import), so this
	// is exactly the condition nmilat#72's warning fires on.
	config = RelayConfig{
		Nip11: nip11.Metadata{PubKey: strings.Repeat("a", 64), PrivKey: strings.Repeat("1", 64)},
	}

	s := NewServer(store, nil)
	t.Cleanup(s.Stop)

	if !strings.Contains(buf.String(), "nip11.url is not set") {
		t.Errorf("log output = %q, want the SDK's nip11.url warning to reach the configured logger", buf.String())
	}
}
