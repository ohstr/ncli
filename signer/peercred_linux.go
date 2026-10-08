//go:build linux

package signer

import (
	"net"
	"syscall"
)

// PeerCredSupported reports whether peer uid/gid checks work here.
const PeerCredSupported = true

// peerCredOf reads SO_PEERCRED. Only the peer's primary gid is available,
// not its supplementary groups.
func peerCredOf(conn net.Conn) Peer {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Peer{}
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Peer{}
	}
	var cred *syscall.Ucred
	_ = raw.Control(func(fd uintptr) {
		cred, err = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil || cred == nil {
		return Peer{}
	}
	uid, gid, pid := int(cred.Uid), int(cred.Gid), int(cred.Pid)
	return Peer{UID: &uid, GID: &gid, PID: &pid}
}
