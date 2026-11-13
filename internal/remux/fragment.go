package remux

import (
	"encoding/binary"
	"io"
	"math"

	"downex/internal/media"
)

// Limits on untrusted fragment input.
const (
	// maxTopBoxes bounds the top-level boxes of one file.
	maxTopBoxes = 1 << 16
	// maxMoovBytes bounds an initialization segment's 'moov' box.
	maxMoovBytes = 16 << 20
	// maxMoofBytes bounds one 'moof' box.
	maxMoofBytes = 64 << 20
	// maxMoofSamples bounds the samples described by one 'moof' box, which the scanner
	// holds in memory at a time (16 bytes each).
	maxMoofSamples = 1 << 22
	// maxMoofRuns bounds the track runs of one 'moof' box.
	maxMoofRuns = 1 << 16
	// maxFileEmptySamples bounds the samples of the default size 0 in one file: they
	// occupy neither 'mdat' nor 'trun' bytes, so nothing else limits their number.
	// Every other sample of a file claims 'mdat' bytes of its own (fragmentParser.dataLeft)
	// or 4 bytes of a 'trun'.
	maxFileEmptySamples = 1 << 20
)

// ISO/IEC 14496-12 §8.8.7 tfhd flags.
const (
	tfhdBaseDataOffset    = 0x000001
	tfhdSampleDescIndex   = 0x000002
	tfhdDefaultDuration   = 0x000008
	tfhdDefaultSize       = 0x000010
	tfhdDefaultFlags      = 0x000020
	tfhdDefaultBaseIsMoof = 0x020000
)

// ISO/IEC 14496-12 §8.8.8 trun flags.
const (
	trunDataOffset       = 0x000001
	trunFirstSampleFlags = 0x000004
	trunDuration         = 0x000100
	trunSize             = 0x000200
	trunFlags            = 0x000400
	trunCTO              = 0x000800
)

// sampleNonSync is sample_is_non_sync_sample in sample flags (§8.8.3.1).
const sampleNonSync = 0x00010000

// boxHeader locates a box in a file.
type boxHeader struct {
	typ  string
	off  int64 // offset of the box's first byte
	size int64 // total size including the header
	hdr  int64 // header length
}

func (b boxHeader) payloadOff() int64 { return b.off + b.hdr }
func (b boxHeader) end() int64        { return b.off + b.size }

// readTopBoxes lists the top-level boxes of a file of the given size without
// reading their payloads.
func readTopBoxes(r io.ReaderAt, size int64) ([]boxHeader, error) {
	var boxes []boxHeader
	var buf [16]byte
	for off := int64(0); off < size; {
		if len(boxes) >= maxTopBoxes {
			return nil, errf(ErrMalformed, "more than %d top-level boxes", maxTopBoxes)
		}
		rem := size - off
		if rem < 8 {
			return nil, errf(ErrMalformed, "truncated box header at offset %d", off)
		}
		if err := readFull(r, buf[:8], off); err != nil {
			return nil, err
		}
		sz := uint64(binary.BigEndian.Uint32(buf[0:4]))
		typ := string(buf[4:8])
		hl := int64(8)
		switch sz {
		case 0: // extends to the end of the file
			sz = uint64(rem)
		case 1: // 64-bit largesize
			if rem < 16 {
				return nil, errf(ErrMalformed, "truncated box header at offset %d", off)
			}
			if err := readFull(r, buf[8:16], off+8); err != nil {
				return nil, err
			}
			sz, hl = binary.BigEndian.Uint64(buf[8:16]), 16
		}
		if sz < uint64(hl) {
			return nil, errf(ErrMalformed, "box %q at offset %d is smaller than its header", printable(typ), off)
		}
		if sz > uint64(rem) {
			return nil, errf(ErrMalformed, "box %q at offset %d extends past the end of the file", printable(typ), off)
		}
		boxes = append(boxes, boxHeader{typ: typ, off: off, size: int64(sz), hdr: hl})
		off += int64(sz)
	}
	return boxes, nil
}

func readFull(r io.ReaderAt, p []byte, off int64) error {
	n, err := r.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// printable returns a box type safe for error messages.
func printable(typ string) string {
	b := []byte(typ)
	for i, c := range b {
		if c < 0x20 || c > 0x7e {
			b[i] = '?'
		}
	}
	return string(b)
}

// trexDefaults are a track's TrackExtendsBox defaults (§8.8.3).
type trexDefaults struct {
	sdi, dur, size, flags uint32
}

// sampleMeta is one sample of a track run.
type sampleMeta struct {
	size  uint32
	dur   uint32
	flags uint32
	cto   int32
}

func (s sampleMeta) sync() bool { return s.flags&sampleNonSync == 0 }

// fragRun is one track run (trun) of a movie fragment: samples whose data is
// contiguous in the file.
type fragRun struct {
	trackID uint32 // source track_ID
	sdi     uint32 // sample_description_index (1-based)
	hasTime bool   // a tfdt gives the decode time of the run's first sample
	time    uint64 // that decode time
	offset  int64  // file offset of the first sample's data
	size    int64  // total sample data bytes
	samples []sampleMeta

	// data holds the sample data of a run built in memory (transport stream
	// conversion); runs of fragmented MP4 inputs are copied from path instead.
	data []byte

	// Filled in by the scanner.
	track   int    // index of the output track
	file    int    // index of the file in the muxer's file table
	path    string // path of the file
	srcTime uint64 // source decode time of the first sample
	outTime uint64 // decode time in the output track timeline
	dur     uint64 // sum of sample durations
}

// fragmentParser parses movie fragment boxes (§8.8.4–§8.8.12) of one file.
type fragmentParser struct {
	// defaults returns the trex defaults of a source track and whether the track
	// is declared by the initialization segment.
	defaults func(trackID uint32) (trexDefaults, bool)
	samples  int // samples parsed from the current moof
	// Budgets of the file the moofs are in (beginFile): the 'mdat' payload bytes that
	// track runs have not claimed yet, and the samples of the default size 0 still
	// allowed. Track runs claim their data from the first, so the movie fragments of
	// a file cannot describe more sample data than it holds, however often they point
	// at the same bytes.
	dataLeft  int64
	emptyLeft int
}

// beginFile resets the file budgets for a file whose 'mdat' payloads hold data bytes.
func (p *fragmentParser) beginFile(data int64) {
	p.dataLeft, p.emptyLeft = data, maxFileEmptySamples
}

// parseMoof parses a complete 'moof' box (header included) that starts at file
// offset moofOff and returns its non-empty track runs in order.
func (p *fragmentParser) parseMoof(box []byte, moofOff int64, hdrLen int) ([]fragRun, error) {
	p.samples = 0
	var runs []fragRun
	// The data of a traf without base_data_offset and default-base-is-moof starts
	// where the previous traf's data ended (or at the moof for the first one).
	prevEnd := moofOff
	err := eachChild(box[hdrLen:], func(typ string, payload []byte) error {
		switch typ {
		case "traf":
			var err error
			runs, prevEnd, err = p.parseTraf(payload, moofOff, prevEnd, runs)
			return err
		case "pssh":
			return wrapEncrypted("movie fragment has a 'pssh' box")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return runs, nil
}

func wrapEncrypted(msg string) error {
	return &remuxError{kind: media.ErrEncrypted, msg: msg}
}

// eachChild calls fn with the type and payload of each box in p.
func eachChild(p []byte, fn func(typ string, payload []byte) error) error {
	for len(p) > 0 {
		if len(p) < 8 {
			return errf(ErrMalformed, "truncated box header")
		}
		sz := uint64(binary.BigEndian.Uint32(p[0:4]))
		typ := string(p[4:8])
		hl := uint64(8)
		switch sz {
		case 0:
			sz = uint64(len(p))
		case 1:
			if len(p) < 16 {
				return errf(ErrMalformed, "truncated box header")
			}
			sz, hl = binary.BigEndian.Uint64(p[8:16]), 16
		}
		if sz < hl || sz > uint64(len(p)) {
			return errf(ErrMalformed, "box %q does not fit its container", printable(typ))
		}
		if err := fn(typ, p[hl:sz]); err != nil {
			return err
		}
		p = p[sz:]
	}
	return nil
}

type tfhdInfo struct {
	trackID uint32
	flags   uint32
	base    uint64
	sdi     uint32
	dur     uint32
	size    uint32
	sflags  uint32
}

func (p *fragmentParser) parseTraf(payload []byte, moofOff, prevEnd int64, runs []fragRun) ([]fragRun, int64, error) {
	var (
		tfhd     *tfhdInfo
		def      trexDefaults
		hasTime  bool
		time     uint64
		base     int64 // base data offset of this traf
		next     int64 // where a trun without data_offset starts
		firstRun = len(runs)
	)
	err := eachChild(payload, func(typ string, b []byte) error {
		switch typ {
		case "tfhd":
			if tfhd != nil {
				return errf(ErrMalformed, "track fragment has two 'tfhd' boxes")
			}
			h, err := parseTfhd(b)
			if err != nil {
				return err
			}
			d, ok := p.defaults(h.trackID)
			if !ok {
				return errf(ErrMalformed, "movie fragment refers to track %d, which the initialization segment does not declare", h.trackID)
			}
			tfhd, def = h, d
			switch {
			case h.flags&tfhdBaseDataOffset != 0:
				if h.base > math.MaxInt64 {
					return errf(ErrMalformed, "base_data_offset out of range")
				}
				base = int64(h.base)
			case h.flags&tfhdDefaultBaseIsMoof != 0:
				base = moofOff
			default:
				// The first traf's data is based at the moof, a later one's where
				// the previous traf's data ended (ISO/IEC 14496-12 §8.8.7.1).
				base = prevEnd
			}
			next = base
		case "tfdt":
			if len(b) < 8 {
				return errf(ErrMalformed, "'tfdt' box is too short")
			}
			if b[0] == 1 {
				if len(b) < 12 {
					return errf(ErrMalformed, "'tfdt' box is too short")
				}
				time = binary.BigEndian.Uint64(b[4:12])
			} else {
				time = uint64(binary.BigEndian.Uint32(b[4:8]))
			}
			hasTime = true
		case "trun":
			if tfhd == nil {
				return errf(ErrMalformed, "'trun' before 'tfhd'")
			}
			r, err := p.parseTrun(b, tfhd, def, base, next)
			if err != nil {
				return err
			}
			next = r.offset + r.size
			if len(r.samples) > 0 {
				if len(runs) >= maxMoofRuns {
					return errf(ErrMalformed, "more than %d track runs in one movie fragment", maxMoofRuns)
				}
				runs = append(runs, r)
			}
		case "senc":
			return wrapEncrypted("track fragment carries sample encryption information")
		case "uuid":
			if isPIFFSenc(b) {
				return wrapEncrypted("track fragment carries sample encryption information")
			}
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	if tfhd == nil {
		return nil, 0, errf(ErrMalformed, "track fragment without 'tfhd'")
	}
	// tfdt dates the first sample of the traf; runs without samples before the
	// first kept one add no time.
	if hasTime && firstRun < len(runs) {
		runs[firstRun].hasTime, runs[firstRun].time = true, time
	}
	return runs, next, nil
}

// isPIFFSenc reports a 'uuid' box of the sample encryption extended type
// (a2394f52-5a9b-4f14-a244-6c427c648df4).
func isPIFFSenc(b []byte) bool {
	piff := [16]byte{0xa2, 0x39, 0x4f, 0x52, 0x5a, 0x9b, 0x4f, 0x14, 0xa2, 0x44, 0x6c, 0x42, 0x7c, 0x64, 0x8d, 0xf4}
	return len(b) >= 16 && [16]byte(b[:16]) == piff
}

func parseTfhd(b []byte) (*tfhdInfo, error) {
	if len(b) < 8 {
		return nil, errf(ErrMalformed, "'tfhd' box is too short")
	}
	h := &tfhdInfo{flags: binary.BigEndian.Uint32(b[0:4]) & 0xffffff, trackID: binary.BigEndian.Uint32(b[4:8])}
	pos := 8
	need := func(n int) bool { return len(b)-pos >= n }
	if h.flags&tfhdBaseDataOffset != 0 {
		if !need(8) {
			return nil, errf(ErrMalformed, "'tfhd' box is too short")
		}
		h.base = binary.BigEndian.Uint64(b[pos:])
		pos += 8
	}
	for _, f := range []struct {
		flag uint32
		dst  *uint32
	}{{tfhdSampleDescIndex, &h.sdi}, {tfhdDefaultDuration, &h.dur}, {tfhdDefaultSize, &h.size}, {tfhdDefaultFlags, &h.sflags}} {
		if h.flags&f.flag == 0 {
			continue
		}
		if !need(4) {
			return nil, errf(ErrMalformed, "'tfhd' box is too short")
		}
		*f.dst = binary.BigEndian.Uint32(b[pos:])
		pos += 4
	}
	return h, nil
}

func (p *fragmentParser) parseTrun(b []byte, h *tfhdInfo, def trexDefaults, base, next int64) (fragRun, error) {
	if len(b) < 8 {
		return fragRun{}, errf(ErrMalformed, "'trun' box is too short")
	}
	flags := binary.BigEndian.Uint32(b[0:4]) & 0xffffff
	count := binary.BigEndian.Uint32(b[4:8])
	pos := 8
	r := fragRun{trackID: h.trackID, offset: next}
	if flags&trunDataOffset != 0 {
		if len(b)-pos < 4 {
			return fragRun{}, errf(ErrMalformed, "'trun' box is too short")
		}
		// data_offset is relative to the base data offset of the traf.
		r.offset = base + int64(int32(binary.BigEndian.Uint32(b[pos:])))
		pos += 4
	}
	var firstFlags uint32
	hasFirst := flags&trunFirstSampleFlags != 0
	if hasFirst {
		if len(b)-pos < 4 {
			return fragRun{}, errf(ErrMalformed, "'trun' box is too short")
		}
		firstFlags = binary.BigEndian.Uint32(b[pos:])
		pos += 4
	}
	per := 0
	for _, f := range []uint32{trunDuration, trunSize, trunFlags, trunCTO} {
		if flags&f != 0 {
			per += 4
		}
	}
	if per > 0 && uint64(count)*uint64(per) > uint64(len(b)-pos) {
		return fragRun{}, errf(ErrMalformed, "'trun' declares %d samples but is too short", count)
	}
	dDur, dSize, dFlags := def.dur, def.size, def.flags
	if h.flags&tfhdDefaultDuration != 0 {
		dDur = h.dur
	}
	if h.flags&tfhdDefaultSize != 0 {
		dSize = h.size
	}
	if h.flags&tfhdDefaultFlags != 0 {
		dFlags = h.sflags
	}
	if flags&trunSize == 0 {
		// Samples of the default size must fit the file's unclaimed sample data (checked
		// before the samples are allocated); empty ones are bounded per file.
		switch {
		case dSize == 0 && uint64(count) > uint64(max(p.emptyLeft, 0)):
			return fragRun{}, errf(ErrMalformed, "more than %d empty samples in one file", maxFileEmptySamples)
		case dSize == 0:
			p.emptyLeft -= int(count)
		case uint64(count)*uint64(dSize) > uint64(max(p.dataLeft, 0)):
			return fragRun{}, errf(ErrMalformed, "the movie fragments describe more sample data than the file's 'mdat' boxes hold")
		}
	}
	if uint64(p.samples)+uint64(count) > maxMoofSamples {
		return fragRun{}, errf(ErrMalformed, "more than %d samples in one movie fragment", maxMoofSamples)
	}
	p.samples += int(count)
	r.sdi = def.sdi
	if h.flags&tfhdSampleDescIndex != 0 {
		r.sdi = h.sdi
	}
	r.samples = make([]sampleMeta, count)
	var total int64
	for i := range r.samples {
		s := sampleMeta{dur: dDur, size: dSize, flags: dFlags}
		if hasFirst && i == 0 {
			s.flags = firstFlags
		}
		if flags&trunDuration != 0 {
			s.dur = binary.BigEndian.Uint32(b[pos:])
			pos += 4
		}
		if flags&trunSize != 0 {
			s.size = binary.BigEndian.Uint32(b[pos:])
			pos += 4
		}
		if flags&trunFlags != 0 {
			s.flags = binary.BigEndian.Uint32(b[pos:])
			pos += 4
		}
		if flags&trunCTO != 0 {
			// Version 0 offsets are unsigned and version 1 signed; values above
			// 2^31 in version 0 are taken as the negative offsets they encode.
			s.cto = int32(binary.BigEndian.Uint32(b[pos:]))
			pos += 4
		}
		total += int64(s.size)
		r.samples[i] = s
	}
	r.size = total
	if r.offset < 0 || r.offset > math.MaxInt64-total {
		return fragRun{}, errf(ErrMalformed, "sample data offset out of range")
	}
	if total > p.dataLeft {
		return fragRun{}, errf(ErrMalformed, "the movie fragments describe more sample data than the file's 'mdat' boxes hold")
	}
	p.dataLeft -= total
	return r, nil
}

// mdatRange is the payload of an 'mdat' box.
type mdatRange struct{ start, end int64 }

// checkRunData verifies that the data of every run lies inside one 'mdat'
// payload of the file.
func checkRunData(runs []fragRun, mdats []mdatRange) error {
	for i := range runs {
		r := &runs[i]
		if r.size == 0 {
			continue
		}
		ok := false
		for _, m := range mdats {
			if r.offset >= m.start && r.offset+r.size <= m.end {
				ok = true
				break
			}
		}
		if !ok {
			return errf(ErrMalformed, "sample data of track %d lies outside the 'mdat' boxes", r.trackID)
		}
	}
	return nil
}
