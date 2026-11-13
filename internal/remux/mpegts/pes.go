package mpegts

// PES packets (ISO/IEC 13818-1 §2.4.3.6).

// MaxPESBytes bounds one PES unit (header included); a longer one is
// ErrMalformed, so that a stream that never starts a new unit cannot grow memory.
const MaxPESBytes = 32 << 20

// maxPESHeader is the longest PES header: 9 fixed bytes and 255 bytes of
// PES_header_data.
const maxPESHeader = 9 + 255

// pesHeader is the parsed start of a PES packet.
type pesHeader struct {
	length     int   // PES_packet_length; 0 = unbounded
	pts, dts   int64 // 33-bit values; -1 = absent
	scrambled  bool  // PES_scrambling_control ≠ 0
	payloadOff int   // offset of the PES packet data
}

// pesLength returns PES_packet_length from the first six bytes of a unit, or -1
// when fewer bytes are known or the start code prefix is missing.
func pesLength(b []byte) int {
	if len(b) < 6 || b[0] != 0 || b[1] != 0 || b[2] != 1 {
		return -1
	}
	return int(b[4])<<8 | int(b[5])
}

// parsePESHeader parses the header of a PES packet. ok is false when the start
// code prefix is missing or the header does not fit.
func parsePESHeader(b []byte) (h pesHeader, ok bool) {
	h.pts, h.dts = -1, -1
	l := pesLength(b)
	if l < 0 {
		return h, false
	}
	h.length = l
	switch b[3] {
	case 0xbc, 0xbe, 0xbf, 0xf0, 0xf1, 0xf2, 0xf8, 0xff:
		// program_stream_map, padding_stream, private_stream_2, ECM, EMM,
		// DSMCC_stream, ITU-T H.222.1 type E, program_stream_directory: no optional
		// header.
		h.payloadOff = 6
		return h, true
	}
	if len(b) < 9 || b[6]&0xc0 != 0x80 {
		return h, false
	}
	h.scrambled = b[6]&0x30 != 0
	flags := b[7] >> 6
	hl := int(b[8])
	if 9+hl > len(b) || (l > 0 && 6+l < 9+hl) {
		return h, false
	}
	switch flags {
	case 1:
		return h, false // forbidden PTS_DTS_flags value
	case 2:
		if hl < 5 {
			return h, false
		}
		h.pts = timestamp(b[9:14])
	case 3:
		if hl < 10 {
			return h, false
		}
		h.pts, h.dts = timestamp(b[9:14]), timestamp(b[14:19])
	}
	h.payloadOff = 9 + hl
	return h, true
}

// timestamp decodes a 33-bit PTS or DTS from its five bytes; the marker bits are
// not checked.
func timestamp(b []byte) int64 {
	return int64(b[0]>>1&7)<<30 | int64(b[1])<<22 | int64(b[2]>>1)<<15 | int64(b[3])<<7 | int64(b[4]>>1)
}
