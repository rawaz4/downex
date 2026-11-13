// Package dlock provides the per-download, cross-process advisory locks shared by the
// standalone CLI and downexd, so that a download never has two writers: two
// processes never run the same download, and none deletes a download another runs.
//
// The lock of download id of the database at dbPath is the file
// "<dbPath>.locks/<id>.lock", locked with LockFileEx (its first byte) on Windows and
// flock(2) on Linux, macOS and the BSDs. The operating system drops the lock when the
// holding process dies, so a crash or kill leaves no stale lock.
package dlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"downex/internal/model"
)

// ErrBusy reports a download that another downex process (or another Lock in this
// process) holds.
var ErrBusy = errors.New("download is in use by another downex process")

// Dir returns the directory of the lock files for the database at dbPath.
func Dir(dbPath string) string { return dbPath + ".locks" }

// Lock is a held lock of one download. A nil *Lock is valid and holds nothing.
type Lock struct {
	f    *os.File
	path string
}

// Acquire takes the lock of download id in dir (see Dir) without waiting, creating
// dir (mode 0700) and the lock file (0600) as needed. It fails with an error
// wrapping ErrBusy when the lock is held elsewhere.
func Acquire(dir string, id model.DownloadID) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	path := filepath.Join(dir, strconv.FormatInt(int64(id), 10)+".lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := tryLock(f); err != nil {
		_ = f.Close()
		if errors.Is(err, ErrBusy) {
			return nil, fmt.Errorf("download %d: %w", id, ErrBusy)
		}
		return nil, fmt.Errorf("lock download %d: %w", id, err)
	}
	return &Lock{f: f, path: path}, nil
}

// daemonLockName is the lock file in Dir(dbPath) held by the daemon that owns the
// database. It cannot collide with a download's "<id>.lock".
const daemonLockName = "daemon.lock"

// AcquireDaemon takes, without waiting, the lock that makes one daemon the only owner of
// the database at dbPath (whatever endpoint it serves): "<dbPath>.locks/daemon.lock".
// It fails with an error wrapping ErrBusy while another daemon holds it. Release it when
// the daemon stops; the operating system drops it if the daemon dies.
func AcquireDaemon(dbPath string) (*Lock, error) {
	dir := Dir(dbPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	path := filepath.Join(dir, daemonLockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := tryLock(f); err != nil {
		_ = f.Close()
		if errors.Is(err, ErrBusy) {
			return nil, fmt.Errorf("database %s is in use by another downexd: %w", dbPath, ErrBusy)
		}
		return nil, fmt.Errorf("lock database %s: %w", dbPath, err)
	}
	return &Lock{f: f, path: path}, nil
}

// Release unlocks and closes the lock file. It is safe on nil and more than once.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unlock(l.f)
	_ = l.f.Close()
	l.f = nil
}

// Discard releases the lock and removes its file. It is meant for a download whose
// record was deleted: ids are never reused, so nobody locks that file again.
func (l *Lock) Discard() {
	if l == nil {
		return
	}
	l.Release()
	_ = os.Remove(l.path)
}
