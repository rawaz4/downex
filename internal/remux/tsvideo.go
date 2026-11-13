package remux

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"slices"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/remux/mpegts"
)

// Video conversion of transport streams (docs/VIDEO-DETECT-SPEC.md §4.3): one PES
// is one access unit; ISO/IEC 14496-10 Annex B byte streams become length-prefixed
// samples (ISO/IEC 14496-15) whose parameter sets move into avcC/hvcC sample
// entries.

const (
	// maxParamSetBytes bounds one parameter set NAL unit: 64 KiB, less the one
	// byte that avcC and hvcC, with their 16-bit lengths, cannot hold.
	maxParamSetBytes = 64<<10 - 1
	// maxParamStateBytes bounds all parameter sets in effect together.
	maxParamStateBytes = 256 << 10
	// maxSampleEntries bounds the sample descriptions of a track.
	maxSampleEntries = 32
)

// H.264 NAL unit types (ISO/IEC 14496-10 Table 7-1).
const (
	avcSliceFirst = 1
	avcIDR        = 5
	avcSliceLast  = 5
	avcSPS        = 7
	avcPPS        = 8
	avcAUD        = 9
	avcEndSeq     = 10
	avcEndStream  = 11
	avcFiller     = 12
	avcSPSExt     = 13
)

// HEVC NAL unit types (ISO/IEC 23008-2 Table 7-1).
const (
	hevcRASLN    = 8
	hevcRASLR    = 9
	hevcIRAPMin  = 16
	hevcIRAPMax  = 23
	hevcCRA      = 21
	hevcVCLLimit = 32
	hevcVPS      = 32
	hevcSPS      = 33
	hevcPPS      = 34
	hevcAUD      = 35
	hevcEOS      = 36
	hevcEOB      = 37
	hevcFD       = 38
)

// errNoParamSets reports a sync sample whose sample entry cannot be built yet.
var errNoParamSets = errors.New("no parameter sets")

// splitAnnexB calls fn with every NAL unit of an Annex B byte stream: the bytes
// between 3-byte start codes (a 4-byte start code is a zero byte and a 3-byte
// one), without trailing zero bytes. Bytes before the first start code and empty
// units are skipped.
func splitAnnexB(b []byte, fn func(nal []byte) error) error {
	start := -1
	for i := 0; ; {
		j := bytes.Index(b[i:], []byte{0, 0, 1})
		end := len(b)
		if j >= 0 {
			end = i + j
		}
		if start >= 0 {
			nal := b[start:end]
			for len(nal) > 0 && nal[len(nal)-1] == 0 {
				nal = nal[:len(nal)-1]
			}
			if len(nal) > 0 {
				if err := fn(nal); err != nil {
					return err
				}
			}
		}
		if j < 0 {
			return nil
		}
		i = end + 3
		start = i
	}
}

// rbspReader reads the bits of a NAL unit payload, skipping emulation prevention
// bytes (ISO/IEC 14496-10 §7.4.1).
type rbspReader struct {
	b     []byte
	pos   int
	zeros int
	cur   byte
	left  uint // bits left in cur
	bad   bool
}

func (r *rbspReader) byte() byte {
	for {
		if r.pos >= len(r.b) {
			r.bad = true
			return 0
		}
		c := r.b[r.pos]
		r.pos++
		if r.zeros >= 2 && c == 3 {
			r.zeros = 0
			continue
		}
		if c == 0 {
			r.zeros++
		} else {
			r.zeros = 0
		}
		return c
	}
}

func (r *rbspReader) bits(n int) uint64 {
	var v uint64
	for range n {
		if r.left == 0 {
			r.cur, r.left = r.byte(), 8
		}
		r.left--
		v = v<<1 | uint64(r.cur>>r.left&1)
	}
	return v
}

// ue reads an unsigned Exp-Golomb code of at most 32 bits.
func (r *rbspReader) ue() uint64 {
	n := 0
	for r.bits(1) == 0 {
		if n++; n > 31 || r.bad {
			r.bad = true
			return 0
		}
	}
	return 1<<n - 1 + r.bits(n)
}

// paramKind is a kind of parameter set.
type paramKind int

const (
	kindVPS paramKind = iota
	kindSPS
	kindPPS
)

// paramState is the latest parameter set per kind and id, with a SHA-256 digest of
// each, so that the identity of the state (which sample entry it is) costs work in
// proportion to what changed, not to the whole state (§4.7: up to 256 KiB).
type paramState struct {
	sets  [3][][]byte   // by kind, indexed by id
	sums  [3][]paramSum // the digest of each set; zero for none
	bytes int
	gen   int      // incremented whenever a set changes
	fp    paramSum // the XOR of the digests of every set in effect
}

// paramSum is the SHA-256 digest of a parameter set with its kind and id.
type paramSum [sha256.Size]byte

func (p *paramSum) xor(o *paramSum) {
	for i := range p {
		p[i] ^= o[i]
	}
}

// set stores a parameter set NAL unit. It reports whether the state changed.
func (s *paramState) set(kind paramKind, id int, nal []byte) (bool, error) {
	if len(nal) > maxParamSetBytes {
		return false, errf(ErrMalformed, "parameter set of %d bytes", len(nal))
	}
	sets := s.sets[kind]
	if id < len(sets) && bytes.Equal(sets[id], nal) {
		return false, nil
	}
	sums := s.sums[kind]
	for len(sets) <= id {
		sets, sums = append(sets, nil), append(sums, paramSum{})
	}
	s.bytes += len(nal) - len(sets[id])
	if s.bytes > maxParamStateBytes {
		return false, errf(ErrMalformed, "parameter sets of more than %d bytes", maxParamStateBytes)
	}
	if sets[id] != nil {
		s.fp.xor(&sums[id])
	}
	h := sha256.New()
	h.Write([]byte{byte(kind), byte(id >> 8), byte(id)})
	h.Write(nal)
	h.Sum(sums[id][:0])
	s.fp.xor(&sums[id])
	sets[id] = append(sets[id][:0:0], nal...)
	s.sets[kind], s.sums[kind] = sets, sums
	s.gen++
	return true, nil
}

// list returns the sets of a kind in id order.
func (s *paramState) list(kind paramKind) [][]byte {
	var out [][]byte
	for _, b := range s.sets[kind] {
		if b != nil {
			out = append(out, b)
		}
	}
	return out
}

// paramSnapshot is the identity of a parameter-set state: its fingerprint and the
// digest of every set.
type paramSnapshot struct {
	fp   paramSum
	sums [3][]paramSum
}

func (s *paramState) snapshot() paramSnapshot {
	snap := paramSnapshot{fp: s.fp}
	for k := range s.sums {
		snap.sums[k] = slices.Clone(s.sums[k])
	}
	return snap
}

// is reports whether the state is the one of snap: the fingerprints first, then
// the digest of every set.
func (s *paramState) is(snap *paramSnapshot) bool {
	if s.fp != snap.fp {
		return false
	}
	for k := range s.sums {
		a, b := s.sums[k], snap.sums[k]
		for i := range max(len(a), len(b)) {
			var x, y paramSum
			if i < len(a) {
				x = a[i]
			}
			if i < len(b) {
				y = b[i]
			}
			if x != y {
				return false
			}
		}
	}
	return true
}

// pendingSample is a converted sample waiting for its successor, which decides
// its duration.
type pendingSample struct {
	ok bool
	s  tsSample
}

// videoConv converts the access units of one H.264 or HEVC stream.
type videoConv struct {
	hevc    bool
	ps      paramState
	entries []*mp4.VisualSampleEntryBox
	states  []paramSnapshot // the parameter sets of each entry
	cur     int             // index of the entry of the current state; -1 = none yet
	curGen  int             // ps.gen when cur was chosen

	domain   int
	synced   bool // a sync sample was kept in this domain
	skipRASL bool // drop RASL pictures (after the domain's first CRA)
	timer    videoTimer

	pend pendingSample
	buf  []byte // the access unit being converted

	// The unit read last, not converted yet: a following unit without a PTS may
	// continue it (an access unit split over several PES packets).
	raw rawUnit
}

// rawUnit is the Annex B data of one access unit with its unwrapped timestamps.
type rawUnit struct {
	ok       bool
	data     []byte
	slice    bool // data holds a slice NAL unit (hasSlice), kept up to date as pieces are joined
	pts, dts int64
	has      bool
	domain   int
}

func newVideoConv(t mpegts.StreamType) *videoConv {
	return &videoConv{hevc: t == mpegts.TypeHEVC, cur: -1, domain: -1}
}

func (c *videoConv) timescale() uint32 { return tsClock }

func (c *videoConv) sampleEntries() []mp4.Box {
	out := make([]mp4.Box, len(c.entries))
	for i, e := range c.entries {
		out[i] = e
	}
	return out
}

// size returns the picture size of the first sample entry.
func (c *videoConv) size() (w, h uint16) {
	if len(c.entries) == 0 {
		return 0, 0
	}
	return c.entries[0].Width, c.entries[0].Height
}

// accessUnit converts the Annex B data of one PES into c.buf and returns what the
// unit is.
func (c *videoConv) accessUnit(data []byte) (vcl, sync, rasl, cra bool, err error) {
	c.buf = c.buf[:0]
	auds := 0
	first := true
	err = splitAnnexB(data, func(nal []byte) error {
		var keep bool
		var err error
		if c.hevc {
			keep, err = c.hevcNAL(nal, &auds)
			if keep && len(nal) >= 2 {
				if t := nal[0] >> 1 & 0x3f; t < hevcVCLLimit {
					vcl = true
					if first {
						first = false
						sync = t >= hevcIRAPMin && t <= hevcIRAPMax
						rasl = t == hevcRASLN || t == hevcRASLR
						cra = t == hevcCRA
					}
				}
			}
		} else {
			keep, err = c.avcNAL(nal, &auds)
			if t := nal[0] & 0x1f; keep && t >= avcSliceFirst && t <= avcSliceLast {
				vcl = true
				sync = sync || t == avcIDR
			}
		}
		if err != nil || !keep {
			return err
		}
		c.buf = binary.BigEndian.AppendUint32(c.buf, uint32(len(nal)))
		c.buf = append(c.buf, nal...)
		return nil
	})
	return vcl, sync, rasl, cra, err
}

// avcNAL handles one H.264 NAL unit and reports whether it stays in the sample.
func (c *videoConv) avcNAL(nal []byte, auds *int) (bool, error) {
	switch nal[0] & 0x1f {
	case avcAUD:
		if *auds++; *auds > 1 {
			return false, errf(ErrUnsupported, "several access units in one PES")
		}
		return false, nil
	case avcSPS:
		r := rbspReader{b: nal[1:]}
		r.bits(24) // profile_idc, constraint flags, level_idc
		id := r.ue()
		if r.bad || id > 31 {
			return false, errf(ErrMalformed, "sequence parameter set without a valid id")
		}
		_, err := c.ps.set(kindSPS, int(id), nal)
		return false, err
	case avcPPS:
		r := rbspReader{b: nal[1:]}
		id := r.ue()
		if r.bad || id > 255 {
			return false, errf(ErrMalformed, "picture parameter set without a valid id")
		}
		_, err := c.ps.set(kindPPS, int(id), nal)
		return false, err
	case avcSPSExt, avcFiller, avcEndSeq, avcEndStream:
		return false, nil
	}
	return true, nil
}

// hevcNAL handles one HEVC NAL unit and reports whether it stays in the sample.
// Parameter sets and delimiters of layers other than the base layer stay.
func (c *videoConv) hevcNAL(nal []byte, auds *int) (bool, error) {
	if len(nal) < 2 {
		return false, nil
	}
	t := nal[0] >> 1 & 0x3f
	layer := (nal[0]&1)<<5 | nal[1]>>3
	switch t {
	case hevcEOS, hevcEOB, hevcFD:
		return false, nil
	}
	if layer != 0 {
		return true, nil
	}
	r := rbspReader{b: nal[2:]}
	switch t {
	case hevcAUD:
		if *auds++; *auds > 1 {
			return false, errf(ErrUnsupported, "several access units in one PES")
		}
		return false, nil
	case hevcVPS:
		id := r.bits(4)
		if r.bad {
			return false, errf(ErrMalformed, "video parameter set without an id")
		}
		_, err := c.ps.set(kindVPS, int(id), nal)
		return false, err
	case hevcSPS:
		r.bits(4) // sps_video_parameter_set_id
		sub := int(r.bits(3))
		r.bits(1)
		r.bits(96) // general profile, tier and level
		var prof, lvl [7]bool
		for i := range sub {
			prof[i], lvl[i] = r.bits(1) == 1, r.bits(1) == 1
		}
		if sub > 0 {
			r.bits(2 * (8 - sub))
		}
		for i := range sub {
			if prof[i] {
				r.bits(88)
			}
			if lvl[i] {
				r.bits(8)
			}
		}
		id := r.ue()
		if r.bad || sub > 6 || id > 15 {
			return false, errf(ErrMalformed, "sequence parameter set without a valid id")
		}
		_, err := c.ps.set(kindSPS, int(id), nal)
		return false, err
	case hevcPPS:
		id := r.ue()
		if r.bad || id > 63 {
			return false, errf(ErrMalformed, "picture parameter set without a valid id")
		}
		_, err := c.ps.set(kindPPS, int(id), nal)
		return false, err
	}
	return true, nil
}

// entry returns the 1-based sample description index of the current parameter
// sets, adding a sample entry when they are new.
func (c *videoConv) entry() (uint32, error) {
	if c.cur >= 0 && c.curGen == c.ps.gen {
		return uint32(c.cur + 1), nil
	}
	for i := range c.states {
		if c.ps.is(&c.states[i]) {
			c.cur, c.curGen = i, c.ps.gen
			return uint32(i + 1), nil
		}
	}
	e, err := c.buildEntry()
	if err != nil {
		return 0, err
	}
	if len(c.entries) >= maxSampleEntries {
		return 0, errf(ErrUnsupported, "parameter sets change more than %d times", maxSampleEntries-1)
	}
	c.entries, c.states = append(c.entries, e), append(c.states, c.ps.snapshot())
	c.cur, c.curGen = len(c.entries)-1, c.ps.gen
	return uint32(len(c.entries)), nil
}

// buildEntry builds an avc1 or hvc1 sample entry from the parameter sets in
// effect. mp4ff's parsers run under recover.
func (c *videoConv) buildEntry() (e *mp4.VisualSampleEntryBox, err error) {
	vps, sps, pps := c.ps.list(kindVPS), c.ps.list(kindSPS), c.ps.list(kindPPS)
	if len(sps) == 0 || len(pps) == 0 || (c.hevc && len(vps) == 0) {
		return nil, errNoParamSets
	}
	defer func() {
		if r := recover(); r != nil {
			e, err = nil, errf(ErrMalformed, "undecodable sequence parameter set")
		}
	}()
	var w, h uint32
	if c.hevc {
		s, err := hevc.ParseSPSNALUnit(sps[0])
		if err != nil {
			return nil, errf(ErrMalformed, "sequence parameter set: %v", err)
		}
		w, h = s.ImageSize()
		if w == 0 || h == 0 || w > 0xffff || h > 0xffff {
			return nil, errf(ErrUnsupported, "picture size %dx%d", w, h)
		}
		hvcC, err := mp4.CreateHvcC(vps, sps, pps, true, true, true, true)
		if err != nil {
			return nil, errf(ErrMalformed, "HEVC configuration: %v", err)
		}
		e = mp4.CreateVisualSampleEntryBox("hvc1", uint16(w), uint16(h), hvcC)
	} else {
		if len(sps) > 31 || len(pps) > 255 {
			return nil, errf(ErrUnsupported, "%d sequence and %d picture parameter sets", len(sps), len(pps))
		}
		s, err := avc.ParseSPSNALUnit(sps[0], false)
		if err != nil {
			return nil, errf(ErrMalformed, "sequence parameter set: %v", err)
		}
		w, h = uint32(s.Width), uint32(s.Height)
		if w == 0 || h == 0 || w > 0xffff || h > 0xffff {
			return nil, errf(ErrUnsupported, "picture size %dx%d", w, h)
		}
		avcC, err := mp4.CreateAvcC(sps, pps, true)
		if err != nil {
			return nil, errf(ErrMalformed, "AVC configuration: %v", err)
		}
		avcC.ChromaFormat = s.ChromaFormatIDC
		avcC.BitDepthLumaMinus1 = byte(s.BitDepthLumaMinus8)
		avcC.BitDepthChromaMinus1 = byte(s.BitDepthChromaMinus8)
		e = mp4.CreateVisualSampleEntryBox("avc1", uint16(w), uint16(h), avcC)
	}
	e.CompressorName = ""
	return e, nil
}

// push takes one PES unit. pts and dts are unwrapped (has: the unit has a PTS);
// emit receives finished samples (their data valid during the call). A unit is
// converted when the next one shows that it is complete: a unit without a PTS that
// continues the access unit (continues) is joined to it first.
func (c *videoConv) push(u *mpegts.Unit, pts, dts int64, has bool, domain int, emit func(*tsSample) error) error {
	if !has && c.continues(u.Data) {
		if u.AfterLoss || !c.raw.ok || c.raw.domain != domain {
			return nil // the rest of an access unit whose start was lost
		}
		if len(c.raw.data)+len(u.Data) > mpegts.MaxPESBytes {
			return errf(ErrMalformed, "access unit larger than %d bytes", mpegts.MaxPESBytes)
		}
		old := len(c.raw.data)
		c.raw.data = append(c.raw.data, u.Data...)
		if !c.raw.slice {
			// Only the new piece, from where a start code ending in it could begin:
			// the NAL units before it were looked at already.
			c.raw.slice = c.hasSlice(c.raw.data[max(0, old-4):])
		}
		return nil
	}
	if err := c.convertRaw(emit); err != nil {
		return err
	}
	c.raw = rawUnit{ok: true, data: append(c.raw.data[:0], u.Data...), pts: pts, dts: dts, has: has, domain: domain}
	c.raw.slice = c.hasSlice(c.raw.data)
	return nil
}

// continues reports whether the data of a unit without a PTS continues the access
// unit before it (ISO/IEC 13818-1 lets a muxer split one access unit over several
// PES packets, of which only the first carries the PTS): it does not start with a
// start code, the unit before has no slice yet, or its first NAL unit can not begin
// an access unit (ISO/IEC 14496-10 §7.4.1.2.3, ISO/IEC 23008-2 §7.4.2.4.4): a slice
// that does not start a picture, a filler or an end of sequence.
func (c *videoConv) continues(data []byte) bool {
	nal := leadingNAL(data)
	if nal == nil {
		return true
	}
	if c.raw.ok && !c.raw.slice {
		return true
	}
	if c.hevc {
		if len(nal) < 3 || (nal[0]&1)<<5|nal[1]>>3 != 0 {
			return true
		}
		switch t := nal[0] >> 1 & 0x3f; {
		case t < hevcVCLLimit:
			return nal[2]&0x80 == 0 // first_slice_segment_in_pic_flag
		case t <= hevcPPS || t == hevcAUD || t == 39 || (t >= 41 && t <= 44) || (t >= 48 && t <= 55):
			return false
		}
		return true
	}
	switch t := nal[0] & 0x1f; {
	case t >= avcSliceFirst && t <= avcSliceLast:
		r := rbspReader{b: nal[1:]}
		return r.ue() != 0 || r.bad // first_mb_in_slice
	case t == 6 || t == avcSPS || t == avcPPS || t == avcAUD || (t >= 14 && t <= 18):
		return false
	}
	return true
}

// leadingNAL returns the NAL unit (or its start) right after a start code at the
// start of data, or nil when data does not start with one.
func leadingNAL(data []byte) []byte {
	i := 0
	for i < len(data) && data[i] == 0 {
		i++
	}
	if i < 2 || i >= len(data)-1 || data[i] != 1 {
		return nil
	}
	return data[i+1:]
}

// hasSlice reports whether Annex B data holds a slice NAL unit of the base layer.
func (c *videoConv) hasSlice(data []byte) bool {
	found := false
	_ = splitAnnexB(data, func(nal []byte) error {
		if c.hevc {
			found = len(nal) >= 2 && nal[0]>>1&0x3f < hevcVCLLimit && (nal[0]&1)<<5|nal[1]>>3 == 0
		} else {
			t := nal[0] & 0x1f
			found = t >= avcSliceFirst && t <= avcSliceLast
		}
		if found {
			return errStop
		}
		return nil
	})
	return found
}

// errStop ends a splitAnnexB walk early.
var errStop = errors.New("stop")

// convertRaw converts the unit held back, if any.
func (c *videoConv) convertRaw(emit func(*tsSample) error) error {
	if !c.raw.ok {
		return nil
	}
	c.raw.ok = false
	return c.convert(c.raw.data, c.raw.pts, c.raw.dts, c.raw.has, c.raw.domain, emit)
}

// convert converts the data of one access unit.
func (c *videoConv) convert(data []byte, pts, dts int64, has bool, domain int, emit func(*tsSample) error) error {
	if domain != c.domain {
		if err := c.endDomain(emit); err != nil {
			return err
		}
		c.domain, c.synced, c.skipRASL = domain, false, false
		c.timer.newDomain()
	}
	vcl, sync, rasl, cra, err := c.accessUnit(data)
	if err != nil || !vcl {
		return err // a unit without a slice is dropped
	}
	switch {
	case !c.synced && !sync:
		return nil // before the first sync sample of the domain
	case sync:
		c.skipRASL = c.hevc && cra && !c.synced
	case c.skipRASL && rasl:
		return nil // a leading picture of the domain's first CRA: not decodable
	}
	sdi, err := c.entry()
	if errors.Is(err, errNoParamSets) {
		if !c.synced {
			return nil // a sync sample whose parameter sets are still missing
		}
		err = errf(ErrMalformed, "parameter sets missing")
	}
	if err != nil {
		return err
	}
	c.synced = true
	d, cto, prevDur := c.timer.place(pts, dts, has)
	if c.pend.ok {
		c.pend.s.dur = uint32(prevDur)
		if err := emit(&c.pend.s); err != nil {
			return err
		}
	}
	c.buf, c.pend.s.data = c.pend.s.data, c.buf
	c.pend.ok = true
	c.pend.s = tsSample{data: c.pend.s.data, dts: d, cto: cto, sync: sync, sdi: sdi, domain: domain}
	return nil
}

// endDomain emits the pending sample as the last of its domain.
func (c *videoConv) endDomain(emit func(*tsSample) error) error {
	if !c.pend.ok {
		return nil
	}
	c.pend.ok = false
	c.pend.s.dur = uint32(c.timer.dur())
	c.pend.s.last = true
	return emit(&c.pend.s)
}

func (c *videoConv) flush(emit func(*tsSample) error) error {
	if err := c.convertRaw(emit); err != nil {
		return err
	}
	return c.endDomain(emit)
}
