//go:build unix

package bunker

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// spawnDaemon re-execs the current binary as a fully detached background
// daemon: a new session (SysProcAttr.Setsid, so it survives this
// process's controlling terminal closing), stdin /dev/null, stdout/stderr
// redirected directly to logPath (opened here and handed to the child as
// its own fd, not piped through this process). Then waits (bounded by
// readyTimeout) for the daemon's control socket to start accepting
// connections before returning.
//
// The already-unlocked private key crosses the exec boundary over a pipe
// (fd 3, via ExtraFiles) -- never as an argv value (visible to any local
// `ps`) or a persistent env var (readable via /proc/[pid]/environ on
// Linux for the lifetime of the process). Written once, then closed.
// vaultLabel rides along on the same pipe as a second line -- it's
// display-only (not secret, unlike privKeyHex), but there's no other
// channel that reaches the detached child, so it goes over regardless.
func spawnDaemon(privKeyHex, vaultLabel string, relays []string, logPath, socketPath string, readyTimeout time.Duration) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own executable path: %w", err)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open daemon log %s: %w", logPath, err)
	}
	defer func() { _ = logFile.Close() }()

	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create identity key pipe: %w", err)
	}

	args := []string{"bunker", daemonHiddenArg}
	for _, r := range relays {
		args = append(args, "--relay", r)
	}

	cmd := exec.Command(exe, args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.ExtraFiles = []*os.File{pr}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return fmt.Errorf("start daemon process: %w", err)
	}

	// The child now has its own copy of the read end (dup'd via
	// ExtraFiles); this process's copy is redundant and must be closed so
	// the child sees EOF once we finish writing, rather than hanging
	// waiting for a write end it doesn't know is otherwise unreachable.
	_ = pr.Close()

	// Detach entirely -- this process doesn't reap the daemon (Wait), it
	// outlives this invocation by design.
	if err := cmd.Process.Release(); err != nil {
		_ = pw.Close()
		return fmt.Errorf("release daemon process: %w", err)
	}

	if _, err := io.WriteString(pw, privKeyHex+"\n"+vaultLabel+"\n"); err != nil {
		_ = pw.Close()
		return fmt.Errorf("write identity key to daemon: %w", err)
	}
	_ = pw.Close()

	if err := waitForSocket(socketPath, readyTimeout); err != nil {
		return fmt.Errorf("%w (check %s for daemon startup errors)", err, logPath)
	}
	return nil
}

func waitForSocket(socketPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if socketIsLive(socketPath) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("timed out waiting for the daemon to start")
}

// ReadIdentityKeyFromFD3 reads the unlocked identity private key (and,
// second line, the vault label -- possibly empty -- see spawnDaemon
// above) the parent process wrote once, called exactly once, at startup,
// by the hidden "__daemon" subcommand's RunE.
func ReadIdentityKeyFromFD3() (privKeyHex, vaultLabel string, err error) {
	f := os.NewFile(keyFD, "identity-key-pipe")
	if f == nil {
		return "", "", errors.New("bunker: fd 3 not available -- __daemon must be started via spawnDaemon, not invoked directly")
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(f)
	if err != nil {
		return "", "", fmt.Errorf("read identity key from fd 3: %w", err)
	}
	lines := strings.SplitN(strings.TrimRight(string(data), "\n"), "\n", 2)
	privKeyHex = strings.TrimSpace(lines[0])
	if privKeyHex == "" {
		return "", "", errors.New("bunker: empty identity key received from parent")
	}
	if len(lines) > 1 {
		vaultLabel = strings.TrimSpace(lines[1])
	}
	return privKeyHex, vaultLabel, nil
}
