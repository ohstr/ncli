// Package adminplane serves the relay's admin listener: Prometheus metrics,
// the dashboard and its API, on an address separate from the public relay
// port. Every route needs a credential: a metrics token for /metrics, a
// NIP-98 signature or a dashboard session for /admin/api.
package adminplane

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"time"
)

// Config is the relay config's `admin:` block.
type Config struct {
	// Listen is host:port for the admin listener; "off" disables it.
	// Default 127.0.0.1:<relay port + 1>.
	Listen     string           `mapstructure:"listen"`
	TLS        *TLSConfig       `mapstructure:"tls"`
	AllowCIDRs []string         `mapstructure:"allowCIDRs"`
	Dashboard  *DashboardConfig `mapstructure:"dashboard"`
	Metrics    *MetricsConfig   `mapstructure:"metrics"`
}

// TLSConfig serves the admin listener over TLS; ClientCA additionally
// requires every client to present a certificate it signed.
type TLSConfig struct {
	Cert     string `mapstructure:"cert"`
	Key      string `mapstructure:"key"`
	ClientCA string `mapstructure:"clientCA"`
}

// DashboardConfig serves the embedded dashboard at /admin/ui/.
type DashboardConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// SessionTTL bounds a browser login. Default 15m, at most 24h.
	SessionTTL string `mapstructure:"sessionTTL"`
}

// MetricsConfig serves Prometheus metrics at /metrics.
type MetricsConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// TokensFile holds the sha256 hashes of accepted bearer tokens.
	TokensFile string `mapstructure:"tokensFile"`
	// ScrapeCacheTTL is how long one rendering of /metrics is reused.
	// Default 5s.
	ScrapeCacheTTL string `mapstructure:"scrapeCacheTTL"`
}

// Settings is a validated Config.
type Settings struct {
	Addr string
	// Defaulted is true when Addr was derived rather than configured, so a
	// relay whose neighbor already holds that port can still start.
	Defaulted  bool
	Loopback   bool
	TLS        *TLSConfig
	AllowCIDRs []netip.Prefix
	Dashboard  bool
	SessionTTL time.Duration
	Metrics    bool
	TokensFile string
	ScrapeTTL  time.Duration
}

const (
	defaultSessionTTL = 15 * time.Minute
	maxSessionTTL     = 24 * time.Hour
	defaultScrapeTTL  = 5 * time.Second
)

// ConfigError is a validation failure; Usage marks a missing or
// contradictory setting rather than a malformed value.
type ConfigError struct {
	Err   error
	Usage bool
	Input string
}

func (e *ConfigError) Error() string { return e.Err.Error() }
func (e *ConfigError) Unwrap() error { return e.Err }

func usageErr(format string, a ...any) error {
	return &ConfigError{Err: fmt.Errorf(format, a...), Usage: true}
}

func invalidErr(input, format string, a ...any) error {
	return &ConfigError{Err: fmt.Errorf(format, a...), Input: input}
}

// Resolve validates c for a relay on relayPort whose admins are admins.
// A nil c means defaults: a loopback listener with only the NIP-98 API.
// It returns nil Settings when the listener is "off".
func Resolve(c *Config, relayPort int, admins []string) (*Settings, error) {
	if c == nil {
		c = &Config{}
	}
	if c.Listen == "off" {
		if (c.Dashboard != nil && c.Dashboard.Enabled) || (c.Metrics != nil && c.Metrics.Enabled) {
			return nil, usageErr("admin.listen is off, but admin.dashboard or admin.metrics is enabled")
		}
		return nil, nil
	}

	s := &Settings{Addr: c.Listen, SessionTTL: defaultSessionTTL, ScrapeTTL: defaultScrapeTTL}
	if s.Addr == "" {
		s.Addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(relayPort+1))
		s.Defaulted = true
	}
	host, portStr, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return nil, invalidErr(s.Addr, "admin.listen: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return nil, invalidErr(s.Addr, "admin.listen: invalid port %q", portStr)
	}
	if port == relayPort {
		return nil, usageErr("admin.listen must not use the relay's own port %d", relayPort)
	}
	s.Loopback = isLoopback(host)

	if c.TLS != nil {
		if c.TLS.Cert == "" || c.TLS.Key == "" {
			return nil, usageErr("admin.tls needs both cert and key")
		}
		for _, f := range []string{c.TLS.Cert, c.TLS.Key, c.TLS.ClientCA} {
			if f == "" {
				continue
			}
			if _, err := os.Stat(f); err != nil {
				return nil, invalidErr(f, "admin.tls: %v", err)
			}
		}
		s.TLS = c.TLS
	}
	if !s.Loopback && s.TLS == nil {
		return nil, usageErr("admin.listen %s is not loopback: set admin.tls, or listen on 127.0.0.1 and tunnel to it", s.Addr)
	}

	for _, raw := range c.AllowCIDRs {
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			addr, aerr := netip.ParseAddr(raw)
			if aerr != nil {
				return nil, invalidErr(raw, "admin.allowCIDRs: %v", err)
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		s.AllowCIDRs = append(s.AllowCIDRs, p.Masked())
	}

	if d := c.Dashboard; d != nil && d.Enabled {
		if len(admins) == 0 {
			return nil, usageErr("admin.dashboard needs an admin pubkey (nip11.pubkey or nip86.admins)")
		}
		s.Dashboard = true
		if d.SessionTTL != "" {
			ttl, err := time.ParseDuration(d.SessionTTL)
			if err != nil || ttl < time.Minute || ttl > maxSessionTTL {
				return nil, invalidErr(d.SessionTTL, "admin.dashboard.sessionTTL must be a duration from 1m to 24h")
			}
			s.SessionTTL = ttl
		}
	}

	if m := c.Metrics; m != nil && m.Enabled {
		if m.TokensFile == "" {
			return nil, usageErr("admin.metrics needs tokensFile (create one with `ncli relay metrics token add`)")
		}
		if _, err := LoadTokens(m.TokensFile); err != nil {
			return nil, err
		}
		s.Metrics = true
		s.TokensFile = m.TokensFile
		if m.ScrapeCacheTTL != "" {
			ttl, err := time.ParseDuration(m.ScrapeCacheTTL)
			if err != nil || ttl < 0 || ttl > time.Minute {
				return nil, invalidErr(m.ScrapeCacheTTL, "admin.metrics.scrapeCacheTTL must be a duration up to 1m")
			}
			s.ScrapeTTL = ttl
		}
	}
	return s, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// errTokensFileMode is returned for a tokens file others can read or write.
var errTokensFileMode = errors.New("tokens file must not be readable or writable by other users (chmod 600)")
