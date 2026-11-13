package mpegts

import (
	"io"
)

// Transport stream packet constants (ISO/IEC 13818-1 §2.4.3).
const (
	// PacketSize is the size of a transport stream packet.
	PacketSize = 188
	// SyncByte starts every packet.
	SyncByte = 0x47
	// NullPID is the PID of null packets.
	NullPID = 0x1fff
	// resyncWindow bounds the bytes scanned for a sync position after a missing
	// sync byte.
	resyncWindow = 64 << 10
	// readSize is the size of one read from a file; the buffer holds one read plus a
	// resync window and three packets.
	readSize = 256 << 10
)

// packet is a parsed transport stream packet. payload points into the reader's
// buffer.
type packet struct {
	pid           uint16
	pusi          bool // payload_unit_start_indicator
	scrambling    byte // transport_scrambling_control
	cc            byte // continuity_counter
	hasPayload    bool
	discontinuity bool // discontinuity_indicator
	randomAccess  bool // random_access_indicator
	payload       []byte
}

// parsePacket parses a packet that starts with the sync byte. ok is false for a
// packet to skip: one with transport_error_indicator, a null packet, the reserved
// adaptation_field_control 00, or an adaptation field longer than the packet.
func parsePacket(b []byte) (p packet, ok bool) {
	if b[1]&0x80 != 0 {
		return p, false
	}
	p.pid = uint16(b[1]&0x1f)<<8 | uint16(b[2])
	if p.pid == NullPID {
		return p, false
	}
	p.pusi = b[1]&0x40 != 0
	p.scrambling = b[3] >> 6
	afc := (b[3] >> 4) & 3
	p.cc = b[3] & 0x0f
	pos := 4
	switch afc {
	case 0:
		return p, false
	case 2, 3:
		l := int(b[4])
		limit := 183
		if afc == 3 {
			limit = 182
		}
		if l > limit {
			return p, false
		}
		if l > 0 {
			f := b[5]
			p.discontinuity = f&0x80 != 0
			p.randomAccess = f&0x40 != 0
		}
		pos = 5 + l
	}
	if afc&1 != 0 {
		p.hasPayload = true
		p.payload = b[pos:PacketSize]
	}
	return p, true
}

// packetReader reads the packets of one file from the first one FindStart finds,
// resynchronising after a missing sync byte. A trailing partial packet is dropped.
type packetReader struct {
	r          io.Reader
	buf        []byte
	start, end int
	eof        bool
	consumed   int64 // bytes of the file passed so far
	fresh      bool  // nothing read yet: the first packet is still to be found
}

// reset starts reading r. size is the number of bytes r holds, or -1 when unknown:
// a smaller buffer serves a small input held in memory.
func (pr *packetReader) reset(r io.Reader, size int) {
	n := readSize + resyncWindow + 3*PacketSize
	if size >= 0 && size < n {
		n = size + 1
	}
	if len(pr.buf) < n {
		pr.buf = make([]byte, n)
	}
	pr.r, pr.start, pr.end, pr.eof, pr.consumed, pr.fresh = r, 0, 0, false, 0, true
}

// fill makes at least n bytes available unless the file ends first.
func (pr *packetReader) fill(n int) error {
	for pr.end-pr.start < n && !pr.eof {
		if pr.start > 0 && len(pr.buf)-pr.end < readSize {
			copy(pr.buf, pr.buf[pr.start:pr.end])
			pr.end -= pr.start
			pr.start = 0
		}
		if pr.end == len(pr.buf) {
			return nil // a full buffer: the input is larger than announced
		}
		m, err := pr.r.Read(pr.buf[pr.end:])
		pr.end += m
		if err == io.EOF {
			pr.eof = true
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// isSync reports whether the packet at o of the available bytes is plausibly
// aligned: a sync byte there and at +188 and +376 where the file has bytes.
func (pr *packetReader) isSync(o int) bool {
	if o+PacketSize > pr.end || pr.buf[o] != SyncByte {
		return false
	}
	for _, k := range []int{PacketSize, 2 * PacketSize} {
		if o+k < pr.end && pr.buf[o+k] != SyncByte {
			return false
		}
	}
	return true
}

// next returns the next packet (valid until the following call), io.EOF at the
// end of the file, or an *Error of kind ErrMalformed when no sync position is
// found within the resync window. skipped is the number of bytes skipped before
// the packet: the data before the file's first packet (FindStart), or what a
// resynchronisation passed. A file in which FindStart finds no packet is read from
// its offset 0, resynchronising as after a missing sync byte.
func (pr *packetReader) next() (pkt []byte, skipped int, err error) {
	if pr.fresh {
		pr.fresh = false
		if err := pr.fill(StartBytes); err != nil {
			return nil, 0, err
		}
		if k, ok := FindStart(pr.buf[pr.start:pr.end]); ok {
			skipped = k
			pr.consumed += int64(k)
			pr.start += k
		}
	}
	if err := pr.fill(PacketSize); err != nil {
		return nil, 0, err
	}
	if pr.end-pr.start < PacketSize {
		pr.consumed += int64(pr.end - pr.start)
		pr.start = pr.end
		return nil, 0, io.EOF
	}
	if pr.buf[pr.start] != SyncByte {
		if err := pr.fill(resyncWindow + 3*PacketSize); err != nil {
			return nil, 0, err
		}
		limit := min(pr.start+resyncWindow, pr.end)
		o := pr.start
		for ; o < limit && !pr.isSync(o); o++ {
		}
		if o == limit {
			return nil, 0, newError(ErrMalformed, -1, -1, "sync lost at offset %d: no packet start within %d bytes", pr.consumed, resyncWindow)
		}
		skipped += o - pr.start
		pr.consumed += int64(o - pr.start)
		pr.start = o
	}
	pkt = pr.buf[pr.start : pr.start+PacketSize]
	pr.start += PacketSize
	pr.consumed += PacketSize
	return pkt, skipped, nil
}
