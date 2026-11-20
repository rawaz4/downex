package filecheck

import (
	"context"
	"hash/crc32"
	"io"
)

// RAR5 walk bounds (§9.3).
const (
	maxRARHeaders     = 10000
	maxRARHeaderBytes = 4 << 20
	maxRARHeader      = 2 << 20
)

// vint reads a RAR5 variable-length integer (7 bits a byte, low bits first) from b.
func vint(b []byte) (v uint64, n int, ok bool) {
	for n < len(b) && n < 10 {
		c := b[n]
		v |= uint64(c&0x7F) << (7 * n)
		n++
		if c&0x80 == 0 {
			return v, n, true
		}
	}
	return 0, 0, false
}

type vreader struct {
	b   []byte
	bad bool
}

func (v *vreader) next() uint64 {
	if v.bad {
		return 0
	}
	x, n, ok := vint(v.b)
	if !ok {
		v.bad = true
		return 0
	}
	v.b = v.b[n:]
	return x
}

func (v *vreader) skip(n uint64) {
	if v.bad || n > uint64(len(v.b)) {
		v.bad = true
		return
	}
	v.b = v.b[n:]
}

// listRAR5 walks the blocks of a RAR 5.0 archive that starts at offset 0 (RARLAB's
// "RAR 5.0 archive format") and records the names of its file headers. An archive
// whose headers are encrypted stops the walk.
func listRAR5(ctx context.Context, r io.ReaderAt, size int64) *listing {
	l := newListing()
	pos := int64(8)
	var total int64
	pre := make([]byte, 4+10)
	for n := 0; ; n++ {
		if n >= maxRARHeaders || ctx.Err() != nil {
			l.incomplete = true
			return l
		}
		if pos >= size {
			l.incomplete = true // no end of archive header
			return l
		}
		k, err := r.ReadAt(pre, pos)
		if k < 5 && err != nil {
			l.incomplete = true
			return l
		}
		hsize, vn, ok := vint(pre[4:k])
		if !ok || hsize == 0 || hsize > maxRARHeader {
			l.incomplete = true
			return l
		}
		total += int64(hsize)
		if total > maxRARHeaderBytes {
			l.incomplete = true
			return l
		}
		hdr := make([]byte, vn+int(hsize))
		if !readFull(r, hdr, pos+4) {
			l.incomplete = true
			return l
		}
		if crc32.ChecksumIEEE(hdr) != leU32(pre) {
			l.incomplete = true
			return l
		}
		v := &vreader{b: hdr[vn:]}
		typ, flags := v.next(), v.next()
		if flags&1 != 0 {
			v.next() // extra area size
		}
		var data uint64
		if flags&2 != 0 {
			data = v.next()
		}
		switch typ {
		case 2: // file header
			fileFlags := v.next()
			v.next() // unpacked size
			v.next() // attributes
			if fileFlags&2 != 0 {
				v.skip(4)
			}
			if fileFlags&4 != 0 {
				v.skip(4)
			}
			v.next() // compression information
			v.next() // host OS
			nameLen := v.next()
			if v.bad || nameLen > uint64(len(v.b)) {
				l.incomplete = true
				return l
			}
			name := string(v.b[:nameLen])
			if fileFlags&1 != 0 {
				name += "/"
			}
			l.add(name)
		case 4: // archive encryption header: the rest cannot be read
			l.incomplete = true
			return l
		case 5: // end of archive
			return l
		}
		if v.bad {
			l.incomplete = true
			return l
		}
		next := pos + 4 + int64(vn) + int64(hsize) + int64(data)
		if data > uint64(size) || next <= pos {
			l.incomplete = true
			return l
		}
		pos = next
	}
}

func leU32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
