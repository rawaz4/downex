//go:build darwin || linux

package e2e

import (
	"errors"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

// getxattr reads the extended attribute name of path.
func getxattr(path, name string) (string, error) {
	buf := make([]byte, 64<<10)
	n, err := unix.Getxattr(path, name, buf)
	switch {
	case errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.EOPNOTSUPP):
		return "", errNoMarks
	case err != nil:
		return "", err
	}
	return string(buf[:n]), nil
}

// readOriginMark returns the origin of path: on macOS the kMDItemWhereFroms binary plist
// (after checking the quarantine attribute), on Linux user.xdg.origin.url.
func readOriginMark(path string) (string, error) {
	if runtime.GOOS == "linux" {
		return getxattr(path, "user.xdg.origin.url")
	}
	q, err := getxattr(path, "com.apple.quarantine")
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(q, "0081;") || !strings.Contains(q, ";Downex;") {
		return q, errors.New("unexpected quarantine value " + q)
	}
	return getxattr(path, "com.apple.metadata:kMDItemWhereFroms")
}
