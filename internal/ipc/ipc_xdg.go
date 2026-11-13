//go:build linux || freebsd

package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// maxSocketPathLen is the longest socket path every supported system accepts
// (sun_path holds 104 bytes on the BSDs and 108 on Linux, including the NUL).
const maxSocketPathLen = 103

// Endpoint returns $XDG_RUNTIME_DIR/downex/rpc.sock. When XDG_RUNTIME_DIR is unset or
// not absolute it falls back to a private directory below the user's home,
// $XDG_STATE_HOME/downex/run/rpc.sock (default ~/.local/state/downex/run/rpc.sock),
// which no other account can create first; a shared directory such as /tmp would let
// another user pre-create the directory and keep the daemon from starting. Only when
// the home directory is unknown or that path is too long for a socket address is
// /tmp/downex-<uid>/rpc.sock used. A non-empty EnvEndpoint overrides it; the override
// must be an absolute socket path.
func Endpoint() (string, error) {
	if e := os.Getenv(EnvEndpoint); e != "" {
		if err := checkSocketPath(e); err != nil {
			return "", fmt.Errorf("ipc: %s: %w", EnvEndpoint, err)
		}
		return filepath.Clean(e), nil
	}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" && filepath.IsAbs(rt) {
		return filepath.Join(rt, "downex", socketName), nil
	}
	state := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(state) { // unset, empty or relative: the XDG spec says to ignore it
		state = ""
		if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
			state = filepath.Join(home, ".local", "state")
		}
	}
	if state != "" {
		if p := filepath.Join(state, "downex", "run", socketName); len(p) <= maxSocketPathLen {
			return p, nil
		}
	}
	return filepath.Join("/tmp", "downex-"+strconv.Itoa(os.Getuid()), socketName), nil
}

// checkSocketPathLen accepts every length: an address too long for sun_path fails when
// it is bound or connected to.
func checkSocketPathLen(string) error { return nil }

// keepFresh does nothing here: the endpoint normally lives in $XDG_RUNTIME_DIR or below
// the home directory, which nothing cleans by age.
func keepFresh(string, <-chan struct{}) {}
