package mediajob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"

	"downex/internal/media"
	"downex/internal/model"
	"downex/internal/remux"
	"downex/internal/remux/mpegts"
	"downex/internal/transport"
)

// What ProbeMP4 measures of an HLS video or muxed track whose playlists give it no
// BANDWIDTH: above all the single track of a bare media playlist, which a player may
// load without its master (no RESOLUTION, CODECS or BANDWIDTH either). The track gets
// a size (Track.Size, always SizeApprox: the bytes of its segments, exact where every
// one is a byte range or was sampled, else estimated, while the file saved is an MP4
// written anew) and, where the playlists declare none, its picture, frame rate, codecs
// and a name ("1080p"): from the start of its first segment for a transport stream; for
// fragmented MP4 from its initialization section (probeinit.go) and the 'moof' boxes of its
// first segment. At most describeMaxTracks tracks per probe, describeRequests requests
// per track, made one at a time on one connection, describeHeadBytes read of a
// transport stream's first segment or of an initialization section, describeMoofBytes
// of a fragmented MP4's first segment and one byte of the others, with the headers the
// job's own fetch of the fragment would carry; nothing of it can fail the probe: what
// cannot be measured stays unknown.
const (
	// describeMaxTracks bounds the tracks one probe measures: the default video first,
	// then the others as listed.
	describeMaxTracks = 4
	// describeRequests bounds the requests made for one track: the initialization
	// section of a fragmented MP4 track that is described, a request for each of
	// describeSamples media segments, and a HEAD for one whose answer gave no size
	// while a request is left for each sample still to be asked (a HEAD is dropped
	// before a sample is).
	describeRequests = 4
	// describeSamples is the number of media segments whose sizes are sampled: the
	// first, the middle one and the last but one (the last is often short).
	describeSamples = 3
	// describeHeadBytes bounds what is read of the first media segment of a transport
	// stream, or of the initialization section of a fragmented MP4 track, whose picture
	// or codecs are not declared.
	describeHeadBytes = 512 << 10
	// describeBudget bounds the time of a probe's media requests; with a deadline, they
	// take at most half the time left to it.
	describeBudget = 8 * time.Second
	// maxDescribedSize bounds a measured size, as a site's sizes are bounded (2^40
	// bytes).
	maxDescribedSize = 1 << 40
	// maxFrameTimes bounds the video timestamps a frame rate is measured from.
	maxFrameTimes = 16
)

// describeTracks measures the tracks of info that need it (newDescribeJob), at most
// describeMaxTracks of them, the default video first, and fills in what their
// playlists leave out. byID holds the planned track of each listed one. The requests
// are made one at a time on one transport slot, within describeBudget (or half the
// time left before ctx's deadline); a server that throttles them (429, 503) is asked
// nothing more.
func describeTracks(ctx context.Context, tf transport.Factory, hp headerPolicy, info *Info, byID map[string]*track) {
	budget := describeBudget
	if dl, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(dl)/2)
	}
	if budget <= 0 {
		return
	}
	order := make([]int, 0, len(info.Tracks))
	for i := range info.Tracks {
		if info.Tracks[i].ID == info.DefaultVideo {
			order = append([]int{i}, order...)
		} else {
			order = append(order, i)
		}
	}
	var jobs []*describeJob
	for _, i := range order {
		if j := newDescribeJob(&info.Tracks[i], byID[info.Tracks[i].ID]); j != nil {
			jobs = append(jobs, j)
		}
		if len(jobs) == describeMaxTracks {
			break
		}
	}
	if len(jobs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	f := tf.NewSlot()
	defer f.Close()
	for _, j := range jobs {
		more := j.run(ctx, f, hp)
		j.apply()
		if !more {
			return
		}
	}
}

// describeJob is the measurement of one track.
type describeJob struct {
	t    *media.Track
	kind remux.Kind
	plan *media.Plan
	// picture is set when the picture or the codecs are not declared and the start of the
	// stream is read and described: a transport stream's first segment (describeHeadBytes),
	// or a fragmented MP4's initialization section (init) and then the 'moof' boxes of its first
	// segment.
	picture bool
	// init is the initialization section of a fragmented MP4 track with picture set,
	// requested before the samples (one of the track's describeRequests); initSize is the
	// size its answer named (-1 = unknown) and ipic what it shows (nil: not read, or no
	// video track in it).
	init     *media.Fragment
	initSize int64
	ipic     *initPicture
	samples  []*sampleReq
	// requests counts the requests made for the track (describeRequests at most).
	requests int
	// pic is what the start of the stream shows (when picture is set and it was read);
	// refused is set when that start is one Run refuses before its other fragments: a
	// transport stream its early check refuses (remux.CheckTS), an encrypted
	// initialization section (media.CheckInit).
	pic     *streamPicture
	refused bool
}

// sampleReq is one sampled media fragment of a describeJob.
type sampleReq struct {
	frag *media.Fragment
	// head is set on the first segment of a track with picture set: a transport stream's
	// is read (describeHeadBytes) for its picture; a fragmented MP4's, when its size is
	// asked for anyway, for its 'moof' boxes (describeMoofBytes, once the init showed a video
	// track).
	head bool
	need bool // a request is needed: its size is not known from a byte range, or head
	// size is the fragment's size in bytes (-1 = unknown).
	size int64
}

// newDescribeJob plans the measurement of t (planned as tr), or returns nil when t is
// not measured: it has a BANDWIDTH or a size already, carries no video, or has no plan.
func newDescribeJob(t *media.Track, tr *track) *describeJob {
	if tr == nil || tr.plan == nil || !tr.hls || t.Bandwidth > 0 || t.Size > 0 ||
		(t.Kind != media.KindVideo && t.Kind != media.KindMuxed) {
		return nil
	}
	j := &describeJob{t: t, kind: remux.Kind(string(t.Kind)), plan: tr.plan, initSize: -1}
	if t.Width <= 0 || t.Height <= 0 || t.Codecs == "" {
		switch assumedContainer(tr) {
		case media.ContainerTS:
			j.picture = true
		case media.ContainerFMP4:
			if i := slices.IndexFunc(tr.plan.Fragments, func(f media.Fragment) bool { return f.Init }); i >= 0 {
				j.picture, j.init = true, &tr.plan.Fragments[i]
			}
		}
	}
	var mediaFrags []int // indices into plan.Fragments
	for i := range tr.plan.Fragments {
		if !tr.plan.Fragments[i].Init {
			mediaFrags = append(mediaFrags, i)
		}
	}
	n := len(mediaFrags)
	if n == 0 {
		return nil
	}
	exact := !slices.ContainsFunc(tr.plan.Fragments, func(f media.Fragment) bool { return f.ByteRange == nil })
	picks := []int{0, n / 2, max(n-2, 0)}[:min(describeSamples, n)]
	seen := make(map[int]bool)
	for k, p := range picks {
		if seen[p] {
			continue
		}
		seen[p] = true
		f := &tr.plan.Fragments[mediaFrags[p]]
		s := &sampleReq{frag: f, size: -1}
		if f.ByteRange != nil {
			s.size = f.ByteRange[1] - f.ByteRange[0] + 1
		}
		s.need = s.size < 0 && !exact
		// A transport stream's first segment is read for its picture; a fragmented MP4's
		// only when it is asked for anyway: no request is made for the frame rate alone.
		s.head = k == 0 && j.picture && (j.init == nil || s.need)
		s.need = s.need || s.head
		j.samples = append(j.samples, s)
	}
	return j
}

// run makes the job's requests in order, one at a time: the initialization section of a
// fragmented MP4 track whose picture is described (its first describeHeadBytes,
// described at once), then the samples, the first segment first: for each the head of
// the fragment (describeHeadBytes of a transport stream, describeMoofBytes of fragmented
// MP4 once its init showed a video track; described at once), else its first byte, both
// of which name the resource's size; when a successful answer named none, a HEAD, if
// the track has a request left after one for each sample still to be asked. A refused
// request is not asked again as a HEAD. Once the start of the stream shows one Run would
// refuse (refused), nothing more of it is requested: Run's early check fetches nothing
// after the first segment of such a stream either, and its init check refuses an
// encrypted initialization section before any segment. run returns false when no other
// track should be measured: the server throttled a request (429, 503) or the time is up.
func (j *describeJob) run(ctx context.Context, f transport.Fetcher, hp headerPolicy) bool {
	if j.init != nil {
		if ctx.Err() != nil {
			return false
		}
		j.requests++
		size, data, err := probeFragment(ctx, f, hp, j.init, http.MethodGet, describeHeadBytes, true)
		if len(data) > 0 {
			j.describeInit(data)
		}
		j.initSize = size
		if err != nil && (throttled(err) || ctx.Err() != nil) {
			return false
		}
	}
	for i, s := range j.samples {
		if !s.need || j.refused {
			continue
		}
		if ctx.Err() != nil {
			return false
		}
		if j.requests >= describeRequests {
			return true
		}
		j.requests++
		n := j.headBytes(s)
		size, data, err := probeFragment(ctx, f, hp, s.frag, http.MethodGet, n, n > 1)
		if n > 1 && len(data) > 0 {
			j.describe(data)
		}
		if s.size < 0 && size >= 0 {
			s.size = size
		}
		if err != nil {
			if throttled(err) || ctx.Err() != nil {
				return false
			}
			continue
		}
		if s.size >= 0 || j.refused || j.requests+1+j.pending(i+1) > describeRequests {
			continue
		}
		j.requests++
		size, _, err = probeFragment(ctx, f, hp, s.frag, http.MethodHead, 1, false)
		switch {
		case err == nil && size >= 0:
			s.size = size
		case throttled(err) || ctx.Err() != nil:
			return false
		}
	}
	return true
}

// throttled reports whether err is a server's answer to ask less (429, 503).
func throttled(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && (se.Code == http.StatusTooManyRequests || se.Code == http.StatusServiceUnavailable)
}

// headBytes is what the request for sample s reads: describeHeadBytes of a transport
// stream's first segment, describeMoofBytes of a fragmented MP4's once its init showed
// a video track, else one byte (the size only).
func (j *describeJob) headBytes(s *sampleReq) int64 {
	switch {
	case !s.head:
	case j.init == nil:
		return describeHeadBytes
	case j.ipic != nil:
		return describeMoofBytes
	}
	return 1
}

// pending counts the samples from the from-th on that still need a request.
func (j *describeJob) pending(from int) int {
	n := 0
	for _, s := range j.samples[from:] {
		if s.need {
			n++
		}
	}
	return n
}

// describe reads the head of the first segment (its bytes are not kept). Of fragmented
// MP4, the frame rate its 'moof' boxes give (initPicture.frameRate). Of a transport stream (one
// whose first packet starts within mpegts.MaxPrefix bytes, after a junk prefix such as
// an image), what describeTS finds and whether Run's early check would refuse its start
// (remux.CheckTS: scrambled, or not converted).
func (j *describeJob) describe(head []byte) {
	if j.init != nil {
		if j.ipic != nil {
			j.pic.FrameRate = j.ipic.frameRate(head)
		}
		return
	}
	if _, ok := mpegts.FindStart(head[:min(len(head), mpegts.StartBytes)]); !ok {
		return
	}
	pic := describeTS(context.Background(), head)
	j.pic = &pic
	err := remux.CheckTS(head, j.kind)
	j.refused = errors.Is(err, remux.ErrUnsupported) || errors.Is(err, media.ErrEncrypted)
}

// describeInit reads the head of a fragmented MP4 track's initialization section (its
// bytes are not kept): the picture and codecs describeInit finds of its video track, and
// whether Run's init check refuses it (encrypted), after which nothing more of the
// stream is requested and its codecs stay unknown.
func (j *describeJob) describeInit(head []byte) {
	in := describeInit(head)
	if in == (initPicture{}) {
		return
	}
	j.ipic = &in
	pic := in.pic
	j.pic = &pic
	j.refused = in.encrypted
}

// probeFragment requests the first n bytes of fragment fr (of its byte range, when it
// has one) with method and the headers the job's fetch of fr carries, and returns the
// size of the resource the answer names (Content-Range, else the Content-Length of a
// whole one; -1 when it names none) and, when read is set, the bytes of the range (at
// most n; the whole body when the server sends one shorter than n, whose length is
// then the size).
func probeFragment(ctx context.Context, f transport.Fetcher, hp headerPolicy, fr *media.Fragment, method string, n int64,
	read bool) (int64, []byte, error) {
	start := int64(0)
	end := n - 1
	if fr.ByteRange != nil {
		start = fr.ByteRange[0]
		end = min(fr.ByteRange[1], start+n-1)
	}
	resp, err := f.Fetch(ctx, transport.RangeRequest{
		Request: model.Request{URL: fr.URL, Headers: hp.forURL(fr.URL), Method: method},
		Start:   start,
		End:     end,
	})
	if err != nil {
		return -1, nil, redactURL(err)
	}
	defer resp.Body.Close()
	size := int64(-1)
	skip := int64(0)
	switch resp.StatusCode {
	case http.StatusPartialContent:
		cr := resp.ContentRange
		if cr == nil || cr.Start != start || cr.End < cr.Start || cr.End > end {
			return -1, nil, fmt.Errorf("%w: asked from byte %d", ErrRangeMismatch, start)
		}
		if cr.Total >= 0 {
			size = cr.Total
		}
	case http.StatusOK:
		// The whole resource, the range ignored.
		size, skip = resp.ContentLength, start
		if skip > describeHeadBytes {
			read = false // a sub-range far into the resource: not read for its picture
		}
	default:
		return -1, nil, &StatusError{Code: resp.StatusCode, RetryAfter: resp.RetryAfter}
	}
	if !read || method == http.MethodHead {
		return size, nil, nil
	}
	if skip > 0 {
		if _, err := io.CopyN(io.Discard, resp.Body, skip); err != nil {
			return size, nil, err
		}
	}
	want := end - start + 1
	data, err := io.ReadAll(io.LimitReader(resp.Body, want))
	if err != nil {
		return size, data, err
	}
	if size < 0 && fr.ByteRange == nil && start == 0 && int64(len(data)) < want {
		size = int64(len(data)) // the body ended before the bytes asked for: all of it
	}
	return size, data, nil
}

// apply fills in what the measurement found and the playlists leave out.
func (j *describeJob) apply() {
	known := make(map[int]int64, len(j.samples)+1)
	for _, s := range j.samples {
		if s.size >= 0 {
			known[s.frag.Index] = s.size
		}
	}
	if j.init != nil && j.initSize >= 0 {
		known[j.init.Index] = j.initSize // its byte range counts instead, when it has one
	}
	if size := estimateSize(j.plan, known); size > 0 {
		j.t.Size, j.t.SizeApprox = size, true
	}
	if !j.picture || j.pic == nil {
		return
	}
	pic, t := j.pic, j.t
	// The name the playlists' picture gave (media.Track.Name), which the measured one
	// replaces; any other name (an HLS rendition's NAME) stays.
	declared := media.QualityName(t.Width, t.Height, t.FrameRate, false)
	if (t.Width <= 0 || t.Height <= 0) && pic.Width > 0 && pic.Height > 0 {
		t.Width, t.Height = pic.Width, pic.Height
	}
	if t.FrameRate <= 0 {
		t.FrameRate = pic.FrameRate
	}
	if t.Codecs == "" {
		t.Codecs = pic.Codecs
	}
	if t.Name == declared {
		t.Name = media.QualityName(t.Width, t.Height, t.FrameRate, false)
	}
}

// estimateSize returns the bytes of plan's fragments from the sizes known of some of
// them (known, by Fragment.Index; a fragment with a byte range has its size anyway):
// their sum when every fragment's size is known; else the known sizes plus those of
// the other media fragments at the known media fragments' bytes per second of
// duration (bytes per fragment when a duration is missing). Initialization sections of
// unknown size are left out (they are small). 0 when nothing of the media is known or
// the result is empty or larger than maxDescribedSize.
func estimateSize(plan *media.Plan, known map[int]int64) int64 {
	var (
		sum           float64 // bytes of the fragments of known size
		mediaBytes    float64 // bytes of the media fragments of known size
		mediaDur      time.Duration
		mediaKnown    int
		unknownDur    time.Duration
		unknownMedia  int
		missingDur    bool // a media fragment without a duration
		knownAnything bool
	)
	for i := range plan.Fragments {
		f := &plan.Fragments[i]
		size, ok := known[f.Index]
		if f.ByteRange != nil {
			size, ok = f.ByteRange[1]-f.ByteRange[0]+1, true
		}
		switch {
		case ok && size >= 0:
			sum += float64(size)
			if !f.Init {
				mediaBytes += float64(size)
				mediaDur += f.Duration
				mediaKnown++
				missingDur = missingDur || f.Duration <= 0
				knownAnything = true
			}
		case f.Init:
		default:
			unknownMedia++
			unknownDur += f.Duration
			missingDur = missingDur || f.Duration <= 0
		}
	}
	if !knownAnything {
		return 0
	}
	total := sum
	if unknownMedia > 0 {
		if !missingDur && mediaDur > 0 {
			total += mediaBytes / mediaDur.Seconds() * unknownDur.Seconds()
		} else {
			total += mediaBytes / float64(mediaKnown) * float64(unknownMedia)
		}
	}
	if !(total >= 1) || total > maxDescribedSize {
		return 0
	}
	return int64(math.Round(total))
}

// streamPicture is what the start of a stream shows: the picture size and frame rate of
// its first video, and the RFC 6381 codecs of that video and of its first audio. Of a
// transport stream (describeTS): its first H.264 or HEVC stream and the program's first
// audio stream, the codecs "" unless both are ones the TS→MP4 remuxer converts
// (media.TSConvertible) and were read. Of fragmented MP4: what describeInit finds in
// the initialization section, and the frame rate the first segment's 'moof' boxes give.
type streamPicture struct {
	Width, Height int
	FrameRate     float64
	Codecs        string
}

// describeTS reads the start of a transport stream with internal/remux/mpegts (from its
// first packet: the demuxer skips a junk prefix, mpegts.FindStart): the first H.264 or
// HEVC sequence parameter set (mp4ff's parsers, under recover) for the picture and the
// video codec, the first ADTS header of each AAC stream for its object type, and the
// frame rate from the video timestamps (else the SPS's timing information). Anything
// malformed or missing leaves its part unknown.
func describeTS(ctx context.Context, b []byte) (pic streamPicture) {
	if len(b) < mpegts.PacketSize {
		return streamPicture{}
	}
	keep := func(_ mpegts.Program, s mpegts.Stream) bool {
		switch s.Type {
		case mpegts.TypeH264, mpegts.TypeHEVC, mpegts.TypeAAC:
			return true
		}
		return false
	}
	d := mpegts.NewBytesDemuxer([][]byte{b}, keep)
	defer d.Close()
	var (
		videoPID = -1
		video    string
		vuiRate  float64
		times    []int64
		aac      = make(map[uint16]string) // codec by PID (at most 8), from the first ADTS header of each AAC stream
	)
	done := func() bool {
		if video == "" || len(times) < maxFrameTimes {
			return false
		}
		a, ok := firstAudio(d)
		return ok && (a == nil || aac[a.PID] != "")
	}
	for !done() {
		u, err := d.Next(ctx)
		if err != nil {
			break
		}
		switch u.Type {
		case mpegts.TypeH264, mpegts.TypeHEVC:
			if videoPID < 0 {
				videoPID = int(u.PID)
			}
			if int(u.PID) != videoPID {
				continue
			}
			t := u.DTS
			if t < 0 {
				t = u.PTS
			}
			if t >= 0 && len(times) < maxFrameTimes {
				times = append(times, t)
			}
			if video == "" {
				var w, h int
				if video, w, h, vuiRate = parseSPS(u.Type, u.Data); video != "" {
					pic.Width, pic.Height = w, h
				}
			}
		case mpegts.TypeAAC:
			if c, seen := aac[u.PID]; c == "" && (seen || len(aac) < 8) {
				aac[u.PID] = adtsCodec(u.Data)
			}
		}
	}
	if pic.FrameRate = frameRate(times); pic.FrameRate == 0 {
		pic.FrameRate = vuiRate
	}
	a, ok := firstAudio(d)
	if video == "" || !ok {
		return pic
	}
	codecs := video
	if a != nil {
		if a.Type != mpegts.TypeAAC || aac[a.PID] == "" {
			return pic // audio not read, or not converted: the codecs stay unknown
		}
		codecs += "," + aac[a.PID]
	}
	if media.TSConvertible(codecs) {
		pic.Codecs = codecs
	}
	return pic
}

// firstAudio returns the first audio stream of the demuxer's program, the one a
// conversion keeps (nil when the program has none); ok is false before the program is
// known.
func firstAudio(d *mpegts.Demuxer) (*mpegts.Stream, bool) {
	prog, ok := d.Program()
	if !ok {
		return nil, false
	}
	for i := range prog.Streams {
		if audioType(prog.Streams[i]) {
			return &prog.Streams[i], true
		}
	}
	return nil, true
}

// audioType reports whether a PMT entry is audio.
func audioType(s mpegts.Stream) bool {
	switch s.Type {
	case mpegts.TypeMPEG1Audio, mpegts.TypeMPEG2Audio, mpegts.TypeAAC, mpegts.TypeLATM, mpegts.TypeAACRaw,
		mpegts.TypeAC3, mpegts.TypeEAC3, mpegts.TypeSAESAAC, mpegts.TypeSAESAC3, mpegts.TypeSAESEAC3:
		return true
	case mpegts.TypePrivatePES:
		return s.Dolby
	}
	return false
}

// parseSPS finds the first sequence parameter set in an Annex B access unit of an H.264
// or HEVC stream (typ) and returns the RFC 6381 codec ("avc1.64001F",
// "hvc1.1.6.L120.90"), the picture size and the frame rate its timing information
// gives (0 without); "" when there is none or it does not parse. mp4ff's parsers run
// under recover.
func parseSPS(typ mpegts.StreamType, data []byte) (codec string, width, height int, fps float64) {
	defer func() {
		if recover() != nil {
			codec, width, height, fps = "", 0, 0, 0
		}
	}()
	valid := func(w, h uint32) bool { return w > 0 && h > 0 && w <= 0xffff && h <= 0xffff }
	if typ == mpegts.TypeHEVC {
		_, spss, _ := hevc.GetParameterSetsFromByteStream(data)
		if len(spss) == 0 {
			return "", 0, 0, 0
		}
		s, err := hevc.ParseSPSNALUnit(spss[0])
		if err != nil {
			return "", 0, 0, 0
		}
		w, h := s.ImageSize()
		if !valid(w, h) {
			return "", 0, 0, 0
		}
		if v := s.VUI; v != nil && v.TimingInfoPresentFlag && v.NumUnitsInTick > 0 {
			fps = float64(v.TimeScale) / float64(v.NumUnitsInTick)
		}
		return hevc.CodecString("hvc1", s), int(w), int(h), roundRate(fps)
	}
	spss, _ := avc.GetParameterSetsFromByteStream(data)
	if len(spss) == 0 {
		return "", 0, 0, 0
	}
	s, err := avc.ParseSPSNALUnit(spss[0], true)
	if err != nil || !valid(uint32(s.Width), uint32(s.Height)) {
		return "", 0, 0, 0
	}
	if v := s.VUI; v != nil && v.TimingInfoPresentFlag && v.NumUnitsInTick > 0 {
		fps = float64(v.TimeScale) / float64(2*v.NumUnitsInTick)
	}
	return avc.CodecString("avc1", s), int(s.Width), int(s.Height), roundRate(fps)
}

// adtsCodec returns the RFC 6381 codec of the AAC frame at the start of a PES unit's
// data ("mp4a.40.2" for AAC LC: the ADTS profile plus one); "" when it holds none.
func adtsCodec(data []byte) string {
	if len(data) < 7 || data[0] != 0xff || data[1]&0xf6 != 0xf0 {
		return ""
	}
	return fmt.Sprintf("mp4a.40.%d", data[2]>>6+1)
}

// frameRate measures the frame rate of video timestamps in decode order (90 kHz): the
// mean distance between them, when they rise strictly and there are at least three; 0
// otherwise or outside 1-240 frames per second.
func frameRate(times []int64) float64 {
	if len(times) < 3 {
		return 0
	}
	for i := 1; i < len(times); i++ {
		if times[i] <= times[i-1] {
			return 0
		}
	}
	return roundRate(90000 * float64(len(times)-1) / float64(times[len(times)-1]-times[0]))
}

// roundRate rounds a frame rate to two decimals (29.97, 23.98); 0 outside 1-240.
func roundRate(fps float64) float64 {
	if !(fps >= 1 && fps <= 240) {
		return 0
	}
	return math.Round(fps*100) / 100
}
