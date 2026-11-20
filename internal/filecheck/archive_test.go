package filecheck

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sync/atomic"
	"testing"

	"downex/internal/model"
)

func zipListing(t *testing.T, b []byte) (*listing, zipDir) {
	t.Helper()
	tail := b[max(0, len(b)-TailBytes):]
	d, ok := findZipDir(context.Background(), bytes.NewReader(b), int64(len(b)), tail)
	if !ok {
		t.Fatal("no ZIP directory found")
	}
	return listZip(context.Background(), bytes.NewReader(b), d), d
}

func TestZipListing(t *testing.T) {
	l, d := zipListing(t, zipBytes(t, nil, "a/", "a/readme.txt", "a/run.bat", "Foo.app/Contents/MacOS/Foo"))
	if l.entries != 4 || l.programs != 2 || l.member() != "a/run.bat" || l.incomplete || d.prepended != 0 {
		t.Fatalf("listing = %+v dir %+v", l, d)
	}
	// Prepended data (a self-extractor's program).
	// Offsets relative to the archive's own start, as a self-extractor joined by copy has.
	_, d = zipListing(t, append(peBytes(false), zipBytes(t, nil, "x.txt")...))
	if d.prepended != 1024 {
		t.Fatalf("prepended = %d", d.prepended)
	}
	// A disguised member is reported before an earlier plain program.
	l, _ = zipListing(t, zipBytes(t, nil, "setup.exe", "photo.jpg.exe"))
	if l.member() != "photo.jpg.exe" || l.programs != 2 {
		t.Fatalf("listing = %+v", l)
	}
}

func TestZipKinds(t *testing.T) {
	tests := map[string][]string{
		"ooxml": {"[Content_Types].xml", "word/document.xml"},
		"odf":   {"mimetype", "content.xml", "META-INF/manifest.xml"},
		"epub":  {"mimetype", "META-INF/container.xml"},
		"apk":   {"AndroidManifest.xml", "META-INF/MANIFEST.MF"},
		"jar":   {"META-INF/MANIFEST.MF"},
		"appx":  {"[Content_Types].xml", "AppxManifest.xml"},
		"ipa":   {"Payload/App.app/App"},
		"xpi":   {"manifest.json"},
		"nupkg": {"[Content_Types].xml", "pkg.nuspec"},
		"vsix":  {"[Content_Types].xml", "extension.vsixmanifest"},
		"whl":   {"pkg-1.0.dist-info/RECORD"},
		"":      {"docs/manifest.json", "a.txt"},
	}
	for want, names := range tests {
		l, _ := zipListing(t, zipBytes(t, nil, names...))
		if got := l.kind(); got != want {
			t.Errorf("kind(%v) = %q, want %q", names, got, want)
		}
	}
}

func TestZipCP437Names(t *testing.T) {
	if got := zipName([]byte{'c', 'a', 'f', 0x82, '.', 't', 'x', 't'}, false); got != "café.txt" {
		t.Fatalf("CP437 = %q", got)
	}
	if got := zipName([]byte("ü.txt"), false); got != "ü.txt" {
		t.Fatalf("UTF-8 without the flag = %q", got)
	}
	if len(cp437) != 128 {
		t.Fatalf("cp437 has %d runes", len(cp437))
	}
}

func TestZipMultiDisk(t *testing.T) {
	b := zipBytes(t, nil, "a.exe")
	i := bytes.LastIndex(b, []byte("PK\x05\x06"))
	binary.LittleEndian.PutUint16(b[i+4:], 1) // this disk = 1
	l, _ := zipListing(t, b)
	if !l.incomplete || l.entries != 0 {
		t.Fatalf("multi-disk listing = %+v", l)
	}
}

// countingReader fails the test once more than budget bytes were read.
type countingReader struct {
	r      io.ReaderAt
	read   atomic.Int64
	reads  atomic.Int64
	budget int64
	t      *testing.T
	after  func(n int64) // called after every read with the read count
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	if c.read.Add(int64(n)) > c.budget {
		c.t.Errorf("read %d bytes, budget %d", c.read.Load(), c.budget)
	}
	k := c.reads.Add(1)
	if c.after != nil {
		c.after(k)
	}
	return n, err
}

func manyEntries(t *testing.T, n int) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < n; i++ {
		if _, err := zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("%d", i), Method: zip.Store}); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestZipEntryBoundAndZip64(t *testing.T) {
	b := manyEntries(t, 70000) // more than 65,535 entries: a Zip64 end record
	cr := &countingReader{r: bytes.NewReader(b), budget: HeadBytes + TailBytes + maxZipDirBytes + 64<<10, t: t}
	tail := b[len(b)-TailBytes:]
	d, ok := findZipDir(context.Background(), cr, int64(len(b)), tail)
	if !ok || d.entries != 70000 {
		t.Fatalf("Zip64 directory = %+v %v", d, ok)
	}
	l := listZip(context.Background(), cr, d)
	if !l.incomplete || l.entries != maxZipEntries {
		t.Fatalf("listing = entries %d incomplete %v", l.entries, l.incomplete)
	}
	fc, err := CheckFile(context.Background(), cr, int64(len(b)), "movie.mp4", false, "")
	if err != nil || fc == nil || fc.Reason != "archive_disguised" || !fc.ListingIncomplete {
		t.Fatalf("CheckFile = %+v, %v", fc, err)
	}
	if Detail(fc) != "Downex could not list every file inside it." {
		t.Fatalf("Detail = %q", Detail(fc))
	}
}

func TestCheckStopsWhenCancelled(t *testing.T) {
	b := manyEntries(t, 20000)
	ctx, cancel := context.WithCancel(context.Background())
	var cancelledAt atomic.Int64
	cr := &countingReader{r: bytes.NewReader(b), budget: 1 << 30, t: t}
	cr.after = func(n int64) {
		if n == 4 { // head, tail, directory check, first directory block
			cancel()
			cancelledAt.Store(n)
		}
	}
	fc, err := CheckFile(ctx, cr, int64(len(b)), "movie.mp4", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if extra := cr.reads.Load() - cancelledAt.Load(); extra > 1 {
		t.Fatalf("%d reads after the cancel, want at most 1", extra)
	}
	if fc == nil || !fc.ListingIncomplete || fc.Level != model.FileCheckCaution {
		t.Fatalf("result = %+v", fc)
	}
}

func TestRAR5Listing(t *testing.T) {
	b := rar5Bytes(false, "a.txt", "b/run.exe")
	l := listRAR5(context.Background(), bytes.NewReader(b), int64(len(b)))
	if l.incomplete || l.entries != 2 || l.programs != 1 || l.member() != "b/run.exe" {
		t.Fatalf("listing = %+v", l)
	}
	enc := rar5Bytes(true, "a.exe")
	l = listRAR5(context.Background(), bytes.NewReader(enc), int64(len(enc)))
	if !l.incomplete || l.entries != 0 {
		t.Fatalf("encrypted listing = %+v", l)
	}
	bad := bytes.Clone(b)
	bad[8] ^= 0xFF // the first header's CRC
	l = listRAR5(context.Background(), bytes.NewReader(bad), int64(len(bad)))
	if !l.incomplete {
		t.Fatalf("bad CRC listing = %+v", l)
	}
	fc := checkBytes(t, "movie.mp4", rar5Bytes(false, "movie.mp4.exe"), "")
	if fc == nil || fc.Reason != "archive_with_program" || fc.Member != "movie.mp4.exe" {
		t.Fatalf("rar5 as mp4 = %+v", fc)
	}
}

func TestCABListing(t *testing.T) {
	b := cabBytes("driver.inf", "setup.exe")
	l := listCAB(context.Background(), bytes.NewReader(b), int64(len(b)), b)
	if l.incomplete || l.entries != 2 || l.programs != 1 {
		t.Fatalf("listing = %+v", l)
	}
	cut := b[:len(b)-3]
	l = listCAB(context.Background(), bytes.NewReader(cut), int64(len(cut)), cut)
	if !l.incomplete || l.entries != 1 {
		t.Fatalf("cut listing = %+v", l)
	}
}

func TestMemberNameIsClipped(t *testing.T) {
	long := string(bytes.Repeat([]byte("é"), 200)) + ".exe"
	fc := checkBytes(t, "movie.mp4", zipBytes(t, nil, long), "")
	if fc == nil || len(fc.Member) > maxMemberBytes || fc.Member == "" {
		t.Fatalf("member %d bytes", len(fc.Member))
	}
}

func FuzzZipDirectory(f *testing.F) {
	f.Add(zipBytes(f, nil, "a.exe", "b/c.txt"))
	f.Add(zipBytes(f, []byte("%PDF-1.4"), "x.scr"))
	f.Fuzz(func(t *testing.T, b []byte) {
		cr := &countingReader{r: bytes.NewReader(b), budget: int64(len(b))*4 + HeadBytes + TailBytes + 1<<20, t: t}
		_, _ = CheckFile(context.Background(), cr, int64(len(b)), "a.mp4", false, "")
	})
}

func FuzzRAR5(f *testing.F) {
	f.Add(rar5Bytes(false, "a.exe"))
	f.Add(rar5Bytes(true, "a.exe"))
	f.Fuzz(func(t *testing.T, b []byte) {
		b = append([]byte("Rar!\x1a\x07\x01\x00"), b...)
		_ = listRAR5(context.Background(), bytes.NewReader(b), int64(len(b)))
	})
}

func FuzzCheckFile(f *testing.F) {
	f.Add(peBytes(false), "movie.mp4")
	f.Add(cabBytes("a.exe"), "a.cab")
	f.Add([]byte("<html>"), "a.zip")
	f.Fuzz(func(t *testing.T, b []byte, name string) {
		fc, err := CheckFile(context.Background(), bytes.NewReader(b), int64(len(b)), name, false, "video/mp4")
		if err == nil && fc != nil && Summary(fc, name) == "" {
			t.Fatalf("result without a summary: %+v", fc)
		}
	})
}
