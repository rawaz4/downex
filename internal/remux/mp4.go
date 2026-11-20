package remux

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"os"
	"slices"

	"github.com/Eyevinn/mp4ff/mp4"
)

const (
	// movieTimescale is the timescale of the output movie header and edit lists.
	movieTimescale = 1000
	// maxGapSeconds is the largest decode-time gap between consecutive runs of a
	// track that is kept as a gap; larger jumps (in either direction) are
	// timestamp discontinuities, across which the track continues seamlessly.
	maxGapSeconds = 10
	// chunkSeconds bounds the media duration of one progressive chunk, so that
	// the tracks are interleaved finely enough for progressive playback.
	chunkSeconds = 1
)

// maxProgressiveSamples bounds the samples of all tracks for which a progressive
// MP4 is built; beyond it the in-memory sample tables would grow too large and a
// fragmented MP4 is written instead. A variable for tests.
var maxProgressiveSamples uint64 = 4 << 20

// largeThreshold is the largest offset or box size written in 32 bits; beyond
// it chunk offsets use co64 and mdat boxes a 64-bit largesize. A variable for
// tests.
var largeThreshold uint64 = math.MaxUint32

// track is one output track.
type track struct {
	src   *initTrack
	input int
	outID uint32

	started  bool
	firstSrc uint64 // source decode time of the first sample
	next     uint64 // output decode time after the last sample = media duration
	nSamples uint64

	// Progressive sample tables.
	sizes   []uint32
	durs    []uint32
	ctos    []int32
	syncs   []uint32 // 1-based numbers of the sync samples
	nonSync bool
	minCTO  int32
	anyCTO  bool
	chunks  []chunk
	payload int64

	// Output timing (finish).
	empty     uint64 // leading empty edit, movie timescale
	mediaTime int64  // media time the presentation starts at
	shift     int64  // added to every composition offset (progressive)
	presDur   uint64 // duration of the media edit, movie timescale
	tkhdDur   uint64 // track duration, movie timescale
	offset    uint64 // decode time offset of the track (fragmented), media timescale

	stco *mp4.StcoBox
	co64 *mp4.Co64Box

	// A window (applyWindow): lo is the number of the track's first samples cut away
	// (chunk.first counts from the uncut track), edit forces an edit list.
	lo   uint64
	edit bool
}

// chunk is a run of contiguous samples of one track in one source file.
type chunk struct {
	file int
	off  int64
	size int64
	n    uint32
	sdi  uint32
	time uint64 // output decode time of the first sample
	// first is the 0-based number of the chunk's first sample in the track before a
	// window cut it (applyWindow); transport stream replay skips up to it.
	first uint64
}

type muxer struct {
	tracks       []*track
	inits        []*initInfo // per input
	files        []string
	fileIdx      map[string]int
	progressive  bool
	totalSamples uint64
	// origin is the source time of output presentation 0 (movie timescale) of
	// fragmented MP4 inputs (finish): the offset of an OriginMedia window.
	origin uint64
}

// chunkSource writes the sample data of progressive chunks, which writeProgressive
// requests in chunkOrder.
type chunkSource interface {
	writeChunk(ctx context.Context, track int, c *chunk, w io.Writer) error
	close()
}

// runSource yields the runs of one source of fragmented output in decode order
// (io.EOF at the end) and writes their sample data.
type runSource interface {
	next(ctx context.Context) (*fragRun, error)
	writeRun(ctx context.Context, r *fragRun, w io.Writer) error
	close()
}

// fileChunks copies the chunks of fragmented MP4 inputs from their files.
type fileChunks struct {
	m   *muxer
	fc  *fileCache
	buf []byte
}

func (s *fileChunks) writeChunk(ctx context.Context, _ int, c *chunk, w io.Writer) error {
	return s.fc.copy(ctx, s.m.files[c.file], c.off, c.size, w, s.buf)
}

func (s *fileChunks) close() { s.fc.close() }

// scanRuns replays the runs of one fragmented MP4 input, copying their data from
// the files.
type scanRuns struct {
	s   *scanner
	fc  *fileCache
	buf []byte
}

func (s *scanRuns) next(ctx context.Context) (*fragRun, error) { return s.s.next(ctx) }

func (s *scanRuns) writeRun(ctx context.Context, r *fragRun, w io.Writer) error {
	return s.fc.copy(ctx, r.path, r.offset, r.size, w, s.buf)
}

func (s *scanRuns) close() { s.s.close() }

func assembleMP4(ctx context.Context, inputs []Input, w io.Writer, win Window, res *Result) error {
	m := &muxer{inits: make([]*initInfo, len(inputs)), fileIdx: map[string]int{}, progressive: true}
	for i, in := range inputs {
		if err := m.collectInput(ctx, newScanner(m, i, in)); err != nil {
			return err
		}
	}
	if err := m.finish(); err != nil {
		return err
	}
	if !win.IsZero() {
		offset := m.firstInputOffset()
		if win.Origin == OriginMedia {
			offset = movieDuration(m.origin)
		}
		r, err := m.applyWindow(win, offset)
		if err != nil {
			return err
		}
		*res = r
	}
	fc := newFileCache()
	defer fc.close()
	buf := make([]byte, 1<<20)
	if m.progressive {
		return m.writeProgressive(ctx, w, &fileChunks{m: m, fc: fc, buf: buf})
	}
	srcs := make([]runSource, len(inputs))
	for i, in := range inputs {
		s := newScanner(m, i, in)
		s.replay = true
		srcs[i] = &scanRuns{s: s, fc: fc, buf: buf}
	}
	return m.writeFragmented(ctx, srcs, w)
}

// collectInput adds every run of the input s reads to its track.
func (m *muxer) collectInput(ctx context.Context, s *scanner) error {
	defer s.close()
	for {
		r, err := s.next(ctx)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := m.collect(r); err != nil {
			return withContext(err, "input %d", s.idx)
		}
	}
}

func (m *muxer) fileID(path string) int {
	if id, ok := m.fileIdx[path]; ok {
		return id
	}
	m.files = append(m.files, path)
	m.fileIdx[path] = len(m.files) - 1
	return len(m.files) - 1
}

// collect adds a run to its track.
func (m *muxer) collect(r *fragRun) error {
	t := m.tracks[r.track]
	n := uint64(len(r.samples))
	if t.nSamples+n > math.MaxUint32 {
		return errf(ErrUnsupported, "track has more than %d samples", uint32(math.MaxUint32))
	}
	if !t.started {
		t.started, t.firstSrc = true, r.srcTime
	}
	m.totalSamples += n
	if m.progressive && m.totalSamples > maxProgressiveSamples {
		m.progressive = false
		for _, u := range m.tracks {
			u.sizes, u.durs, u.ctos, u.syncs, u.chunks = nil, nil, nil, nil, nil
		}
	}
	if m.progressive {
		// The scanner placed the run on the output timeline; a gap or overlap with
		// the previous run lengthens or shortens the previous sample.
		if t.nSamples > 0 && r.outTime != t.next {
			last := len(t.durs) - 1
			if r.outTime > t.next {
				t.durs[last] += uint32(r.outTime - t.next)
			} else {
				t.durs[last] -= uint32(t.next - r.outTime)
			}
		}
		limit := uint64(t.src.timescale) * chunkSeconds
		c := chunk{file: r.file, off: r.offset, sdi: r.sdi, time: r.outTime, first: t.nSamples}
		var cdur uint64
		for i, s := range r.samples {
			if c.n > 0 && cdur+uint64(s.dur) > limit {
				t.chunks = append(t.chunks, c)
				c = chunk{file: r.file, off: c.off + c.size, sdi: r.sdi, time: c.time + cdur, first: t.nSamples + uint64(i)}
				cdur = 0
			}
			num := uint32(t.nSamples) + uint32(i) + 1
			t.sizes = append(t.sizes, s.size)
			t.durs = append(t.durs, s.dur)
			t.ctos = append(t.ctos, s.cto)
			if s.sync() {
				t.syncs = append(t.syncs, num)
			} else {
				t.nonSync = true
			}
			if s.cto != 0 {
				t.anyCTO = true
			}
			if s.cto < t.minCTO {
				t.minCTO = s.cto
			}
			c.n++
			c.size += int64(s.size)
			cdur += uint64(s.dur)
		}
		t.chunks = append(t.chunks, c)
		t.payload += r.size
	}
	t.next = r.outTime + r.dur
	t.nSamples += n
	return nil
}

// finish checks the tracks and works out their timing: tracks are aligned on
// their first decode times, and the edit of each initialization segment is kept.
func (m *muxer) finish() error {
	starts := make([]uint64, len(m.tracks))
	origin := uint64(math.MaxUint64)
	for i, t := range m.tracks {
		if t.nSamples == 0 {
			return errf(ErrMalformed, "track %d of input %d has no samples", t.src.trackID, t.input)
		}
		ts := t.src.timescale
		srcMovie := m.inits[t.input].movieTimescale
		if srcMovie == 0 {
			srcMovie = ts
		}
		starts[i] = satAdd(scaleTime(t.firstSrc, movieTimescale, ts), scaleTime(t.src.emptyEdit, movieTimescale, srcMovie))
		origin = min(origin, starts[i])
	}
	m.origin = origin
	for i, t := range m.tracks {
		ts := t.src.timescale
		t.empty = starts[i] - origin
		mt := t.src.mediaTime
		if mt < 0 || uint64(mt) >= t.next {
			// Not an offset into this track; an edit written on the absolute
			// timeline of the source is made relative to its first sample.
			if mt > 0 && uint64(mt) >= t.firstSrc && uint64(mt)-t.firstSrc < t.next {
				mt -= int64(t.firstSrc)
			} else {
				mt = 0
			}
		}
		t.mediaTime = mt
		if m.progressive && t.minCTO < 0 {
			// Progressive tables use non-negative offsets (ctts version 0); the edit
			// list absorbs the shift so that presentation times do not change.
			t.shift = -int64(t.minCTO)
		}
		t.presDur = scaleTime(t.next-uint64(mt), movieTimescale, ts)
		t.tkhdDur = satAdd(t.empty, t.presDur)
		t.offset = scaleTime(t.empty, ts, movieTimescale)
	}
	return nil
}

// hasEdit reports whether the track needs an edit list. Fragmented output aligns
// the tracks through their decode times instead of empty edits.
func (t *track) hasEdit(progressive bool) bool {
	if t.edit {
		return true
	}
	if progressive {
		return t.empty > 0 || t.mediaTime+t.shift != 0
	}
	return t.mediaTime != 0
}

func (t *track) edts(progressive bool) *mp4.EdtsBox {
	if !t.hasEdit(progressive) {
		return nil
	}
	elst := &mp4.ElstBox{}
	if progressive && t.empty > 0 {
		elst.Entries = append(elst.Entries, mp4.ElstEntry{SegmentDuration: t.empty, MediaTime: -1, MediaRateInteger: 1})
	}
	mt := t.mediaTime
	if progressive {
		mt += t.shift
	}
	elst.Entries = append(elst.Entries, mp4.ElstEntry{SegmentDuration: t.presDur, MediaTime: mt, MediaRateInteger: 1})
	for _, e := range elst.Entries {
		if e.SegmentDuration > math.MaxUint32 || e.MediaTime > math.MaxInt32 {
			elst.Version = 1
		}
	}
	edts := &mp4.EdtsBox{}
	edts.AddChild(elst)
	return edts
}

// buildMoov builds the movie box. Progressive output gets complete sample tables
// with chunk offsets still to be assigned; fragmented output gets empty tables
// and a movie extends box.
func (m *muxer) buildMoov(progressive, co64 bool) (*mp4.MoovBox, error) {
	moov := mp4.NewMoovBox()
	mvhd := mp4.CreateMvhd()
	mvhd.Timescale = movieTimescale
	var dur uint64
	for _, t := range m.tracks {
		dur = max(dur, t.tkhdDur)
	}
	if progressive {
		mvhd.Duration = dur
		if dur > math.MaxUint32 {
			mvhd.Version = 1
		}
	}
	mvhd.NextTrackID = uint32(len(m.tracks)) + 1
	moov.AddChild(mvhd)
	for _, t := range m.tracks {
		trak, err := m.buildTrak(t, progressive, co64)
		if err != nil {
			return nil, err
		}
		moov.AddChild(trak)
	}
	if !progressive {
		mvex := mp4.NewMvexBox()
		mehd := &mp4.MehdBox{FragmentDuration: int64(min(dur, math.MaxInt64))}
		if dur > math.MaxUint32 {
			mehd.Version = 1
		}
		mvex.AddChild(mehd)
		for _, t := range m.tracks {
			mvex.AddChild(mp4.CreateTrex(t.outID))
		}
		moov.AddChild(mvex)
	}
	return moov, nil
}

func (m *muxer) buildTrak(t *track, progressive, co64 bool) (*mp4.TrakBox, error) {
	src := t.src.trak
	tkhd := *src.Tkhd
	tkhd.TrackID = t.outID
	tkhd.Flags |= 0x000003 // track_enabled, track_in_movie
	tkhd.Duration = 0
	mediaDur := uint64(0)
	if progressive {
		tkhd.Duration = t.tkhdDur
		mediaDur = t.next
	}
	tkhd.Version = 0
	if tkhd.Duration > math.MaxUint32 || tkhd.CreationTime > math.MaxUint32 || tkhd.ModificationTime > math.MaxUint32 {
		tkhd.Version = 1
	}
	trak := mp4.NewTrakBox()
	trak.AddChild(&tkhd)
	if edts := t.edts(progressive); edts != nil {
		trak.AddChild(edts)
	}

	mdia := mp4.NewMdiaBox()
	mdhd := *src.Mdia.Mdhd
	mdhd.Duration = mediaDur
	mdhd.Version = 0
	if mdhd.Duration > math.MaxUint32 || mdhd.CreationTime > math.MaxUint32 || mdhd.ModificationTime > math.MaxUint32 {
		mdhd.Version = 1
	}
	mdia.AddChild(&mdhd)
	mdia.AddChild(src.Mdia.Hdlr)
	if src.Mdia.Elng != nil {
		mdia.AddChild(src.Mdia.Elng)
	}

	minf := mp4.NewMinfBox()
	hasHeader := false
	if src.Mdia.Minf != nil {
		for _, c := range src.Mdia.Minf.Children {
			switch c.Type() {
			case "dinf", "stbl":
				continue
			case "vmhd", "smhd", "sthd", "nmhd", "hmhd":
				hasHeader = true
			}
			minf.AddChild(c)
		}
	}
	if !hasHeader {
		if t.src.handler == "vide" {
			minf.AddChild(mp4.CreateVmhd())
		} else {
			minf.AddChild(mp4.CreateSmhd())
		}
	}
	dinf := &mp4.DinfBox{}
	dinf.AddChild(mp4.CreateDref()) // the samples are in this file
	minf.AddChild(dinf)
	stbl, err := m.buildStbl(t, progressive, co64)
	if err != nil {
		return nil, err
	}
	minf.AddChild(stbl)
	mdia.AddChild(minf)
	trak.AddChild(mdia)
	return trak, nil
}

func (m *muxer) buildStbl(t *track, progressive, co64 bool) (*mp4.StblBox, error) {
	stbl := mp4.NewStblBox()
	stbl.AddChild(t.src.stsd)
	if !progressive {
		stbl.AddChild(&mp4.SttsBox{})
		stbl.AddChild(&mp4.StscBox{})
		stbl.AddChild(&mp4.StszBox{})
		stbl.AddChild(&mp4.StcoBox{})
		m.addSampleGroups(t, stbl)
		return stbl, nil
	}
	n := len(t.sizes)

	stts := &mp4.SttsBox{}
	for i := 0; i < n; {
		j := i + 1
		for j < n && t.durs[j] == t.durs[i] {
			j++
		}
		stts.SampleCount = append(stts.SampleCount, uint32(j-i))
		stts.SampleTimeDelta = append(stts.SampleTimeDelta, t.durs[i])
		i = j
	}
	stbl.AddChild(stts)

	if t.anyCTO {
		var counts []uint32
		var offsets []int32
		for i := 0; i < n; {
			j := i + 1
			for j < n && t.ctos[j] == t.ctos[i] {
				j++
			}
			off := int64(t.ctos[i]) + t.shift
			if off > math.MaxInt32 {
				return nil, errf(ErrUnsupported, "composition offsets span more than 2^31 ticks")
			}
			counts = append(counts, uint32(j-i))
			offsets = append(offsets, int32(off))
			i = j
		}
		ctts := &mp4.CttsBox{}
		if err := ctts.AddSampleCountsAndOffset(counts, offsets); err != nil {
			return nil, err
		}
		stbl.AddChild(ctts)
	}

	stsc := &mp4.StscBox{}
	for i, c := range t.chunks {
		if i == 0 || c.n != t.chunks[i-1].n || c.sdi != t.chunks[i-1].sdi {
			if err := stsc.AddEntry(uint32(i+1), c.n, c.sdi); err != nil {
				return nil, err
			}
		}
	}
	stbl.AddChild(stsc)

	uniform := n > 0
	for _, s := range t.sizes {
		if s != t.sizes[0] {
			uniform = false
			break
		}
	}
	if uniform && t.sizes[0] > 0 {
		stbl.AddChild(&mp4.StszBox{SampleUniformSize: t.sizes[0], SampleNumber: uint32(n)})
	} else {
		stbl.AddChild(&mp4.StszBox{SampleNumber: uint32(n), SampleSize: t.sizes})
	}

	if co64 {
		t.co64, t.stco = &mp4.Co64Box{ChunkOffset: make([]uint64, len(t.chunks))}, nil
		stbl.AddChild(t.co64)
	} else {
		t.stco, t.co64 = &mp4.StcoBox{ChunkOffset: make([]uint32, len(t.chunks))}, nil
		stbl.AddChild(t.stco)
	}
	if t.nonSync {
		stbl.AddChild(&mp4.StssBox{SampleNumber: t.syncs})
	}
	m.addSampleGroups(t, stbl)
	return stbl, nil
}

// addSampleGroups keeps the sample group descriptions of the initialization
// segment; without sample-to-group boxes only their default entries apply, as
// they did to the fragments.
func (m *muxer) addSampleGroups(t *track, stbl *mp4.StblBox) {
	src := t.src.trak.Mdia.Minf.Stbl
	for _, g := range src.Sgpds {
		stbl.AddChild(g)
	}
}

func (m *muxer) ftyp(progressive bool) *mp4.FtypBox {
	avc := false
	for _, t := range m.tracks {
		if t.src.stsd.AvcX != nil {
			avc = true
		}
	}
	brands := []string{"isom", "iso2"}
	if !progressive {
		brands = []string{"isom", "iso6"}
	}
	if avc {
		brands = append(brands, "avc1")
	}
	brands = append(brands, "mp41")
	return mp4.NewFtyp("isom", 0x200, brands)
}

type chunkRef struct {
	track int
	idx   int
	key   uint64
}

// chunkOrder interleaves the chunks of all tracks by their start on the movie
// timeline (microseconds), keeping track order for equal times.
func (m *muxer) chunkOrder() []chunkRef {
	var refs []chunkRef
	for ti, t := range m.tracks {
		for ci, c := range t.chunks {
			key := satAdd(scaleTime(t.empty, 1_000_000, movieTimescale), scaleTime(c.time, 1_000_000, t.src.timescale))
			refs = append(refs, chunkRef{track: ti, idx: ci, key: key})
		}
	}
	slices.SortStableFunc(refs, func(a, b chunkRef) int {
		switch {
		case a.key < b.key:
			return -1
		case a.key > b.key:
			return 1
		}
		return a.track - b.track
	})
	return refs
}

// writeProgressive writes ftyp, moov and one mdat whose chunks src supplies.
func (m *muxer) writeProgressive(ctx context.Context, w io.Writer, src chunkSource) error {
	var payload int64
	for _, t := range m.tracks {
		if t.payload > math.MaxInt64-payload {
			return errf(ErrUnsupported, "sample data too large")
		}
		payload += t.payload
	}
	order := m.chunkOrder()
	ftyp := m.ftyp(true)
	mdatHdr := uint64(8)
	if uint64(payload)+8 > largeThreshold {
		mdatHdr = 16
	}
	var moov *mp4.MoovBox
	var dataStart uint64
	for _, co64 := range []bool{false, true} {
		var err error
		if moov, err = m.buildMoov(true, co64); err != nil {
			return err
		}
		dataStart = ftyp.Size() + moov.Size() + mdatHdr
		if co64 || dataStart+uint64(payload) <= largeThreshold {
			break
		}
	}
	pos := dataStart
	for _, ref := range order {
		t := m.tracks[ref.track]
		if t.stco != nil {
			t.stco.ChunkOffset[ref.idx] = uint32(pos)
		} else {
			t.co64.ChunkOffset[ref.idx] = pos
		}
		pos += uint64(t.chunks[ref.idx].size)
	}
	if err := ftyp.Encode(w); err != nil {
		return fmt.Errorf("remux: write output: %w", err)
	}
	if err := moov.Encode(w); err != nil {
		return fmt.Errorf("remux: write output: %w", err)
	}
	if err := writeMdatHeader(w, uint64(payload), mdatHdr); err != nil {
		return err
	}
	for _, ref := range order {
		if err := src.writeChunk(ctx, ref.track, &m.tracks[ref.track].chunks[ref.idx], w); err != nil {
			return err
		}
	}
	return nil
}

func writeMdatHeader(w io.Writer, payload, hdr uint64) error {
	var b [16]byte
	if hdr == 16 {
		binary.BigEndian.PutUint32(b[0:4], 1)
		copy(b[4:8], "mdat")
		binary.BigEndian.PutUint64(b[8:16], payload+16)
	} else {
		binary.BigEndian.PutUint32(b[0:4], uint32(payload+8))
		copy(b[4:8], "mdat")
	}
	if _, err := w.Write(b[:hdr]); err != nil {
		return fmt.Errorf("remux: write output: %w", err)
	}
	return nil
}

// writeFragmented writes a fragmented MP4: one movie box for all tracks, then
// one movie fragment per run of srcs, interleaved by decode time. The sources
// read their inputs a second time, so memory stays bounded by one run per source.
func (m *muxer) writeFragmented(ctx context.Context, srcs []runSource, w io.Writer) error {
	defer func() {
		for _, s := range srcs {
			s.close()
		}
	}()
	moov, err := m.buildMoov(false, false)
	if err != nil {
		return err
	}
	if err := m.ftyp(false).Encode(w); err != nil {
		return fmt.Errorf("remux: write output: %w", err)
	}
	if err := moov.Encode(w); err != nil {
		return fmt.Errorf("remux: write output: %w", err)
	}
	heads := make([]*fragRun, len(srcs))
	for i, s := range srcs {
		if heads[i], err = nextOrNil(ctx, s); err != nil {
			return err
		}
	}
	seq := uint32(1)
	for {
		best := -1
		var bestKey uint64
		for i, r := range heads {
			if r == nil {
				continue
			}
			t := m.tracks[r.track]
			key := scaleTime(satAdd(r.outTime, t.offset), 1_000_000, t.src.timescale)
			if best < 0 || key < bestKey {
				best, bestKey = i, key
			}
		}
		if best < 0 {
			return nil
		}
		if err := m.writeFragment(ctx, heads[best], seq, w, srcs[best]); err != nil {
			return err
		}
		seq++
		if heads[best], err = nextOrNil(ctx, srcs[best]); err != nil {
			return err
		}
	}
}

func nextOrNil(ctx context.Context, s runSource) (*fragRun, error) {
	r, err := s.next(ctx)
	if err == io.EOF {
		return nil, nil
	}
	return r, err
}

func (m *muxer) writeFragment(ctx context.Context, r *fragRun, seq uint32, w io.Writer, src runSource) error {
	t := m.tracks[r.track]
	moof := &mp4.MoofBox{}
	traf := &mp4.TrafBox{}
	tfhd := mp4.CreateTfhd(t.outID)
	tfhd.Flags = mp4.TfhdDefaultBaseIsMoofFlag | mp4.TfhdSampleDescriptionIndexPresentFlag
	tfhd.SampleDescriptionIndex = r.sdi
	trun := mp4.CreateTrun(0)
	trun.Samples = make([]mp4.Sample, len(r.samples))
	for i, s := range r.samples {
		trun.Samples[i] = mp4.Sample{Flags: s.flags, Dur: s.dur, Size: s.size, CompositionTimeOffset: s.cto}
	}
	for _, err := range []error{
		moof.AddChild(mp4.CreateMfhd(seq)),
		traf.AddChild(tfhd),
		traf.AddChild(mp4.CreateTfdt(satAdd(r.outTime, t.offset))),
		traf.AddChild(trun),
		moof.AddChild(traf),
	} {
		if err != nil {
			return fmt.Errorf("remux: build movie fragment: %w", err)
		}
	}
	mdatHdr := uint64(8)
	if uint64(r.size)+8 > largeThreshold {
		mdatHdr = 16
	}
	trun.DataOffset = int32(moof.Size() + mdatHdr)
	if err := moof.Encode(w); err != nil {
		return fmt.Errorf("remux: write output: %w", err)
	}
	if err := writeMdatHeader(w, uint64(r.size), mdatHdr); err != nil {
		return err
	}
	return src.writeRun(ctx, r, w)
}

// fileCache keeps a few source files open while chunks of several tracks are
// interleaved.
type fileCache struct {
	files map[string]*os.File
	order []string
}

func newFileCache() *fileCache { return &fileCache{files: map[string]*os.File{}} }

func (c *fileCache) get(path string) (*os.File, error) {
	if f, ok := c.files[path]; ok {
		return f, nil
	}
	if len(c.order) >= 8 {
		old := c.order[0]
		c.order = c.order[1:]
		_ = c.files[old].Close()
		delete(c.files, old)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("remux: %w", err)
	}
	c.files[path] = f
	c.order = append(c.order, path)
	return f, nil
}

func (c *fileCache) close() {
	for _, f := range c.files {
		_ = f.Close()
	}
	c.files, c.order = map[string]*os.File{}, nil
}

// copy copies size bytes at off of the file at path to w.
func (c *fileCache) copy(ctx context.Context, path string, off, size int64, w io.Writer, buf []byte) error {
	f, err := c.get(path)
	if err != nil {
		return err
	}
	for size > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := int64(len(buf))
		if size < n {
			n = size
		}
		if err := readFull(f, buf[:n], off); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return errf(ErrMalformed, "fragment file shrank while it was assembled")
			}
			return fmt.Errorf("remux: read fragment: %w", err)
		}
		if _, err := w.Write(buf[:n]); err != nil {
			return fmt.Errorf("remux: write output: %w", err)
		}
		off += n
		size -= n
	}
	return nil
}

// scaleTime converts v from timescale from to timescale to, rounding to nearest
// and saturating.
func scaleTime(v uint64, to, from uint32) uint64 {
	if from == 0 || to == from {
		return v
	}
	hi, lo := bits.Mul64(v, uint64(to))
	var c uint64
	lo, c = bits.Add64(lo, uint64(from)/2, 0)
	hi += c
	if hi >= uint64(from) {
		return math.MaxUint64
	}
	q, _ := bits.Div64(hi, lo, uint64(from))
	return q
}

func satAdd(a, b uint64) uint64 {
	if s, c := bits.Add64(a, b, 0); c == 0 {
		return s
	}
	return math.MaxUint64
}
