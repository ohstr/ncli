package relay

import (
	"fmt"
	"net"
	"strconv"

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

// checkListen validates listen and, when it is set, makes its port the
// relay's port, so everything derived from the port (the admin listener's
// default port+1) follows it.
func checkListen(c *RelayConfig) error {
	if c.Listen == "" {
		return nil
	}
	_, port, err := splitListen(c.Listen)
	if err != nil {
		return &common.CLIError{Err: err, Code: common.CodeInvalidInput, Input: c.Listen}
	}
	if c.Port != 0 && c.Port != port {
		log.Warn().Int("port", c.Port).Str("listen", c.Listen).Msg("port is ignored: listen sets the relay's address")
	}
	c.Port = port
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
