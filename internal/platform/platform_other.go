//go:build !windows && !linux && !darwin

package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Preallocate sets the length of f to size bytes without writing zeros. Bytes in
// [0, min(old length, size)) keep their contents; bytes beyond size are discarded. On
// this platform it is f.Truncate, which is sparse where the file system allows it.
func Preallocate(f *os.File, size int64) error {
	if err := checkSize(f, size); err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		return fmt.Errorf("platform: preallocate: %w", err)
	}
	return nil
}

// StartDetached starts exe with args as a background process and returns as soon as it
// has started, with the null device as standard input, output and error. On this
// platform it is not moved to a session of its own. A goroutine reaps it when it exits.
func StartDetached(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("platform: start %s: %w", exe, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// Elevated reports whether the effective user or group id differs from the real one
// (a set-uid or set-gid executable).
func Elevated() (bool, error) {
	return os.Geteuid() != os.Getuid() || os.Getegid() != os.Getgid(), nil
}

// DownloadsDir returns ~/Downloads, creating it if it is missing.
func DownloadsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("platform: locate the home directory: %w", err)
	}
	return ensureDir(filepath.Join(home, "Downloads"), 0o755)
}

// LogDir returns, creating it with mode 0700 if needed, the directory for daemon logs
// below the user's cache directory.
func LogDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("platform: locate the cache directory: %w", err)
	}
	return ensureDir(filepath.Join(base, "Downex", "logs"), 0o700)
}

// IsSparse is not implemented on this platform; its error matches errors.ErrUnsupported.
func IsSparse(path string) (bool, error) {
	return false, fmt.Errorf("platform: sparse check of %s: %w", path, errors.ErrUnsupported)
}
