//go:build linux

package ipc

import "golang.org/x/sys/unix"

// peerUID returns the uid of the process on the other end of the unix socket fd
// (SO_PEERCRED: the connecting process for a server, the listening one for a client).
func peerUID(fd uintptr) (int, error) {
	cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return -1, err
	}
	return int(cred.Uid), nil
}
