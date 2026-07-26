package bunker

import "errors"

// daemonHiddenArg is the internal re-exec subcommand command.go wires up
// (Hidden: true, never advertised) -- the detached child runs exactly
// "ncli bunker __daemon" on platforms where spawnDaemon is actually
// implemented (see spawn_unix.go).
const daemonHiddenArg = "__daemon"

// keyFD is the file descriptor number the detached child reads its
// unlocked identity private key from -- 3 is the first descriptor past
// the standard three (stdin/stdout/stderr), passed via exec.Cmd.ExtraFiles
// (spawn_unix.go).
const keyFD = 3

// ErrBackgroundUnsupported is returned by spawnDaemon/ReadIdentityKeyFromFD3
// on a platform where background/attach isn't supported (Windows, per the
// platform-scope decision) -- command.go treats it as the signal to run
// the daemon in-process instead (via localClient) rather than a fatal
// error: the interactive TUI itself is unaffected, only backgrounding it
// and `ncli bunker attach`/`status`/`stop` from a separate invocation are
// unavailable there.
var ErrBackgroundUnsupported = errors.New("bunker: background/attach is not supported on this platform; the TUI still runs directly")
