//go:build linux || darwin

package ipc

import (
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// shortTempDir returns a new directory in the system's temporary directory, removed
// when the test ends. Unlike t.TempDir its name does not include the test's name, so
// that socket paths below it stay short: on macOS $TMPDIR is already about 50 bytes
// long and sun_path holds at most 103.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "dx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestRelativePathsRefused(t *testing.T) {
	t.Parallel()
	if l, err := Listen("rpc.sock"); err == nil {
		_ = l.Close()
		t.Error("Listen accepted a relative path")
	}
	if c, err := Dial(t.Context(), "@abstract"); err == nil {
		_ = c.Close()
		t.Error("Dial accepted a non-absolute path")
	}
	if r, err := AcquireInstanceLock("rpc.sock"); err == nil {
		r()
		t.Error("AcquireInstanceLock accepted a relative path")
	}
}

func TestSocketAndDirectoryModes(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	_ = listenT(t, ep)

	fi, err := os.Lstat(ep)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Type() != fs.ModeSocket || fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode = %v, want socket 0600", fi.Mode())
	}
	di, err := os.Lstat(filepath.Dir(ep))
	if err != nil {
		t.Fatal(err)
	}
	if !di.IsDir() || di.Mode().Perm() != 0o700 {
		t.Errorf("directory mode = %v, want dir 0700", di.Mode())
	}
}

func TestListenRefusesOpenDirectory(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	dir := filepath.Dir(ep)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if l, err := Listen(ep); err == nil {
		_ = l.Close()
		t.Fatal("Listen used a 0755 directory")
	} else if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Listen: %v, want fs.ErrPermission", err)
	}
	if r, err := AcquireInstanceLock(ep); err == nil {
		r()
		t.Fatal("AcquireInstanceLock used a 0755 directory")
	} else if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("AcquireInstanceLock: %v, want fs.ErrPermission", err)
	}
}

func TestListenRefusesSymlinkDirectory(t *testing.T) {
	t.Parallel()
	base := shortTempDir(t)
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if l, err := Listen(filepath.Join(link, socketName)); err == nil {
		_ = l.Close()
		t.Fatal("Listen used a symlinked directory")
	} else if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Listen: %v, want fs.ErrPermission", err)
	}
}

// staleSocket leaves a socket file at ep with nobody listening on it.
func staleSocket(t *testing.T, ep string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(ep), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: ep, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(ep); err != nil {
		t.Fatalf("stale socket missing: %v", err)
	}
}

func TestStaleSocketReplaced(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	staleSocket(t, ep)

	if _, err := dialT(t, ep); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Dial to stale socket: %v, want ErrNotRunning", err)
	}
	l := listenT(t, ep)
	go serveLineEcho(l)
	c, err := dialT(t, ep)
	if err != nil {
		t.Fatalf("Dial after replacing the stale socket: %v", err)
	}
	_ = c.Close()
}

func TestListenRefusesNonSocket(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	if err := os.MkdirAll(filepath.Dir(ep), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ep, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if l, err := Listen(ep); err == nil {
		_ = l.Close()
		t.Fatal("Listen replaced a regular file")
	}
	if b, err := os.ReadFile(ep); err != nil || string(b) != "keep me" {
		t.Fatalf("regular file changed: %q, %v", b, err)
	}
}

// TestAcceptRejectsOtherUID makes the listener expect a different uid; the client's
// connection must be closed by the server without ever reaching Accept's caller.
func TestAcceptRejectsOtherUID(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	l, err := Listen(ep)
	if err != nil {
		t.Fatal(err)
	}
	pl, ok := l.(*peerCheckListener)
	if !ok {
		t.Fatalf("Listen returned %T, want *peerCheckListener", l)
	}
	pl.uid = os.Getuid() + 1

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- c
	}()

	c, err := dialT(t, ep) // the server's uid is ours, so the client side passes
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("Read on rejected connection = %d, %v; want EOF/ECONNRESET", n, err)
	}

	_ = l.Close()
	select {
	case c := <-accepted:
		_ = c.Close()
		t.Fatal("Accept returned a connection from a foreign uid")
	case err := <-acceptErr:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept after Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Accept did not return after Close")
	}
}

// TestDialRejectsOtherServerUID checks the client-side server credential check.
func TestDialRejectsOtherServerUID(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	l := listenT(t, ep)
	go serveLineEcho(l)

	c, err := dialT(t, ep)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	uc := c.(*net.UnixConn)
	if err := checkPeerUID(uc, os.Getuid()); err != nil {
		t.Fatalf("checkPeerUID(own uid): %v", err)
	}
	if err := checkPeerUID(uc, os.Getuid()+1); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("checkPeerUID(other uid) = %v, want fs.ErrPermission", err)
	}
}

func TestInstanceLockFile(t *testing.T) {
	t.Parallel()
	ep := testEndpoint(t)
	release, err := AcquireInstanceLock(ep)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fi, err := os.Lstat(filepath.Join(filepath.Dir(ep), lockFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf("lock file mode = %v, want regular 0600", fi.Mode())
	}
}
