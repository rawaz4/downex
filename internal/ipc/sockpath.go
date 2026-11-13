package ipc

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// This file holds the OS-independent parts of the unix-socket endpoints, in particular
// of the macOS one (docs/MACOS-SPEC.md §3). They are kept free of build constraints so
// that every platform runs their tests. Paths are slash-separated (package path).

// socketName is the file name of the daemon's socket in its endpoint directory.
const socketName = "rpc.sock"

// darwinSunPathLen is the size of sockaddr_un.sun_path on macOS, 104 bytes, which
// includes the NUL that ends the path: a socket path holds at most 103 bytes there.
const darwinSunPathLen = 104

// checkSunPath refuses a socket path that does not fit a sun_path of sunPathLen bytes
// (with its terminating NUL), and one with a NUL byte inside. The length error wraps
// syscall.ENAMETOOLONG; the kernel would otherwise refuse the address with a bare
// EINVAL.
func checkSunPath(p string, sunPathLen int) error {
	if strings.IndexByte(p, 0) >= 0 {
		return fmt.Errorf("ipc: endpoint %q contains a NUL byte: %w", p, syscall.EINVAL)
	}
	if len(p) >= sunPathLen {
		return fmt.Errorf("ipc: endpoint %q is %d bytes long; a socket path holds at most %d bytes here (set %s to a shorter one): %w",
			p, len(p), sunPathLen-1, EnvEndpoint, syscall.ENAMETOOLONG)
	}
	return nil
}

// darwinEndpoint is the default endpoint on macOS for the user uid:
// <tmp>/downex-<uid>/rpc.sock in the user's own temporary directory tmp (the
// confstr(_CS_DARWIN_USER_TEMP_DIR) directory /var/folders/…/T/, which only the user
// can enter, so that no other account can occupy the endpoint directory first).
// When tmp is unknown ("" or relative) or that path does not fit a socket address, it
// is <home>/Library/Caches/Downex/run/rpc.sock. It fails, with an error wrapping
// syscall.ENAMETOOLONG when a candidate was too long, if neither can be used.
func darwinEndpoint(tmp, home string, uid int) (string, error) {
	var tooLong error
	if path.IsAbs(tmp) {
		p := path.Join(tmp, "downex-"+strconv.Itoa(uid), socketName)
		err := checkSunPath(p, darwinSunPathLen)
		if err == nil {
			return p, nil
		}
		tooLong = err
	}
	if path.IsAbs(home) {
		p := path.Join(home, "Library", "Caches", "Downex", "run", socketName)
		err := checkSunPath(p, darwinSunPathLen)
		if err == nil {
			return p, nil
		}
		tooLong = err
	}
	if tooLong != nil {
		return "", fmt.Errorf("ipc: no default endpoint fits a socket address: %w", tooLong)
	}
	return "", fmt.Errorf("ipc: no default endpoint: neither the temporary directory nor the home directory is known (set %s)", EnvEndpoint)
}

// refreshTimes sets the access and modification times of each of paths to the current
// time every interval until done is closed. Errors (a path that is gone) are ignored.
func refreshTimes(paths []string, interval time.Duration, done <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			now := time.Now()
			for _, p := range paths {
				_ = os.Chtimes(p, now, now)
			}
		}
	}
}
