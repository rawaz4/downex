package tools

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type zipEntry struct {
	name string
	data []byte
	mode fs.FileMode
}

// writeZip writes a zip archive of entries (buildZip) to a temporary file.
func writeZip(t *testing.T, entries ...zipEntry) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(p, buildZip(t, false, entries...), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUnzipExe(t *testing.T) {
	content := bytes.Repeat([]byte("deno"), 1000)
	archive := writeZip(t, zipEntry{name: "deno.exe", data: content})
	dst := filepath.Join(t.TempDir(), "deno.exe")
	n, sum, err := unzipExe(archive, "deno.exe", dst, 1<<20)
	if err != nil || n != int64(len(content)) || sum != sha256.Sum256(content) {
		t.Fatalf("unzipExe = %d, %x, %v", n, sum, err)
	}
	if got := readAll(t, dst); !bytes.Equal(got, content) {
		t.Error("unpacked content differs")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o755 {
			t.Errorf("mode %v, want 0755", fi.Mode().Perm())
		}
	}
	// dst must not exist.
	if _, _, err := unzipExe(archive, "deno.exe", dst, 1<<20); err == nil {
		t.Error("existing destination overwritten")
	}
}

func TestUnzipExeRefuses(t *testing.T) {
	small := []byte("small")
	cases := map[string]struct {
		entries []zipEntry
		limit   int64
	}{
		"traversal":         {[]zipEntry{{name: "../deno.exe", data: small}}, 1 << 20},
		"subfolder":         {[]zipEntry{{name: "bin/deno.exe", data: small}}, 1 << 20},
		"backslash":         {[]zipEntry{{name: `bin\deno.exe`, data: small}}, 1 << 20},
		"absolute":          {[]zipEntry{{name: "/deno.exe", data: small}}, 1 << 20},
		"other name":        {[]zipEntry{{name: "denort.exe", data: small}}, 1 << 20},
		"extra entry":       {[]zipEntry{{name: "deno.exe", data: small}, {name: "README.md", data: small}}, 1 << 20},
		"duplicate":         {[]zipEntry{{name: "deno.exe", data: small}, {name: "deno.exe", data: small}}, 1 << 20},
		"directory":         {[]zipEntry{{name: "deno.exe/", mode: fs.ModeDir | 0o755}}, 1 << 20},
		"symlink":           {[]zipEntry{{name: "deno.exe", data: []byte("/bin/sh"), mode: fs.ModeSymlink | 0o777}}, 1 << 20},
		"size bomb":         {[]zipEntry{{name: "deno.exe", data: make([]byte, 64<<10)}}, 1 << 10},
		"empty archive":     {nil, 1 << 20},
		"drive-letter name": {[]zipEntry{{name: "C:deno.exe", data: small}}, 1 << 20},
	}
	for name, c := range cases {
		archive := writeZip(t, c.entries...)
		dst := filepath.Join(t.TempDir(), "deno.exe")
		if _, _, err := unzipExe(archive, "deno.exe", dst, c.limit); !errors.Is(err, ErrArchive) {
			t.Errorf("%s: %v, want ErrArchive", name, err)
		}
		if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: destination left behind", name)
		}
	}
	// Not a zip at all.
	p := filepath.Join(t.TempDir(), "x.zip")
	_ = os.WriteFile(p, []byte("<!DOCTYPE html>"), 0o600)
	if _, _, err := unzipExe(p, "deno.exe", filepath.Join(t.TempDir(), "deno.exe"), 1<<20); !errors.Is(err, ErrArchive) {
		t.Errorf("not a zip: %v", err)
	}
}

// TestUnzipExeLyingSize: an entry whose header understates its size is caught while
// unpacking.
func TestUnzipExeLyingSize(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	data := make([]byte, 64<<10)
	h := &zip.FileHeader{Name: "deno.exe", Method: zip.Store}
	h.SetMode(0o755)
	w, err := zw.CreateRaw(&zip.FileHeader{Name: h.Name, Method: zip.Store, CompressedSize64: uint64(len(data)),
		UncompressedSize64: 100, CRC32: 0, ExternalAttrs: h.ExternalAttrs, CreatorVersion: h.CreatorVersion})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(data)
	_ = zw.Close()
	p := filepath.Join(t.TempDir(), "lying.zip")
	_ = os.WriteFile(p, buf.Bytes(), 0o600)
	if _, _, err := unzipExe(p, "deno.exe", filepath.Join(t.TempDir(), "deno.exe"), 1<<10); !errors.Is(err, ErrArchive) {
		t.Errorf("lying size: %v, want ErrArchive", err)
	}
}
