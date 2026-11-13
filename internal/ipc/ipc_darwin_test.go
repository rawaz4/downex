//go:build darwin

package ipc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// testEndpoint returns a socket path inside a not-yet-existing directory of a fresh
// short temporary directory, so Listen exercises directory creation.
func testEndpoint(t *testing.T) string {
	t.Helper()
	return filepath.Join(shortTempDir(t), "dx", socketName)
}

// TestEndpointDarwin: the default endpoint is in the user's temporary directory as
// confstr reports it, whatever $TMPDIR says, and agrees with getconf(1).
func TestEndpointDarwin(t *testing.T) {
	t.Setenv(EnvEndpoint, "")
	out, err := exec.Command("/usr/bin/getconf", "DARWIN_USER_TEMP_DIR").Output()
	if err != nil {
		t.Fatalf("getconf DARWIN_USER_TEMP_DIR: %v", err)
	}
	tmp := strings.TrimSuffix(string(out), "\n")
	want := filepath.Join(tmp, "downex-"+strconv.Itoa(os.Getuid()), socketName)
	if len(want) > darwinSunPathLen-1 {
		t.Skipf("the temporary directory %q is too long for a socket path", tmp)
	}
	for _, env := range []string{"", "/tmp", "relative"} {
		t.Setenv("TMPDIR", env)
		if ep, err := Endpoint(); err != nil || ep != want {
			t.Fatalf("TMPDIR=%q: Endpoint = %q, %v; want %q", env, ep, err, want)
		}
	}
}

// TestEndpointOverrideDarwin: the override is cleaned, must be absolute and must fit
// sun_path.
func TestEndpointOverrideDarwin(t *testing.T) {
	t.Setenv(EnvEndpoint, "/tmp/profile-a/../profile-b/rpc.sock")
	if ep, err := Endpoint(); err != nil || ep != "/tmp/profile-b/rpc.sock" {
		t.Fatalf("Endpoint = %q, %v; want the cleaned override", ep, err)
	}
	t.Setenv(EnvEndpoint, "relative.sock")
	if ep, err := Endpoint(); err == nil {
		t.Fatalf("Endpoint accepted a relative override: %q", ep)
	}
	t.Setenv(EnvEndpoint, "/tmp/"+strings.Repeat("x", 99)) // 104 bytes
	if ep, err := Endpoint(); !errors.Is(err, syscall.ENAMETOOLONG) {
		t.Fatalf("Endpoint = %q, %v; want ENAMETOOLONG", ep, err)
	}
}

// pathOfLen returns a socket path of exactly n bytes in a fresh short directory.
func pathOfLen(t *testing.T, n int) string {
	t.Helper()
	base := shortTempDir(t)
	pad := n - len(base) - len("/") - len("/"+socketName)
	if pad < 1 {
		t.Skipf("the temporary directory %q is too long for a %d-byte socket path", base, n)
	}
	p := filepath.Join(base, strings.Repeat("d", pad), socketName)
	if len(p) != n {
		t.Fatalf("built a %d-byte path, want %d", len(p), n)
	}
	return p
}

// TestSocketPathLimit: a 103-byte path (the most sun_path holds) works end to end; a
// 104-byte one is refused by Listen, Dial and AcquireInstanceLock with ENAMETOOLONG,
// before anything is created.
func TestSocketPathLimit(t *testing.T) {
	t.Parallel()
	longest := pathOfLen(t, darwinSunPathLen-1)
	l := listenT(t, longest)
	go serveLineEcho(l)
	c, err := dialT(t, longest)
	if err != nil {
		t.Fatalf("Dial(103-byte path): %v", err)
	}
	_ = c.Close()

	tooLong := pathOfLen(t, darwinSunPathLen)
	if l, err := Listen(tooLong); !errors.Is(err, syscall.ENAMETOOLONG) {
		if err == nil {
			_ = l.Close()
		}
		t.Fatalf("Listen(104-byte path) = %v, want ENAMETOOLONG", err)
	}
	if _, err := dialT(t, tooLong); !errors.Is(err, syscall.ENAMETOOLONG) {
		t.Fatalf("Dial(104-byte path) = %v, want ENAMETOOLONG", err)
	}
	if r, err := AcquireInstanceLock(tooLong); !errors.Is(err, syscall.ENAMETOOLONG) {
		if err == nil {
			r()
		}
		t.Fatalf("AcquireInstanceLock(104-byte path) = %v, want ENAMETOOLONG", err)
	}
	if _, err := os.Lstat(filepath.Dir(tooLong)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refused endpoint's directory was created (%v)", err)
	}
}

// TestPeerCredentials: LOCAL_PEERCRED reports this process's uid for both ends of a
// socket pair, as a version-0 xucred.
func TestPeerCredentials(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	for _, fd := range fds {
		cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			t.Fatalf("LOCAL_PEERCRED: %v", err)
		}
		if cred.Version != xucredVersion {
			t.Fatalf("xucred version = %d, want %d", cred.Version, xucredVersion)
		}
		if uid, err := peerUID(uintptr(fd)); err != nil || uid != os.Geteuid() {
			t.Fatalf("peerUID = %d, %v; want %d", uid, err, os.Geteuid())
		}
	}
	// An unconnected socket has no peer.
	s, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(s)
	if uid, err := peerUID(uintptr(s)); err == nil {
		t.Fatalf("peerUID(unconnected socket) = %d, want an error", uid)
	}
}

// TestListenerStopsRefresh: Close ends the listener's refresh of its files
// (refreshTimes itself is tested on every system).
func TestListenerStopsRefresh(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	release, err := AcquireInstanceLock(ep)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	l, err := Listen(ep)
	if err != nil {
		t.Fatal(err)
	}
	pl := l.(*peerCheckListener)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pl.done:
	default:
		t.Fatal("Close did not stop the refresh")
	}
	_ = l.Close() // a second Close must not close done again
}
