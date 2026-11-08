package relay

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/relay/adminplane"
	"github.com/ohstr/nmilat/nip11"
	"github.com/ohstr/nmilat/relay"
	"github.com/spf13/viper"
)

func TestCheckListen(t *testing.T) {
	good := []struct {
		listen   string
		port     int
		wantPort int
		wantAddr string
	}{
		{"", 0, 0, ":0"},
		{"", 7000, 7000, ":7000"},
		{"127.0.0.1:7000", 0, 7000, "127.0.0.1:7000"},
		{"127.0.0.1:7000", 6000, 7000, "127.0.0.1:7000"}, // listen wins
		{":7000", 0, 7000, ":7000"},
		{"[::1]:7000", 0, 7000, "[::1]:7000"},
		{"relay.internal:443", 0, 443, "relay.internal:443"},
		{"0.0.0.0:65535", 0, 65535, "0.0.0.0:65535"},
	}
	for _, g := range good {
		c := &RelayConfig{Listen: g.listen, Port: g.port}
		if err := checkListen(c); err != nil {
			t.Errorf("listen %q: %v", g.listen, err)
			continue
		}
		if c.Port != g.wantPort || c.listenAddr() != g.wantAddr {
			t.Errorf("listen %q port %d: port %d addr %q, want %d %q", g.listen, g.port, c.Port, c.listenAddr(), g.wantPort, g.wantAddr)
		}
	}

	for _, bad := range []string{"7000", "127.0.0.1", "127.0.0.1:", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:-1", "127.0.0.1:http", "::1:7000", "a:b:c"} {
		err := checkListen(&RelayConfig{Listen: bad})
		var ce *common.CLIError
		if !errors.As(err, &ce) || ce.Code != common.CodeInvalidInput || ce.Input != bad {
			t.Errorf("listen %q: err %v, want invalid_input naming it", bad, err)
		}
	}
}

// The admin listener's default port is the relay's port + 1, so with only
// listen set it follows listen's port.
func TestListenSetsAdminDefaultPort(t *testing.T) {
	c := &RelayConfig{Listen: "10.0.0.5:7000"}
	if err := checkListen(c); err != nil {
		t.Fatal(err)
	}
	s, err := adminplane.Resolve(nil, c.Port, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Addr != "127.0.0.1:7001" {
		t.Errorf("admin default = %q, want 127.0.0.1:7001", s.Addr)
	}

	// admin.listen on the port listen took is the relay's own port.
	if _, err := adminplane.Resolve(&adminplane.Config{Listen: "127.0.0.1:7000"}, c.Port, nil); err == nil {
		t.Error("admin.listen on listen's port accepted")
	}
}

func TestRelayDialAddr(t *testing.T) {
	cases := []struct {
		name     string
		kv       map[string]any
		wantAddr string
		wantPort int
	}{
		{"default", map[string]any{"port": 0, "listen": ""}, "localhost:5500", 5500},
		{"port", map[string]any{"port": 7000, "listen": ""}, "localhost:7000", 7000},
		{"listen host", map[string]any{"port": 6000, "listen": "127.0.0.2:7000"}, "127.0.0.2:7000", 7000},
		{"listen wildcard", map[string]any{"listen": "0.0.0.0:7000"}, "localhost:7000", 7000},
		{"listen no host", map[string]any{"listen": ":7000"}, "localhost:7000", 7000},
		{"listen ipv6", map[string]any{"listen": "[::1]:7000"}, "[::1]:7000", 7000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withViper(t, c.kv)
			addr, port := relayDialAddr()
			if addr != c.wantAddr || port != c.wantPort {
				t.Errorf("relayDialAddr = %q, %d; want %q, %d", addr, port, c.wantAddr, c.wantPort)
			}
		})
	}

	withViper(t, map[string]any{"listen": "127.0.0.2:7000"})
	if got, _, err := adminTarget(adminCmd(t, "")); err != nil || got != "http://127.0.0.1:7001" {
		t.Errorf("adminTarget with listen = %q, %v; want http://127.0.0.1:7001", got, err)
	}
}

// The relay binds only listen's host: on 127.0.0.2 it answers there and
// not on 127.0.0.1.
func TestNewServerBindsListenHost(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("127.0.0.2 not available here: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	prevConfig := config
	t.Cleanup(func() { config = prevConfig })
	store, err := relay.NewEventStore(filepath.Join(t.TempDir(), "test.db"), &nip11.Limitation{MaxLimit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	listen := net.JoinHostPort("127.0.0.2", strconv.Itoa(port))
	config = RelayConfig{
		Nip11:  nip11.Metadata{PubKey: strings.Repeat("a", 64), PrivKey: strings.Repeat("1", 64)},
		Listen: listen,
	}
	if err := checkListen(&config); err != nil {
		t.Fatal(err)
	}
	s := NewServer(store, nil)
	t.Cleanup(s.Stop)

	if s.server.Addr != listen {
		t.Fatalf("server addr = %q, want %q", s.server.Addr, listen)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", listen, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("relay not answering on %s: %v", listen, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Errorf("relay answered on 127.0.0.1:%d; it should bind only 127.0.0.2", port)
	}
}

// loadRelayEnvConfig loads body as relay.yaml with the root command's
// NCLI_* env setup and runs initConfig on it.
func loadRelayEnvConfig(t *testing.T, body string) error {
	t.Helper()
	prevConfig, prevAdmin := config, adminSettings
	t.Cleanup(func() { config, adminSettings = prevConfig, prevAdmin })
	viper.Reset()
	t.Cleanup(viper.Reset)
	p := filepath.Join(t.TempDir(), "relay.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetEnvPrefix("NCLI")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	if err := common.LoadViperConfig(p); err != nil {
		t.Fatal(err)
	}
	config = RelayConfig{}
	return initConfig()
}

const envRelayYAML = `
nip11:
  pubkey: bb50e2d89a4ed70663d080659fe0ad4b9bc3e06c17a227433966cb59ceee020d
store: test.db
`

// NCLI_LISTEN / NCLI_PORT with no listen/port in the file move the server
// and its clients together.
func TestListenEnvReachesServerAndClients(t *testing.T) {
	t.Run("listen", func(t *testing.T) {
		t.Setenv("NCLI_LISTEN", "127.0.0.2:7300")
		if err := loadRelayEnvConfig(t, envRelayYAML); err != nil {
			t.Fatal(err)
		}
		if got := config.listenAddr(); got != "127.0.0.2:7300" {
			t.Errorf("server binds %q, want 127.0.0.2:7300", got)
		}
		if addr, _ := relayDialAddr(); addr != "127.0.0.2:7300" {
			t.Errorf("clients dial %q, want 127.0.0.2:7300", addr)
		}
		if adminSettings == nil || adminSettings.Addr != "127.0.0.1:7301" {
			t.Errorf("admin listener = %+v, want 127.0.0.1:7301", adminSettings)
		}
	})
	t.Run("port", func(t *testing.T) {
		t.Setenv("NCLI_PORT", "7400")
		if err := loadRelayEnvConfig(t, envRelayYAML); err != nil {
			t.Fatal(err)
		}
		if got := config.listenAddr(); got != ":7400" {
			t.Errorf("server binds %q, want :7400", got)
		}
		if addr, _ := relayDialAddr(); addr != "localhost:7400" {
			t.Errorf("clients dial %q, want localhost:7400", addr)
		}
	})
	t.Run("garbage listen fails", func(t *testing.T) {
		t.Setenv("NCLI_LISTEN", "garbage")
		var ce *common.CLIError
		if err := loadRelayEnvConfig(t, envRelayYAML); !errors.As(err, &ce) || ce.Code != common.CodeInvalidInput || ce.Input != "garbage" {
			t.Errorf("err %v, want invalid_input naming garbage", err)
		}
	})
	t.Run("garbage port fails", func(t *testing.T) {
		t.Setenv("NCLI_PORT", "garbage")
		var ce *common.CLIError
		if err := loadRelayEnvConfig(t, envRelayYAML); !errors.As(err, &ce) || ce.Code != common.CodeInvalidInput {
			t.Errorf("err %v, want invalid_input", err)
		}
	})
}
