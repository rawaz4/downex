package mediajob

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/av1"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/media"
	"downex/internal/testmedia"
)

// Bare media playlists of fragmented MP4, as movy.sx serves them (#EXT-X-VERSION:6, an
// EXT-X-MAP on another host, segments that each begin with a 'moof'): ProbeMP4 reads
// the picture and codecs from the initialization section, where a transport stream
// would carry them in its first segment, and the frame rate from the 'moof' of the
// first segment, whose size it asks for anyway.

// avcHigh1080SPS is a High profile (100), level 4.0 sequence parameter set of a
// 1920×1080 picture (1088 coded lines, the last 8 cropped), as most 1080p streams carry.
func avcHigh1080SPS() []byte {
	var w rbsp
	w.bits(100, 8) // profile_idc
	w.bits(0, 8)   // constraint flags
	w.bits(40, 8)  // level_idc
	w.ue(0)        // seq_parameter_set_id
	w.ue(1)        // chroma_format_idc (4:2:0)
	w.ue(0)        // bit_depth_luma_minus8
	w.ue(0)        // bit_depth_chroma_minus8
	w.bit(0)       // qpprime_y_zero_transform_bypass_flag
	w.bit(0)       // seq_scaling_matrix_present_flag
	w.ue(0)        // log2_max_frame_num_minus4
	w.ue(0)        // pic_order_cnt_type
	w.ue(2)        // log2_max_pic_order_cnt_lsb_minus4
	w.ue(2)        // max_num_ref_frames
	w.bit(0)       // gaps_in_frame_num_value_allowed_flag
	w.ue(119)      // pic_width_in_mbs_minus1
	w.ue(67)       // pic_height_in_map_units_minus1
	w.bit(1)       // frame_mbs_only_flag
	w.bit(1)       // direct_8x8_inference_flag
	w.bit(1)       // frame_cropping_flag
	w.ue(0)        // frame_crop_left_offset
	w.ue(0)        // frame_crop_right_offset
	w.ue(0)        // frame_crop_top_offset
	w.ue(4)        // frame_crop_bottom_offset (2 lines each)
	w.bit(0)       // vui_parameters_present_flag
	return w.nal(0x67)
}

// hevc720ParamSets are the VPS, SPS and PPS of a 1280×720 HEVC Main stream.
func hevc720ParamSets() [][]byte {
	return testmedia.VideoConfig{Codec: testmedia.HEVC, Width: 1280, Height: 720}.ParamSets()
}

// fmp4Stream describes a fragmented MP4 stream generated with mp4ff.
type fmp4Stream struct {
	video   string // "avc1" (High, 1920×1080), "hvc1" (Main, 1280×720) or "av01" (3840×2160)
	audio   bool   // an AAC LC track at 48 kHz (track 2)
	encrypt bool   // the video track protected (cenc: encv with sinf/tenc, and a pssh)
	fps     int    // video frames per second, at a 90 kHz timescale
	// defaultDur moves the video sample duration into tfhd (default_sample_duration).
	defaultDur bool
	// trexDur, when set, is the video track's trex default_sample_duration, and the
	// segments carry no video sample durations (neither trun nor tfhd).
	trexDur uint32
}

// init returns the initialization section of st: ftyp and moov (mvex with a trex per
// track), the video track 1 and the audio track 2.
func (st fmp4Stream) init(t testing.TB) []byte {
	t.Helper()
	return mp4Bytes(t, st.initBox(t))
}

// edited returns the initialization section of st changed by edit before it is encoded.
func (st fmp4Stream) edited(t testing.TB, edit func(init *mp4.InitSegment)) []byte {
	t.Helper()
	init := st.initBox(t)
	edit(init)
	return mp4Bytes(t, init)
}

// initBox builds the initialization section of st.
func (st fmp4Stream) initBox(t testing.TB) *mp4.InitSegment {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = 1000
	trak := init.AddEmptyTrack(90000, "video", "und")
	var err error
	switch st.video {
	case "avc1":
		err = trak.SetAVCDescriptor("avc1", [][]byte{avcHigh1080SPS()}, [][]byte{avcPPS()}, true)
	case "hvc1":
		ps := hevc720ParamSets()
		err = trak.SetHEVCDescriptor("hvc1", ps[:1], ps[1:2], ps[2:], nil, true)
	case "av01":
		err = trak.SetAV1Descriptor("av01", &mp4.Av1CBox{CodecConfRec: av1.CodecConfRec{Version: 1, SeqLevelIdx0: 12,
			ChromaSubsamplingX: 1, ChromaSubsamplingY: 1}}, 3840, 2160)
	default:
		t.Fatalf("no video %q", st.video)
	}
	if err != nil {
		t.Fatal(err)
	}
	init.Moov.Mvex.Trexs[0].DefaultSampleDuration = st.trexDur
	if st.encrypt {
		kid, err := mp4.NewUUIDFromString("00112233-4455-6677-8899-aabbccddeeff")
		if err != nil {
			t.Fatal(err)
		}
		system, err := mp4.NewUUIDFromString("edef8ba9-79d6-4ace-a3c8-27dcd51d21ed")
		if err != nil {
			t.Fatal(err)
		}
		pssh := &mp4.PsshBox{SystemID: system, Data: []byte("test pssh data")}
		if _, err := mp4.InitProtect(init, make([]byte, 16), make([]byte, 8), "cenc", kid, []*mp4.PsshBox{pssh}); err != nil {
			t.Fatal(err)
		}
	}
	if st.audio {
		if err := init.AddEmptyTrack(48000, "audio", "und").SetAACDescriptor(aac.AAClc, 48000); err != nil {
			t.Fatal(err)
		}
	}
	return init
}

// segment returns media segment i (sequence number i+1) of st: a moof and an mdat of
// seconds of video (frames of frameBytes) and, with audio, the AAC frames of that time.
func (st fmp4Stream) segment(t testing.TB, i, seconds, frameBytes int) []byte {
	t.Helper()
	return mp4Bytes(t, st.fragment(t, i, seconds, frameBytes))
}

// fragment builds media segment i of st (segment).
func (st fmp4Stream) fragment(t testing.TB, i, seconds, frameBytes int) *mp4.Fragment {
	t.Helper()
	ids := []uint32{1}
	if st.audio {
		ids = append(ids, 2)
	}
	frag, err := mp4.CreateMultiTrackFragment(uint32(i+1), ids)
	if err != nil {
		t.Fatal(err)
	}
	dur := uint32(90000 / st.fps)
	frames := seconds * st.fps
	for k := range frames {
		flags := mp4.NonSyncSampleFlags
		if k == 0 {
			flags = mp4.SyncSampleFlags
		}
		s := mp4.FullSample{Sample: mp4.Sample{Flags: flags, Dur: dur, Size: uint32(frameBytes)},
			DecodeTime: uint64(i*frames+k) * uint64(dur), Data: filler(frameBytes, uint64(i*1000+k))}
		if err := frag.AddFullSampleToTrack(s, 1); err != nil {
			t.Fatal(err)
		}
	}
	if st.audio {
		n := seconds * 48000 / 1024
		for k := range n {
			s := mp4.FullSample{Sample: mp4.Sample{Flags: mp4.SyncSampleFlags, Dur: 1024, Size: 200},
				DecodeTime: uint64(i*n+k) * 1024, Data: filler(200, uint64(i*1000+k+500))}
			if err := frag.AddFullSampleToTrack(s, 2); err != nil {
				t.Fatal(err)
			}
		}
	}
	if st.defaultDur {
		if err := frag.Moof.Trafs[0].OptimizeTfhdTrun(); err != nil {
			t.Fatal(err)
		}
		if frag.Moof.Trafs[0].Trun.HasSampleDuration() {
			t.Fatal("the video durations stayed in trun")
		}
	}
	if st.trexDur > 0 {
		frag.Moof.Trafs[0].Trun.Flags &^= mp4.TrunSampleDurationPresentFlag
	}
	return frag
}

// trackFragment is a movie fragment (moof and mdat) of n samples of one track, each of
// dur ticks and size bytes, as packagers write one fragment per track.
func trackFragment(t testing.TB, seq, trackID uint32, n int, dur, size uint32) []byte {
	t.Helper()
	frag, err := mp4.CreateFragment(seq, trackID)
	if err != nil {
		t.Fatal(err)
	}
	for k := range n {
		frag.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Flags: mp4.SyncSampleFlags, Dur: dur, Size: size},
			DecodeTime: uint64(k) * uint64(dur), Data: filler(int(size), uint64(seq)<<20+uint64(k))})
	}
	return mp4Bytes(t, frag)
}

// nestedBox is depth boxes of type typ, each the only child of the one before it.
func nestedBox(typ string, depth int) *mp4.UnknownBox {
	b := make([]byte, 0, 8*depth)
	for k := range depth {
		b = binary.BigEndian.AppendUint32(b, uint32(8*(depth-k)))
		b = append(b, typ...)
	}
	return mp4.CreateUnknownBox(typ, uint64(len(b)), b[8:])
}

// freeBox is a 'free' box of n bytes, header included.
func freeBox(n int) *mp4.UnknownBox {
	return mp4.CreateUnknownBox("free", uint64(n), make([]byte, n-8))
}

// fastest is the shortest time of three calls of f, and what the last one returned.
func fastest[T any](f func() T) (v T, d time.Duration) {
	d = time.Hour
	for range 3 {
		start := time.Now()
		v = f()
		d = min(d, time.Since(start))
	}
	return v, d
}

// fmp4Bare is a bare fragmented MP4 media playlist served by serveFMP4Bare.
type fmp4Bare struct {
	playlist string // the playlist's URL
	initPath string // the path of the init section on its server
	segPaths []string
	init     []byte
	total    int64 // bytes of the init and of every segment
}

// serveFMP4Bare serves st as the bare media playlist dir+"index-s1080p-v1-a1.m3u8" on s
// of n segments dir+"seg-<i>-s1080p-v1-a1.m4s" (i from 1) of seconds each, frames of
// frameBytes (plus 7 bytes per segment index, so their sizes differ), whose EXT-X-MAP
// names the init section on cdn (another host, as movy.sx's playlists name theirs).
func serveFMP4Bare(t *testing.T, s, cdn *server, dir string, st fmp4Stream, n, seconds, frameBytes int) fmp4Bare {
	t.Helper()
	b := fmp4Bare{playlist: s.url(dir + "index-s1080p-v1-a1.m3u8"), initPath: dir + "init-s1080p-v1-a1.mp4", init: st.init(t)}
	cdn.add(b.initPath, b.init, "video/mp4")
	b.total = int64(len(b.init))
	var pl strings.Builder
	fmt.Fprintf(&pl, "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n"+
		"#EXT-X-INDEPENDENT-SEGMENTS\n#EXT-X-MAP:URI=%q\n", seconds, cdn.url(b.initPath))
	for i := range n {
		name := fmt.Sprintf("seg-%d-s1080p-v1-a1.m4s", i+1)
		seg := st.segment(t, i, seconds, frameBytes+7*i)
		if string(seg[4:8]) != "moof" {
			t.Fatalf("segment %d begins with %q", i, seg[4:8])
		}
		s.add(dir+name, seg, "video/iso.segment")
		b.segPaths = append(b.segPaths, dir+name)
		b.total += int64(len(seg))
		fmt.Fprintf(&pl, "#EXTINF:%d.000,\n%s\n", seconds, name)
	}
	pl.WriteString("#EXT-X-ENDLIST\n")
	s.add(dir+"index-s1080p-v1-a1.m3u8", []byte(pl.String()), hlsType)
	return b
}

// mp4Bytes encodes an mp4ff box or segment.
func mp4Bytes(t testing.TB, b interface{ Encode(w io.Writer) error }) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := b.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// requests sums the requests s saw for paths.
func requests(s *server, paths ...string) int {
	n := 0
	for _, p := range paths {
		n += s.hitCount(p)
	}
	return n
}

// The reported stream: a bare playlist of H.264 High 1080p with AAC in fragmented MP4,
// its init on another host. The track is listed as 1080p with its codecs, frame rate and
// an estimated size, from four requests: the init section (up to 512 KiB), the start of
// the first segment (its 'moof', which the size sample asks for anyway) and one byte of
// the middle and the last but one segments, each with the headers its fetch carries.
func TestProbeMP4DescribesBareFMP4Init(t *testing.T) {
	s, cdn := newServer(t), newServer(t)
	b := serveFMP4Bare(t, s, cdn, "/vd/abc/", fmp4Stream{video: "avc1", audio: true, fps: 25}, 12, 2, 1500)
	sel := Selection{ManifestURL: b.playlist, Headers: http.Header{"Referer": {"https://movy.example/watch"}}}
	info, err := ProbeMP4(testContext(t), testFactory(t), sel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Tracks) != 1 || info.DefaultVideo != "v0" || info.Container != "mp4" {
		t.Fatalf("info = %+v", info)
	}
	tr := info.Tracks[0]
	if tr.Kind != media.KindMuxed || tr.Width != 1920 || tr.Height != 1080 || tr.FrameRate != 25 ||
		tr.Codecs != "avc1.640028,mp4a.40.2" || tr.Name != "1080p (Full HD)" || tr.Bandwidth != 0 {
		t.Errorf("track = %+v", tr)
	}
	if !tr.SizeApprox || tr.Size < b.total*4/5 || tr.Size > b.total*6/5 {
		t.Errorf("size %d (approx %v), want about %d", tr.Size, tr.SizeApprox, b.total)
	}
	if got := cdn.seen(b.initPath); !slices.Equal(got.ranges, []string{"bytes=0-524287"}) ||
		!slices.Equal(got.referers, []string{"https://movy.example/watch"}) {
		t.Errorf("init requests: ranges %q, referers %q", got.ranges, got.referers)
	}
	n := len(b.segPaths)
	want := map[int]string{0: "bytes=0-65535", n / 2: "bytes=0-0", n - 2: "bytes=0-0"}
	for i, p := range b.segPaths {
		var w []string
		if r, ok := want[i]; ok {
			w = []string{r}
		}
		if got := s.seen(p).ranges; !slices.Equal(got, w) {
			t.Errorf("segment %d: ranges %q, want %q", i, got, w)
		}
	}
	if got := requests(s, b.segPaths...) + requests(cdn, b.initPath); got != describeRequests {
		t.Errorf("%d media requests, want %d", got, describeRequests)
	}
}

// HEVC video without audio, at 60 frames per second given as tfhd's
// default_sample_duration: the codec is named as describeTS names an HEVC stream, and the
// name carries the frame rate.
func TestProbeMP4DescribesBareFMP4HEVC(t *testing.T) {
	s, cdn := newServer(t), newServer(t)
	b := serveFMP4Bare(t, s, cdn, "/h/", fmp4Stream{video: "hvc1", fps: 60, defaultDur: true}, 6, 1, 900)
	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: b.playlist}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sps, err := hevc.ParseSPSNALUnit(hevc720ParamSets()[1])
	if err != nil {
		t.Fatal(err)
	}
	tr := info.Tracks[0]
	if codec := hevc.CodecString("hvc1", sps); tr.Codecs != codec || tr.Width != 1280 || tr.Height != 720 ||
		tr.FrameRate != 60 || tr.Name != "720p (HD) 60fps" || !tr.SizeApprox || tr.Size < b.total*4/5 || tr.Size > b.total*6/5 {
		t.Errorf("track = %+v, want %s and about %d bytes", tr, codec, b.total)
	}
}

// An encrypted init section (encv with sinf/tenc, a pssh) is a stream Run refuses: the
// track is not described as one that can be saved (no codecs), and nothing more of it is
// requested, as after a transport stream whose start the early check refuses. Also when
// the section is larger than the describeHeadBytes the probe reads of it (Run reads up to
// maxInitBytes): the protection in the part read counts.
func TestProbeMP4DescribeEncryptedInit(t *testing.T) {
	st := fmp4Stream{video: "avc1", encrypt: true, fps: 25}
	for _, c := range []struct {
		name string
		init []byte
	}{
		{"small", st.init(t)},
		{"larger than describeHeadBytes", st.edited(t, func(i *mp4.InitSegment) { i.Moov.AddChild(freeBox(600 << 10)) })},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := media.CheckInit(c.init); !errors.Is(err, media.ErrEncrypted) {
				t.Fatalf("the fixture's init: %v", err)
			}
			s, cdn := newServer(t), newServer(t)
			b := serveFMP4Bare(t, s, cdn, "/e/", st, 6, 2, 1000)
			cdn.add(b.initPath, c.init, "video/mp4")
			info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: b.playlist}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tr := info.Tracks[0]; tr.Codecs != "" || tr.Size != 0 || tr.SizeApprox {
				t.Errorf("track = %+v, want no codecs and no size", tr)
			}
			if n := cdn.hitCount(b.initPath); n != 1 {
				t.Errorf("init requested %d times", n)
			}
			if n := requests(s, b.segPaths...); n != 0 {
				t.Errorf("%d segment requests after an encrypted init", n)
			}
		})
	}
}

// An init section that nests boxes 60,000 deep where media.CheckInit does not look (a
// 'udta' in the 'moov', 481 KB in all): decoding the whole 'moov' with mp4ff took 18 s,
// past the probe's budget, and the track lost its size. The probe describes and measures
// it at once.
func TestProbeMP4DescribeNestedInit(t *testing.T) {
	s, cdn := newServer(t), newServer(t)
	st := fmp4Stream{video: "avc1", audio: true, fps: 25}
	b := serveFMP4Bare(t, s, cdn, "/n/", st, 12, 2, 1500)
	deep := st.edited(t, func(i *mp4.InitSegment) { i.Moov.AddChild(nestedBox("udta", 60000)) })
	if len(deep) > describeHeadBytes || media.CheckInit(deep) != nil {
		t.Fatalf("the fixture's init: %d bytes, %v", len(deep), media.CheckInit(deep))
	}
	cdn.add(b.initPath, deep, "video/mp4")
	start := time.Now()
	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: b.playlist}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > describeBudget/4 {
		t.Errorf("the probe took %v", d)
	}
	total := b.total - int64(len(b.init)) + int64(len(deep))
	if tr := info.Tracks[0]; tr.Codecs != "avc1.640028,mp4a.40.2" || tr.Name != "1080p (Full HD)" || tr.FrameRate != 25 ||
		!tr.SizeApprox || tr.Size < total*4/5 || tr.Size > total*6/5 {
		t.Errorf("track = %+v, want about %d bytes", tr, total)
	}
}

// A track whose playlist declares its picture and codecs is measured without its init
// section: one byte of each sampled segment.
func TestProbeMP4DescribeFMP4Declared(t *testing.T) {
	s, cdn := newServer(t), newServer(t)
	b := serveFMP4Bare(t, s, cdn, "/d/v/", fmp4Stream{video: "avc1", audio: true, fps: 25}, 6, 2, 1500)
	s.add("/d/master.m3u8", []byte("#EXTM3U\n#EXT-X-STREAM-INF:RESOLUTION=1920x1080,CODECS=\"avc1.640028,mp4a.40.2\"\n"+
		"v/index-s1080p-v1-a1.m3u8\n"), hlsType)
	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/d/master.m3u8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tr := info.Tracks[0]; tr.Size == 0 || tr.FrameRate != 0 || tr.Name != "1080p (Full HD)" {
		t.Errorf("track = %+v", tr)
	}
	if n := cdn.hitCount(b.initPath); n != 0 {
		t.Errorf("init requested %d times", n)
	}
	if got := s.seen(b.segPaths[0]).ranges; !slices.Equal(got, []string{"bytes=0-0"}) {
		t.Errorf("first segment: %q", got)
	}
}

// The init request counts against the track's four: against a server that names no
// length, the HEAD that would ask for one is dropped, never a size sample.
func TestProbeMP4DescribeFMP4RequestBudget(t *testing.T) {
	st := fmp4Stream{video: "avc1", fps: 25}
	init := st.init(t)
	const n = 8
	segs := make([][]byte, n)
	list := make([]testmedia.Segment, n)
	for i := range segs {
		segs[i] = st.segment(t, i, 2, 200+3*i)
		list[i] = testmedia.Segment{URI: fmt.Sprintf("s%d.m4s", i), Duration: 2}
	}
	var mu sync.Mutex
	var methods []string // "<method> <path>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.m3u8" {
			w.Header().Set("Content-Type", hlsType)
			pl := strings.Replace(testmedia.MediaPlaylist(list), "#EXTINF", "#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF", 1)
			_, _ = w.Write([]byte(pl))
			return
		}
		body := init
		if r.URL.Path != "/init.mp4" {
			var i int
			if _, err := fmt.Sscanf(r.URL.Path, "/s%d.m4s", &i); err != nil || i >= n {
				http.NotFound(w, r)
				return
			}
			body = segs[i]
		}
		mu.Lock()
		methods = append(methods, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			return
		}
		// The whole resource, ranges ignored, of unknown length.
		w.(http.Flusher).Flush()
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: srv.URL + "/index.m3u8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	total := int64(len(init))
	for _, seg := range segs {
		total += int64(len(seg))
	}
	tr := info.Tracks[0]
	if tr.Width != 1920 || tr.Codecs != "avc1.640028" || tr.FrameRate != 25 || !tr.SizeApprox || tr.Size < total*4/5 ||
		tr.Size > total*6/5 {
		t.Errorf("track = %+v, want about %d bytes", tr, total)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"GET /init.mp4", "GET /s0.m4s", fmt.Sprintf("GET /s%d.m4s", n/2), fmt.Sprintf("GET /s%d.m4s", n-2)}
	if !slices.Equal(methods, want) {
		t.Errorf("requests %q, want %q", methods, want)
	}
}

// describeInit reads an fMP4 initialization section with mp4ff: the picture of its first
// video track (its sample entry, else tkhd), the RFC 6381 codecs of that video and of
// the first audio track (none when either is not named), and what a frame rate is
// measured with (track ID, mdhd timescale, trex default duration). Protection marks it
// encrypted, without codecs. Anything else (no video, cut short, not an init section)
// describes nothing.
func TestDescribeInit(t *testing.T) {
	sps, err := hevc.ParseSPSNALUnit(hevc720ParamSets()[1])
	if err != nil {
		t.Fatal(err)
	}
	avcAAC := fmp4Stream{video: "avc1", audio: true}.init(t)
	unnamed := mp4.CreateEmptyInit()
	if err := unnamed.AddEmptyTrack(90000, "video", "und").SetAVCDescriptor("avc1", [][]byte{avcHigh1080SPS()},
		[][]byte{avcPPS()}, true); err != nil {
		t.Fatal(err)
	}
	unnamed.AddEmptyTrack(48000, "audio", "und").Mdia.Minf.Stbl.Stsd.AddChild(mp4.CreateAudioSampleEntryBox("mp4a", 2, 16, 48000, nil))
	encAudio := mp4.CreateEmptyInit() // enca only: encrypted, though there is no picture to describe
	if err := encAudio.AddEmptyTrack(48000, "audio", "und").SetAACDescriptor(aac.AAClc, 48000); err != nil {
		t.Fatal(err)
	}
	kid, err := mp4.NewUUIDFromString("00112233-4455-6677-8899-aabbccddeeff")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mp4.InitProtect(encAudio, make([]byte, 16), make([]byte, 8), "cenc", kid, nil); err != nil {
		t.Fatal(err)
	}
	video := func(w, h int, codecs string) initPicture {
		return initPicture{pic: streamPicture{Width: w, Height: h, Codecs: codecs}, trackID: 1, timescale: 90000}
	}
	encrypted := video(1920, 1080, "")
	encrypted.encrypted = true
	trex := video(1920, 1080, "avc1.640028")
	trex.trexDur = 3000
	// Sections Run's init parser (remux) refuses as malformed: described as nothing.
	avcAACStream := fmp4Stream{video: "avc1", audio: true}
	malformed := func(edit func(i *mp4.InitSegment)) []byte { return avcAACStream.edited(t, edit) }
	dropChild := func(children []mp4.Box, typ string) []mp4.Box {
		return slices.DeleteFunc(children, func(b mp4.Box) bool { return b.Type() == typ })
	}
	withText := func(entries bool) []byte { // a text track besides, which Run's assembly leaves out
		return malformed(func(i *mp4.InitSegment) {
			text := i.AddEmptyTrack(1000, "wvtt", "und") // no sample description yet
			if entries {
				if err := text.SetWvttDescriptor("WEBVTT"); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	// Encrypted sections larger than describeHeadBytes, of which only that much is read.
	encStream := fmp4Stream{video: "avc1", encrypt: true}
	encBig := encStream.edited(t, func(i *mp4.InitSegment) { i.Moov.AddChild(freeBox(600 << 10)) })
	encLate := encStream.edited(t, func(i *mp4.InitSegment) { // the protection after the part read
		i.Moov.Children = append([]mp4.Box{freeBox(600 << 10)}, i.Moov.Children...)
	})
	clearBig := avcAACStream.edited(t, func(i *mp4.InitSegment) { i.Moov.AddChild(freeBox(600 << 10)) })
	// The part read ends 3 bytes into the header of a box after the video track: the
	// moov ends at the end of the section, and boxes added to it follow the traks and pssh.
	encHeaderCut := encStream.edited(t, func(i *mp4.InitSegment) {
		i.Moov.AddChild(freeBox(describeHeadBytes - 3 - len(encStream.init(t))))
		i.Moov.AddChild(freeBox(600 << 10))
	})
	if got := encHeaderCut[describeHeadBytes-3 : describeHeadBytes+5]; string(got[4:]) != "free" {
		t.Fatalf("the cut box header: %q", got)
	}
	for _, c := range []struct {
		name string
		data []byte
		want initPicture
	}{
		{"H.264 with AAC", avcAAC, video(1920, 1080, "avc1.640028,mp4a.40.2")},
		{"HEVC", fmp4Stream{video: "hvc1"}.init(t), video(1280, 720, hevc.CodecString("hvc1", sps))},
		{"AV1 with AAC", fmp4Stream{video: "av01", audio: true}.init(t), video(3840, 2160, "av01.0.12M.08,mp4a.40.2")},
		{"trex default duration", fmp4Stream{video: "avc1", trexDur: 3000}.init(t), trex},
		{"encrypted", fmp4Stream{video: "avc1", encrypt: true}.init(t), encrypted},
		{"encrypted, no video track", mp4Bytes(t, encAudio), initPicture{encrypted: true}},
		{"audio not named: the picture, no codecs", mp4Bytes(t, unnamed), video(1920, 1080, "")},
		{"audio timescale 0", malformed(func(i *mp4.InitSegment) { i.Moov.Traks[1].Mdia.Mdhd.Timescale = 0 }), initPicture{}},
		{"track ID used twice", malformed(func(i *mp4.InitSegment) { i.Moov.Traks[1].Tkhd.TrackID = 1 }), initPicture{}},
		{"video track ID 0", malformed(func(i *mp4.InitSegment) { i.Moov.Traks[0].Tkhd.TrackID = 0 }), initPicture{}},
		{"audio track without mdhd", malformed(func(i *mp4.InitSegment) {
			m := i.Moov.Traks[1].Mdia
			m.Children, m.Mdhd = dropChild(m.Children, "mdhd"), nil
		}), initPicture{}},
		{"audio track without a sample description", malformed(func(i *mp4.InitSegment) {
			stsd := i.Moov.Traks[1].Mdia.Minf.Stbl.Stsd
			stsd.Children, stsd.SampleCount = nil, 0
		}), initPicture{}},
		{"no movie header", malformed(func(i *mp4.InitSegment) {
			i.Moov.Children, i.Moov.Mvhd = dropChild(i.Moov.Children, "mvhd"), nil
		}), initPicture{}},
		{"a text track besides", withText(true), video(1920, 1080, "avc1.640028,mp4a.40.2")},
		{"a text track without a sample description", withText(false), video(1920, 1080, "avc1.640028,mp4a.40.2")},
		{"encrypted, larger than describeHeadBytes", encBig, encrypted},
		{"encrypted, cut in a box header", encHeaderCut, encrypted},
		{"encrypted after the part read", encLate, initPicture{}},
		{"not encrypted, larger than describeHeadBytes", clearBig, initPicture{}},
		{"no video track", initSegment("x", false), initPicture{}},
		{"cut short", avcAAC[:len(avcAAC)/2], initPicture{}},
		{"not an init section", []byte(strings.Repeat("<html>no video</html>", 9)), initPicture{}},
		{"empty", nil, initPicture{}},
	} {
		if got := describeInit(c.data); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	// Damaged anywhere: what is found, never a panic.
	for i := range avcAAC {
		damaged := slices.Clone(avcAAC)
		damaged[i] ^= 0xa5
		if got := describeInit(damaged); got.pic.Width < 0 || got.pic.Width > 0xffff || got.encrypted && got.pic.Codecs != "" {
			t.Errorf("byte %d damaged: %+v", i, got)
		}
	}
}

// The frame rate of a first segment's 'moof': the video track's samples over their
// durations (trun's, else tfhd's default_sample_duration, else trex's) in the init's
// timescale, after any styp/sidx; 0 when the moof is cut short, has no sample of the
// video track or no durations, or the init gave no video track.
func TestMoofFrameRate(t *testing.T) {
	avc := fmp4Stream{video: "avc1", audio: true, fps: 25}
	in := describeInit(avc.init(t))
	seg := avc.segment(t, 3, 2, 300)
	hevc60 := fmp4Stream{video: "hvc1", fps: 60, defaultDur: true}
	trex30 := fmp4Stream{video: "avc1", fps: 30, trexDur: 3000}
	prefix := append(box("styp", []byte("msdh\x00\x00\x00\x00msdhmsix")), box("sidx", make([]byte, 24))...)
	other := in
	other.trackID = 9
	// One fragment per track, as some packagers write a muxed segment: 2 s of AAC
	// (track 2), then 2 s of video at 25 frames per second (track 1).
	audioFrag := trackFragment(t, 1, 2, 94, 1024, 100)
	videoFrag := trackFragment(t, 2, 1, 50, 3600, 300)
	bigAudio := trackFragment(t, 1, 2, 94, 1024, describeMoofBytes/94+1)
	for _, c := range []struct {
		name string
		in   initPicture
		head []byte
		want float64
	}{
		{"trun durations, an audio track too", in, seg, 25},
		{"after styp and sidx", in, append(slices.Clip(prefix), seg...), 25},
		{"cut after the moof", in, seg[:binary32(seg)+8], 25},
		{"tfhd default duration", describeInit(hevc60.init(t)), hevc60.segment(t, 0, 1, 100), 60},
		{"trex default duration", describeInit(trex30.init(t)), trex30.segment(t, 0, 1, 100), 30},
		{"one moof per track, the audio first", in, append(slices.Clip(audioFrag), videoFrag...), 25},
		{"one moof per track, the video first", in, append(slices.Clip(videoFrag), audioFrag...), 25},
		{"the video moof after describeMoofBytes", in, append(slices.Clip(bigAudio), videoFrag...), 0},
		{"moof cut short", in, seg[:binary32(seg)-1], 0},
		{"no sample of the video track", other, seg, 0},
		{"no video track in the init", initPicture{}, seg, 0},
		{"not a segment", in, []byte("<html>no video</html>"), 0},
		{"empty", in, nil, 0},
	} {
		if got := c.in.frameRate(c.head); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Boxes nested thousands deep where media.CheckInit does not look: a 'udta' in the
// 'moov', a child of the video sample entry, a 'udta' in a 'traf'. mp4ff decodes nested
// containers in time quadratic in their depth (the 'moov' of 60,000 took 18 s), so the
// description reads only the boxes it needs, each on its own: the same description, at
// once.
func TestDescribeInitNestedBoxes(t *testing.T) {
	st := fmp4Stream{video: "avc1", audio: true, fps: 25}
	want := describeInit(st.init(t))
	if want.pic.Codecs != "avc1.640028,mp4a.40.2" {
		t.Fatalf("the plain init: %+v", want)
	}
	for _, c := range []struct {
		name string
		init []byte
	}{
		{"udta nested in the moov", st.edited(t, func(i *mp4.InitSegment) { i.Moov.AddChild(nestedBox("udta", 60000)) })},
		{"udta nested in the video sample entry", st.edited(t, func(i *mp4.InitSegment) {
			i.Moov.Traks[0].Mdia.Minf.Stbl.Stsd.Children[0].(*mp4.VisualSampleEntryBox).AddChild(nestedBox("udta", 60000))
		})},
	} {
		if len(c.init) > describeHeadBytes || media.CheckInit(c.init) != nil {
			t.Fatalf("%s: %d bytes, %v", c.name, len(c.init), media.CheckInit(c.init))
		}
		if got, d := fastest(func() initPicture { return describeInit(c.init) }); got != want || d > 500*time.Millisecond {
			t.Errorf("%s: %+v in %v, want %+v at once", c.name, got, d, want)
		}
	}
	frag := st.fragment(t, 0, 2, 300)
	if err := frag.Moof.Trafs[0].AddChild(nestedBox("udta", 7000)); err != nil {
		t.Fatal(err)
	}
	seg := mp4Bytes(t, frag)
	if binary32(seg) > describeMoofBytes {
		t.Fatalf("moof of %d bytes", binary32(seg))
	}
	if fps, d := fastest(func() float64 { return want.frameRate(seg) }); fps != 25 || d > 100*time.Millisecond {
		t.Errorf("udta nested in the traf: %v fps in %v, want 25 at once", fps, d)
	}
}

// binary32 is the size field of the box at the start of b.
func binary32(b []byte) int { return int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3]) }

// FuzzDescribeInit: whatever an init section and the head of a first segment hold,
// describing them never panics and gives a bounded description: sizes within 16 bits
// (both or neither), no codecs for an encrypted init, codecs of bounded length, a frame
// rate of 0 or 1-240. closeCut, which describeInit runs only on a section cut at
// describeHeadBytes, is run on every init: nil, or no longer than its input and closed
// (closing it again changes nothing).
func FuzzDescribeInit(f *testing.F) {
	for _, st := range []fmp4Stream{
		{video: "avc1", audio: true, fps: 25},
		{video: "hvc1", fps: 60, defaultDur: true},
		{video: "av01", audio: true, fps: 30},
		{video: "avc1", fps: 24, trexDur: 3750},
		{video: "avc1", encrypt: true, fps: 24},
	} {
		f.Add(st.init(f), st.segment(f, 0, 1, 16))
	}
	st := fmp4Stream{video: "avc1", audio: true, fps: 25}
	nested := st.edited(f, func(i *mp4.InitSegment) {
		i.Moov.Traks[0].Mdia.Minf.Stbl.Stsd.Children[0].(*mp4.VisualSampleEntryBox).AddChild(nestedBox("udta", 8))
		i.Moov.AddChild(nestedBox("udta", 8))
	})
	perTrack := append(trackFragment(f, 1, 2, 4, 1024, 8), trackFragment(f, 2, 1, 2, 3600, 8)...)
	f.Add(nested, perTrack)
	f.Add(nested[:len(nested)-5], perTrack[:len(perTrack)-5])
	f.Add(initSegment("x", true), mediaSegment(1, 64))
	f.Add([]byte(nil), []byte(nil))
	f.Fuzz(func(t *testing.T, init, head []byte) {
		if c := closeCut(init); c != nil && (len(c) > len(init) || !bytes.Equal(closeCut(c), c)) {
			t.Fatalf("closeCut: %d bytes of %d, closed again %x", len(c), len(init), closeCut(c))
		}
		in := describeInit(init)
		p := in.pic
		if p.Width < 0 || p.Width > 0xffff || p.Height < 0 || p.Height > 0xffff || (p.Width == 0) != (p.Height == 0) {
			t.Fatalf("picture %dx%d", p.Width, p.Height)
		}
		if p.FrameRate != 0 || (in.encrypted && p.Codecs != "") || len(p.Codecs) > maxInitCodecs {
			t.Fatalf("description %+v", in)
		}
		if fps := in.frameRate(head); fps != 0 && (fps < 1 || fps > 240) {
			t.Fatalf("frame rate %v", fps)
		}
	})
}
