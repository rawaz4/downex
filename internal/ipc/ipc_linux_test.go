//go:build linux

package ipc

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// testEndpoint returns a socket path inside a not-yet-existing directory of a fresh
// temporary directory, so Listen exercises directory creation.
func testEndpoint(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "dx", socketName)
}

func TestEndpointDefault(t *testing.T) {
	t.Setenv(EnvEndpoint, "")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/4242")
	if ep, err := Endpoint(); err != nil || ep != "/run/user/4242/downex/rpc.sock" {
		t.Fatalf("Endpoint = %q, %v; want the XDG_RUNTIME_DIR socket", ep, err)
	}
	// Without XDG_RUNTIME_DIR the socket lives below the user's home, where no other
	// account can pre-create its directory (SEC-5), not in /tmp.
	t.Setenv("HOME", "/home/alice")
	t.Setenv("XDG_STATE_HOME", "")
	for _, rt := range []string{"", "relative/dir"} {
		t.Setenv("XDG_RUNTIME_DIR", rt)
		if ep, err := Endpoint(); err != nil || ep != "/home/alice/.local/state/downex/run/rpc.sock" {
			t.Fatalf("XDG_RUNTIME_DIR=%q: Endpoint = %q, %v; want the home fallback", rt, ep, err)
		}
	}
	t.Setenv("XDG_STATE_HOME", "/srv/state")
	if ep, err := Endpoint(); err != nil || ep != "/srv/state/downex/run/rpc.sock" {
		t.Fatalf("Endpoint = %q, %v; want the XDG_STATE_HOME fallback", ep, err)
	}
	// Only a path too long for a socket address falls back to /tmp.
	t.Setenv("XDG_STATE_HOME", "/"+strings.Repeat("d", maxSocketPathLen))
	fallback := "/tmp/downex-" + strconv.Itoa(os.Getuid()) + "/rpc.sock"
	if ep, err := Endpoint(); err != nil || ep != fallback {
		t.Fatalf("Endpoint = %q, %v; want %q", ep, err, fallback)
	}
}

// TestHomeFallbackIsPrivate: Listen creates the home fallback directory 0700.
func TestHomeFallbackIsPrivate(t *testing.T) {
	t.Setenv(EnvEndpoint, "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	state, err := os.MkdirTemp("", "dxs-") // short: sun_path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	t.Setenv("XDG_STATE_HOME", state)
	ep, err := Endpoint()
	if err != nil || !strings.HasPrefix(ep, state+"/") {
		t.Fatalf("Endpoint = %q, %v; want a socket below %s", ep, err, state)
	}
	l := listenT(t, ep)
	go serveLineEcho(l)
	fi, err := os.Stat(filepath.Dir(ep))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("endpoint directory: %v, %v; want mode 0700", fi, err)
	}
	c, err := dialT(t, ep)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}

func TestEndpointOverride(t *testing.T) {
	t.Setenv(EnvEndpoint, "/srv/profile-a/../profile-b/rpc.sock")
	if ep, err := Endpoint(); err != nil || ep != "/srv/profile-b/rpc.sock" {
		t.Fatalf("Endpoint = %q, %v; want the cleaned override", ep, err)
	}
	t.Setenv(EnvEndpoint, "relative.sock")
	if ep, err := Endpoint(); err == nil {
		t.Fatalf("Endpoint accepted a relative override: %q", ep)
	}
}
