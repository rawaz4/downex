package webstorage

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"downex/ui/internal/research"
)

// ---- a small LevelDB writer for the tests (doc/log_format.md, doc/table_format.md) ----------

func mask(c uint32) uint32 { return (c>>15 | c<<17) + 0xa282ead8 }

// logFile frames records in 32 KiB blocks, split into FIRST/MIDDLE/LAST where needed.
func logFile(records ...[]byte) []byte {
	var out []byte
	for _, rec := range records {
		first := true
		for {
			left := blockSize - len(out)%blockSize
			if left < headerSize {
				out = append(out, make([]byte, left)...)
				continue
			}
			n := min(len(rec), left-headerSize)
			typ := byte(1)
			switch {
			case first && n == len(rec):
				typ = 1
			case first:
				typ = 2
			case n == len(rec):
				typ = 4
			default:
				typ = 3
			}
			h := make([]byte, headerSize)
			binary.LittleEndian.PutUint16(h[4:6], uint16(n))
			h[6] = typ
			binary.LittleEndian.PutUint32(h[0:4], mask(crc32.Update(crc32.Checksum([]byte{typ}, castagnoli), castagnoli, rec[:n])))
			out = append(append(out, h...), rec[:n]...)
			rec = rec[n:]
			first = false
			if len(rec) == 0 {
				break
			}
		}
	}
	return out
}

type op struct {
	key, value []byte
	del        bool
}

func batch(seq uint64, ops ...op) []byte {
	b := make([]byte, 12)
	binary.LittleEndian.PutUint64(b, seq)
	binary.LittleEndian.PutUint32(b[8:], uint32(len(ops)))
	for _, o := range ops {
		if o.del {
			b = append(b, 0)
			b = binary.AppendUvarint(b, uint64(len(o.key)))
			b = append(b, o.key...)
			continue
		}
		b = append(b, 1)
		b = binary.AppendUvarint(b, uint64(len(o.key)))
		b = append(b, o.key...)
		b = binary.AppendUvarint(b, uint64(len(o.value)))
		b = append(b, o.value...)
	}
	return b
}

// snappyLiteral encodes src as Snappy literals (a valid stream without back-references).
func snappyLiteral(src []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(len(src)))
	for len(src) > 0 {
		n := min(len(src), 1<<16)
		if n <= 60 {
			out = append(out, byte(n-1)<<2)
		} else {
			out = append(out, 61<<2, byte(n-1), byte((n-1)>>8))
		}
		out = append(out, src[:n]...)
		src = src[n:]
	}
	return out
}

type entry struct {
	user []byte
	seq  uint64
	del  bool
	v    []byte
}

func blockOf(entries [][2][]byte) []byte {
	var b []byte
	for _, e := range entries {
		b = binary.AppendUvarint(b, 0)
		b = binary.AppendUvarint(b, uint64(len(e[0])))
		b = binary.AppendUvarint(b, uint64(len(e[1])))
		b = append(append(b, e[0]...), e[1]...)
	}
	b = binary.LittleEndian.AppendUint32(b, 0) // one restart at 0
	return binary.LittleEndian.AppendUint32(b, 1)
}

// table writes one data block (compressed with Snappy when snappy) and its index.
func table(snappy bool, entries ...entry) []byte {
	var kv [][2][]byte
	for _, e := range entries {
		ikey := append([]byte(nil), e.user...)
		typ := uint64(1)
		if e.del {
			typ = 0
		}
		ikey = binary.LittleEndian.AppendUint64(ikey, e.seq<<8|typ)
		kv = append(kv, [2][]byte{ikey, e.v})
	}
	var out []byte
	write := func(contents []byte, compress bool) (off, size uint64) {
		typ := byte(0)
		if compress {
			contents, typ = snappyLiteral(contents), 1
		}
		off, size = uint64(len(out)), uint64(len(contents))
		out = append(out, contents...)
		crc := crc32.Update(crc32.Checksum(contents, castagnoli), castagnoli, []byte{typ})
		out = append(out, typ)
		out = binary.LittleEndian.AppendUint32(out, mask(crc))
		return off, size
	}
	off, size := write(blockOf(kv), snappy)
	h := binary.AppendUvarint(binary.AppendUvarint(nil, off), size)
	ioff, isize := write(blockOf([][2][]byte{{kv[len(kv)-1][0], h}}), false)
	moff, msize := write(blockOf(nil), false)
	footer := binary.AppendUvarint(nil, moff)
	footer = binary.AppendUvarint(footer, msize)
	footer = binary.AppendUvarint(footer, ioff)
	footer = binary.AppendUvarint(footer, isize)
	footer = append(footer, make([]byte, 40-len(footer))...)
	footer = binary.LittleEndian.AppendUint64(footer, tableMagic)
	return append(out, footer...)
}

func manifest(logNum uint64, tables ...uint64) []byte {
	var e []byte
	e = binary.AppendUvarint(e, 1)
	e = binary.AppendUvarint(e, uint64(len("leveldb.BytewiseComparator")))
	e = append(e, "leveldb.BytewiseComparator"...)
	e = binary.AppendUvarint(binary.AppendUvarint(e, 2), logNum)
	for _, n := range tables {
		e = binary.AppendUvarint(e, 7)
		e = binary.AppendUvarint(e, 0)
		e = binary.AppendUvarint(e, n)
		e = binary.AppendUvarint(e, 100)
		e = binary.AppendUvarint(binary.AppendUvarint(e, 1), 'a')
		e = binary.AppendUvarint(binary.AppendUvarint(e, 1), 'z')
	}
	return logFile(e)
}

// lsKey is Chromium's key of a localStorage item: "_" origin NUL, then Latin-1 (1) or
// UTF-16LE (0).
func lsKey(origin, key string) []byte { return append([]byte("_"+origin+"\x00"), latin1(key)...) }

func latin1(s string) []byte { return append([]byte{1}, s...) }

func utf16le(s string) []byte {
	out := []byte{0}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

func writeFiles(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// ---- the tests -----------------------------------------------------------------------------

func TestReadTakesTheNewestRecordOfEachKey(t *testing.T) {
	o := WailsOrigin
	dir := writeFiles(t, map[string][]byte{
		"CURRENT":         []byte("MANIFEST-000002\n"),
		"MANIFEST-000002": manifest(4, 3, 7),
		// an obsolete table the MANIFEST no longer lists: never read
		"000001.ldb": table(false, entry{user: lsKey(o, "downex.theme"), seq: 99, v: latin1("sepia")}),
		"000003.ldb": table(false,
			entry{user: lsKey(o, "downex.density"), seq: 1, v: latin1("compact")},
			entry{user: lsKey(o, "downex.theme"), seq: 2, v: latin1("dark")},
		),
		"000007.ldb": table(true,
			entry{user: lsKey(o, "downex.columnWidths"), seq: 3, v: latin1(`{"size":82}`)},
			entry{user: lsKey(o, "downex.existingFile"), seq: 4, v: latin1("replace")},
		),
		// a log older than the MANIFEST's: already in the tables
		"000002.log": logFile(batch(50, op{key: lsKey(o, "downex.density"), value: latin1("stale")})),
		"000004.log": logFile(
			batch(5, op{key: lsKey(o, "downex.theme"), value: latin1("light")}, op{key: lsKey(o, "downex.existingFile"), del: true}),
			batch(7, op{key: lsKey(o, "downex.test"), value: utf16le("سلێمانی ✓ 😀")}),
			batch(8, op{key: lsKey("http://other.example", "downex.density"), value: latin1("compact")}),
		),
	})
	got, err := Read(dir, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"downex.density": "compact", "downex.theme": "light", "downex.columnWidths": `{"size":82}`, "downex.test": "سلێمانی ✓ 😀"}
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q, want %q", k, got[k], v)
		}
	}
	only, _ := Read(dir, o, []string{"downex.theme", "downex.missing"})
	if len(only) != 1 || only["downex.theme"] != "light" {
		t.Fatal(only)
	}
}

func TestReadSkipsWhatItCannotRead(t *testing.T) {
	o := WailsOrigin
	big := strings.Repeat("x", 70_000)
	recs := [][]byte{
		batch(1, op{key: lsKey(o, "downex.density"), value: latin1("compact")}),
		batch(2, op{key: lsKey(o, "downex.theme"), value: latin1("dark")}),
		batch(3, op{key: lsKey(o, "big"), value: latin1(big)}), // spans three blocks
	}
	log := logFile(recs...)
	log[len(logFile(recs[:2]...))-1] ^= 0xff // the second record's checksum no longer matches
	dir := writeFiles(t, map[string][]byte{
		// no CURRENT: every table and log is read
		"000005.log": log,
		"000006.log": logFile(batch(4, op{key: lsKey(o, "downex.progress.details"), value: latin1("1")}))[:20], // cut off
		"000007.ldb": []byte("not a table"),
		"000008.ldb": table(true, entry{user: lsKey(o, "downex.detailsCollapsed"), seq: 9, v: latin1("1")})[:30],
		"README":     []byte("ignored"),
	})
	got, err := Read(dir, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["downex.density"] != "compact" || got["big"] != big {
		t.Fatal(len(got), got["downex.density"])
	}
	if _, err := Read(filepath.Join(dir, "missing"), o, nil); err == nil {
		t.Fatal("a missing folder read")
	}
	if _, err := Read(t.TempDir(), o, nil); err != ErrNoStorage {
		t.Fatal(err)
	}
}

func TestSnappyDecodesLiteralsAndCopies(t *testing.T) {
	// "abcd" then a 1-byte-offset copy of 8 from 4 back (overlapping) and a 2-byte one
	src := []byte{17, 3 << 2, 'a', 'b', 'c', 'd', 1 | (8-4)<<2, 4, 2 | 4<<2, 12, 0}
	got, err := snappyDecode(src)
	if err != nil || string(got) != "abcdabcdabcdabcda" {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range [][]byte{{5, 0}, {4, 1 | 0<<2, 9}, {0x80}, {2, 4 << 2, 'a'}} {
		if _, err := snappyDecode(bad); err == nil {
			t.Fatalf("%v decoded", bad)
		}
	}
	long := bytes.Repeat([]byte("downex "), 20_000)
	if got, err := snappyDecode(snappyLiteral(long)); err != nil || !bytes.Equal(got, long) {
		t.Fatal(err)
	}
}

func TestDecodeReadsChromiumsStringFormats(t *testing.T) {
	for _, c := range []struct {
		in   []byte
		want string
		ok   bool
	}{
		{latin1("compact"), "compact", true},
		{append([]byte{1}, 0xe9), "é", true},
		{utf16le("😀"), "😀", true},
		{[]byte{0, 1}, "", false},
		{[]byte{2, 'x'}, "", false},
		{nil, "", false},
	} {
		if got, ok := decode(c.in); got != c.want || ok != c.ok {
			t.Errorf("%v: %q %v", c.in, got, ok)
		}
	}
}

// DefaultCopies is where the copies of real WebView2 localStorage folders are: in the
// research folder beside the repository ($DOWNEX_RESEARCH names another), by the step-1 references
// (as formattest.DefaultGoldens): made in a
// scratch profile by a WebView2 that served http://wails.localhost, never the owner's, and
// kept out of the repo as binary files. $DOWNEX_WEBSTORAGE_COPIES names another folder.
var DefaultCopies = research.Beside(research.NativeUI, "webstorage-copies")

// TestReadsRealWebView2Folders reads two copies of one profile's folder: after a page
// stored the owner-shaped values (they were compacted into a Snappy table), and after a
// second page changed some and removed others (those changes are in the write-ahead log).
func TestReadsRealWebView2Folders(t *testing.T) {
	root := os.Getenv("DOWNEX_WEBSTORAGE_COPIES")
	if root == "" {
		root = DefaultCopies
	}
	if _, err := os.Stat(filepath.Join(root, "a", "CURRENT")); err != nil {
		t.Skipf("SKIPPED: no copies of real WebView2 folders at %s (set $DOWNEX_WEBSTORAGE_COPIES to a folder holding a and b); the synthetic tests still cover the reader", root)
	}
	common := map[string]string{
		"downex.columnWidths":     `{"size":82}`,
		"downex.existingFile":     "replace",
		"downex.progress.details": "1",
		"downex.test.unicode":     "سلێمانی ✓ 😀",
	}
	for _, c := range []struct {
		dir  string
		more map[string]string
		gone []string
	}{
		{"a", map[string]string{"downex.density": "compact", "downex.detailsCollapsed": "1", "downex.theme": "dark"}, nil},
		{"b", map[string]string{"downex.density": "comfortable", "downex.theme": "light"}, []string{"downex.detailsCollapsed", "filler.0", "filler.1"}},
	} {
		got, err := Read(filepath.Join(root, c.dir), WailsOrigin, nil)
		if err != nil {
			t.Fatal(c.dir, err)
		}
		for k, v := range common {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", c.dir, k, got[k], v)
			}
		}
		for k, v := range c.more {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", c.dir, k, got[k], v)
			}
		}
		for _, k := range c.gone {
			if _, ok := got[k]; ok {
				t.Errorf("%s: %s still there", c.dir, k)
			}
		}
	}
}

// A block that claims more than its input can decode to is refused before anything is
// allocated for it; the densest real block (a run of copies) still decodes.
func TestSnappyRefusesALengthItsInputCannotHold(t *testing.T) {
	claim := binary.AppendUvarint(nil, maxFile) // 64 MiB from 7 bytes
	bad := append(claim, 0, 'a', 2|63<<2, 1, 0)
	allocs := testing.AllocsPerRun(10, func() {
		if _, err := snappyDecode(bad); err == nil {
			t.Fatal("a 64 MiB claim from 7 bytes decoded")
		}
	})
	if allocs != 0 {
		t.Fatalf("%v allocations for a refused block", allocs)
	}
	// "a" then 1,000 copies of 64 bytes from 1 back: 64,001 bytes from 3,002 (21.3 a byte)
	run := []byte{0, 'a'}
	for range 1000 {
		run = append(run, 2|63<<2, 1, 0)
	}
	src := append(binary.AppendUvarint(nil, 64001), run...)
	got, err := snappyDecode(src)
	if err != nil || len(got) != 64001 || !bytes.Equal(got, bytes.Repeat([]byte{'a'}, 64001)) {
		t.Fatal(len(got), err)
	}
}
