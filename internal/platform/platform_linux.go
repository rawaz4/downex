//go:build linux

package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// Preallocate sets the length of f to size bytes without writing zeros. Bytes in
// [0, min(old length, size)) keep their contents; bytes beyond size are discarded, as
// with f.Truncate. f must be open for writing.
//
// The range is reserved with fallocate(2) (mode 0), which allocates unwritten extents
// without touching existing data and reports a full disk (ENOSPC) up front. File systems
// or kernels without fallocate (EOPNOTSUPP, ENOSYS) get a sparse extension through
// ftruncate(2) instead.
func Preallocate(f *os.File, size int64) error {
	return preallocate(f, size, fallocate)
}

// fallocate reserves [0, size) of fd, extending the file when it is shorter.
func fallocate(fd int, size int64) error {
	return unix.Fallocate(fd, 0, 0, size)
}

// preallocate implements Preallocate with alloc in place of fallocate, so that tests can
// simulate file systems without it.
func preallocate(f *os.File, size int64, alloc func(fd int, size int64) error) error {
	if err := checkSize(f, size); err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("platform: preallocate: %w", err)
	}
	if fi.Size() > size { // fallocate never shrinks a file
		if err := f.Truncate(size); err != nil {
			return fmt.Errorf("platform: preallocate: %w", err)
		}
	}
	if size == 0 {
		return nil
	}
	err = control(f, func(fd uintptr) error {
		for {
			// A signal (the runtime preempts goroutines with one) can interrupt a long
			// allocation; it is then restarted.
			if err := alloc(int(fd), size); !errors.Is(err, unix.EINTR) {
				return err
			}
		}
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EOPNOTSUPP), errors.Is(err, unix.ENOSYS):
		if err := f.Truncate(size); err != nil {
			return fmt.Errorf("platform: preallocate: %w", err)
		}
		return nil
	}
	return fmt.Errorf("platform: preallocate: %w", &fs.PathError{Op: "fallocate", Path: f.Name(), Err: err})
}

// closeOnExecAbove sets the close-on-exec flag of every open descriptor above fd:
// close_range(2) with CLOSE_RANGE_CLOEXEC (Linux 5.11), else one fcntl(2) per entry of
// /proc/self/fd.
func closeOnExecAbove(fd int) error {
	err := unix.CloseRange(uint(fd+1), ^uint(0), unix.CLOSE_RANGE_CLOEXEC)
	if err == nil || (!errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL)) {
		return err
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return fmt.Errorf("list open descriptors: %w", err)
	}
	for _, e := range entries {
		n, err := strconv.Atoi(e.Name())
		if err != nil || n <= fd {
			continue
		}
		// FD_CLOEXEC is the only descriptor flag. EBADF: the directory's own descriptor,
		// closed by now.
		if _, err := unix.FcntlInt(uintptr(n), unix.F_SETFD, unix.FD_CLOEXEC); err != nil && !errors.Is(err, unix.EBADF) {
			return fmt.Errorf("mark descriptor %d close-on-exec: %w", n, err)
		}
	}
	return nil
}

// Elevated reports whether this process runs with more rights than its user: a set-uid
// or set-gid executable (effective ids that differ from the real ones). The root user's
// own processes are not elevated in this sense.
func Elevated() (bool, error) {
	return os.Geteuid() != os.Getuid() || os.Getegid() != os.Getgid(), nil
}

// DownloadsDir returns the user's Downloads folder, creating it if it is missing: the
// XDG_DOWNLOAD_DIR entry of $XDG_CONFIG_HOME/user-dirs.dirs (default ~/.config), which
// xdg-user-dirs maintains, else ~/Downloads.
func DownloadsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("platform: locate the home directory: %w", err)
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(cfg) { // unset, empty or relative: the XDG spec says to ignore it
		cfg = filepath.Join(home, ".config")
	}
	dir := ""
	// An unreadable or malformed file is not an error: the default folder is used.
	if data, err := os.ReadFile(filepath.Join(cfg, "user-dirs.dirs")); err == nil {
		dir = userDir(data, "DOWNLOAD", home)
	}
	if dir == "" {
		dir = filepath.Join(home, "Downloads")
	}
	return ensureDir(dir, 0o755)
}

// LogDir returns, creating it with mode 0700 if needed, the directory for daemon logs:
// $XDG_STATE_HOME/downex, by default ~/.local/state/downex.
func LogDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(base) { // unset, empty or relative: the XDG spec says to ignore it
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("platform: locate the home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return ensureDir(filepath.Join(base, "downex"), 0o700)
}
