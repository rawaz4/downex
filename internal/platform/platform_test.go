package platform

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// createFile creates path for reading and writing and closes it when the test ends.
func createFile(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// pattern returns n bytes that differ from zero and from each other's neighbours.
func pattern(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte(i%251) + 1
	}
	return p
}

func fileSize(t *testing.T, f *os.File) int64 {
	t.Helper()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return fi.Size()
}

func readAt(t *testing.T, f *os.File, off int64, n int) []byte {
	t.Helper()
	got := make([]byte, n)
	if _, err := f.ReadAt(got, off); err != nil {
		t.Fatalf("read %d bytes at %d: %v", n, off, err)
	}
	return got
}

// TestPreallocateKeepsData: growing, re-preallocating and shrinking keep every byte
// below the new size, and the new range reads as zeros.
func TestPreallocateKeepsData(t *testing.T) {
	f := createFile(t, filepath.Join(t.TempDir(), "keep.dxpart"))
	data := pattern(1 << 20)
	if _, err := f.WriteAt(data, 0); err != nil {
		t.Fatalf("write: %v", err)
	}
	const grown = 8 << 20
	for range 2 { // the second call finds the file already sized (and sparse)
		if err := Preallocate(f, grown); err != nil {
			t.Fatalf("Preallocate(%d): %v", grown, err)
		}
		if got := fileSize(t, f); got != grown {
			t.Fatalf("size = %d, want %d", got, grown)
		}
		if got := readAt(t, f, 0, len(data)); !bytes.Equal(got, data) {
			t.Fatal("existing data changed by Preallocate")
		}
		if got := readAt(t, f, int64(len(data)), 4096); !bytes.Equal(got, make([]byte, 4096)) {
			t.Fatal("preallocated range does not read as zeros")
		}
	}
	const shrunk = 512 << 10
	if err := Preallocate(f, shrunk); err != nil {
		t.Fatalf("Preallocate(%d): %v", shrunk, err)
	}
	if got := fileSize(t, f); got != shrunk {
		t.Fatalf("size = %d, want %d", got, shrunk)
	}
	if got := readAt(t, f, 0, shrunk); !bytes.Equal(got, data[:shrunk]) {
		t.Fatal("data within the new size changed by shrinking")
	}
}

// TestPreallocateTailWrite: a write at the very end of a preallocated file reads back,
// and the size does not change.
func TestPreallocateTailWrite(t *testing.T) {
	f := createFile(t, filepath.Join(t.TempDir(), "tail.dxpart"))
	const size = 64 << 20
	if err := Preallocate(f, size); err != nil {
		t.Fatalf("Preallocate: %v", err)
	}
	tail := pattern(4096)
	if _, err := f.WriteAt(tail, size-int64(len(tail))); err != nil {
		t.Fatalf("write tail: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := readAt(t, f, size-int64(len(tail)), len(tail)); !bytes.Equal(got, tail) {
		t.Fatal("tail does not read back")
	}
	if got := fileSize(t, f); got != size {
		t.Fatalf("size = %d, want %d", got, size)
	}
}

func TestPreallocateZeroAndNegative(t *testing.T) {
	f := createFile(t, filepath.Join(t.TempDir(), "zero.dxpart"))
	if _, err := f.WriteAt([]byte("data"), 0); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := Preallocate(f, -1); err == nil {
		t.Fatal("Preallocate(-1) succeeded")
	}
	if got := fileSize(t, f); got != 4 {
		t.Fatalf("size after rejected call = %d, want 4", got)
	}
	if err := Preallocate(f, 0); err != nil {
		t.Fatalf("Preallocate(0): %v", err)
	}
	if got := fileSize(t, f); got != 0 {
		t.Fatalf("size = %d, want 0", got)
	}
}

func TestPreallocateReadOnlyFileFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ro.dxpart")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := Preallocate(f, 1<<20); err == nil {
		t.Fatal("Preallocate on a read-only file succeeded")
	}
	if got := fileSize(t, f); got != 4 {
		t.Fatalf("size = %d, want 4", got)
	}
}

func TestIsSparseMissingFile(t *testing.T) {
	_, err := IsSparse(filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("IsSparse(missing) error = %v, want fs.ErrNotExist", err)
	}
}

// TestDownloadsDir: the user's Downloads folder is an existing absolute directory.
func TestDownloadsDir(t *testing.T) {
	dir, err := DownloadsDir()
	if err != nil {
		t.Fatalf("DownloadsDir: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Fatalf("DownloadsDir = %q, not absolute", dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("DownloadsDir = %q: not an existing directory (%v)", dir, err)
	}
}

func TestEnsureDirRejectsRelativeAndFiles(t *testing.T) {
	if _, err := ensureDir("relative/dir", 0o700); err == nil {
		t.Fatal("ensureDir accepted a relative path")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureDir(file, 0o700); err == nil {
		t.Fatal("ensureDir accepted a regular file")
	}
	if _, err := ensureDir(filepath.Join(file, "sub"), 0o700); err == nil {
		t.Fatal("ensureDir created a directory below a regular file")
	}
}
