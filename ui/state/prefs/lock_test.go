package prefs

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A writer's unlock removes only its own lock: when another process took it as stale and
// holds the lock now, that lock stays.
func TestUnlockLeavesAnotherProcesssLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	f, err := Open(path, Options{Poll: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	unlock := f.lock()
	theirs := []byte("4242 another\n")
	if err := os.WriteFile(path+".lock", theirs, 0o600); err != nil { // the lock taken over
		t.Fatal(err)
	}
	unlock()
	got, err := os.ReadFile(path + ".lock")
	if err != nil || !bytes.Equal(got, theirs) {
		t.Fatalf("the other lock is gone: %q %v", got, err)
	}
	_ = os.Remove(path + ".lock")

	// its own lock it removes
	unlock = f.lock()
	unlock()
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// A lock left by a process that died is taken over after lockStale.
func TestAStaleLockIsTakenOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	f, err := Open(path, Options{Poll: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.WriteFile(path+".lock", []byte("1 dead\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * lockStale)
	if err := os.Chtimes(path+".lock", old, old); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := f.Set("downex.theme", "dark"); err != nil {
		t.Fatal(err)
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) >= lockWait {
		t.Fatal("waited for a stale lock", time.Since(start))
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("lock left behind", err)
	}
}

// The stamp a write records is the file's as a poll sees it: a poll after this process's own
// write reads nothing again, and a write another process makes later changes the stamp.
func TestTheStampOfAWriteIsTheFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	f, err := Open(path, Options{Poll: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i, v := range []string{"dark", "light", "dark"} {
		if err := f.Set("downex.theme", v); err != nil {
			t.Fatal(err)
		}
		if err := f.Flush(); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		st := f.stamp
		f.mu.Unlock()
		if disk := stampOf(path); disk != st || !st.ok {
			t.Fatalf("write %d recorded %+v, the file has %+v", i, st, disk)
		}
		_, read, err := readShared(path)
		if err != nil || read != st {
			t.Fatalf("write %d recorded %+v, a read gives %+v (%v)", i, st, read, err)
		}
	}
}
