package bunker

import (
	"errors"

	"github.com/ohstr/ncli/cli/common"
)

// ErrNoRelayReachable is returned when a pairing could not reach a single
// one of the relays it was given. Distinguished from an ordinary failure
// so the CLI reports it as `network` (retryable, exit 6) rather than
// `invalid_input` -- the URI was fine, the relays were not.
var ErrNoRelayReachable = errors.New("no relay reachable")

// codedError carries an ncli error code across the IPC socket, which a
// plain error cannot survive: the daemon marshals only err.Error(), so
// without this every daemon-side failure reaches the CLI as an untyped
// string and gets reported as invalid_input, whatever it actually was.
type codedError struct {
	code common.ErrorCode
	msg  string
}

func (e codedError) Error() string { return e.msg }

// errorCode classifies a daemon-side error for the wire. Only failures
// that deserve a code other than the CLI's default are named here;
// everything else stays unclassified and is reported the way it was.
func errorCode(err error) common.ErrorCode {
	if errors.Is(err, ErrNoRelayReachable) {
		return common.CodeNetwork
	}
	return ""
}

// ErrorCode reports the ncli error code carried by err, or "" if it has
// none -- command.go's hook for turning a daemon-side failure into the
// right exit code, whether it arrived over the socket or came from an
// in-process daemon.
func ErrorCode(err error) common.ErrorCode {
	var coded codedError
	if errors.As(err, &coded) {
		return coded.code
	}
	return errorCode(err)
}
