package filecheck

import (
	"context"
	"encoding/binary"
	"io"
	"strings"
)

// CAB listing bounds (§9.3).
const (
	maxCABFiles = 65535
	maxCABBytes = 16 << 20
	maxCABName  = 256
)

// listCAB reads the CFFILE entries of a cabinet ([MS-CAB] 2.1, 2.3): 16 fixed bytes
// then a NUL-terminated name, UTF-8 when attribute 0x80 is set, else Latin-1.
func listCAB(ctx context.Context, r io.ReaderAt, size int64, head []byte) *listing {
	l := newListing()
	if len(head) < 36 {
		l.incomplete = true
		return l
	}
	coff := int64(binary.LittleEndian.Uint32(head[16:]))
	files := int(binary.LittleEndian.Uint16(head[28:]))
	if coff < 36 || coff >= size {
		l.incomplete = true
		return l
	}
	end := min(size, coff+maxCABBytes)
	br := newSeq(ctx, r, coff, end)
	fixed := make([]byte, 16)
	for i := 0; i < files; i++ {
		if i >= maxCABFiles {
			l.incomplete = true
			break
		}
		if _, err := io.ReadFull(br, fixed); err != nil {
			l.incomplete = true
			break
		}
		raw, err := br.ReadSlice(0)
		if err != nil || len(raw) > maxCABName+1 {
			l.incomplete = true
			break
		}
		raw = raw[:len(raw)-1]
		var name string
		if binary.LittleEndian.Uint16(fixed[14:])&0x80 != 0 {
			name = strings.ToValidUTF8(string(raw), "�")
		} else {
			rs := make([]rune, len(raw))
			for k, c := range raw {
				rs[k] = rune(c)
			}
			name = string(rs)
		}
		l.add(name)
	}
	return l
}
