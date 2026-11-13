package testmedia

import (
	"cmp"
	"slices"
)

// Stream types (ISO/IEC 13818-1 Table 2-34, and the SAMPLE-AES and ATSC
// registrations).
const (
	TypeMPEG2Video = 0x02
	TypeMPEG1Audio = 0x03
	TypePrivate    = 0x06 // PES private data (AC-3 with a descriptor, subtitles, ...)
	TypeAAC        = 0x0f // ISO/IEC 13818-7 ADTS
	TypeLATM       = 0x11
	TypeID3        = 0x15
	TypeH264       = 0x1b
	TypeHEVC       = 0x24
	TypeAC3        = 0x81
	TypeSCTE35     = 0x86
	TypeEAC3       = 0x87
	TypeSAESAC3    = 0xc1
	TypeSAESAAC    = 0xcf
	TypeSAESH264   = 0xdb
)

// TS constants.
const (
	PacketSize = 188
	SyncByte   = 0x47
	NullPID    = 0x1fff
	PCRDelay   = 9000 // the PCR of a unit's first packet is its decode time minus this many 90 kHz ticks
)

// Stream is one elementary stream of the program.
type Stream struct {
	PID         uint16
	Type        byte
	Language    string // ISO 639-2 code for an ISO_639_language_descriptor; "" = none
	Descriptors []byte // further ES_info descriptors, encoded
}

// PES is one PES packet to multiplex.
type PES struct {
	PID      uint16
	StreamID byte  // 0 = 0xE0 for H.264/HEVC streams, else 0xC0
	PTS, DTS int64 // 90 kHz ticks, unwrapped; -1 = absent (other values, negative ones too, are written modulo 2^33). DTS only when it differs from PTS
	Data     []byte
	// RandomAccess sets random_access_indicator in the first packet.
	RandomAccess bool
	// Bounded writes PES_packet_length (when it fits); otherwise it is 0, as video
	// may use.
	Bounded bool
	// PESScrambled sets PES_scrambling_control to 01.
	PESScrambled bool
	// TSScrambled sets transport_scrambling_control to 10 on the unit's packets.
	TSScrambled bool
}

// Muxer writes a single-program transport stream. The zero value needs Streams.
type Muxer struct {
	ProgramNumber uint16 // default 1
	PMTPID        uint16 // default 0x1000
	PCRPID        uint16 // default: the first stream's PID
	Streams       []Stream
	PMTVersion    byte
	// ProgramInfo are program-level descriptors of the PMT.
	ProgramInfo []byte
	cc          map[uint16]byte
}

func (m *Muxer) init() {
	if m.cc == nil {
		m.cc = map[uint16]byte{}
	}
	if m.ProgramNumber == 0 {
		m.ProgramNumber = 1
	}
	if m.PMTPID == 0 {
		m.PMTPID = 0x1000
	}
	if m.PCRPID == 0 && len(m.Streams) > 0 {
		m.PCRPID = m.Streams[0].PID
	}
}

// CRC32 is the CRC-32/MPEG-2 of b (polynomial 0x04C11DB7, initial value all ones,
// no reflection, no final XOR; ISO/IEC 13818-1 Annex A).
func CRC32(b []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, c := range b {
		crc ^= uint32(c) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// Section returns a long-form PSI section (section_syntax_indicator 1) with its
// CRC: table_id, the 16-bit table id extension, version_number, section 0 of 0.
func Section(tableID byte, ext uint16, version byte, body []byte) []byte {
	n := 5 + len(body) + 4
	s := []byte{tableID, 0xb0 | byte(n>>8)&0x0f, byte(n), byte(ext >> 8), byte(ext), 0xc1 | (version&0x1f)<<1, 0, 0}
	s = append(s, body...)
	c := CRC32(s)
	return append(s, byte(c>>24), byte(c>>16), byte(c>>8), byte(c))
}

// PATSection returns a program association section listing one program.
func PATSection(program, pmtPID uint16, version byte) []byte {
	return Section(0x00, 1, version, []byte{byte(program >> 8), byte(program), 0xe0 | byte(pmtPID>>8), byte(pmtPID)})
}

// LanguageDescriptor is an ISO_639_language_descriptor (tag 0x0A).
func LanguageDescriptor(lang string) []byte {
	return append([]byte{0x0a, 4}, append([]byte(lang), 0)...)
}

// AC3Descriptor is the DVB AC-3 descriptor (tag 0x6A) that marks a private stream
// (type 0x06) as AC-3.
func AC3Descriptor() []byte { return []byte{0x6a, 1, 0} }

// PMTSection returns the program map section of m.
func (m *Muxer) PMTSection() []byte {
	m.init()
	body := []byte{0xe0 | byte(m.PCRPID>>8), byte(m.PCRPID), 0xf0 | byte(len(m.ProgramInfo)>>8), byte(len(m.ProgramInfo))}
	body = append(body, m.ProgramInfo...)
	for _, s := range m.Streams {
		var es []byte
		if s.Language != "" {
			es = append(es, LanguageDescriptor(s.Language)...)
		}
		es = append(es, s.Descriptors...)
		body = append(body, s.Type, 0xe0|byte(s.PID>>8), byte(s.PID), 0xf0|byte(len(es)>>8), byte(len(es)))
		body = append(body, es...)
	}
	return Section(0x02, m.ProgramNumber, m.PMTVersion, body)
}

// SectionPackets returns the packets carrying one PSI section on pid: a
// pointer_field of 0, the section, then 0xFF stuffing.
func (m *Muxer) SectionPackets(pid uint16, section []byte) []byte {
	m.init()
	data := append([]byte{0}, section...)
	var out []byte
	for first := true; len(data) > 0; first = false {
		n := min(len(data), 184)
		pkt := m.header(pid, first, false, false)
		pkt = append(pkt, data[:n]...)
		for len(pkt) < PacketSize {
			pkt = append(pkt, 0xff)
		}
		out = append(out, pkt...)
		data = data[n:]
	}
	return out
}

// PSI returns a PAT packet and the PMT packets.
func (m *Muxer) PSI() []byte {
	m.init()
	return append(m.SectionPackets(0, PATSection(m.ProgramNumber, m.PMTPID, 0)), m.SectionPackets(m.PMTPID, m.PMTSection())...)
}

// header starts a packet with a payload: 4 header bytes, the continuity counter
// incremented.
func (m *Muxer) header(pid uint16, pusi, scrambled, af bool) []byte {
	cc := m.cc[pid]
	m.cc[pid] = (cc + 1) & 0x0f
	b1 := byte(pid>>8) & 0x1f
	if pusi {
		b1 |= 0x40
	}
	b3 := 0x10 | cc
	if af {
		b3 |= 0x20
	}
	if scrambled {
		b3 |= 0x80
	}
	return []byte{SyncByte, b1, byte(pid), b3}
}

func (m *Muxer) streamType(pid uint16) byte {
	for _, s := range m.Streams {
		if s.PID == pid {
			return s.Type
		}
	}
	return 0
}

// timestamp encodes a 33-bit PTS or DTS with its 4-bit prefix (§2.4.3.7).
func timestamp(prefix byte, t int64) []byte {
	v := uint64(t) & (1<<33 - 1)
	return []byte{
		prefix<<4 | byte(v>>29)&0x0e | 1,
		byte(v >> 22),
		byte(v>>14) | 1,
		byte(v >> 7),
		byte(v<<1) | 1,
	}
}

// PESBytes returns the PES packet (header and data) of p.
func (m *Muxer) PESBytes(p PES) []byte {
	sid := p.StreamID
	if sid == 0 {
		switch m.streamType(p.PID) {
		case TypeH264, TypeHEVC, TypeMPEG2Video, TypeSAESH264:
			sid = 0xe0
		default:
			sid = 0xc0
		}
	}
	var opt []byte
	flags := byte(0)
	switch {
	case p.PTS != -1 && p.DTS != -1 && p.DTS != p.PTS:
		flags = 0xc0
		opt = append(timestamp(3, p.PTS), timestamp(1, p.DTS)...)
	case p.PTS != -1:
		flags = 0x80
		opt = timestamp(2, p.PTS)
	}
	b2 := byte(0x84) // '10', data_alignment_indicator
	if p.PESScrambled {
		b2 |= 0x10
	}
	hdr := []byte{0, 0, 1, sid, 0, 0, b2, flags, byte(len(opt))}
	hdr = append(hdr, opt...)
	if l := len(hdr) - 6 + len(p.Data); p.Bounded && l <= 0xffff {
		hdr[4], hdr[5] = byte(l>>8), byte(l)
	}
	return append(hdr, p.Data...)
}

// Packets returns the TS packets of one PES unit. The first packet carries an
// adaptation field with the random access indicator and, on the PCR PID, a PCR
// PCRDelay ticks before the unit's decode time.
func (m *Muxer) Packets(p PES) []byte {
	m.init()
	data := m.PESBytes(p)
	var out []byte
	for first := true; len(data) > 0; first = false {
		pcr := first && p.PID == m.PCRPID && (p.DTS != -1 || p.PTS != -1)
		ra := first && p.RandomAccess
		fixed := 0
		if pcr || ra {
			fixed = 2
			if pcr {
				fixed += 6
			}
		}
		n := min(len(data), 184-fixed)
		afLen := 184 - n // bytes of adaptation field, its length byte included
		pkt := m.header(p.PID, first, p.TSScrambled, afLen > 0)
		if afLen > 0 {
			af := []byte{byte(afLen - 1)}
			if afLen > 1 {
				fl := byte(0)
				if ra {
					fl |= 0x40
				}
				if pcr {
					fl |= 0x10
				}
				af = append(af, fl)
				if pcr {
					t := p.DTS
					if t == -1 {
						t = p.PTS
					}
					base := uint64(t-PCRDelay) & (1<<33 - 1)
					af = append(af, byte(base>>25), byte(base>>17), byte(base>>9), byte(base>>1), byte(base<<7)|0x7e, 0)
				}
				for len(af) < afLen {
					af = append(af, 0xff)
				}
			}
			pkt = append(pkt, af...)
		}
		pkt = append(pkt, data[:n]...)
		out = append(out, pkt...)
		data = data[n:]
	}
	return out
}

// Segment returns the PSI followed by the packets of units.
func (m *Muxer) Segment(units []PES) []byte {
	out := m.PSI()
	for _, u := range units {
		out = append(out, m.Packets(u)...)
	}
	return out
}

// Segments splits units into transport stream segments, each starting with the
// PSI: a new segment starts at every unit (but the first) for which cut reports
// true. cut is called for every unit in order. Continuity counters run on across
// segments.
func (m *Muxer) Segments(units []PES, cut func(i int, p PES) bool) [][]byte {
	var out [][]byte
	start := 0
	for i, u := range units {
		if cut(i, u) && i > start {
			out = append(out, m.Segment(units[start:i]))
			start = i
		}
	}
	if start < len(units) {
		out = append(out, m.Segment(units[start:]))
	}
	return out
}

// VideoPES returns one unbounded PES per access unit, with PTS and DTS and the
// random access indicator on sync pictures.
func VideoPES(pid uint16, aus []AccessUnit) []PES {
	out := make([]PES, len(aus))
	for i, au := range aus {
		out[i] = PES{PID: pid, PTS: au.PTS, DTS: au.DTS, Data: au.AnnexB, RandomAccess: au.Sync}
	}
	return out
}

// AudioPES packs perPES ADTS frames into each bounded PES, timed by its first
// frame.
func AudioPES(pid uint16, frames []AudioFrame, perPES int) []PES {
	var out []PES
	for i := 0; i < len(frames); i += perPES {
		var data []byte
		for _, f := range frames[i:min(i+perPES, len(frames))] {
			data = append(data, f.ADTS...)
		}
		out = append(out, PES{PID: pid, PTS: frames[i].PTS, DTS: -1, Data: data, Bounded: true, RandomAccess: true})
	}
	return out
}

// decodeTime is the time a unit is multiplexed at: its DTS, else its PTS.
func (p PES) decodeTime() int64 {
	if p.DTS != -1 {
		return p.DTS
	}
	return p.PTS
}

// Interleave merges unit lists by decode time, keeping the order of each list and
// of the lists for equal times.
func Interleave(lists ...[]PES) []PES {
	var out []PES
	for _, l := range lists {
		out = append(out, l...)
	}
	slices.SortStableFunc(out, func(a, b PES) int { return cmp.Compare(a.decodeTime(), b.decodeTime()) })
	return out
}

// Packet returns the n-th packet of ts (no bounds check beyond the slice).
func Packet(ts []byte, n int) []byte { return ts[n*PacketSize : (n+1)*PacketSize] }

// PacketPID returns the PID of a packet.
func PacketPID(pkt []byte) uint16 { return uint16(pkt[1]&0x1f)<<8 | uint16(pkt[2]) }

// DropPacket returns ts without the n-th packet (0-based) on pid that carries a
// payload, which leaves a continuity gap.
func DropPacket(ts []byte, pid uint16, n int) []byte {
	return editPacket(ts, pid, n, func(p []byte) []byte { return nil })
}

// DuplicatePacket returns ts with the n-th packet on pid repeated once.
func DuplicatePacket(ts []byte, pid uint16, n int) []byte {
	return editPacket(ts, pid, n, func(p []byte) []byte { return append(slices.Clone(p), p...) })
}

func editPacket(ts []byte, pid uint16, n int, edit func(p []byte) []byte) []byte {
	var out []byte
	k := 0
	for i := 0; i+PacketSize <= len(ts); i += PacketSize {
		p := ts[i : i+PacketSize]
		if PacketPID(p) == pid && p[3]&0x10 != 0 {
			if k == n {
				out = append(out, edit(p)...)
				k++
				continue
			}
			k++
		}
		out = append(out, p...)
	}
	return out
}

// NullPackets returns n null packets (PID 0x1FFF).
func NullPackets(n int) []byte {
	var out []byte
	for range n {
		p := make([]byte, PacketSize)
		p[0], p[1], p[2], p[3] = SyncByte, 0x1f, 0xff, 0x10
		for j := 4; j < PacketSize; j++ {
			p[j] = 0xff
		}
		out = append(out, p...)
	}
	return out
}

// Filler returns n packets of a sync byte followed by pseudo-random bytes, as a
// test server might serve in place of real segments: no PAT, no PMT.
func Filler(seed int64, n int) []byte {
	out := make([]byte, n*PacketSize)
	x := uint64(seed)*0x9e3779b97f4a7c15 + 1
	for i := range out {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		out[i] = byte(x)
		if i%PacketSize == 0 {
			out[i] = SyncByte
		}
	}
	return out
}
