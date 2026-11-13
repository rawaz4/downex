package webstorage

import (
	"encoding/binary"
	"errors"
)

var errSnappy = errors.New("corrupt snappy block")

// snappyMaxExpansion bounds what one input byte can decode to: the densest element is a copy
// with a 2-byte offset, 3 bytes giving 64 (format_description.txt). A block claiming more
// than this of its input is corrupt, so nothing is allocated for it.
const snappyMaxExpansion = 22

// snappyDecode decodes one Snappy block (the raw format of Snappy's format_description.txt,
// which LevelDB uses for its blocks): the uncompressed length as a varint, then literals
// and back-references.
func snappyDecode(src []byte) ([]byte, error) {
	n, k := binary.Uvarint(src)
	if k <= 0 || n > maxFile || n > uint64(len(src)-k)*snappyMaxExpansion {
		return nil, errSnappy
	}
	src = src[k:]
	dst := make([]byte, 0, n)
	// Lengths and offsets are kept in uint64 and checked against what is left before they
	// index anything: a 4-byte literal length or offset does not fit a 32-bit int.
	for len(src) > 0 {
		tag := src[0]
		left := n - uint64(len(dst)) // len(dst) never exceeds n
		switch tag & 3 {
		case 0: // literal
			length := uint64(tag >> 2)
			src = src[1:]
			if length >= 60 {
				extra := int(length - 59)
				if len(src) < extra {
					return nil, errSnappy
				}
				length = 0
				for i := extra - 1; i >= 0; i-- {
					length = length<<8 | uint64(src[i])
				}
				src = src[extra:]
			}
			length++ // at most 2^32
			if length > uint64(len(src)) || length > left {
				return nil, errSnappy
			}
			dst = append(dst, src[:length]...)
			src = src[length:]
		default:
			var length, offset uint64
			switch tag & 3 {
			case 1:
				if len(src) < 2 {
					return nil, errSnappy
				}
				length = 4 + uint64(tag>>2&7)
				offset = uint64(tag>>5)<<8 | uint64(src[1])
				src = src[2:]
			case 2:
				if len(src) < 3 {
					return nil, errSnappy
				}
				length = 1 + uint64(tag>>2)
				offset = uint64(binary.LittleEndian.Uint16(src[1:3]))
				src = src[3:]
			case 3:
				if len(src) < 5 {
					return nil, errSnappy
				}
				length = 1 + uint64(tag>>2)
				offset = uint64(binary.LittleEndian.Uint32(src[1:5]))
				src = src[5:]
			}
			if offset == 0 || offset > uint64(len(dst)) || length > left {
				return nil, errSnappy
			}
			// byte by byte: a copy may overlap what it writes (a run)
			start := len(dst) - int(offset) // offset ≤ len(dst): fits an int
			for i := range int(length) {
				dst = append(dst, dst[start+i])
			}
		}
	}
	if uint64(len(dst)) != n {
		return nil, errSnappy
	}
	return dst, nil
}
