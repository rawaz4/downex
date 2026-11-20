package media

import (
	"encoding/binary"
	"math"
	"math/bits"
	"time"
)

// MaxSidxRefs bounds the references of one segment index box (docs/MEDIA-SPEC.md §8
// C15).
const MaxSidxRefs = 100_000

// SidxRef is one reference of a segment index (ISO/IEC 14496-12 §8.16.3): a subsegment
// of the resource.
type SidxRef struct {
	// Offset and Size locate the subsegment in the resource (bytes).
	Offset, Size int64
	// Start is its earliest presentation time and Duration its length.
	Start, Duration time.Duration
	// StartsWithSAP and SAPType describe its first access unit.
	StartsWithSAP bool
	SAPType       uint8
}

// End is the offset just past the subsegment.
func (r SidxRef) End() int64 { return r.Offset + r.Size }

// SegmentIndex is a parsed 'sidx' box whose references all point at media.
type SegmentIndex struct {
	Timescale uint32
	Refs      []SidxRef
}

// ParseSidx parses a complete 'sidx' box (header included) whose last byte is at
// resource offset boxEnd-1 (the references are counted from boxEnd, the "anchor point"
// of ISO/IEC 14496-12 §8.16.3), versions 0 and 1. A reference to another index
// (reference_type 1, a hierarchical index) is ErrUnsupported; more than MaxSidxRefs
// references ErrTooLarge; anything malformed ErrMalformed.
func ParseSidx(box []byte, boxEnd int64) (*SegmentIndex, error) {
	if len(box) < 8 || string(box[4:8]) != "sidx" {
		return nil, errf(ErrMalformed, "not a 'sidx' box")
	}
	size := uint64(binary.BigEndian.Uint32(box[0:4]))
	hdr := 8
	if size == 1 {
		if len(box) < 16 {
			return nil, errf(ErrMalformed, "truncated 'sidx' box")
		}
		size, hdr = binary.BigEndian.Uint64(box[8:16]), 16
	}
	if size != uint64(len(box)) {
		return nil, errf(ErrMalformed, "'sidx' box size does not match its data")
	}
	p := box[hdr:]
	if len(p) < 12 {
		return nil, errf(ErrMalformed, "truncated 'sidx' box")
	}
	version := p[0]
	ts := binary.BigEndian.Uint32(p[8:12])
	if ts == 0 {
		return nil, errf(ErrMalformed, "'sidx' timescale is 0")
	}
	pos := 12
	var ept, first uint64
	switch version {
	case 0:
		if len(p) < pos+8 {
			return nil, errf(ErrMalformed, "truncated 'sidx' box")
		}
		ept, first = uint64(binary.BigEndian.Uint32(p[pos:])), uint64(binary.BigEndian.Uint32(p[pos+4:]))
		pos += 8
	case 1:
		if len(p) < pos+16 {
			return nil, errf(ErrMalformed, "truncated 'sidx' box")
		}
		ept, first = binary.BigEndian.Uint64(p[pos:]), binary.BigEndian.Uint64(p[pos+8:])
		pos += 16
	default:
		return nil, errf(ErrUnsupported, "'sidx' version %d", version)
	}
	if len(p) < pos+4 {
		return nil, errf(ErrMalformed, "truncated 'sidx' box")
	}
	n := int(binary.BigEndian.Uint16(p[pos+2:]))
	pos += 4
	if n > MaxSidxRefs {
		return nil, errf(ErrTooLarge, "more than %d segment index references", MaxSidxRefs)
	}
	if len(p) != pos+12*n {
		return nil, errf(ErrMalformed, "'sidx' box holds %d bytes of references for %d references", len(p)-pos, n)
	}
	if boxEnd < 0 || first > math.MaxInt64-uint64(boxEnd) {
		return nil, errf(ErrMalformed, "'sidx' first_offset out of range")
	}
	ix := &SegmentIndex{Timescale: ts, Refs: make([]SidxRef, 0, n)}
	off := boxEnd + int64(first)
	t := ept
	for range n {
		a := binary.BigEndian.Uint32(p[pos:])
		dur := binary.BigEndian.Uint32(p[pos+4:])
		sap := binary.BigEndian.Uint32(p[pos+8:])
		pos += 12
		if a>>31 == 1 {
			return nil, errf(ErrUnsupported, "a hierarchical segment index (a reference to another 'sidx')")
		}
		sz := int64(a & 0x7fffffff)
		if sz == 0 {
			return nil, errf(ErrMalformed, "a segment index reference of 0 bytes")
		}
		if off > math.MaxInt64-sz {
			return nil, errf(ErrMalformed, "segment index references out of range")
		}
		start, ok1 := ticks(t, ts)
		end, ok2 := ticks(t+uint64(dur), ts)
		if !ok1 || !ok2 || t > math.MaxUint64-uint64(dur) {
			return nil, errf(ErrMalformed, "segment index times out of range")
		}
		ix.Refs = append(ix.Refs, SidxRef{
			Offset: off, Size: sz, Start: start, Duration: end - start,
			StartsWithSAP: sap>>31 == 1, SAPType: uint8(sap >> 28 & 7),
		})
		off += sz
		t += uint64(dur)
	}
	return ix, nil
}

// ticks converts v ticks of timescale ts to a duration, reporting overflow.
func ticks(v uint64, ts uint32) (time.Duration, bool) {
	hi, lo := bits.Mul64(v, uint64(time.Second))
	if hi >= uint64(ts) {
		return 0, false
	}
	q, _ := bits.Div64(hi, lo, uint64(ts))
	if q > math.MaxInt64 {
		return 0, false
	}
	return time.Duration(q), true
}

// Span returns the first and last references that overlap [from, end) (end 0 = to the
// end), the first moved back by leadIn references (never before the first): the
// subsegments a clip needs (docs/MEDIA-SPEC.md §8 B7). ok is false when none overlaps.
func (ix *SegmentIndex) Span(from, end time.Duration, leadIn int) (first, last int, ok bool) {
	first, last = -1, -1
	for k, r := range ix.Refs {
		if (end == 0 || r.Start < end) && r.Start+r.Duration > from {
			if first < 0 {
				first = k
			}
			last = k
		}
	}
	if first < 0 {
		return 0, 0, false
	}
	first = max(first-max(leadIn, 0), 0)
	return first, last, true
}
