//go:build darwin || linux

package glue

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"downex/ui/host"
)

// The single-instance lock and the hand-off of a window process on macOS (pure Go, so that it
// is tested on any Unix): a flock(2) on "<dir>/<instance id>.lock", the file and the lock
// Wails v2.17 takes in NSTemporaryDirectory(), so that today's windows and the native ones
// exclude each other; and, beside it, a user-only unix socket on which the host answers a
// later start (the macOS stand-in for Windows' WM_COPYDATA with dwData host.HostHandoffKind
// and its DXH answers). Wails itself has no such channel: its starts post a distributed
// notification and never hear back (start_darwin.go speaks that to an older window).

// fileLock is a held flock: closing the file lets it go.
type fileLock struct {
	f    *os.File
	path string
}

// lockPath is the lock file of instance id in dir, Wails' name.
func lockPath(dir, id string) string { return filepath.Join(dir, id+".lock") }

// claimLock takes the exclusive lock of path without waiting. held reports another process
// holding it (err is nil then); any other failure is err.
func claimLock(path string) (l *fileLock, held bool, err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return &fileLock{f: f, path: path}, false, nil
}

// Close lets the lock go (nil does nothing). The file stays, as Wails leaves its own.
func (l *fileLock) Close() {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Close()
	l.f = nil
}

// socketDir is the folder of the hand-off sockets in the per-user temporary folder dir: only
// this user may enter it (made 0700, refused when another user owns it or others may enter).
func socketDir(dir string) (string, error) {
	d := filepath.Join(dir, "dxui-"+strconv.Itoa(os.Getuid()))
	if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	fi, err := os.Lstat(d)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("glue: %s is not this user's own folder", d)
	}
	return d, nil
}

// socketPath is the hand-off socket of the lock named id: a short name (sun_path holds 104
// bytes on macOS), the same for every build that shares the lock.
func socketPath(dir, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(dir, hex.EncodeToString(sum[:6])+".sock")
}

// The hand-off on the socket: a start writes requestMagic, the kind (host.HostHandoffKind),
// the payload's length and the payload (handoffJSON), each number big-endian uint32; the host
// answers one big-endian uint32 (host.HostAnswer, HostClosing or HostBusy).
const requestMagic = 0x44585131 // "DXQ1"

// handoffJSON is the hand-off's payload as Wails v2.17 posts it on macOS: UTF-8 JSON
// {"Args":[…],"WorkingDirectory":"…"} (not host.EncodeHandoff's UTF-16, which is
// WM_COPYDATA's).
func handoffJSON(args []string, wd string) ([]byte, error) {
	if args == nil {
		args = []string{} // Wails sends [] for no arguments, never null
	}
	b, err := json.Marshal(host.Handoff{Args: args, WorkingDirectory: wd})
	if err != nil {
		return nil, err
	}
	if len(b) > host.MaxHandoff {
		return nil, errors.New("the hand-off is too long")
	}
	return b, nil
}

// parseHandoffJSON reads a hand-off payload (unknown fields ignored).
func parseHandoffJSON(b []byte) (host.Handoff, error) {
	var h host.Handoff
	if len(b) > host.MaxHandoff {
		return h, errors.New("the hand-off is too long")
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return h, fmt.Errorf("the hand-off is not readable: %w", err)
	}
	return h, nil
}

// errNoListener: nobody listens on the socket (no file, or a dead one): the lock's holder is
// not a host yet, or it is an older build's window.
var errNoListener = errors.New("glue: nobody listens for hand-offs")

// sendHandoff hands payload to the host listening at path and returns its answer.
func sendHandoff(path string, kind uint32, payload []byte, timeout time.Duration) (uint32, error) {
	c, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return 0, errNoListener
		}
		return 0, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	head := make([]byte, 12, 12+len(payload))
	binary.BigEndian.PutUint32(head[0:], requestMagic)
	binary.BigEndian.PutUint32(head[4:], kind)
	binary.BigEndian.PutUint32(head[8:], uint32(len(payload)))
	if _, err := c.Write(append(head, payload...)); err != nil {
		return 0, err
	}
	var a [4]byte
	if _, err := io.ReadFull(c, a[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(a[:]), nil
}

// handoffListener is a host's socket: each request's answer comes from answer, which runs on
// the listener's goroutines.
type handoffListener struct {
	ln   net.Listener
	path string
	wg   sync.WaitGroup
	once sync.Once
}

// requestTimeout bounds one start's request on the host's side.
const requestTimeout = 5 * time.Second

// listenHandoffs listens at path (a stale socket of a host that has gone is replaced: the
// caller holds the lock, so nobody else listens there).
func listenHandoffs(path string, answer func(kind uint32, payload []byte) uint32) (*handoffListener, error) {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	l := &handoffListener{ln: ln, path: path}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			l.wg.Add(1)
			go func() {
				defer l.wg.Done()
				serveHandoff(c, answer)
			}()
		}
	}()
	return l, nil
}

func serveHandoff(c net.Conn, answer func(kind uint32, payload []byte) uint32) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(requestTimeout))
	var head [12]byte
	if _, err := io.ReadFull(c, head[:]); err != nil || binary.BigEndian.Uint32(head[0:]) != requestMagic {
		return
	}
	kind, n := binary.BigEndian.Uint32(head[4:]), binary.BigEndian.Uint32(head[8:])
	if n > host.MaxHandoff {
		return
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(c, payload); err != nil {
		return
	}
	var a [4]byte
	binary.BigEndian.PutUint32(a[:], answer(kind, payload))
	_, _ = c.Write(a[:])
}

// Close stops listening and removes the socket (nil does nothing); it waits for the requests
// being answered.
func (l *handoffListener) Close() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		_ = l.ln.Close()
		_ = os.Remove(l.path)
		l.wg.Wait()
	})
}

// What a start makes of a host's answer (hostReply).
const (
	replyOlder = iota // nobody answers: an older build's window holds the lock
	replyTaken        // a host took it
	replyRetry        // a host that is closing or busy, or not listening yet
)

// hostReply is what a start makes of the answer to its hand-off: taken, try again (a closing
// or busy host, or a lock's holder that does not listen yet), or an older build's (nobody
// listens, or anything else).
func hostReply(answer uint32, err error) int {
	switch {
	case err == nil && answer == host.HostAnswer:
		return replyTaken
	case err == nil && (answer == host.HostClosing || answer == host.HostBusy):
		return replyRetry
	case errors.Is(err, errNoListener):
		return replyRetry
	}
	return replyOlder
}
