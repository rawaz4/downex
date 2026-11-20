package filecheck

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
)

// Hand-built fixtures: the smallest bytes each signature of §9.2 needs. No binaries
// are committed.

func peBytes(dll bool) []byte {
	b := make([]byte, 1024)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3C:], 0x80)
	copy(b[0x80:], "PE\x00\x00")
	if dll {
		binary.LittleEndian.PutUint16(b[0x80+22:], 0x2002)
	} else {
		binary.LittleEndian.PutUint16(b[0x80+22:], 0x0102)
	}
	return b
}

func cfbBytes(shift uint16, clsid []byte) []byte {
	sector := 1 << shift
	b := make([]byte, 2*sector)
	copy(b, cfbMagic)
	binary.LittleEndian.PutUint16(b[0x1E:], shift)
	binary.LittleEndian.PutUint32(b[0x30:], 0) // first directory sector
	copy(b[sector+0x50:], clsid)
	return b
}

func lnkBytes() []byte {
	b := make([]byte, 76)
	binary.LittleEndian.PutUint32(b, 0x4C)
	copy(b[4:], lnkCLSID)
	return b
}

func isoBytes() []byte {
	b := make([]byte, 40000)
	copy(b, "\x33\xed\x90\x90") // boot code of a hybrid image
	b[510], b[511] = 0x55, 0xAA
	copy(b[32769:], "CD001")
	return b
}

func udfBytes() []byte {
	b := make([]byte, 40000)
	copy(b[32769:], "BEA01")
	copy(b[32769+2048:], "NSR03")
	return b
}

func tsBytes() []byte {
	b := make([]byte, 188*4)
	for i := 0; i < 4; i++ {
		b[i*188] = 0x47
	}
	return b
}

func repeatText(s string, n int) []byte {
	return bytes.Repeat([]byte(s), n)
}

func zipBytes(t testing.TB, prefix []byte, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write(prefix)
	zw := zip.NewWriter(&buf)
	zw.SetOffset(int64(len(prefix)))
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("x"))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// rar5 builders (RARLAB "RAR 5.0 archive format").

func vintBytes(v uint64) []byte {
	var out []byte
	for {
		c := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			out = append(out, c|0x80)
			continue
		}
		return append(out, c)
	}
}

func rarBlock(data []byte) []byte {
	sz := vintBytes(uint64(len(data)))
	body := append(sz, data...)
	crc := make([]byte, 4)
	binary.LittleEndian.PutUint32(crc, crc32.ChecksumIEEE(body))
	return append(crc, body...)
}

func rarFile(name string, dir bool) []byte {
	var d []byte
	d = append(d, vintBytes(2)...) // file header
	d = append(d, vintBytes(0)...) // header flags
	ff := uint64(0)
	if dir {
		ff = 1
	}
	d = append(d, vintBytes(ff)...)
	d = append(d, vintBytes(0)...) // unpacked size
	d = append(d, vintBytes(0)...) // attributes
	d = append(d, vintBytes(0)...) // compression
	d = append(d, vintBytes(0)...) // host OS
	d = append(d, vintBytes(uint64(len(name)))...)
	d = append(d, name...)
	return rarBlock(d)
}

func rar5Bytes(encrypted bool, names ...string) []byte {
	b := []byte("Rar!\x1a\x07\x01\x00")
	if encrypted {
		b = append(b, rarBlock([]byte{4, 0, 0, 0})...)
	}
	b = append(b, rarBlock([]byte{1, 0, 0})...) // main archive header
	for _, n := range names {
		b = append(b, rarFile(n, false)...)
	}
	return append(b, rarBlock([]byte{5, 0, 0})...)
}

// cabBytes builds a cabinet with one folder and the named files ([MS-CAB]).
func cabBytes(names ...string) []byte {
	h := make([]byte, 36+8)
	copy(h, "MSCF")
	binary.LittleEndian.PutUint32(h[16:], 44) // coffFiles
	h[24], h[25] = 3, 1
	binary.LittleEndian.PutUint16(h[26:], 1)
	binary.LittleEndian.PutUint16(h[28:], uint16(len(names)))
	for _, n := range names {
		f := make([]byte, 16)
		binary.LittleEndian.PutUint16(f[14:], 0x80)
		h = append(h, f...)
		h = append(h, n...)
		h = append(h, 0)
	}
	binary.LittleEndian.PutUint32(h[8:], uint32(len(h)))
	return h
}
