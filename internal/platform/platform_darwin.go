//go:build darwin

package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// Preallocate sets the length of f to size bytes without writing zeros. Bytes in
// [0, min(old length, size)) keep their contents; bytes beyond size are discarded, as
// with f.Truncate. f must be open for writing.
//
// When the file grows, the missing space is first reserved with fcntl(F_PREALLOCATE)
// from the file's physical end (one contiguous run if the file system has one, else
// wherever it is free), which reports a full disk (ENOSPC) up front and never touches
// existing data; ftruncate(2) then sets the length. File systems without
// F_PREALLOCATE (see preallocateUnsupported) get the plain ftruncate, which is sparse
// on APFS.
func Preallocate(f *os.File, size int64) error {
	return preallocate(f, size, fstore)
}

// fstore asks the file system to allocate length more bytes after fd's physical end of
// file, all of them or none (F_ALLOCATEALL), in one contiguous run when contiguous is
// set. It neither changes the file's length nor its data.
func fstore(fd int, length int64, contiguous bool) error {
	st := unix.Fstore_t{Flags: unix.F_ALLOCATEALL, Posmode: unix.F_PEOFPOSMODE, Length: length}
	if contiguous {
		st.Flags |= unix.F_ALLOCATECONTIG
	}
	return unix.FcntlFstore(uintptr(fd), unix.F_PREALLOCATE, &st)
}

// preallocate implements Preallocate with alloc in place of fstore, so that tests can
// simulate file systems without it.
func preallocate(f *os.File, size int64, alloc func(fd int, length int64, contiguous bool) error) error {
	if err := checkSize(f, size); err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("platform: preallocate: %w", err)
	}
	if fi.Size() >= size {
		if fi.Size() > size {
			if err := f.Truncate(size); err != nil {
				return fmt.Errorf("platform: preallocate: %w", err)
			}
		}
		return nil
	}
	// F_PEOFPOSMODE counts from the physical end of file: ask only for what is not
	// allocated yet (st_blocks counts 512-byte units).
	need := size
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		need -= st.Blocks * 512
	}
	if need > 0 {
		err = control(f, func(fd uintptr) error {
			err := retryEINTR(func() error { return alloc(int(fd), need, true) })
			if err != nil {
				// No contiguous run that long (or no contiguous allocation at all):
				// take the space wherever it is free.
				err = retryEINTR(func() error { return alloc(int(fd), need, false) })
			}
			return err
		})
		if err != nil && !preallocateUnsupported(err) {
			return fmt.Errorf("platform: preallocate: %w", &fs.PathError{Op: "fcntl F_PREALLOCATE", Path: f.Name(), Err: err})
		}
	}
	if err := f.Truncate(size); err != nil {
		return fmt.Errorf("platform: preallocate: %w", err)
	}
	return nil
}

// retryEINTR runs fn again while a signal (the runtime preempts goroutines with one)
// interrupts it.
func retryEINTR(fn func() error) error {
	for {
		if err := fn(); !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

// preallocateUnsupported reports an F_PREALLOCATE failure that only means the file
// system cannot reserve space: the kernel's default for file systems without the
// operation is ENOTSUP, some report EOPNOTSUPP or ENOSYS, and EINVAL is a file
// system rejecting the request itself (the request is always well-formed). Every other
// error (ENOSPC, EBADF for a file not open for writing, EIO, EFBIG) is reported.
func preallocateUnsupported(err error) bool {
	return errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL)
}

// closeOnExecAbove sets the close-on-exec flag of every open descriptor above fd: one
// fcntl(2) per entry of /dev/fd, where the kernel lists the calling process's open
// descriptors, else (when /dev/fd cannot be listed) per descriptor below the
// RLIMIT_NOFILE soft limit, at most maxDescriptorScan of them.
func closeOnExecAbove(fd int) error {
	if entries, err := os.ReadDir("/dev/fd"); err == nil {
		for _, e := range entries {
			n, err := strconv.Atoi(e.Name())
			if err != nil || n <= fd {
				continue
			}
			if err := setCloseOnExec(n); err != nil {
				return err
			}
		}
		return nil
	}
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		return fmt.Errorf("list open descriptors: %w", err)
	}
	end := uint64(maxDescriptorScan)
	if lim.Cur < end {
		end = lim.Cur
	}
	for n := fd + 1; uint64(n) < end; n++ {
		if err := setCloseOnExec(n); err != nil {
			return err
		}
	}
	return nil
}

// maxDescriptorScan bounds the descriptors closeOnExecAbove visits without /dev/fd.
const maxDescriptorScan = 1 << 16

// setCloseOnExec sets FD_CLOEXEC (the only descriptor flag) on descriptor n. EBADF: n
// is not open (or was the listing's own descriptor, closed by now).
func setCloseOnExec(n int) error {
	if _, err := unix.FcntlInt(uintptr(n), unix.F_SETFD, unix.FD_CLOEXEC); err != nil && !errors.Is(err, unix.EBADF) {
		return fmt.Errorf("mark descriptor %d close-on-exec: %w", n, err)
	}
	return nil
}

// Elevated reports whether this process runs with more rights than a user's own
// processes (docs/MACOS-SPEC.md §2): as root (effective uid 0, for example under sudo,
// which keeps HOME on macOS and would leave root-owned files in the user's Library), or
// from a set-uid or set-gid executable (effective ids that differ from the real ones).
func Elevated() (bool, error) {
	euid := os.Geteuid()
	return euid == 0 || euid != os.Getuid() || os.Getegid() != os.Getgid(), nil
}

// DownloadsDir returns ~/Downloads, creating it (mode 0700, like the folder macOS
// creates) if it is missing.
func DownloadsDir() (string, error) {
	dirs, err := userDirs()
	if err != nil {
		return "", err
	}
	return ensureDir(dirs.downloads, 0o700)
}

// LogDir returns, creating it with mode 0700 if needed, the directory for daemon logs:
// ~/Library/Logs/Downex, where Console.app finds them.
func LogDir() (string, error) {
	dirs, err := userDirs()
	if err != nil {
		return "", err
	}
	return ensureDir(dirs.logs, 0o700)
}

// userDirs returns the per-user locations below $HOME.
func userDirs() (darwinDirs, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return darwinDirs{}, fmt.Errorf("platform: locate the home directory: %w", err)
	}
	return darwinPaths(home)
}
