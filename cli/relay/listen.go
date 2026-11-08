package relay

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

// listenAddr is the address the relay binds: listen when set, else every
// interface on port.
func (c *RelayConfig) listenAddr() string {
	if c.Listen != "" {
		return c.Listen
	}
	return fmt.Sprintf(":%d", c.Port)
}

// splitListen reads a host:port listen address. The host may be empty
// (every interface); the port must be 1-65535.
func splitListen(listen string) (string, int, error) {
	if strings.TrimSpace(listen) != listen {
		return "", 0, fmt.Errorf("listen %q has leading or trailing whitespace", listen)
	}
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return "", 0, fmt.Errorf("listen %q must be host:port: %v", listen, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("listen %q: port must be 1-65535", listen)
	}
	return host, port, nil
}

// bindAddrEnv makes NCLI_LISTEN and NCLI_PORT reach viper.Unmarshal even
// when the file has no listen or port key: AutomaticEnv alone only
// overrides keys the file already has, while the admin commands read
// viper.GetString("listen") and see the variable either way. Bound, the
// relay binds the same address its clients dial.
func bindAddrEnv(v *viper.Viper) {
	_ = v.BindEnv("listen")
	_ = v.BindEnv("port")
}

// checkListen validates listen and, when it is set, makes its port the
// relay's port, so everything derived from the port (the admin listener's
// default port+1) follows it.
func checkListen(c *RelayConfig) error {
	if c.Listen == "" {
		return nil
	}
	host, port, err := splitListen(c.Listen)
	if err != nil {
		return &common.CLIError{Err: err, Code: common.CodeInvalidInput, Input: c.Listen}
	}
	if err := checkListenHost(host); err != nil {
		return &common.CLIError{Err: fmt.Errorf("listen %q: %w", c.Listen, err), Code: common.CodeInvalidInput, Input: c.Listen}
	}
	if c.Port != 0 && c.Port != port {
		log.Warn().Int("port", c.Port).Str("listen", c.Listen).Msg("port is ignored: listen sets the relay's address")
	}
	c.Port = port
	return nil
}

// listenLookupTimeout bounds checkListenHost's DNS lookup.
const listenLookupTimeout = 3 * time.Second

// checkListenHost makes sure a hostname in listen resolves, so a typo fails
// --check rather than the bind at startup. An IP literal (or no host) needs
// no lookup.
func checkListenHost(host string) error {
	if host == "" {
		return nil
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), listenLookupTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("host %q does not resolve", host)
	}
	return nil
}

// relayDialAddr is the host:port a local client reaches the relay at, from
// the loaded config: listen's host when it names one, else localhost.
func relayDialAddr() (string, int) {
	if listen := viper.GetString("listen"); listen != "" {
		if host, port, err := splitListen(listen); err == nil {
			if host == "" || host == "0.0.0.0" || host == "::" {
				host = "localhost"
			}
			return net.JoinHostPort(host, strconv.Itoa(port)), port
		}
	}
	port := viper.GetInt("port")
	if port == 0 {
		port = defaultPort
	}
	return net.JoinHostPort("localhost", strconv.Itoa(port)), port
}
