//go:build linux || darwin || freebsd

package ipc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	lockFileName = "daemon.lock"
	// staleProbeTimeout bounds the dial Listen uses to tell a live socket from a stale one.
	staleProbeTimeout = time.Second
)

// Listen creates the unix socket endpoint and returns a listener for it.
//
// The socket's directory is created with mode 0700 if missing, and Listen refuses to
// use it unless it is a real directory (not a symlink) owned by the current uid with no
// group/other permission bits; that error wraps fs.ErrPermission. An existing socket
// file is removed only when a dial to it is refused (nobody listens); when someone
// answers, the error wraps ErrAlreadyRunning. A non-socket file at the path is never
// removed. The socket is chmod 0600, and Accept drops (closes and skips) connections
// whose peer uid (SO_PEERCRED, LOCAL_PEERCRED on macOS) differs from the current uid.
// On macOS the listener also keeps the times of its socket, directory and instance
// lock current while it is open (keepFresh).
//
// Callers should hold AcquireInstanceLock first: it serialises concurrent starts so
// that the stale-socket check and the bind cannot interleave between two daemons.
func Listen(endpoint string) (net.Listener, error) {
	if err := checkSocketPath(endpoint); err != nil {
		return nil, err
	}
	if err := ensurePrivateDir(filepath.Dir(endpoint)); err != nil {
		return nil, err
	}
	if err := removeStaleSocket(endpoint); err != nil {
		return nil, err
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return nil, fmt.Errorf("ipc: listen on %s: %w (%w)", endpoint, ErrAlreadyRunning, err)
		}
		return nil, fmt.Errorf("ipc: %w", err)
	}
	// The directory is already 0700, so nobody else can reach the socket in between.
	if err := os.Chmod(endpoint, 0o600); err != nil {
		_ = l.Close() // also unlinks the socket
		return nil, fmt.Errorf("ipc: restrict socket permissions: %w", err)
	}
	pl := &peerCheckListener{l: l, uid: os.Getuid(), done: make(chan struct{})}
	keepFresh(endpoint, pl.done)
	return pl, nil
}

// Dial connects to the daemon's unix socket. It returns an error wrapping ErrNotRunning
// when the socket is missing or nobody listens on it. Before returning, Dial checks the
// server's uid (SO_PEERCRED, LOCAL_PEERCRED on macOS) and refuses a socket served by
// another uid (the error wraps ErrForeignEndpoint and fs.ErrPermission).
func Dial(ctx context.Context, endpoint string) (net.Conn, error) {
	if err := checkSocketPath(endpoint); err != nil {
		return nil, err
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", endpoint)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, fmt.Errorf("ipc: dial %s: %w", endpoint, ErrNotRunning)
		}
		return nil, fmt.Errorf("ipc: %w", err)
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		_ = c.Close()
		return nil, fmt.Errorf("ipc: dial %s: unexpected connection type %T", endpoint, c)
	}
	if err := checkPeerUID(uc, os.Getuid()); err != nil {
		_ = c.Close()
		if errors.Is(err, fs.ErrPermission) {
			return nil, fmt.Errorf("ipc: dial %s: %w (%w)", endpoint, ErrForeignEndpoint, err)
		}
		return nil, fmt.Errorf("ipc: dial %s: %w", endpoint, err)
	}
	return uc, nil
}

// AcquireInstanceLock takes an exclusive, non-blocking flock on daemon.lock in the
// endpoint's directory (created 0700 and verified like Listen does). When another
// process (or another acquisition in this process) holds it, the error wraps
// ErrAlreadyRunning. release unlocks and closes the file; it is idempotent. The lock
// file itself is left in place, and the kernel releases the lock when the process
// exits. Endpoints that share a directory share this lock.
func AcquireInstanceLock(endpoint string) (release func(), err error) {
	if err := checkSocketPath(endpoint); err != nil {
		return nil, err
	}
	dir := filepath.Dir(endpoint)
	if err := ensurePrivateDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, lockFileName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("ipc: open instance lock: %w", err)
	}
	if err := flock(f, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("ipc: instance lock %s: %w", path, ErrAlreadyRunning)
		}
		return nil, fmt.Errorf("ipc: lock %s: %w", path, err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = flock(f, unix.LOCK_UN)
			_ = f.Close()
		})
	}, nil
}

// flock applies how to f, retrying on EINTR.
func flock(f *os.File, how int) error {
	raw, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var opErr error
	if err := raw.Control(func(fd uintptr) {
		for {
			opErr = unix.Flock(int(fd), how)
			if !errors.Is(opErr, unix.EINTR) {
				return
			}
		}
	}); err != nil {
		return err
	}
	return opErr
}

// checkSocketPath requires an absolute filesystem path (abstract sockets and paths
// relative to a working directory are refused) that fits a socket address
// (checkSocketPathLen: macOS refuses paths longer than 103 bytes with a clear error).
func checkSocketPath(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("ipc: endpoint %q is not an absolute socket path", p)
	}
	return checkSocketPathLen(p)
}

// ensurePrivateDir creates dir (mode 0700) if needed and verifies it with checkPrivateDir.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("ipc: create endpoint directory: %w", err)
	}
	return checkPrivateDir(dir)
}

// checkPrivateDir verifies that dir is a directory (not a symlink) owned by the current
// uid with no group/other permission bits. Violations wrap fs.ErrPermission.
func checkPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("ipc: inspect endpoint directory: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("ipc: endpoint directory %s is not a directory (mode %v): %w", dir, fi.Mode(), fs.ErrPermission)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("ipc: endpoint directory %s: no ownership information", dir)
	}
	if uid := os.Getuid(); int64(st.Uid) != int64(uid) {
		return fmt.Errorf("ipc: endpoint directory %s is owned by uid %d, not %d: %w (%w)",
			dir, st.Uid, uid, ErrForeignEndpoint, fs.ErrPermission)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("ipc: endpoint directory %s has mode %#o, want 0700: %w", dir, perm, fs.ErrPermission)
	}
	return nil
}

// removeStaleSocket removes a socket file at path that nobody answers. A live socket
// yields ErrAlreadyRunning; a non-socket file is refused and left alone.
func removeStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ipc: inspect endpoint: %w", err)
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("ipc: endpoint %s exists and is not a socket (mode %v); refusing to replace it", path, fi.Mode())
	}
	ctx, cancel := context.WithTimeout(context.Background(), staleProbeTimeout)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err == nil {
		_ = c.Close()
		return fmt.Errorf("ipc: listen on %s: %w", path, ErrAlreadyRunning)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		// Someone holds the socket but did not accept in time (full backlog on Linux,
		// timeout), or the probe failed for another reason: never delete what may be
		// live. (macOS refuses a connection to a full backlog too; there the instance
		// lock is what keeps a live daemon from being probed.)
		return fmt.Errorf("ipc: probe existing endpoint %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("ipc: remove stale endpoint: %w", err)
	}
	return nil
}

// peerCheckListener accepts only connections whose peer runs as uid.
type peerCheckListener struct {
	l   *net.UnixListener
	uid int
	// done is closed by the first Close; it ends keepFresh.
	done      chan struct{}
	closeOnce sync.Once
}

// Accept returns the next connection from a peer with the listener's uid. Connections
// from other uids (or whose credentials cannot be read) are closed and skipped.
func (p *peerCheckListener) Accept() (net.Conn, error) {
	for {
		c, err := p.l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		if err := checkPeerUID(c, p.uid); err != nil {
			_ = c.Close()
			continue
		}
		return c, nil
	}
}

// Close closes the listener and unlinks the socket file.
func (p *peerCheckListener) Close() error {
	p.closeOnce.Do(func() { close(p.done) })
	return p.l.Close()
}

// Addr returns the socket address.
func (p *peerCheckListener) Addr() net.Addr { return p.l.Addr() }

// checkPeerUID reads the peer's credentials on c and requires uid want. A mismatch
// wraps fs.ErrPermission.
func checkPeerUID(c *net.UnixConn, want int) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return fmt.Errorf("ipc: peer credentials: %w", err)
	}
	var (
		got     int
		credErr error
	)
	if err := raw.Control(func(fd uintptr) { got, credErr = peerUID(fd) }); err != nil {
		return fmt.Errorf("ipc: peer credentials: %w", err)
	}
	if credErr != nil {
		return fmt.Errorf("ipc: peer credentials: %w", credErr)
	}
	if got != want {
		return fmt.Errorf("ipc: peer uid %d is not the current uid %d: %w", got, want, fs.ErrPermission)
	}
	return nil
}
