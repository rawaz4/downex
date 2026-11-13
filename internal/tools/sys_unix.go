//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package tools

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// tryLock takes an exclusive flock(2) lock on f without waiting; busy is true when
// another open file holds it.
func tryLock(f *os.File) (busy bool, err error) {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EWOULDBLOCK):
			return true, nil
		}
		return false, err
	}
}

func unlockFile(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }

// freeSpace returns the bytes available to unprivileged users on the volume of dir.
func freeSpace(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// isDiskFull reports a write that failed because the volume (or the user's quota) is full.
func isDiskFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}

// chmodExe makes f executable by its owner and readable by everyone (0755), whatever
// the umask.
func chmodExe(f *os.File) error { return f.Chmod(0o755) }
