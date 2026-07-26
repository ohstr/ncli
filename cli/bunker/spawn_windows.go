//go:build windows

package bunker

import "time"

// spawnDaemon has no Windows implementation -- per the platform-scope
// decision, command.go falls back to running the daemon in-process
// (localClient) instead of detaching a background process and attaching
// to it over a control socket.
func spawnDaemon(privKeyHex, vaultLabel string, relays []string, logPath, socketPath string, readyTimeout time.Duration) error {
	return ErrBackgroundUnsupported
}

// ReadIdentityKeyFromFD3 has no Windows implementation -- the hidden
// "__daemon" re-exec path (spawn_unix.go) is never reached there, since
// spawnDaemon always fails over to the in-process fallback first.
func ReadIdentityKeyFromFD3() (privKeyHex, vaultLabel string, err error) {
	return "", "", ErrBackgroundUnsupported
}
