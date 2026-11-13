//go:build darwin

package ipc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Endpoint returns the per-user default endpoint (docs/MACOS-SPEC.md §3):
// $TMPDIR/downex-<uid>/rpc.sock in the user's own temporary directory, else
// ~/Library/Caches/Downex/run/rpc.sock (darwinEndpoint). The temporary directory is
// confstr(_CS_DARWIN_USER_TEMP_DIR), the one launchd puts in $TMPDIR, so that every
// process of the user (an app started by Launch Services, a terminal, an ssh session
// without TMPDIR) finds the same endpoint; $TMPDIR is used only when confstr cannot be
// read. A non-empty EnvEndpoint overrides it; the override must be an absolute socket
// path of at most 103 bytes.
func Endpoint() (string, error) {
	if e := os.Getenv(EnvEndpoint); e != "" {
		e = filepath.Clean(e)
		if err := checkSocketPath(e); err != nil {
			return "", fmt.Errorf("ipc: %s: %w", EnvEndpoint, err)
		}
		return e, nil
	}
	tmp := userTempDir()
	if tmp == "" {
		if d := os.Getenv("TMPDIR"); filepath.IsAbs(d) {
			tmp = d
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return darwinEndpoint(tmp, home, os.Getuid())
}

// getconfTimeout bounds the getconf(1) run of userTempDir.
const getconfTimeout = 5 * time.Second

// userTempDir is confstr(_CS_DARWIN_USER_TEMP_DIR), read once per process through
// getconf(1) because these programs are built without cgo; "" when it cannot be read.
// The value depends on the user only, not on the environment.
var userTempDir = sync.OnceValue(func() string {
	ctx, cancel := context.WithTimeout(context.Background(), getconfTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/getconf", "DARWIN_USER_TEMP_DIR")
	cmd.Dir = "/"
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	dir := strings.TrimSuffix(string(out), "\n")
	if !filepath.IsAbs(dir) || strings.ContainsAny(dir, "\x00\n") {
		return ""
	}
	return dir
})

// checkSocketPathLen refuses a path longer than macOS's 104-byte sun_path holds.
func checkSocketPathLen(p string) error { return checkSunPath(p, darwinSunPathLen) }

// freshInterval is how often a listening daemon renews the times of its endpoint files.
const freshInterval = time.Hour

// keepFresh renews, every freshInterval until done is closed, the access and
// modification times of the socket at endpoint, of its directory and of the instance
// lock there. The system's periodic cleanup of /var/folders may remove files of the
// per-user temporary directory that have gone unused for days, and a socket's times do
// not change when clients connect: without this, a daemon running for a week could
// lose its socket (clients would find no daemon) and its lock file (a second daemon
// could take the instance lock).
func keepFresh(endpoint string, done <-chan struct{}) {
	dir := filepath.Dir(endpoint)
	go refreshTimes([]string{endpoint, filepath.Join(dir, lockFileName), dir}, freshInterval, done)
}
