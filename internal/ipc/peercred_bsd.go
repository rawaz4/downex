//go:build darwin || freebsd

package ipc

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// xucredVersion is XUCRED_VERSION of <sys/ucred.h>: the layout of struct xucred
// (cr_version, cr_uid, cr_ngroups, cr_groups) that unix.Xucred mirrors.
const xucredVersion = 0

// peerUID returns the effective uid of the process on the other end of the unix socket
// fd: getsockopt(SOL_LOCAL, LOCAL_PEERCRED) reads the credentials the peer had when it
// connected (for a server's accepted connection) or called listen (for a client).
func peerUID(fd uintptr) (int, error) {
	cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return -1, err
	}
	if cred.Version != xucredVersion {
		return -1, fmt.Errorf("LOCAL_PEERCRED: unknown xucred version %d", cred.Version)
	}
	return int(cred.Uid), nil
}
