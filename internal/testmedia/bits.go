package testmedia

import "math/bits"

// BitWriter writes bits most significant first, as the RBSP syntax of ISO/IEC
// 14496-10 §7.2 and ISO/IEC 23008-2 §7.2 reads them.
type BitWriter struct {
	buf []byte
	n   uint
}

// Bit writes the lowest bit of b.
func (w *BitWriter) Bit(b uint64) {
	if w.n%8 == 0 {
		w.buf = append(w.buf, 0)
	}
	if b&1 != 0 {
		w.buf[len(w.buf)-1] |= 0x80 >> (w.n % 8)
	}
	w.n++
}

// Flag writes one bit.
func (w *BitWriter) Flag(f bool) {
	if f {
		w.Bit(1)
	} else {
		w.Bit(0)
	}
}

// Bits writes the n lowest bits of v, most significant first.
func (w *BitWriter) Bits(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		w.Bit(v >> uint(i))
	}
}

// UE writes an unsigned Exp-Golomb code (ISO/IEC 14496-10 §9.1).
func (w *BitWriter) UE(v uint64) {
	n := bits.Len64(v + 1)
	w.Bits(0, n-1)
	w.Bits(v+1, n)
}

// SE writes a signed Exp-Golomb code (§9.1.1).
func (w *BitWriter) SE(v int64) {
	if v > 0 {
		w.UE(uint64(2*v - 1))
	} else {
		w.UE(uint64(-2 * v))
	}
}

// Len returns the number of bits written.
func (w *BitWriter) Len() int { return int(w.n) }

// Bytes returns the bytes written so far; a partial last byte is padded with zero
// bits.
func (w *BitWriter) Bytes() []byte { return w.buf }

// RBSP appends rbsp_trailing_bits (a one bit, then zero bits up to the byte
// boundary) and returns the bytes.
func (w *BitWriter) RBSP() []byte {
	w.Bit(1)
	for w.n%8 != 0 {
		w.Bit(0)
	}
	return w.buf
}

// EBSP inserts emulation_prevention_three_byte where the RBSP would otherwise
// contain 0x000000, 0x000001, 0x000002 or 0x000003 (ISO/IEC 14496-10 §7.4.1), and
// after trailing zero bytes, so that the result never contains a start code.
func EBSP(rbsp []byte) []byte {
	out := make([]byte, 0, len(rbsp)+len(rbsp)/64+1)
	zeros := 0
	for _, b := range rbsp {
		if zeros >= 2 && b <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	if zeros > 0 {
		out = append(out, 3)
	}
	return out
}

// Unescape removes emulation_prevention_three_byte from an EBSP.
func Unescape(ebsp []byte) []byte {
	out := make([]byte, 0, len(ebsp))
	zeros := 0
	for _, b := range ebsp {
		if zeros >= 2 && b == 3 {
			zeros = 0
			continue
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// NAL returns a NAL unit: the header bytes followed by the escaped RBSP.
func NAL(header []byte, rbsp []byte) []byte {
	return append(append([]byte(nil), header...), EBSP(rbsp)...)
}

// AnnexB joins NAL units into a byte stream (ISO/IEC 14496-10 Annex B). The first
// unit and every unit for which long reports true get a 4-byte start code, the
// others a 3-byte one.
func AnnexB(nalus [][]byte, long func(i int) bool) []byte {
	var out []byte
	for i, n := range nalus {
		if i == 0 || (long != nil && long(i)) {
			out = append(out, 0, 0, 0, 1)
		} else {
			out = append(out, 0, 0, 1)
		}
		out = append(out, n...)
	}
	return out
}

// LengthPrefixed joins NAL units with 4-byte big-endian lengths, as an MP4 sample
// with lengthSizeMinusOne 3 holds them (ISO/IEC 14496-15 §5.3.2).
func LengthPrefixed(nalus [][]byte) []byte {
	var out []byte
	for _, n := range nalus {
		l := len(n)
		out = append(out, byte(l>>24), byte(l>>16), byte(l>>8), byte(l))
		out = append(out, n...)
	}
	return out
}
