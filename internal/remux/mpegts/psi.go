package mpegts

// Program-specific information (ISO/IEC 13818-1 §2.4.4).

const (
	// maxSectionLength bounds section_length of the PAT and PMT (§2.4.4.3, 2.4.4.8).
	maxSectionLength = 1021
	// maxStreams bounds the elementary streams read from one PMT.
	maxStreams = 64
)

// StreamType is the stream_type of a PMT entry (ISO/IEC 13818-1 Table 2-34).
type StreamType uint8

// Stream types this package's callers distinguish.
const (
	TypeMPEG1Video StreamType = 0x01
	TypeMPEG2Video StreamType = 0x02
	TypeMPEG1Audio StreamType = 0x03
	TypeMPEG2Audio StreamType = 0x04
	TypePrivatePES StreamType = 0x06 // AC-3/E-AC-3 when Stream.Dolby is set
	TypeAAC        StreamType = 0x0f // ISO/IEC 13818-7 audio with ADTS transport syntax
	TypeMPEG4Video StreamType = 0x10
	TypeLATM       StreamType = 0x11 // ISO/IEC 14496-3 audio with LATM transport syntax
	TypeMetadata   StreamType = 0x15 // metadata in PES packets (ID3)
	TypeAACRaw     StreamType = 0x1c // ISO/IEC 14496-3 audio without additional transport syntax
	TypeH264       StreamType = 0x1b
	TypeHEVC       StreamType = 0x24
	TypeVVC        StreamType = 0x33
	TypeAC3        StreamType = 0x81 // ATSC A/52
	TypeSCTE35     StreamType = 0x86
	TypeEAC3       StreamType = 0x87 // ATSC A/52 Annex G
	TypeSAESAC3    StreamType = 0xc1 // HLS SAMPLE-AES AC-3
	TypeSAESEAC3   StreamType = 0xc2 // HLS SAMPLE-AES E-AC-3
	TypeSAESAAC    StreamType = 0xcf // HLS SAMPLE-AES AAC (ADTS)
	TypeSAESH264   StreamType = 0xdb // HLS SAMPLE-AES H.264
	TypeVC1        StreamType = 0xea
)

// Stream is one elementary stream of a program.
type Stream struct {
	PID  uint16
	Type StreamType
	// Language is the ISO 639-2 code of the first ISO_639_language_descriptor
	// entry, lower case; "" = none.
	Language string
	// Dolby reports an AC-3 or E-AC-3 descriptor (DVB 0x6A/0x7A, ATSC 0x81/0xCC) or
	// a registration descriptor "AC-3"/"EAC3", which mark a private stream (type
	// 0x06) as Dolby audio.
	Dolby bool
}

// Program is a program of the transport stream: its number, the PID of its
// program map table, its PCR PID and its elementary streams in PMT order.
type Program struct {
	Number, PMTPID, PCRPID uint16
	Streams                []Stream
}

// crcTable is the table of CRC-32/MPEG-2 (polynomial 0x04C11DB7, no reflection).
var crcTable = func() (t [256]uint32) {
	for i := range t {
		c := uint32(i) << 24
		for range 8 {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04c11db7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// crc32 returns the CRC-32/MPEG-2 of b (ISO/IEC 13818-1 Annex A); a section with
// its CRC_32 field checks to 0.
func crc32(b []byte) uint32 {
	c := uint32(0xffffffff)
	for _, x := range b {
		c = c<<8 ^ crcTable[byte(c>>24)^x]
	}
	return c
}

// sectionAssembler reassembles the sections of one PID across packets.
type sectionAssembler struct {
	buf    []byte
	need   int  // total length of the section being assembled; 0 = header incomplete
	active bool // a section is being assembled
}

func (a *sectionAssembler) reset() {
	a.buf, a.need, a.active = a.buf[:0], 0, false
}

// push consumes the payload of one packet and calls fn with every section it
// completes (the slice is valid during the call). A pointer_field beyond the
// payload, or a section_length above 1021, abandons the section.
func (a *sectionAssembler) push(payload []byte, pusi bool, fn func(section []byte)) {
	if !pusi {
		if a.active {
			a.add(payload, fn)
		}
		return
	}
	if len(payload) == 0 {
		a.reset()
		return
	}
	ptr := int(payload[0])
	payload = payload[1:]
	if ptr > len(payload) {
		a.reset()
		return
	}
	if a.active {
		a.add(payload[:ptr], fn)
	}
	a.reset()
	a.active = true
	a.add(payload[ptr:], fn)
}

func (a *sectionAssembler) add(data []byte, fn func(section []byte)) {
	for len(data) > 0 && a.active {
		if len(a.buf) == 0 && data[0] == 0xff {
			a.reset() // stuffing: no further section in this packet
			return
		}
		if len(a.buf) < 3 {
			n := min(3-len(a.buf), len(data))
			a.buf = append(a.buf, data[:n]...)
			data = data[n:]
			if len(a.buf) < 3 {
				return
			}
			l := int(a.buf[1]&0x0f)<<8 | int(a.buf[2])
			if l > maxSectionLength {
				a.reset()
				return
			}
			a.need = 3 + l
		}
		n := min(a.need-len(a.buf), len(data))
		a.buf = append(a.buf, data[:n]...)
		data = data[n:]
		if len(a.buf) == a.need {
			fn(a.buf)
			a.buf, a.need = a.buf[:0], 0
		}
	}
}

// section is a long-form PSI section (section_syntax_indicator 1).
type section struct {
	tableID byte
	ext     uint16 // transport_stream_id (PAT) or program_number (PMT)
	version byte
	current bool // current_next_indicator
	number  byte
	body    []byte // between last_section_number and CRC_32
	crc     uint32 // the CRC_32 field
}

// parseSection parses a section whose CRC checks; ok is false otherwise.
func parseSection(b []byte) (s section, ok bool) {
	if len(b) < 12 || b[1]&0x80 == 0 || crc32(b) != 0 {
		return s, false
	}
	n := len(b)
	return section{
		tableID: b[0],
		ext:     uint16(b[3])<<8 | uint16(b[4]),
		version: (b[5] >> 1) & 0x1f,
		current: b[5]&1 != 0,
		number:  b[6],
		body:    b[8 : n-4],
		crc:     uint32(b[n-4])<<24 | uint32(b[n-3])<<16 | uint32(b[n-2])<<8 | uint32(b[n-1]),
	}, true
}

// firstProgram returns the first program_number ≠ 0 of a PAT section body and its
// program_map_PID.
func firstProgram(body []byte) (number, pmtPID uint16, ok bool) {
	for ; len(body) >= 4; body = body[4:] {
		n := uint16(body[0])<<8 | uint16(body[1])
		if n != 0 {
			return n, uint16(body[2]&0x1f)<<8 | uint16(body[3]), true
		}
	}
	return 0, 0, false
}

// parsePMT parses a PMT section body. At most maxStreams streams are read; an
// entry that does not fit the section ends the list.
func parsePMT(number, pmtPID uint16, body []byte) (Program, bool) {
	if len(body) < 4 {
		return Program{}, false
	}
	p := Program{Number: number, PMTPID: pmtPID, PCRPID: uint16(body[0]&0x1f)<<8 | uint16(body[1])}
	pil := int(body[2]&0x0f)<<8 | int(body[3])
	if 4+pil > len(body) {
		return Program{}, false
	}
	rest := body[4+pil:]
	for len(rest) >= 5 && len(p.Streams) < maxStreams {
		esl := int(rest[3]&0x0f)<<8 | int(rest[4])
		if 5+esl > len(rest) {
			break
		}
		s := Stream{PID: uint16(rest[1]&0x1f)<<8 | uint16(rest[2]), Type: StreamType(rest[0])}
		parseDescriptors(rest[5:5+esl], &s)
		p.Streams = append(p.Streams, s)
		rest = rest[5+esl:]
	}
	return p, true
}

// parseDescriptors reads the descriptors of an ES_info loop that this package
// reports (§2.6).
func parseDescriptors(b []byte, s *Stream) {
	for len(b) >= 2 {
		tag, l := b[0], int(b[1])
		if 2+l > len(b) {
			return
		}
		d := b[2 : 2+l]
		switch tag {
		case 0x0a: // ISO_639_language_descriptor
			if len(d) >= 3 && s.Language == "" && letters(d[:3]) {
				s.Language = string([]byte{d[0] | 0x20, d[1] | 0x20, d[2] | 0x20})
			}
		case 0x6a, 0x7a, 0x81, 0xcc: // AC-3 / E-AC-3 descriptors (DVB, ATSC)
			s.Dolby = true
		case 0x05: // registration_descriptor
			if len(d) >= 4 && (string(d[:4]) == "AC-3" || string(d[:4]) == "EAC3") {
				s.Dolby = true
			}
		}
		b = b[2+l:]
	}
}

func letters(b []byte) bool {
	for _, c := range b {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}
