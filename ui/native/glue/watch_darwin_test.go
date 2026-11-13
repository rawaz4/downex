//go:build darwin

package glue

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestWatchFilesSeesAnAtomicWrite: another process's write of ui-theme.json (temp file then
// rename) reaches the window at once, and a file it does not watch is not told.
func TestWatchFilesSeesAnAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	got := make(chan string, 8)
	fw := watchFiles(dir, []string{"ui-theme.json"}, time.Hour, func(n string) { got <- n })
	defer fw.Stop()
	if fw.kq < 0 {
		t.Fatal("no kqueue on the folder")
	}
	write := func(name, s string) {
		tmp := filepath.Join(dir, name+".tmp")
		if err := os.WriteFile(tmp, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	write("other.json", "x")
	write("ui-theme.json", `{"theme":"dark"}`)
	select {
	case n := <-got:
		if n != "ui-theme.json" {
			t.Errorf("told %q", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the write was not seen")
	}
	select {
	case n := <-got:
		t.Errorf("told %q again", n)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestWatchFilesStops(t *testing.T) {
	fw := watchFiles(t.TempDir(), []string{"a"}, 10*time.Millisecond, func(string) {})
	done := make(chan struct{})
	go func() { fw.Stop(); fw.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop does not return")
	}
	var none *fileWatch
	none.Stop()
}

// TestWatchFilesFallsBackToPolling: a kqueue that fails while the window is open (any error
// but EINTR) does not end the following: the files are polled from then on.
func TestWatchFilesFallsBackToPolling(t *testing.T) {
	failed := make(chan struct{})
	var once sync.Once
	keventHook = func(kq int, changes, events []unix.Kevent_t, timeout *unix.Timespec) (int, error) {
		if events != nil {
			once.Do(func() { close(failed) })
			return 0, unix.EBADF
		}
		return unix.Kevent(kq, changes, events, timeout)
	}
	t.Cleanup(func() { keventHook = unix.Kevent })
	dir := t.TempDir()
	got := make(chan string, 8)
	fw := watchFiles(dir, []string{"ui-theme.json"}, 20*time.Millisecond, func(n string) { got <- n })
	defer fw.Stop()
	if fw.kq < 0 {
		t.Fatal("no kqueue on the folder")
	}
	<-failed
	if err := os.WriteFile(filepath.Join(dir, "ui-theme.json"), []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("the write was not seen after the kqueue failed")
	}
}

// TestWatchFilesStopsAtOnce: Stop runs on the main thread when a window closes; it wakes the
// kqueue instead of waiting for a timeout.
func TestWatchFilesStopsAtOnce(t *testing.T) {
	fw := watchFiles(t.TempDir(), []string{"a"}, time.Hour, func(string) {})
	if fw.kq < 0 {
		t.Fatal("no kqueue on the folder")
	}
	time.Sleep(50 * time.Millisecond) // the goroutine waits in kevent
	start := time.Now()
	fw.Stop()
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("Stop took %v", d)
	}
}
