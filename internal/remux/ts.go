package remux

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/media"
	"downex/internal/remux/mpegts"
)

// Transport stream to MP4 conversion (docs/VIDEO-DETECT-SPEC.md §4). Pass 1
// demultiplexes every input, converts its units and keeps only per-sample
// metadata (the muxer's tables) and per-domain bounds; the domains are then
// placed on one timeline. Pass 2 converts the units again, per track, and writes
// their data: into the progressive mdat, or as in-memory runs of a fragmented
// MP4. Every sample's size is checked against pass 1.

// maxRunBytes bounds the sample data of one run of transport stream samples.
const maxRunBytes = 16 << 20

// afterTSPass1 runs between the two passes (tests change the fragments there).
var afterTSPass1 = func() {}

// tsSample is one converted sample of a transport stream track, on its domain's
// source timeline (track timescale).
type tsSample struct {
	data   []byte // length-prefixed NAL units or a raw AAC frame
	dts    int64
	cto    int32
	dur    uint32 // for the last sample of a domain: the one before repeated
	sync   bool
	sdi    uint32 // 1-based sample description index
	domain int
	last   bool // the last sample of its domain
}

func (s *tsSample) flags() uint32 {
	if s.sync {
		return mp4.SyncSampleFlags
	}
	return mp4.NonSyncSampleFlags | mp4.SampleDependsOn1
}

// tsConverter turns the PES units of one elementary stream into samples. It is
// deterministic: both passes get the same samples from the same units.
type tsConverter interface {
	// push converts one unit; pts and dts are unwrapped (has: the unit has a PTS),
	// domain is the discontinuity domain of the file it started in. emit receives
	// every finished sample (its data valid during the call).
	push(u *mpegts.Unit, pts, dts int64, has bool, domain int, emit func(*tsSample) error) error
	// flush emits what is pending at the end of the stream.
	flush(emit func(*tsSample) error) error
	timescale() uint32
	sampleEntries() []mp4.Box
}

// role is what a stream is for in its input.
type role int

const (
	roleVideo role = iota
	roleAudio
)

func (r role) String() string {
	if r == roleVideo {
		return "video"
	}
	return "audio"
}

// streamClass is what Assemble does with a stream type that fills a role.
type streamClass int

const (
	classIgnored     streamClass = iota // not video or audio: ID3, SCTE-35, private data
	classSupported                      // H.264, HEVC, AAC in ADTS
	classEncrypted                      // SAMPLE-AES
	classUnsupported                    // another codec
)

// classify returns the role a stream would fill and what it is.
func classify(s mpegts.Stream) (role, streamClass) {
	switch s.Type {
	case mpegts.TypeH264, mpegts.TypeHEVC:
		return roleVideo, classSupported
	case mpegts.TypeSAESH264:
		return roleVideo, classEncrypted
	case mpegts.TypeMPEG1Video, mpegts.TypeMPEG2Video, mpegts.TypeMPEG4Video, mpegts.TypeVVC, mpegts.TypeVC1:
		return roleVideo, classUnsupported
	case mpegts.TypeAAC:
		return roleAudio, classSupported
	case mpegts.TypeSAESAAC, mpegts.TypeSAESAC3, mpegts.TypeSAESEAC3:
		return roleAudio, classEncrypted
	case mpegts.TypeMPEG1Audio, mpegts.TypeMPEG2Audio, mpegts.TypeLATM, mpegts.TypeAACRaw, mpegts.TypeAC3, mpegts.TypeEAC3:
		return roleAudio, classUnsupported
	case mpegts.TypePrivatePES:
		if s.Dolby {
			return roleAudio, classUnsupported
		}
	}
	return roleVideo, classIgnored
}

func roleOfType(t mpegts.StreamType) role {
	if t == mpegts.TypeAAC {
		return roleAudio
	}
	return roleVideo
}

// roles chooses the streams of a program that fill the roles of an input of one
// kind (§4.1): video and muxed inputs the first video stream, audio and muxed
// inputs the first audio stream, in PMT order. The first program must have the
// roles the kind requires; a role's type is fixed where the role first appears (a
// muxed input's audio may first appear in a later program, after a video-only
// slate), and a new PMT version may move a role to a new PID but not change its
// type.
type roles struct {
	kind  Kind
	fixed bool
	have  [2]bool
	typ   [2]mpegts.StreamType
	lang  [2]string
	pid   [2]int // -1 = none in the program in effect
}

func newRoles(k Kind) *roles { return &roles{kind: k, pid: [2]int{-1, -1}} }

func (r *roles) want(ro role) bool {
	if ro == roleVideo {
		return r.kind != KindAudio
	}
	return r.kind != KindVideo
}

func (r *roles) required(ro role) bool {
	if ro == roleVideo {
		return r.kind != KindAudio
	}
	return r.kind == KindAudio
}

// check is the demuxer's program check.
func (r *roles) check(p mpegts.Program) error {
	var pick [2]*mpegts.Stream
	for i := range p.Streams {
		ro, class := classify(p.Streams[i])
		if class != classIgnored && pick[ro] == nil {
			pick[ro] = &p.Streams[i]
		}
	}
	for _, ro := range []role{roleVideo, roleAudio} {
		r.pid[ro] = -1
		s := pick[ro]
		if !r.want(ro) {
			continue
		}
		if s == nil {
			if !r.fixed && r.required(ro) {
				if ro == roleVideo {
					return errf(ErrUnsupported, "the program has no H.264 or HEVC video stream")
				}
				return errf(ErrUnsupported, "the program has no AAC audio stream")
			}
			continue
		}
		switch _, class := classify(*s); class {
		case classEncrypted:
			return &remuxError{kind: media.ErrEncrypted, msg: fmt.Sprintf("the %s stream (PID %d) is SAMPLE-AES encrypted (stream type 0x%02x)", ro, s.PID, s.Type)}
		case classUnsupported:
			return errf(ErrUnsupported, "the %s stream (PID %d) has stream type 0x%02x; only H.264, HEVC and AAC (ADTS) are converted", ro, s.PID, s.Type)
		}
		if r.have[ro] && s.Type != r.typ[ro] {
			return errf(ErrUnsupported, "the %s stream changes from type 0x%02x to 0x%02x", ro, r.typ[ro], s.Type)
		}
		r.pid[ro], r.typ[ro], r.have[ro] = int(s.PID), s.Type, true
		if r.lang[ro] == "" {
			r.lang[ro] = s.Language
		}
	}
	r.fixed = true
	return nil
}

func (r *roles) keep(_ mpegts.Program, s mpegts.Stream) bool {
	return int(s.PID) == r.pid[roleVideo] || int(s.PID) == r.pid[roleAudio]
}

func newConverter(ro role, t mpegts.StreamType) tsConverter {
	if ro == roleVideo {
		return newVideoConv(t)
	}
	return newAudioConv()
}

// fileStat is what pass 1 saw of a file.
type fileStat struct {
	size int64
	mod  time.Time
}

// tsInput is one transport stream input.
type tsInput struct {
	idx     int
	in      Input
	files   []string // the initialization section first, when set
	init    bool
	domains []int // per file: its discontinuity domain
	stats   []fileStat
}

func newTSInput(idx int, in Input) *tsInput {
	ti := &tsInput{idx: idx, in: in, files: in.Fragments}
	if in.Init != "" {
		ti.files, ti.init = append([]string{in.Init}, in.Fragments...), true
	}
	ti.domains = make([]int, len(ti.files))
	d, bi := 0, 0
	for j := range in.Fragments {
		if bi < len(in.Breaks) && in.Breaks[bi] == j {
			d, bi = d+1, bi+1
		}
		if ti.init {
			ti.domains[j+1] = d
		} else {
			ti.domains[j] = d
		}
	}
	return ti
}

// describe names file i of the input.
func (ti *tsInput) describe(i int) string {
	if ti.init {
		if i == 0 {
			return "initialization section"
		}
		i--
	}
	return fmt.Sprintf("fragment %d", i)
}

// stat records the size and modification time of every file.
func (ti *tsInput) stat() error {
	ti.stats = make([]fileStat, len(ti.files))
	for i, p := range ti.files {
		st, err := os.Stat(p)
		if err != nil {
			return withContext(err, "input %d: %s", ti.idx, ti.describe(i))
		}
		ti.stats[i] = fileStat{size: st.Size(), mod: st.ModTime()}
	}
	return nil
}

// unchanged checks that every file is as pass 1 saw it.
func (ti *tsInput) unchanged() error {
	for i, p := range ti.files {
		st, err := os.Stat(p)
		if err != nil {
			return withContext(err, "input %d: %s", ti.idx, ti.describe(i))
		}
		if st.Size() != ti.stats[i].size || !st.ModTime().Equal(ti.stats[i].mod) {
			return errf(ErrMalformed, "input %d: %s: fragments changed between passes", ti.idx, ti.describe(i))
		}
	}
	return nil
}

// wrap turns an error of the demuxer into one of this package, with its place.
func (ti *tsInput) wrap(err error) error {
	var me *mpegts.Error
	if !errors.As(err, &me) {
		return withContext(err, "input %d", ti.idx)
	}
	kind := ErrMalformed
	switch {
	case errors.Is(me.Kind, media.ErrEncrypted):
		kind = media.ErrEncrypted
	case errors.Is(me.Kind, mpegts.ErrUnsupported):
		kind = ErrUnsupported
	}
	msg := me.Msg
	if me.PID >= 0 {
		msg = fmt.Sprintf("PID %d: %s", me.PID, msg)
	}
	if me.File >= 0 {
		msg = ti.describe(me.File) + ": " + msg
	}
	return &remuxError{kind: kind, msg: fmt.Sprintf("input %d: %s", ti.idx, msg)}
}

// unitError places an error of a converter at its unit.
func (ti *tsInput) unitError(err error, u *mpegts.Unit) error {
	return withContext(err, "input %d: %s: PID %d", ti.idx, ti.describe(u.File), u.PID)
}

// tsTrack is one output track of a transport stream input.
type tsTrack struct {
	in   *tsInput
	role role
	typ  mpegts.StreamType
	lang string
	conv tsConverter

	m     *muxer
	t     *track // nil until the first sample
	ti    int    // index of t in m.tracks
	timed timedTrack
	count uint64 // samples so far

	// The run being built in pass 1.
	run    fragRun
	runDom int
	runDur uint64

	// Placement (place).
	base    int64 // output decode time of the first sample
	first   int64 // output presentation time of the earliest sample
	presEnd int64
	shift   int64 // added to every composition offset to make them non-negative
	minCTO  int64
}

// add is the converter's emit in pass 1: it records the sample's metadata.
func (tr *tsTrack) add(s *tsSample) error {
	if tr.t == nil {
		ts := tr.conv.timescale()
		h := "vide"
		if tr.role == roleAudio {
			h = "soun"
		}
		tr.t = &track{src: &initTrack{handler: h, timescale: ts}, input: tr.in.idx}
		tr.m.tracks = append(tr.m.tracks, tr.t)
		tr.ti = len(tr.m.tracks) - 1
		tr.timed = timedTrack{input: tr.in.idx, timescale: ts}
	}
	for len(tr.timed.domains) <= s.domain {
		tr.timed.domains = append(tr.timed.domains, trackDomain{})
	}
	dom := &tr.timed.domains[s.domain]
	pts := s.dts + int64(s.cto)
	end := pts + int64(s.dur)
	if dom.n == 0 {
		*dom = trackDomain{first: tr.count, srcDTS: s.dts, minPTS: pts, presEnd: end, minCTO: s.cto}
	}
	dom.minPTS, dom.presEnd, dom.minCTO = min(dom.minPTS, pts), max(dom.presEnd, end), min(dom.minCTO, s.cto)
	dom.lastDTS, dom.lastDur = s.dts, s.dur
	dom.n++
	tr.count++

	limit := uint64(tr.t.src.timescale) * chunkSeconds
	if n := len(tr.run.samples); n > 0 && (s.sdi != tr.run.sdi || s.domain != tr.runDom ||
		tr.runDur+uint64(s.dur) > limit || tr.run.size+int64(len(s.data)) > maxRunBytes) {
		if err := tr.flushRun(); err != nil {
			return err
		}
	}
	if len(tr.run.samples) == 0 {
		tr.run.sdi, tr.runDom = s.sdi, s.domain
	}
	tr.run.samples = append(tr.run.samples, sampleMeta{size: uint32(len(s.data)), dur: s.dur, flags: s.flags(), cto: s.cto})
	tr.run.size += int64(len(s.data))
	tr.runDur += uint64(s.dur)
	if s.last {
		return tr.flushRun()
	}
	return nil
}

// flushRun hands the run being built to the muxer's tables. Runs follow each
// other in pass 1; place fixes the times at domain boundaries afterwards.
func (tr *tsTrack) flushRun() error {
	if len(tr.run.samples) == 0 {
		return nil
	}
	r := &tr.run
	r.track, r.outTime, r.dur = tr.ti, tr.t.next, tr.runDur
	err := tr.m.collect(r)
	r.samples, r.size = r.samples[:0], 0
	tr.runDur = 0
	if err != nil {
		return withContext(err, "input %d", tr.in.idx)
	}
	return nil
}

// tsAssembler converts transport stream inputs to MP4.
type tsAssembler struct {
	m      *muxer
	inputs []*tsInput
	tracks []*tsTrack // in m.tracks order, after finish
	all    []*tsTrack // every track with samples, in creation order
}

func assembleTS(ctx context.Context, inputs []Input, w io.Writer, win Window, res *Result) error {
	ta := &tsAssembler{m: &muxer{inits: make([]*initInfo, len(inputs)), fileIdx: map[string]int{}, progressive: true}}
	for i, in := range inputs {
		ta.inputs = append(ta.inputs, newTSInput(i, in))
	}
	for _, ti := range ta.inputs {
		if err := ta.scan(ctx, ti); err != nil {
			if !win.IsZero() && !win.Late && errors.Is(err, errNoPicture) {
				// The window's fragments lie inside a picture group: earlier media holds
				// its keyframe (docs/MEDIA-SPEC.md §8 C5).
				return ErrNoKeyframe
			}
			return err
		}
	}
	if err := ta.finish(); err != nil {
		return err
	}
	if !win.IsZero() {
		// Transport streams have no media timeline of their own: times count from the
		// earliest presentation time of the first input (OriginFirst).
		r, err := ta.m.applyWindow(win, ta.m.firstInputOffset())
		if err != nil {
			return err
		}
		*res = r
	}
	afterTSPass1()
	for _, ti := range ta.inputs {
		if err := ti.unchanged(); err != nil {
			return err
		}
	}
	if ta.m.progressive {
		src := &tsChunks{ta: ta, readers: make([]*replay, len(ta.tracks))}
		defer src.close()
		if err := ta.m.writeProgressive(ctx, w, src); err != nil {
			return err
		}
		return src.done(ctx)
	}
	srcs := make([]runSource, len(ta.tracks))
	for i, tr := range ta.tracks {
		srcs[i] = &tsRuns{tr: tr, r: newReplay(tr)}
	}
	return ta.m.writeFragmented(ctx, srcs, w)
}

// scan is pass 1 over one input.
func (ta *tsAssembler) scan(ctx context.Context, ti *tsInput) error {
	if err := ti.stat(); err != nil {
		return err
	}
	r := newRoles(ti.in.Kind)
	d := mpegts.NewDemuxer(ti.files, r.keep, mpegts.WithProgramCheck(r.check))
	defer d.Close()
	var uw unwrapper
	var jumps inputJumps
	var tracks [2]*tsTrack
	for {
		u, err := d.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return ti.wrap(err)
		}
		pts, dts, has := uw.unit(u.PTS, u.DTS)
		ro := roleOfType(u.Type)
		pts, dts = jumps.adjust(ro, pts, dts, has, ti.domains[u.File])
		tr := tracks[ro]
		if tr == nil {
			tr = &tsTrack{in: ti, role: ro, typ: u.Type, lang: r.lang[ro], conv: newConverter(ro, u.Type), m: ta.m}
			tracks[ro] = tr
		}
		if err := tr.conv.push(&u, pts, dts, has, ti.domains[u.File], tr.add); err != nil {
			return ti.unitError(err, &u)
		}
	}
	if _, ok := d.Program(); !ok {
		return errf(ErrMalformed, "input %d: no program association and program map table found", ti.idx)
	}
	for _, tr := range tracks {
		if tr == nil {
			continue
		}
		if err := tr.conv.flush(tr.add); err != nil {
			return withContext(err, "input %d", ti.idx)
		}
		if err := tr.flushRun(); err != nil {
			return err
		}
	}
	// A video (or muxed) input must give video: without a decodable picture only
	// its audio would be saved, which is not the video asked for.
	if ti.in.Kind != KindAudio && (tracks[roleVideo] == nil || tracks[roleVideo].t == nil) {
		return errf(errNoPicture, "input %d: no decodable video (no IDR or IRAP picture)", ti.idx)
	}
	for _, tr := range tracks {
		if tr != nil && tr.t != nil {
			ta.all = append(ta.all, tr)
		}
	}
	return nil
}

// finish orders the tracks (video first, ids 1..N), places their domains on one
// timeline, fixes the pass-1 tables at the domain boundaries, builds the sample
// descriptions and works out the edit lists.
func (ta *tsAssembler) finish() error {
	m := ta.m
	if len(ta.all) == 0 {
		return errf(ErrMalformed, "no H.264, HEVC or AAC samples found")
	}
	ta.tracks = slices.Clone(ta.all)
	slices.SortStableFunc(ta.tracks, func(a, b *tsTrack) int { return cmp.Compare(a.role, b.role) })
	timed := make([]*timedTrack, len(ta.tracks))
	m.tracks = m.tracks[:0]
	for i, tr := range ta.tracks {
		tr.ti = i
		tr.t.outID = uint32(i + 1)
		m.tracks = append(m.tracks, tr.t)
		timed[i] = &tr.timed
	}
	if !placeDomains(timed) {
		return errf(ErrMalformed, "timestamps out of range")
	}
	origin := int64(math.MaxInt64)
	for _, tr := range ta.tracks {
		if err := tr.place(m.progressive); err != nil {
			return err
		}
		origin = min(origin, toClock(tr.first, tr.timed.timescale))
	}
	for _, tr := range ta.tracks {
		tr.buildTrak()
		t, ts := tr.t, tr.timed.timescale
		start := toClock(tr.first, ts) - origin
		t.empty = scaleTime(uint64(start), movieTimescale, tsClock)
		mediaTime := tr.first - tr.base
		if m.progressive {
			t.mediaTime, t.shift = mediaTime, tr.shift
		} else {
			t.mediaTime, t.shift = mediaTime+tr.shift, 0
		}
		t.presDur = scaleTime(uint64(max(tr.presEnd-tr.first, 0)), movieTimescale, ts)
		t.tkhdDur = satAdd(t.empty, t.presDur)
		t.offset = scaleTime(t.empty, ts, movieTimescale)
	}
	return nil
}

// place applies the domain placement to the track: its base, presentation span
// and composition shift, and, for progressive output, the durations at domain
// ends, the composition offsets of delayed domains and the chunk times.
func (tr *tsTrack) place(progressive bool) error {
	t := tr.t
	started := false
	var last *trackDomain
	tr.minCTO = math.MaxInt64
	for i := range tr.timed.domains {
		dom := &tr.timed.domains[i]
		if dom.n == 0 {
			continue
		}
		if !started {
			tr.base, tr.first, tr.presEnd, started = dom.outDTS, dom.presStart, dom.presStop, true
		}
		tr.first, tr.presEnd = min(tr.first, dom.presStart), max(tr.presEnd, dom.presStop)
		tr.minCTO = min(tr.minCTO, int64(dom.minCTO)-dom.delta)
		last = dom
		if !progressive {
			continue
		}
		lo, hi := dom.first, dom.first+uint64(dom.n)
		if dom.delta != 0 {
			for j := lo; j < hi; j++ {
				c := int64(t.ctos[j]) - dom.delta
				if c < math.MinInt32 {
					return errf(ErrUnsupported, "composition offsets span more than 2^31 ticks")
				}
				t.ctos[j] = int32(c)
			}
		}
		t.durs[hi-1] = dom.finalLastDur
	}
	if tr.minCTO < math.MinInt32 {
		return errf(ErrUnsupported, "composition offsets span more than 2^31 ticks")
	}
	tr.shift = max(-tr.minCTO, 0)
	t.next = uint64(last.outDTS + (last.lastDTS - last.srcDTS) + int64(last.finalLastDur) - tr.base)
	if !progressive {
		return nil
	}
	t.anyCTO, t.minCTO = false, 0
	for _, c := range t.ctos {
		t.anyCTO = t.anyCTO || c != 0
		t.minCTO = min(t.minCTO, c)
	}
	var at uint64
	j := 0
	for i := range t.chunks {
		c := &t.chunks[i]
		c.time = at
		for range c.n {
			at += uint64(t.durs[j])
			j++
		}
	}
	return nil
}

// buildTrak builds the track box and sample descriptions of a converted track.
func (tr *tsTrack) buildTrak() {
	t := tr.t
	kind := "video"
	if tr.role == roleAudio {
		kind = "audio"
	}
	lang := mdhdLanguage(tr.lang, tr.in.in.Language)
	trak := mp4.CreateEmptyTrak(t.outID, tr.timed.timescale, kind, lang)
	if tr.role == roleVideo {
		trak.Mdia.Hdlr.Name = "VideoHandler"
		if v, ok := tr.conv.(*videoConv); ok {
			w, h := v.size()
			trak.Tkhd.Width, trak.Tkhd.Height = mp4.Fixed32(uint32(w)<<16), mp4.Fixed32(uint32(h)<<16)
		}
	} else {
		trak.Mdia.Hdlr.Name = "SoundHandler"
	}
	stsd := trak.Mdia.Minf.Stbl.Stsd
	for _, e := range tr.conv.sampleEntries() {
		stsd.AddChild(e)
	}
	t.src.trak, t.src.trackID, t.src.stsd, t.src.nEntries = trak, t.outID, stsd, uint32(len(stsd.Children))
}

// mdhdLanguage chooses the language of a track: the stream's (ISO 639-2 from the
// PMT), else the input's, else "und". A three-letter code goes into mdhd; another
// BCP 47 tag into an elng box (with "und" in mdhd).
func mdhdLanguage(langs ...string) string {
	for _, l := range langs {
		switch {
		case len(l) == 3 && isLetters(l):
			return string([]byte{l[0] | 0x20, l[1] | 0x20, l[2] | 0x20})
		case len(l) >= 2 && len(l) <= 35 && isTag(l):
			return l
		}
	}
	return "und"
}

func isLetters(s string) bool {
	for i := range len(s) {
		if c := s[i] | 0x20; c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

// isTag reports a plausible BCP 47 tag: a letter, then letters, digits and hyphens.
func isTag(s string) bool {
	if !isLetters(s[:1]) {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if c != '-' && (c < '0' || c > '9') && !isLetters(s[i:i+1]) {
			return false
		}
	}
	return true
}

// replay is the pass-2 reader of one track: it demultiplexes the track's input
// again, keeping the track's stream (and the timestamps of its input's other kept
// stream, which the unwrapping and the input's jumps depend on), and converts its
// units as pass 1 did.
type replay struct {
	tr     *tsTrack
	d      *mpegts.Demuxer
	uw     unwrapper
	jumps  inputJumps
	conv   tsConverter
	q      []tsSample
	qi     int
	arena  []byte
	done   bool
	perDom []int
}

func newReplay(tr *tsTrack) *replay {
	r := newRoles(tr.in.in.Kind)
	keep := func(_ mpegts.Program, s mpegts.Stream) bool { return int(s.PID) == r.pid[tr.role] }
	other := func(p mpegts.Program, s mpegts.Stream) bool { return r.keep(p, s) }
	d := mpegts.NewDemuxer(tr.in.files, keep, mpegts.WithProgramCheck(r.check), mpegts.WithTimestampsOnly(other))
	return &replay{tr: tr, d: d, conv: newConverter(tr.role, tr.typ), perDom: make([]int, len(tr.timed.domains))}
}

func (r *replay) add(s *tsSample) error {
	start := len(r.arena)
	r.arena = append(r.arena, s.data...)
	c := *s
	c.data = r.arena[start:len(r.arena):len(r.arena)]
	r.q = append(r.q, c)
	return nil
}

// mismatch reports a pass-2 sample that pass 1 did not see.
func (r *replay) mismatch() error {
	return errf(ErrMalformed, "input %d: fragments changed between passes (the %s track differs)", r.tr.in.idx, r.tr.role)
}

// next returns the next sample (valid until the next call), or io.EOF.
func (r *replay) next(ctx context.Context) (*tsSample, error) {
	for r.qi >= len(r.q) {
		r.q, r.qi, r.arena = r.q[:0], 0, r.arena[:0]
		if r.done {
			return nil, io.EOF
		}
		u, err := r.d.Next(ctx)
		if err == io.EOF {
			r.done = true
			if err := r.conv.flush(r.add); err != nil {
				return nil, withContext(err, "input %d", r.tr.in.idx)
			}
			continue
		}
		if err != nil {
			return nil, r.tr.in.wrap(err)
		}
		pts, dts, has := r.uw.unit(u.PTS, u.DTS)
		ro := roleOfType(u.Type)
		pts, dts = r.jumps.adjust(ro, pts, dts, has, r.tr.in.domains[u.File])
		if ro != r.tr.role {
			continue // the other stream, followed for its timestamps
		}
		if err := r.conv.push(&u, pts, dts, has, r.tr.in.domains[u.File], r.add); err != nil {
			return nil, r.tr.in.unitError(err, &u)
		}
	}
	s := &r.q[r.qi]
	r.qi++
	if s.domain >= len(r.perDom) || r.perDom[s.domain] >= r.tr.timed.domains[s.domain].n {
		return nil, r.mismatch()
	}
	r.perDom[s.domain]++
	return s, nil
}

func (r *replay) close() { _ = r.d.Close() }

// tsChunks supplies the chunks of progressive output from the replay readers.
type tsChunks struct {
	ta      *tsAssembler
	readers []*replay
	pos     []uint64 // samples written per track
}

func (s *tsChunks) writeChunk(ctx context.Context, ti int, c *chunk, w io.Writer) error {
	if s.pos == nil {
		s.pos = make([]uint64, len(s.readers))
	}
	r := s.readers[ti]
	if r == nil {
		r = newReplay(s.ta.tracks[ti])
		s.readers[ti] = r
	}
	t := s.ta.m.tracks[ti]
	// Samples a window cut away (applyWindow) are converted and skipped.
	for s.pos[ti] < c.first {
		if _, err := r.next(ctx); err != nil {
			if err == io.EOF {
				return r.mismatch()
			}
			return err
		}
		s.pos[ti]++
	}
	for range c.n {
		smp, err := r.next(ctx)
		if err == io.EOF || (err == nil && (s.pos[ti] < t.lo || uint32(len(smp.data)) != t.sizes[s.pos[ti]-t.lo])) {
			return r.mismatch()
		}
		if err != nil {
			return err
		}
		if _, err := w.Write(smp.data); err != nil {
			return fmt.Errorf("remux: write output: %w", err)
		}
		s.pos[ti]++
	}
	return nil
}

// done checks that pass 2 found no sample beyond pass 1's.
func (s *tsChunks) done(ctx context.Context) error {
	for i, r := range s.readers {
		if r == nil || s.ta.m.tracks[i].edit {
			continue
		}
		if _, err := r.next(ctx); err != io.EOF {
			if err != nil {
				return err
			}
			return r.mismatch()
		}
	}
	return nil
}

func (s *tsChunks) close() {
	for _, r := range s.readers {
		if r != nil {
			r.close()
		}
	}
}

// tsRuns supplies the runs of one track of fragmented output: at most 1 s or 16
// MiB each, ending at a sample description change or a domain end, placed with
// pass 1's domain placement.
type tsRuns struct {
	tr   *tsTrack
	r    *replay
	peek *tsSample
	run  fragRun
	data []byte
}

func (s *tsRuns) next(ctx context.Context) (*fragRun, error) {
	tr := s.tr
	s.run = fragRun{track: tr.ti, samples: s.run.samples[:0]}
	s.data = s.data[:0]
	limit := uint64(tr.timed.timescale) * chunkSeconds
	var dur uint64
	dom := -1
	for {
		smp := s.peek
		s.peek = nil
		if smp == nil {
			var err error
			if smp, err = s.r.next(ctx); err == io.EOF {
				break
			} else if err != nil {
				return nil, err
			}
		}
		if len(s.run.samples) > 0 && (smp.sdi != s.run.sdi || smp.domain != dom ||
			dur+uint64(smp.dur) > limit || len(s.data)+len(smp.data) > maxRunBytes) {
			s.peek = smp
			break
		}
		td := &tr.timed.domains[smp.domain]
		d := smp.dur
		if smp.last {
			d = td.finalLastDur
		}
		if len(s.run.samples) == 0 {
			s.run.sdi, dom = smp.sdi, smp.domain
			s.run.outTime = uint64(smp.dts - td.srcDTS + td.outDTS - tr.base)
		}
		cto := int64(smp.cto) - td.delta + tr.shift
		if cto > math.MaxInt32 || cto < math.MinInt32 {
			return nil, errf(ErrUnsupported, "composition offsets span more than 2^31 ticks")
		}
		s.run.samples = append(s.run.samples, sampleMeta{size: uint32(len(smp.data)), dur: d, flags: smp.flags(), cto: int32(cto)})
		s.data = append(s.data, smp.data...)
		dur += uint64(d)
		if smp.last {
			break
		}
	}
	if len(s.run.samples) == 0 {
		return nil, io.EOF
	}
	s.run.size, s.run.dur, s.run.data = int64(len(s.data)), dur, s.data
	return &s.run, nil
}

func (s *tsRuns) writeRun(_ context.Context, r *fragRun, w io.Writer) error {
	if _, err := w.Write(r.data); err != nil {
		return fmt.Errorf("remux: write output: %w", err)
	}
	return nil
}

func (s *tsRuns) close() { s.r.close() }
