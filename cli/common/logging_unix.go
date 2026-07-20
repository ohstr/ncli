//go:build !windows

package common

import (
	"os"

	"golang.org/x/sys/unix"
)

// redirectStderr dups f onto fd 2, returning a func that dups the original
// fd 2 back. Goes through golang.org/x/sys/unix rather than the standard
// syscall package because syscall.Dup2 isn't defined on linux/arm64 (the
// kernel there only has dup3, and the standard library never backfilled a
// Dup2 wrapper for it) -- x/sys/unix.Dup2 papers over that per architecture.
func redirectStderr(f *os.File) (restore func(), err error) {
	stderrFd := int(os.Stderr.Fd())
	origStderr, err := unix.Dup(stderrFd)
	if err != nil {
		return nil, err
	}

	if err := unix.Dup2(int(f.Fd()), stderrFd); err != nil {
		unix.Close(origStderr)
		return nil, err
	}

	return func() {
		unix.Dup2(origStderr, stderrFd)
		unix.Close(origStderr)
	}, nil
}
