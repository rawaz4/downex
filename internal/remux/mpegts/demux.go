package mpegts

import (
	"bytes"
	"context"
	"io"
	"os"

	"downex/internal/media"
)

// Unit is one PES unit of a kept stream.
type Unit struct {
	PID      uint16
	Type     StreamType
	PTS, DTS int64 // 33-bit raw values; -1 = absent
	// Data is the PES packet data (the elementary stream bytes), valid until the
	// next call to Next; nil for a stream kept for its timestamps only.
	Data []byte
	// File is the index of the file the unit started in.
	File int
	// RandomAccess is the random_access_indicator of the unit's first packet.
	RandomAccess bool
	// AfterLoss reports that data of this PID was lost since its previous unit: a
	// continuity gap, or a unit dropped for a missing start code or a malformed
	// PES header.
	AfterLoss bool
}

// Stats counts what the demuxer skipped.
type Stats struct {
	Gaps       int   // continuity gaps on kept PIDs
	Duplicates int   // duplicate packets dropped
	Dropped    int   // units discarded: damaged by a gap, no start code, malformed PES header
	Skipped    int64 // bytes skipped to find a packet start
}

// Option configures a Demuxer.
type Option func(*Demuxer)

// WithProgramCheck calls fn with every program (first PMT, and each new PMT
// version) before its streams are chosen. An error from fn ends demultiplexing:
// Next returns it as is.
func WithProgramCheck(fn func(Program) error) Option {
	return func(d *Demuxer) { d.check = fn }
}

// WithTimestampsOnly also follows the streams fn accepts (and keep does not), but
// reports their units without data: the same units with the same timestamps, in
// the same order, as if they were kept, at the cost of their PES headers only.
func WithTimestampsOnly(fn func(Program, Stream) bool) Option {
	return func(d *Demuxer) { d.tsOnly = fn }
}

type source interface {
	open() (io.ReadCloser, error)
	size() int // -1 = unknown
}

type fileSource string

func (f fileSource) open() (io.ReadCloser, error) { return os.Open(string(f)) }
func (f fileSource) size() int                    { return -1 }

type bytesSource []byte

func (b bytesSource) open() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
func (b bytesSource) size() int                    { return len(b) }

// pidState is the state of one followed elementary stream.
type pidState struct {
	s    Stream
	full bool // keep the data (else the timestamps only)
	cc   int  // continuity_counter of the last packet with payload; -1 = unknown
	open bool // a unit is being assembled
	lost bool // data was lost since the last unit
	buf  []byte
	size int // bytes of the unit so far (all of them, also when not kept)
	file int
	ra   bool
}

type queued struct {
	u   Unit
	buf []byte
}

// Demuxer reads the PES units of the streams of the first program of one or more
// transport stream files, in order. Each file starts at its first packet (FindStart:
// data before it, such as an image a CDN put there, is skipped) with its own
// continuity expectations; a unit open at the end of a file continues in the next
// one.
type Demuxer struct {
	srcs   []source
	keep   func(Program, Stream) bool
	tsOnly func(Program, Stream) bool
	check  func(Program) error

	file       int // index of the file being read
	rc         io.ReadCloser
	pr         packetReader
	sinceCheck int64
	done       bool
	err        error

	program  uint16
	pmtPID   int // -1 = unknown
	patCRC   uint32
	havePAT  bool
	pmtCRC   uint32
	havePMT  bool
	prog     Program
	haveProg bool
	pat, pmt sectionAssembler
	patCC    int
	pmtCC    int
	pids     map[uint16]*pidState
	order    []uint16 // followed PIDs in PMT order
	queue    []queued
	lent     [][]byte
	free     [][]byte
	stats    Stats
}

// NewDemuxer reads the files in order (each from its first packet) and yields the
// PES units of the streams keep accepts, as they complete. It keeps no unit beyond
// MaxPESBytes.
func NewDemuxer(files []string, keep func(Program, Stream) bool, opts ...Option) *Demuxer {
	srcs := make([]source, len(files))
	for i, f := range files {
		srcs[i] = fileSource(f)
	}
	return newDemuxer(srcs, keep, opts)
}

// NewBytesDemuxer is NewDemuxer over files held in memory.
func NewBytesDemuxer(files [][]byte, keep func(Program, Stream) bool, opts ...Option) *Demuxer {
	srcs := make([]source, len(files))
	for i, f := range files {
		srcs[i] = bytesSource(f)
	}
	return newDemuxer(srcs, keep, opts)
}

func newDemuxer(srcs []source, keep func(Program, Stream) bool, opts []Option) *Demuxer {
	d := &Demuxer{srcs: srcs, keep: keep, file: -1, pmtPID: -1, patCC: -1, pmtCC: -1, pids: map[uint16]*pidState{}}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Program returns the program in effect and whether one has been found.
func (d *Demuxer) Program() (Program, bool) { return d.prog, d.haveProg }

// Stats returns what has been skipped so far.
func (d *Demuxer) Stats() Stats { return d.stats }

// Close releases the file being read. Next returns io.EOF afterwards.
func (d *Demuxer) Close() error {
	d.done, d.queue = true, nil
	return d.closeFile()
}

func (d *Demuxer) closeFile() error {
	if d.rc == nil {
		return nil
	}
	err := d.rc.Close()
	d.rc = nil
	return err
}

// Next returns the next unit, io.EOF after the last one, or the error that ended
// demultiplexing (again on every later call). ctx is checked at every file and
// every MiB.
func (d *Demuxer) Next(ctx context.Context) (Unit, error) {
	// The buffers of the units returned before are free again.
	d.free = append(d.free, d.lent...)
	d.lent = d.lent[:0]
	for {
		if len(d.queue) > 0 {
			q := d.queue[0]
			copy(d.queue, d.queue[1:])
			d.queue = d.queue[:len(d.queue)-1]
			if q.buf != nil {
				d.lent = append(d.lent, q.buf)
			}
			return q.u, nil
		}
		if d.err != nil {
			return Unit{}, d.err
		}
		if d.done {
			return Unit{}, io.EOF
		}
		if err := d.step(ctx); err != nil {
			d.err = err
			_ = d.closeFile()
		}
	}
}

// step reads and handles one packet, opening the next file when needed.
func (d *Demuxer) step(ctx context.Context) error {
	if d.rc == nil {
		if d.file+1 >= len(d.srcs) {
			d.done = true
			return d.flush()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		d.file++
		rc, err := d.srcs[d.file].open()
		if err != nil {
			return err
		}
		d.rc = rc
		d.pr.reset(rc, d.srcs[d.file].size())
		d.newFile()
	}
	pkt, skipped, err := d.pr.next()
	d.stats.Skipped += int64(skipped)
	d.sinceCheck += int64(skipped + len(pkt))
	if err == io.EOF {
		return d.closeFile()
	}
	if err != nil {
		if e, ok := err.(*Error); ok {
			e.File = d.file
		}
		return err
	}
	if d.sinceCheck >= 1<<20 {
		d.sinceCheck = 0
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return d.handle(pkt)
}

// newFile resets the continuity expectations and the section assemblers.
func (d *Demuxer) newFile() {
	d.patCC, d.pmtCC = -1, -1
	d.pat.reset()
	d.pmt.reset()
	for _, st := range d.pids {
		st.cc = -1
	}
}

func (d *Demuxer) handle(pkt []byte) error {
	p, ok := parsePacket(pkt)
	if !ok {
		return nil
	}
	switch {
	case p.pid == 0:
		return d.psi(&d.pat, &d.patCC, p, d.onPAT)
	case d.pmtPID >= 0 && int(p.pid) == d.pmtPID:
		return d.psi(&d.pmt, &d.pmtCC, p, d.onPMT)
	}
	if st := d.pids[p.pid]; st != nil {
		return d.es(st, p)
	}
	return nil
}

func (d *Demuxer) psi(a *sectionAssembler, cc *int, p packet, fn func([]byte) error) error {
	if p.scrambling != 0 {
		return newError(media.ErrEncrypted, d.file, int(p.pid), "program-specific information is scrambled")
	}
	if !p.hasPayload {
		return nil
	}
	if *cc >= 0 {
		if int(p.cc) == *cc {
			return nil // a duplicate packet
		}
		if int(p.cc) != (*cc+1)&0x0f && !p.discontinuity {
			a.reset()
		}
	}
	*cc = int(p.cc)
	var err error
	a.push(p.payload, p.pusi, func(s []byte) {
		if err == nil {
			err = fn(s)
		}
	})
	return err
}

func (d *Demuxer) onPAT(b []byte) error {
	s, ok := parseSection(b)
	if !ok || s.tableID != 0x00 || !s.current {
		return nil
	}
	if d.havePAT && (s.crc == d.patCRC || s.number != 0) {
		return nil
	}
	n, pid, ok := firstProgram(s.body)
	if !ok || pid == 0 || pid == NullPID {
		return nil
	}
	d.havePAT, d.patCRC = true, s.crc
	if n != d.program || int(pid) != d.pmtPID {
		d.program, d.pmtPID = n, int(pid)
		d.pmt.reset()
		d.pmtCC, d.havePMT = -1, false
	}
	return nil
}

func (d *Demuxer) onPMT(b []byte) error {
	s, ok := parseSection(b)
	if !ok || s.tableID != 0x02 || !s.current || s.ext != d.program {
		return nil
	}
	if d.havePMT && s.crc == d.pmtCRC {
		return nil
	}
	prog, ok := parsePMT(d.program, uint16(d.pmtPID), s.body)
	if !ok {
		return nil
	}
	d.havePMT, d.pmtCRC = true, s.crc
	if d.check != nil {
		if err := d.check(prog); err != nil {
			return err
		}
	}
	return d.apply(prog)
}

// apply makes prog the program in effect: the streams to follow are chosen anew,
// those that keep their PID and type keep their state, and the open units of the
// others are completed.
func (d *Demuxer) apply(prog Program) error {
	pids := map[uint16]*pidState{}
	var order []uint16
	for _, s := range prog.Streams {
		full := d.keep != nil && d.keep(prog, s)
		if !full && (d.tsOnly == nil || !d.tsOnly(prog, s)) {
			continue
		}
		if _, dup := pids[s.PID]; dup || s.PID == 0 || int(s.PID) == d.pmtPID || s.PID == NullPID {
			continue
		}
		st := d.pids[s.PID]
		if st == nil || st.s.Type != s.Type || st.full != full {
			st = &pidState{s: s, full: full, cc: -1}
		}
		st.s = s
		pids[s.PID] = st
		order = append(order, s.PID)
	}
	for _, pid := range d.order {
		if st := d.pids[pid]; pids[pid] != st && st.open {
			if err := d.complete(st); err != nil {
				return err
			}
		}
	}
	d.pids, d.order, d.prog, d.haveProg = pids, order, prog, true
	return nil
}

func (d *Demuxer) es(st *pidState, p packet) error {
	if p.scrambling != 0 {
		return newError(media.ErrEncrypted, d.file, int(p.pid), "scrambled packet")
	}
	if !p.hasPayload {
		return nil
	}
	if st.cc >= 0 {
		switch {
		case int(p.cc) == st.cc:
			d.stats.Duplicates++
			return nil
		case int(p.cc) != (st.cc+1)&0x0f && !p.discontinuity:
			d.stats.Gaps++
			if st.open {
				d.discard(st)
			}
			st.lost = true
		}
	}
	st.cc = int(p.cc)
	if p.pusi {
		if st.open {
			if err := d.complete(st); err != nil {
				return err
			}
		}
		st.open, st.size, st.file, st.ra = true, 0, d.file, p.randomAccess
		st.buf = d.getBuf()
	} else if !st.open {
		return nil // the rest of a unit that was discarded or started before
	}
	st.size += len(p.payload)
	if st.size > MaxPESBytes {
		return newError(ErrMalformed, d.file, int(p.pid), "PES unit larger than %d bytes", MaxPESBytes)
	}
	if st.full {
		st.buf = appendGrow(st.buf, p.payload)
	} else if len(st.buf) < maxPESHeader {
		st.buf = append(st.buf, p.payload[:min(len(p.payload), maxPESHeader-len(st.buf))]...)
	}
	if len(st.buf) >= 3 && (st.buf[0] != 0 || st.buf[1] != 0 || st.buf[2] != 1) {
		d.discard(st) // no packet_start_code_prefix
		st.lost = true
		return nil
	}
	if l := pesLength(st.buf); l > 0 && st.size >= 6+l {
		return d.complete(st)
	}
	return nil
}

// discard drops the unit being assembled.
func (d *Demuxer) discard(st *pidState) {
	d.recycle(st.buf)
	st.buf, st.open = nil, false
	d.stats.Dropped++
}

// complete ends the unit being assembled and queues it.
func (d *Demuxer) complete(st *pidState) error {
	buf := st.buf
	st.buf, st.open = nil, false
	h, ok := parsePESHeader(buf)
	if !ok {
		d.recycle(buf)
		d.stats.Dropped++
		st.lost = true
		return nil
	}
	if h.scrambled {
		d.recycle(buf)
		return newError(media.ErrEncrypted, st.file, int(st.s.PID), "scrambled PES packet")
	}
	u := Unit{PID: st.s.PID, Type: st.s.Type, PTS: h.pts, DTS: h.dts, File: st.file, RandomAccess: st.ra, AfterLoss: st.lost}
	if st.full {
		end := len(buf)
		if h.length > 0 {
			end = min(end, 6+h.length)
		}
		u.Data = buf[h.payloadOff:end]
	}
	st.lost = false
	d.queue = append(d.queue, queued{u: u, buf: buf})
	return nil
}

// flush completes the units open at the end of the last file, in PMT order.
func (d *Demuxer) flush() error {
	for _, pid := range d.order {
		if st := d.pids[pid]; st.open {
			if err := d.complete(st); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Demuxer) getBuf() []byte {
	if n := len(d.free); n > 0 {
		b := d.free[n-1]
		d.free = d.free[:n-1]
		return b[:0]
	}
	return nil
}

func (d *Demuxer) recycle(b []byte) {
	if b != nil && len(d.free) < 8 {
		d.free = append(d.free, b[:0])
	}
}

// appendGrow appends b to buf, doubling its capacity when it is full (up to
// MaxPESBytes), so that the allocations of a growing unit add up to at most twice
// its size.
func appendGrow(buf, b []byte) []byte {
	if n := len(buf) + len(b); n > cap(buf) {
		c := min(max(2*cap(buf), n, 4096), max(MaxPESBytes, n))
		nb := make([]byte, len(buf), c)
		copy(nb, buf)
		buf = nb
	}
	return append(buf, b...)
}
