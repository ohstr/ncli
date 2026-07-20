//go:build windows

package common

import (
	"os"

	"golang.org/x/sys/windows"
)

// redirectStderr points the process's STD_ERROR_HANDLE at f, returning a
// func that points it back at the original handle. Windows has no fd-based
// dup2 equivalent for this -- the OS-level stderr stream is addressed via
// GetStdHandle/SetStdHandle instead of a small-integer file descriptor.
func redirectStderr(f *os.File) (restore func(), err error) {
	origStderr, err := windows.GetStdHandle(windows.STD_ERROR_HANDLE)
	if err != nil {
		return nil, err
	}

	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd())); err != nil {
		return nil, err
	}

	return func() {
		windows.SetStdHandle(windows.STD_ERROR_HANDLE, origStderr)
	}, nil
}
