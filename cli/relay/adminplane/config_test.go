package adminplane

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTokens(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "metrics.tokens")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveDefaults(t *testing.T) {
	s, err := Resolve(nil, 5500, []string{"admin"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Addr != "127.0.0.1:5501" || !s.Loopback || s.Dashboard || s.Metrics {
		t.Fatalf("defaults = %+v", s)
	}
	if s.SessionTTL != 15*time.Minute || s.ScrapeTTL != 5*time.Second {
		t.Fatalf("default TTLs = %v / %v", s.SessionTTL, s.ScrapeTTL)
	}
}

func TestResolveOff(t *testing.T) {
	s, err := Resolve(&Config{Listen: "off"}, 5500, nil)
	if err != nil || s != nil {
		t.Fatalf("off = %+v, %v; want nil, nil", s, err)
	}
	_, err = Resolve(&Config{Listen: "off", Metrics: &MetricsConfig{Enabled: true}}, 5500, nil)
	if err == nil {
		t.Fatal("off with metrics enabled was accepted")
	}
}

func TestResolveRefusesUnsafeConfigs(t *testing.T) {
	good := writeTokens(t, "", 0o600)
	cases := map[string]struct {
		cfg   *Config
		admin []string
		want  string
		usage bool
	}{
		"relay port":           {cfg: &Config{Listen: "127.0.0.1:5500"}, want: "relay's own port", usage: true},
		"public without TLS":   {cfg: &Config{Listen: "0.0.0.0:5501"}, want: "not loopback", usage: true},
		"empty host is public": {cfg: &Config{Listen: ":5501"}, want: "not loopback", usage: true},
		"half TLS":             {cfg: &Config{Listen: "0.0.0.0:5501", TLS: &TLSConfig{Cert: "x"}}, want: "both cert and key", usage: true},
		"metrics no tokens":    {cfg: &Config{Metrics: &MetricsConfig{Enabled: true}}, want: "tokensFile", usage: true},
		"dashboard no admin":   {cfg: &Config{Dashboard: &DashboardConfig{Enabled: true}}, want: "admin pubkey", usage: true},
		"bad cidr":             {cfg: &Config{AllowCIDRs: []string{"10.0.0/8"}}, want: "allowCIDRs"},
		"ttl too long":         {cfg: &Config{Dashboard: &DashboardConfig{Enabled: true, SessionTTL: "48h"}}, admin: []string{"a"}, want: "sessionTTL"},
		"scrape ttl bad":       {cfg: &Config{Metrics: &MetricsConfig{Enabled: true, TokensFile: good, ScrapeCacheTTL: "soon"}}, want: "scrapeCacheTTL"},
		"bad port":             {cfg: &Config{Listen: "127.0.0.1:99999"}, want: "invalid port"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Resolve(c.cfg, 5500, c.admin)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
			var ce *ConfigError
			if !errors.As(err, &ce) || ce.Usage != c.usage {
				t.Fatalf("error %v: usage = %v, want %v", err, ce != nil && ce.Usage, c.usage)
			}
		})
	}
}

func TestResolveAcceptsSingleIPAsCIDR(t *testing.T) {
	s, err := Resolve(&Config{AllowCIDRs: []string{"10.1.2.3", "fd00::/8"}}, 5500, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.AllowCIDRs) != 2 || s.AllowCIDRs[0].Bits() != 32 {
		t.Fatalf("cidrs = %v", s.AllowCIDRs)
	}
}

func TestTokensFileRules(t *testing.T) {
	if _, err := LoadTokens(writeTokens(t, "", 0o644)); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("world-readable file: err = %v", err)
	}
	if _, err := LoadTokens(writeTokens(t, TokenPrefix+"abc\n", 0o600)); err == nil || !strings.Contains(err.Error(), "plaintext") {
		t.Errorf("plaintext token: err = %v", err)
	}
	if _, err := LoadTokens(writeTokens(t, "sha256:abcd\n", 0o600)); err == nil {
		t.Error("short hash accepted")
	}
	entries, err := LoadTokens(writeTokens(t, "# comment\n\n", 0o600))
	if err != nil || len(entries) != 0 {
		t.Errorf("comments only = %v, %v", entries, err)
	}
}

func TestAddTokenCreatesItsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "metrics.tokens")
	if _, err := AddToken(path, "prometheus", time.Now()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("token dir: %v %v", fi, err)
	}
}

func TestTokenLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.tokens")
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)

	tok, err := AddToken(path, "prometheus", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, TokenPrefix) || len(tok) < len(TokenPrefix)+40 {
		t.Fatalf("token %q too short or unprefixed", tok)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("tokens file mode = %v, %v", fi.Mode().Perm(), err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), tok) {
		t.Fatal("plaintext token written to the file")
	}
	if !strings.Contains(string(raw), "name=prometheus created=2026-10-18") {
		t.Fatalf("file = %q", raw)
	}
	if _, err := AddToken(path, "prometheus", now); !errors.Is(err, ErrTokenNameTaken) {
		t.Fatalf("duplicate name err = %v", err)
	}
	if _, err := AddToken(path, "bad name!", now); err == nil {
		t.Fatal("invalid name accepted")
	}

	set, err := newTokenSet(path)
	if err != nil {
		t.Fatal(err)
	}
	if !set.valid(tok, now) {
		t.Fatal("fresh token rejected")
	}
	if set.valid(tok+"x", now) || set.valid("", now) || set.valid("Bearer "+tok, now) {
		t.Fatal("wrong token accepted")
	}

	// Rotation: a second token works without a restart; removing the first
	// revokes it.
	tok2, err := AddToken(path, "grafana-agent", now)
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(2 * recheckEvery)
	if !set.valid(tok2, later) {
		t.Fatal("added token not picked up")
	}
	if err := RemoveToken(path, "prometheus"); err != nil {
		t.Fatal(err)
	}
	evenLater := later.Add(2 * recheckEvery)
	if set.valid(tok, evenLater) {
		t.Fatal("removed token still accepted")
	}
	if !set.valid(tok2, evenLater) {
		t.Fatal("remaining token rejected after removal")
	}
	if err := RemoveToken(path, "prometheus"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("removing unknown token err = %v", err)
	}

	// A file loosened after startup rejects everything until fixed.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	loosened := evenLater.Add(2 * recheckEvery)
	if set.valid(tok2, loosened) {
		t.Fatal("token accepted from a world-readable file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if !set.valid(tok2, loosened.Add(2*recheckEvery)) {
		t.Fatal("token still rejected after the file was fixed")
	}
}
