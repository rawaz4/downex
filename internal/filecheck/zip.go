package filecheck

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"strings"
	"unicode/utf8"
)

// ZIP directory bounds (§9.3).
const (
	maxZipDirBytes = 32 << 20
	maxZipEntries  = 65536
)

// ctxReader reads a section of an io.ReaderAt and checks ctx before every read.
type ctxReader struct {
	ctx context.Context
	r   io.ReaderAt
	off int64
	end int64
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	if c.off >= c.end {
		return 0, io.EOF
	}
	if rest := c.end - c.off; int64(len(p)) > rest {
		p = p[:rest]
	}
	n, err := c.r.ReadAt(p, c.off)
	c.off += int64(n)
	if err == io.EOF && n > 0 {
		err = nil
	}
	return n, err
}

func newSeq(ctx context.Context, r io.ReaderAt, off, end int64) *bufio.Reader {
	return bufio.NewReaderSize(&ctxReader{ctx: ctx, r: r, off: off, end: end}, 32<<10)
}

// zipDir is where a ZIP's central directory is (PKWARE APPNOTE §4.3.16, §4.3.14).
type zipDir struct {
	start, size int64
	entries     uint64
	prepended   int64 // bytes before the archive (a self-extractor's program)
	multiDisk   bool
}

// findZipDir finds the end of central directory record in tail, the last bytes of a
// file of size bytes, searching backwards for one whose comment fits, and checks that
// the directory it points to starts with a directory entry.
func findZipDir(ctx context.Context, r io.ReaderAt, size int64, tail []byte) (zipDir, bool) {
	tailOff := size - int64(len(tail))
	for i := len(tail) - 22; i >= 0; i-- {
		if tail[i] != 'P' || !at(tail, i, "PK\x05\x06") {
			continue
		}
		if i+22+int(binary.LittleEndian.Uint16(tail[i+20:])) > len(tail) {
			continue
		}
		if d, ok := zipDirAt(ctx, r, size, tail, tailOff, i); ok {
			return d, true
		}
	}
	return zipDir{}, false
}

func zipDirAt(ctx context.Context, r io.ReaderAt, size int64, tail []byte, tailOff int64, i int) (zipDir, bool) {
	e := tail[i:]
	d := zipDir{
		entries: uint64(binary.LittleEndian.Uint16(e[10:])),
		size:    int64(binary.LittleEndian.Uint32(e[12:])),
	}
	disk, cdDisk := binary.LittleEndian.Uint16(e[4:]), binary.LittleEndian.Uint16(e[6:])
	cdOff := int64(binary.LittleEndian.Uint32(e[16:]))
	dirEnd := tailOff + int64(i)
	// Zip64: the locator sits just before the record (APPNOTE §4.3.15).
	if i >= 20 && at(tail, i-20, "PK\x06\x07") {
		loc := tail[i-20:]
		if binary.LittleEndian.Uint32(loc[16:]) > 1 {
			d.multiDisk = true
		}
		rec := make([]byte, 56)
		recAt := dirEnd - 20 - 56
		ok := recAt >= 0 && readFull(r, rec, recAt) && at(rec, 0, "PK\x06\x06")
		if !ok { // a record with extensible data: trust the stored offset
			recAt = int64(binary.LittleEndian.Uint64(loc[8:]))
			ok = recAt >= 0 && recAt+56 <= size && readFull(r, rec, recAt) && at(rec, 0, "PK\x06\x06")
		}
		if !ok {
			return zipDir{}, false
		}
		disk, cdDisk = 0, 0
		if binary.LittleEndian.Uint32(rec[16:]) != 0 || binary.LittleEndian.Uint32(rec[20:]) != 0 {
			d.multiDisk = true
		}
		d.entries = binary.LittleEndian.Uint64(rec[32:])
		d.size = int64(binary.LittleEndian.Uint64(rec[40:]))
		cdOff = int64(binary.LittleEndian.Uint64(rec[48:]))
		dirEnd = recAt
	}
	if disk != 0 || cdDisk != 0 {
		d.multiDisk = true
	}
	if d.size < 0 || cdOff < 0 || d.size > dirEnd {
		return zipDir{}, false
	}
	d.start = dirEnd - d.size
	d.prepended = d.start - cdOff
	if d.prepended < 0 {
		return zipDir{}, false
	}
	if d.size == 0 {
		return d, d.entries == 0
	}
	sig := make([]byte, 4)
	if ctx.Err() != nil || !readFull(r, sig, d.start) || !bytes.Equal(sig, []byte("PK\x01\x02")) {
		return zipDir{}, false
	}
	return d, true
}

func readFull(r io.ReaderAt, p []byte, off int64) bool {
	n, err := r.ReadAt(p, off)
	return n == len(p) && (err == nil || err == io.EOF)
}

// listZip reads the member names of the directory d into a new listing (names only).
func listZip(ctx context.Context, r io.ReaderAt, d zipDir) *listing {
	l := newListing()
	if d.multiDisk {
		l.incomplete = true
		return l
	}
	size := d.size
	if size > maxZipDirBytes {
		size = maxZipDirBytes
		l.incomplete = true
	}
	br := newSeq(ctx, r, d.start, d.start+size)
	hdr := make([]byte, 46)
	name := make([]byte, 0, 256)
	for n := uint64(0); n < d.entries; n++ {
		if n >= maxZipEntries {
			l.incomplete = true
			break
		}
		if _, err := io.ReadFull(br, hdr); err != nil || !at(hdr, 0, "PK\x01\x02") {
			l.incomplete = true
			break
		}
		flags := binary.LittleEndian.Uint16(hdr[8:])
		nameLen := int(binary.LittleEndian.Uint16(hdr[28:]))
		skip := int(binary.LittleEndian.Uint16(hdr[30:])) + int(binary.LittleEndian.Uint16(hdr[32:]))
		if cap(name) < nameLen {
			name = make([]byte, nameLen)
		}
		name = name[:nameLen]
		if _, err := io.ReadFull(br, name); err != nil {
			l.incomplete = true
			break
		}
		if _, err := br.Discard(skip); err != nil {
			l.incomplete = true
			break
		}
		l.add(zipName(name, flags&0x800 != 0))
	}
	return l
}

// zipName decodes a member name: UTF-8 when bit 11 is set or the bytes are valid
// UTF-8 (many archivers write UTF-8 without the flag), else IBM code page 437.
func zipName(b []byte, utf bool) string {
	if utf || utf8.Valid(b) {
		return strings.ToValidUTF8(string(b), "�")
	}
	var s strings.Builder
	for _, c := range b {
		if c < 0x80 {
			s.WriteByte(c)
		} else {
			s.WriteRune(cp437[c-0x80])
		}
	}
	return s.String()
}

// cp437 is the upper half of IBM code page 437.
var cp437 = []rune("ÇüéâäàåçêëèïîìÄÅÉæÆôöòûùÿÖÜ¢£¥₧ƒáíóúñÑªº¿⌐¬½¼¡«»░▒▓│┤╡╢╖╕╣║╗╝╜╛┐└┴┬├─┼╞╟╚╔╩╦╠═╬╧╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀αßΓπΣσµτΦΘΩδ∞φε∩≡±≥≤⌠⌡÷≈°∙·√ⁿ²■ ")
