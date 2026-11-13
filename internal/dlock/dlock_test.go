package dlock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDir(t *testing.T) {
	db := filepath.Join("state", "downex.db")
	if got, want := Dir(db), db+".locks"; got != want {
		t.Fatalf("Dir(%q) = %q, want %q", db, got, want)
	}
}

func TestLockIsExclusive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "downex.db.locks") // created on demand
	a, err := Acquire(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir, 1); !errors.Is(err, ErrBusy) {
		t.Fatalf("second lock = %v, want ErrBusy", err)
	}
	b, err := Acquire(dir, 2)
	if err != nil {
		t.Fatalf("lock of another download: %v", err)
	}
	b.Release()
	a.Release()
	a.Release() // idempotent
	c, err := Acquire(dir, 1)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	c.Discard()
	if _, err := os.Stat(filepath.Join(dir, "1.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("discarded lock file still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2.lock")); err != nil {
		t.Errorf("released lock file was removed: %v", err)
	}
	var none *Lock
	none.Release()
	none.Discard()
}

func TestAcquireFailsWhenDirIsAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(file, 1); err == nil || errors.Is(err, ErrBusy) {
		t.Fatalf("Acquire below a file = %v, want a non-busy error", err)
	}
}

// TestDaemonLockIsExclusive (L10): one daemon per database, whatever its endpoint, and
// the daemon lock does not interfere with the download locks.
func TestDaemonLockIsExclusive(t *testing.T) {
	db := filepath.Join(t.TempDir(), "downex.db")
	a, err := AcquireDaemon(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireDaemon(db); !errors.Is(err, ErrBusy) {
		t.Fatalf("second daemon lock = %v, want ErrBusy", err)
	}
	other, err := AcquireDaemon(filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatalf("daemon lock of another database: %v", err)
	}
	other.Release()
	d, err := Acquire(Dir(db), 1)
	if err != nil {
		t.Fatalf("download lock while the daemon lock is held: %v", err)
	}
	d.Release()
	a.Release()
	b, err := AcquireDaemon(db)
	if err != nil {
		t.Fatalf("daemon lock after release: %v", err)
	}
	b.Release()
}
