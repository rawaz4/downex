//go:build linux

package platform

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// wantDetachFacts are the facts a helper started by StartDetached must report: its own
// session, and "/" (not the starter's directory) as working directory (L4).
func wantDetachFacts(*testing.T) []string { return []string{"session=leader", "cwd=/"} }

// detachFacts describes the calling process: whether it leads its own session, its
// working directory, and what each of its descriptors above standard error refers to
// ("fd=<readlink of /proc/self/fd/N>").
func detachFacts() []string {
	facts := []string{"session=inherited"}
	if sid, err := unix.Getsid(0); err == nil && sid == os.Getpid() {
		facts = []string{"session=leader"}
	}
	if wd, err := os.Getwd(); err == nil {
		facts = append(facts, "cwd="+wd)
	}
	entries, _ := os.ReadDir("/proc/self/fd")
	for _, e := range entries {
		if n, err := strconv.Atoi(e.Name()); err == nil && n > 2 {
			if target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name())); err == nil {
				facts = append(facts, "fd="+target)
			}
		}
	}
	return facts
}

// TestStartDetachedInheritsNoDescriptors (L4): a descriptor the caller inherited
// without close-on-exec (a jobserver pipe, a test harness's status descriptor) does not
// reach the detached process, so that it cannot keep the caller's pipe open.
func TestStartDetachedInheritsNoDescriptors(t *testing.T) {
	var p [2]int
	if err := unix.Pipe2(p[:], 0); err != nil { // no O_CLOEXEC, like an inherited descriptor
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	defer unix.Close(p[1])
	target, err := os.Readlink(filepath.Join("/proc/self/fd", strconv.Itoa(p[1])))
	if err != nil {
		t.Fatal(err)
	}
	goFile, marker := helperFiles(t)
	if err := StartDetached(os.Args[0], helperCommand("detached", goFile, marker)...); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	facts := awaitMarker(t, marker)
	checkFacts(t, facts)
	if slices.Contains(facts, "fd="+target) {
		t.Fatalf("the detached process inherited the caller's descriptor %d (%s): %q", p[1], target, facts)
	}
}

func allocated(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return int64(fi.Sys().(*syscall.Stat_t).Blocks) * 512
}

// holesSupported reports whether dir's file system leaves the range added by ftruncate
// unallocated.
func holesSupported(t *testing.T, dir string) bool {
	t.Helper()
	path := filepath.Join(dir, "holes-probe")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if err := os.Truncate(path, 16<<20); err != nil {
		t.Fatal(err)
	}
	return allocated(t, path) < 16<<20
}

// skipWithoutFallocate skips the test when the temporary directory's file system has no
// fallocate, or one that does not reserve blocks (copy-on-write file systems may only
// check for free space).
func skipWithoutFallocate(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "probe")
	f := createFile(t, path)
	const n = 1 << 20
	err := unix.Fallocate(int(f.Fd()), 0, 0, n)
	switch {
	case errors.Is(err, unix.EOPNOTSUPP), errors.Is(err, unix.ENOSYS):
		t.Skipf("the temporary directory's file system has no fallocate: %v", err)
	case err != nil:
		t.Fatalf("fallocate probe: %v", err)
	case allocated(t, path) < n:
		t.Skip("fallocate does not reserve blocks on the temporary directory's file system")
	}
}

// TestPreallocateReservesBlocks: fallocate reserves the whole range up front.
func TestPreallocateReservesBlocks(t *testing.T) {
	dir := t.TempDir()
	skipWithoutFallocate(t, dir)
	path := filepath.Join(dir, "alloc.dxpart")
	f := createFile(t, path)
	data := pattern(1 << 20)
	if _, err := f.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	const size = 64 << 20
	if err := Preallocate(f, size); err != nil {
		t.Fatalf("Preallocate: %v", err)
	}
	if got := fileSize(t, f); got != size {
		t.Fatalf("size = %d, want %d", got, size)
	}
	if got := allocated(t, path); got < size {
		t.Fatalf("allocated %d bytes, want at least %d", got, size)
	}
	if got := readAt(t, f, 0, len(data)); !bytes.Equal(got, data) {
		t.Fatal("existing data changed")
	}
	if got := readAt(t, f, size-4096, 4096); !bytes.Equal(got, make([]byte, 4096)) {
		t.Fatal("reserved range does not read as zeros")
	}
}

// TestPreallocateFallsBackToTruncate: without fallocate (EOPNOTSUPP, ENOSYS) the file is
// extended sparsely and keeps its data.
func TestPreallocateFallsBackToTruncate(t *testing.T) {
	for _, errno := range []error{unix.EOPNOTSUPP, unix.ENOSYS} {
		t.Run(errno.Error(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "noalloc.dxpart")
			f := createFile(t, path)
			data := pattern(64 << 10)
			if _, err := f.WriteAt(data, 0); err != nil {
				t.Fatal(err)
			}
			const size = 64 << 20
			calls := 0
			err := preallocate(f, size, func(int, int64) error { calls++; return errno })
			if err != nil {
				t.Fatalf("preallocate: %v", err)
			}
			if calls != 1 {
				t.Fatalf("alloc called %d times, want 1", calls)
			}
			if got := fileSize(t, f); got != size {
				t.Fatalf("size = %d, want %d", got, size)
			}
			if got := readAt(t, f, 0, len(data)); !bytes.Equal(got, data) {
				t.Fatal("existing data changed")
			}
			if sparse, err := IsSparse(path); err != nil || (!sparse && holesSupported(t, filepath.Dir(path))) {
				t.Fatalf("IsSparse = %v, %v; want a sparse file", sparse, err)
			}
		})
	}
}

// TestPreallocateRetriesEINTR: an allocation interrupted by a signal is restarted.
func TestPreallocateRetriesEINTR(t *testing.T) {
	f := createFile(t, filepath.Join(t.TempDir(), "eintr.dxpart"))
	calls := 0
	err := preallocate(f, 1<<20, func(fd int, size int64) error {
		if calls++; calls < 3 {
			return unix.EINTR
		}
		return unix.Ftruncate(fd, size)
	})
	if err != nil || calls != 3 {
		t.Fatalf("preallocate = %v after %d calls, want success after 3", err, calls)
	}
	if got := fileSize(t, f); got != 1<<20 {
		t.Fatalf("size = %d, want %d", got, 1<<20)
	}
}

// TestPreallocateReportsDiskFull: ENOSPC is returned (matching syscall.ENOSPC), not
// papered over with a sparse extension.
func TestPreallocateReportsDiskFull(t *testing.T) {
	f := createFile(t, filepath.Join(t.TempDir(), "full.dxpart"))
	err := preallocate(f, 1<<20, func(int, int64) error { return unix.ENOSPC })
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("preallocate error = %v, want ENOSPC", err)
	}
}

func TestIsSparseLinux(t *testing.T) {
	dir := t.TempDir()
	holey := filepath.Join(dir, "holey")
	if err := os.WriteFile(holey, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(holey, 64<<20); err != nil {
		t.Fatal(err)
	}
	if !holesSupported(t, dir) {
		t.Skip("the temporary directory's file system does not support holes")
	}
	if sparse, err := IsSparse(holey); err != nil || !sparse {
		t.Fatalf("IsSparse(truncated file) = %v, %v; want true", sparse, err)
	}
	full := filepath.Join(dir, "full")
	if err := os.WriteFile(full, pattern(64<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	if sparse, err := IsSparse(full); err != nil || sparse {
		t.Fatalf("IsSparse(written file) = %v, %v; want false", sparse, err)
	}
}

func TestDownloadsDirFromUserDirs(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, "cfg")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	dirs := "# written by xdg-user-dirs-update\nXDG_DESKTOP_DIR=\"$HOME/Desktop\"\nXDG_DOWNLOAD_DIR=\"$HOME/My \\\"Downloads\\\"\"\n"
	if err := os.WriteFile(filepath.Join(cfg, "user-dirs.dirs"), []byte(dirs), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DownloadsDir()
	if err != nil {
		t.Fatalf("DownloadsDir: %v", err)
	}
	if want := filepath.Join(home, `My "Downloads"`); got != want {
		t.Fatalf("DownloadsDir = %q, want %q", got, want)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Fatalf("DownloadsDir %q was not created (%v)", got, err)
	}
}

func TestDownloadsDirDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "relative/ignored")
	got, err := DownloadsDir()
	if err != nil {
		t.Fatalf("DownloadsDir: %v", err)
	}
	if want := filepath.Join(home, "Downloads"); got != want {
		t.Fatalf("DownloadsDir = %q, want %q", got, want)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Fatalf("DownloadsDir %q was not created (%v)", got, err)
	}
}

func TestLogDirXDGStateHome(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	got, err := LogDir()
	if err != nil {
		t.Fatalf("LogDir: %v", err)
	}
	if want := filepath.Join(state, "downex"); got != want {
		t.Fatalf("LogDir = %q, want %q", got, want)
	}
	fi, err := os.Stat(got)
	if err != nil || !fi.IsDir() {
		t.Fatalf("LogDir %q was not created (%v)", got, err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("LogDir mode = %v, want no group or other access", perm)
	}
}

func TestLogDirDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	got, err := LogDir()
	if err != nil {
		t.Fatalf("LogDir: %v", err)
	}
	if want := filepath.Join(home, ".local", "state", "downex"); got != want {
		t.Fatalf("LogDir = %q, want %q", got, want)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Fatalf("LogDir %q was not created (%v)", got, err)
	}
}
