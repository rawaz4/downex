package remux

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
)

// timeline places the runs of one track on the output decode timeline: the
// first run starts at 0, runs follow each other, and the decode time (tfdt) of
// a run is honoured as long as it is a plausible gap or overlap.
type timeline struct {
	started bool
	srcNext uint64 // source decode time expected next
	next    uint64 // output decode time of the next run
	lastDur uint32 // duration of the previous run's last sample
}

func (tl *timeline) place(r *fragRun, timescale uint32) error {
	var dur uint64
	for _, s := range r.samples {
		dur += uint64(s.dur)
	}
	switch {
	case !tl.started:
		tl.started = true
		r.srcTime, r.outTime = 0, 0
		if r.hasTime {
			r.srcTime = r.time
		}
	case !r.hasTime:
		r.srcTime, r.outTime = tl.srcNext, tl.next
	default:
		r.srcTime, r.outTime = r.time, tl.next
		maxGap := uint64(timescale) * maxGapSeconds
		switch {
		case r.time > tl.srcNext:
			// A gap: the previous sample is lengthened (progressive) or the next
			// fragment starts later (fragmented).
			if d := r.time - tl.srcNext; d <= maxGap && uint64(tl.lastDur)+d <= math.MaxUint32 {
				r.outTime = tl.next + d
			}
		case r.time < tl.srcNext:
			// An overlap shorter than the previous sample shortens it.
			if d := tl.srcNext - r.time; d < uint64(tl.lastDur) {
				r.outTime = tl.next - d
			}
		}
	}
	r.dur = dur
	if r.outTime > math.MaxInt64-dur {
		return errf(ErrMalformed, "track duration overflows")
	}
	tl.srcNext = satAdd(r.srcTime, dur)
	tl.next = r.outTime + dur
	tl.lastDur = r.samples[len(r.samples)-1].dur
	return nil
}

// scanner reads the runs of one input in order, one movie fragment at a time: it
// holds the runs of a single 'moof' box, never those of a whole file, so its memory is
// bounded by maxMoofSamples and maxMoofRuns. A file stays open while it has movie
// fragments left; close releases it early.
type scanner struct {
	m      *muxer
	idx    int
	paths  []string
	init   bool // paths[0] is the initialization segment
	pos    int  // next file to open
	replay bool // second pass: the tracks exist already

	info   *initInfo
	tracks map[uint32]int // source track ID → output track index
	tl     map[uint32]*timeline

	// The file being read and its movie fragments not parsed yet.
	src    io.ReaderAt
	closer io.Closer
	cur    int // index in paths of that file
	fileID int
	path   string
	moofs  []boxHeader
	mdats  []mdatRange

	runs   []fragRun // the kept runs of the last movie fragment
	ri     int
	buf    []byte
	parser fragmentParser
}

func newScanner(m *muxer, idx int, in Input) *scanner {
	s := &scanner{m: m, idx: idx, paths: in.Fragments}
	if in.Init != "" {
		s.paths = append([]string{in.Init}, in.Fragments...)
		s.init = true
	}
	s.parser.defaults = func(id uint32) (trexDefaults, bool) {
		d, ok := s.info.trex[id]
		return d, ok
	}
	return s
}

// next returns the next run of the input, or io.EOF. The run is valid until the
// following call.
func (s *scanner) next(ctx context.Context) (*fragRun, error) {
	for s.ri >= len(s.runs) {
		s.runs, s.ri = s.runs[:0], 0
		if len(s.moofs) > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := s.nextMoof(); err != nil {
				s.close()
				return nil, withContext(err, "input %d: %s", s.idx, s.describe(s.cur))
			}
			continue
		}
		s.close()
		if s.pos >= len(s.paths) {
			if s.info == nil {
				return nil, errf(ErrMalformed, "input %d: no initialization segment ('moov' box)", s.idx)
			}
			return nil, io.EOF
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.cur = s.pos
		s.pos++
		if err := s.open(s.paths[s.cur]); err != nil {
			s.close()
			return nil, withContext(err, "input %d: %s", s.idx, s.describe(s.cur))
		}
	}
	r := &s.runs[s.ri]
	s.ri++
	return r, nil
}

// close releases the file being read, if any.
func (s *scanner) close() {
	if s.closer != nil {
		_ = s.closer.Close()
	}
	s.src, s.closer, s.moofs, s.mdats = nil, nil, nil, nil
}

func (s *scanner) describe(pos int) string {
	if s.init {
		if pos == 0 {
			return "initialization segment"
		}
		pos--
	}
	return fmt.Sprintf("fragment %d", pos)
}

// open opens the file at path and reads its top-level boxes (scanFile). The scanner
// owns the open file until close.
func (s *scanner) open(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	s.closer = f
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() == 0 {
		return errf(ErrMalformed, "empty file")
	}
	return s.scanFile(f, st.Size(), s.m.fileID(path), path)
}

// scanFile starts reading one file: an initialization segment ('moov'), movie
// fragments ('moof' + 'mdat'), or both. It parses the 'moov' boxes and lists the
// movie fragments, which nextMoof then parses one by one from r. Other boxes (styp,
// sidx, emsg, prft, free, ...) are skipped.
func (s *scanner) scanFile(r io.ReaderAt, size int64, fileID int, path string) error {
	boxes, err := readTopBoxes(r, size)
	if err != nil {
		return err
	}
	s.src, s.fileID, s.path = r, fileID, path
	s.moofs, s.mdats = nil, nil
	var payload int64 // 'mdat' payload bytes
	for _, b := range boxes {
		switch b.typ {
		case "moov":
			if b.size > maxMoovBytes {
				return errf(ErrMalformed, "'moov' box larger than %d bytes", maxMoovBytes)
			}
			data, err := s.read(r, b)
			if err != nil {
				return err
			}
			info, err := parseInit(data)
			if err != nil {
				return err
			}
			if err := s.setInit(info); err != nil {
				return err
			}
		case "moof":
			if s.info == nil {
				return errf(ErrMalformed, "movie fragment before the initialization segment")
			}
			if b.size > maxMoofBytes {
				return errf(ErrMalformed, "'moof' box larger than %d bytes", maxMoofBytes)
			}
			s.moofs = append(s.moofs, b)
		case "mdat":
			m := mdatRange{start: b.payloadOff(), end: b.end()}
			s.mdats = append(s.mdats, m)
			payload += m.end - m.start
		}
	}
	if s.init && s.cur == 0 && s.info == nil {
		return errf(ErrMalformed, "initialization segment has no 'moov' box")
	}
	s.parser.beginFile(payload)
	return nil
}

// nextMoof parses the next movie fragment of the current file into s.runs: the runs
// of the kept tracks, placed on their timelines.
func (s *scanner) nextMoof() error {
	b := s.moofs[0]
	s.moofs = s.moofs[1:]
	data, err := s.read(s.src, b)
	if err != nil {
		return err
	}
	runs, err := s.parser.parseMoof(data, b.off, int(b.hdr))
	if err != nil {
		return err
	}
	if err := checkRunData(runs, s.mdats); err != nil {
		return err
	}
	for i := range runs {
		run := &runs[i]
		ti, ok := s.tracks[run.trackID]
		if !ok {
			continue // a track that is not kept
		}
		t := s.m.tracks[ti]
		if run.sdi == 0 {
			run.sdi = 1
		}
		if run.sdi > t.src.nEntries {
			return errf(ErrMalformed, "track %d refers to sample description %d of %d", run.trackID, run.sdi, t.src.nEntries)
		}
		if err := s.tl[run.trackID].place(run, t.src.timescale); err != nil {
			return err
		}
		run.track, run.file, run.path = ti, s.fileID, s.path
		s.runs = append(s.runs, *run)
	}
	return nil
}

func (s *scanner) read(r io.ReaderAt, b boxHeader) ([]byte, error) {
	if int64(cap(s.buf)) < b.size {
		s.buf = make([]byte, b.size)
	}
	data := s.buf[:b.size]
	if err := readFull(r, data, b.off); err != nil {
		return nil, err
	}
	return data, nil
}

// setInit installs the input's initialization segment. A repeated one must
// describe the same tracks.
func (s *scanner) setInit(info *initInfo) error {
	if s.info != nil {
		if !sameTracks(s.info, info) {
			return errf(ErrUnsupported, "initialization segment changes mid-stream")
		}
		return nil
	}
	if prev := s.m.inits[s.idx]; s.replay {
		if prev == nil || !sameTracks(prev, info) {
			return errf(ErrMalformed, "initialization segment changed between passes")
		}
		info = prev
	}
	s.info = info
	s.tracks = map[uint32]int{}
	s.tl = map[uint32]*timeline{}
	for _, it := range info.tracks {
		s.tl[it.trackID] = &timeline{}
		if s.replay {
			for ti, t := range s.m.tracks {
				if t.input == s.idx && t.src == it {
					s.tracks[it.trackID] = ti
				}
			}
			continue
		}
		s.m.tracks = append(s.m.tracks, &track{src: it, input: s.idx, outID: uint32(len(s.m.tracks) + 1)})
		s.tracks[it.trackID] = len(s.m.tracks) - 1
	}
	if !s.replay {
		s.m.inits[s.idx] = info
	}
	return nil
}
