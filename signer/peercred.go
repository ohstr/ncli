package signer

import "errors"

// Peer identifies the process on the other end of a connection, from
// SO_PEERCRED. Fields are nil where the platform can't tell.
type Peer struct {
	UID *int `json:"uid,omitempty"`
	GID *int `json:"gid,omitempty"`
	PID *int `json:"pid,omitempty"`
	// Conn numbers connections since the signer started, so log lines
	// stay distinct when clients reuse request ids.
	Conn uint64 `json:"conn"`
}

// ErrPeerCredUnsupported is returned when a caller allow-list is set on a
// platform without SO_PEERCRED.
var ErrPeerCredUnsupported = errors.New("--allow-uid/--allow-gid need SO_PEERCRED, which is only supported on Linux")
