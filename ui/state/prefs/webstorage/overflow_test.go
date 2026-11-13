package webstorage

import (
	"encoding/binary"
	"hash/crc32"
	"testing"
)

// footerTable is the stage-1 re-check's reproduction (s1-recheck fuzz\footer_test.go.txt):
// 64 bytes, then a footer with the table magic whose index handle is (offset, size). With
// offset 2^64-10 and size 7, end+5 wrapped around, passed the bound, and
// data[h.offset:end] panicked in applyTable and readDB.
func footerTable(offset, size uint64) []byte {
	data := make([]byte, 64)
	foot := make([]byte, 0, 40)
	foot = binary.AppendUvarint(foot, 0)
	foot = binary.AppendUvarint(foot, 0)
	foot = binary.AppendUvarint(foot, offset)
	foot = binary.AppendUvarint(foot, size)
	for len(foot) < 40 {
		foot = append(foot, 0)
	}
	data = append(data, foot...)
	return binary.LittleEndian.AppendUint64(data, tableMagic)
}

// noPanic runs fn and fails the test with what it panicked with. The internal readers have
// no recover of their own, so these tests see a panic that Read would swallow.
func noPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s panicked: %v", what, r)
		}
	}()
	fn()
}

// TestFooterOverflow (the re-check's TestFooterOverflow, with more handles near the limits
// of uint64 and of a 32-bit int): every one is refused by block and by applyTable.
func TestFooterOverflow(t *testing.T) {
	for _, c := range []struct {
		name         string
		offset, size uint64
	}{
		{"offset 2^64-10 size 7 (the re-check's)", ^uint64(0) - 9, 7},
		{"offset 2^64-1 size 1", ^uint64(0), 1},
		{"offset 0 size 2^64-1", 0, ^uint64(0)},
		{"offset 1 size 2^64-3", 1, ^uint64(0) - 2},
		{"offset 2^63 size 2^63", 1 << 63, 1 << 63},
		{"offset len-5 size 1", 64 + 48 - 5, 1},
		{"offset 2^32 size 0", 1 << 32, 0},
		{"offset 0 size 2^32+8", 0, 1<<32 + 8},
		{"offset 2^31 size 2^31", 1 << 31, 1 << 31},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := footerTable(c.offset, c.size)
			noPanic(t, "block", func() {
				if _, err := block(data, handle{c.offset, c.size}); err == nil {
					t.Fatal("an out-of-range handle was read")
				}
			})
			noPanic(t, "applyTable", func() {
				d := db{}
				d.applyTable(data)
				if len(d) != 0 {
					t.Fatal(d)
				}
			})
		})
	}
}

// TestFooterOverflowThroughRead (the re-check's TestFooterOverflowThroughRead): the same
// table in a folder beside a good log, read by readDB (no recover) and by Read (what
// prefs.Open's first-start import calls). The log's value is still read.
func TestFooterOverflowThroughRead(t *testing.T) {
	dir := writeFiles(t, map[string][]byte{
		"000005.ldb": footerTable(^uint64(0)-9, 7),
		"000006.log": logFile(batch(1, op{key: lsKey(WailsOrigin, "downex.theme"), value: latin1("dark")})),
	})
	noPanic(t, "readDB", func() {
		d, err := readDB(dir)
		if err != nil || len(d) != 1 {
			t.Fatal(d, err)
		}
	})
	got, err := Read(dir, WailsOrigin, []string{"downex.theme"})
	if err != nil || got["downex.theme"] != "dark" {
		t.Fatal(got, err)
	}
	// alone, the table holds nothing
	only := writeFiles(t, map[string][]byte{"000001.ldb": footerTable(^uint64(0)-9, 7)})
	if _, err := Read(only, WailsOrigin, nil); err != ErrNoStorage {
		t.Fatal(err)
	}
}

// An index whose entry names a data block past the end of the file (or of any size) is
// skipped without reading.
func TestIndexHandlesOutOfRange(t *testing.T) {
	for _, h := range []handle{{^uint64(0) - 3, 2}, {0, ^uint64(0)}, {1 << 40, 1 << 40}, {3, ^uint64(0) - 2}} {
		hv := binary.AppendUvarint(binary.AppendUvarint(nil, h.offset), h.size)
		ib := blockOf([][2][]byte{{[]byte("k"), hv}})
		data := append([]byte(nil), ib...)
		data = append(data, 0)
		data = binary.LittleEndian.AppendUint32(data, mask(crc32.Update(crc32.Checksum(ib, castagnoli), castagnoli, []byte{0})))
		data = append(data, footerTable(0, uint64(len(ib)))[64:]...)
		noPanic(t, "applyTable", func() {
			d := db{}
			d.applyTable(data)
			if len(d) != 0 {
				t.Fatal(d)
			}
		})
	}
}

// A restart count whose array would not fit the block (int(restarts)*4 wraps on a 32-bit
// int) reads nothing.
func TestBlockRestartCountOverflow(t *testing.T) {
	for _, n := range []uint32{1 << 30, 1<<30 + 1, 1 << 31, ^uint32(0), 3} {
		b := binary.LittleEndian.AppendUint32(make([]byte, 8), n)
		noPanic(t, "blockEntries", func() {
			blockEntries(b, func(_, _ []byte) { t.Fatal("an entry from a bad block") })
		})
	}
}

// Snappy literal and copy lengths and offsets that do not fit what is left are refused: a
// 4-byte literal length of 2^32-1 is negative in a 32-bit int, as is a 4-byte offset of
// 2^31 or more.
func TestSnappyLengthOverflow(t *testing.T) {
	for _, c := range []struct {
		name string
		src  []byte
	}{
		{"literal 63 with 4 bytes ffffffff", []byte{10, 63 << 2, 0xff, 0xff, 0xff, 0xff, 'a'}},
		{"literal 63 with 4 bytes 7fffffff", []byte{10, 63 << 2, 0xff, 0xff, 0xff, 0x7f, 'a'}},
		{"literal 63 with 4 bytes fffffffe", []byte{10, 63 << 2, 0xfe, 0xff, 0xff, 0xff, 'a'}},
		// after 3 bytes, a 32-bit int length of -1 passed both old checks (3-1 <= 10)
		{"literal 63 fffffffe after a literal", []byte{10, 2 << 2, 'a', 'b', 'c', 63 << 2, 0xfe, 0xff, 0xff, 0xff, 'x'}},
		{"literal 63 fffffffd after a literal", []byte{10, 2 << 2, 'a', 'b', 'c', 63 << 2, 0xfd, 0xff, 0xff, 0xff, 'x', 'y'}},
		{"literal 62 with 3 bytes ffffff", []byte{10, 62 << 2, 0xff, 0xff, 0xff, 'a'}},
		{"literal 63 short of its length bytes", []byte{10, 63 << 2, 0xff, 0xff}},
		{"literal longer than the claim", []byte{1, 1 << 2, 'a', 'b'}},
		{"copy-4 offset ffffffff", []byte{10, 0, 'a', 3 | 3<<2, 0xff, 0xff, 0xff, 0xff}},
		{"copy-4 offset 80000000", []byte{10, 0, 'a', 3 | 3<<2, 0, 0, 0, 0x80}},
		{"copy past the claim", []byte{3, 0, 'a', 2 | 63<<2, 1, 0}},
		{"copy offset 0", []byte{5, 0, 'a', 1, 0}},
		{"copy before anything", []byte{5, 1, 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			noPanic(t, "snappyDecode", func() {
				if got, err := snappyDecode(c.src); err == nil {
					t.Fatalf("decoded %q", got)
				}
			})
		})
	}
}
