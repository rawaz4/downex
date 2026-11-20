package e2e

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/mp4"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testmedia"
)

// Streaming media (docs/MEDIA-SPEC.md §6), with real executables: "downex media" and
// browser hand-offs ("addMedia" through downex-nmhost) against an HTTP server that
// serves generated HLS and DASH presentations: fragmented MP4 written with mp4ff and
// MPEG-2 transport streams written with internal/testmedia (H.264, AAC), which downexd
// converts to MP4 (docs/VIDEO-DETECT-SPEC.md §4). downexd runs the real
// internal/mediajob and internal/remux.

// ---- stream server ---------------------------------------------------------------

const (
	hlsMIME  = "application/vnd.apple.mpegurl"
	dashMIME = "application/dash+xml"
)

type streamFile struct {
	data  []byte
	ctype string
}

// streamServer serves fixed files by path, counts the requests and complete responses
// per path, and can hold requests back (setGate) until the gate lets them through or
// the client gives up. The bounded requests with which a probe measures a stream
// (probeRequest) are recorded on their own (probesOf), never held back, and counted
// neither as requests nor as responses: those are the job's fetches.
type streamServer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	files   map[string]streamFile
	reqs    map[string]int
	served  map[string]int
	probes  map[string][]string // "<method> <Range>" of every probe request, per path
	cookies map[string][]string // Cookie header of every request, per path
	gate    func(path string) bool
	wake    chan struct{} // closed when the gate changes
	conns   int           // open client connections
}

func newStreamServer(t *testing.T) *streamServer {
	t.Helper()
	s := &streamServer{
		files: map[string]streamFile{}, reqs: map[string]int{}, served: map[string]int{},
		probes: map[string][]string{}, cookies: map[string][]string{}, wake: make(chan struct{}),
	}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	s.srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch st {
		case http.StateNew:
			s.conns++
		case http.StateClosed, http.StateHijacked:
			s.conns--
		}
	}
	s.srv.Start()
	t.Cleanup(s.srv.Close)
	t.Cleanup(func() { s.setGate(nil) }) // first: Close waits for held requests
	return s
}

func (s *streamServer) add(path string, data []byte, ctype string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = streamFile{data: data, ctype: ctype}
}

func (s *streamServer) url(path string) string { return s.srv.URL + path }

// probeRequest reports whether r is one of the requests with which mediajob.ProbeMP4
// measures a bare media playlist's track: a HEAD, or a GET of the first byte or the
// first 512 KiB of a segment. The job's own fetches of whole segments carry no Range,
// or one open at its end.
func probeRequest(r *http.Request) bool {
	switch rng := r.Header.Get("Range"); {
	case r.Method == http.MethodHead:
		return true
	case r.Method == http.MethodGet:
		return rng == "bytes=0-0" || rng == "bytes=0-524287"
	}
	return false
}

func (s *streamServer) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	probe := probeRequest(r)
	s.mu.Lock()
	f, ok := s.files[p]
	if probe {
		s.probes[p] = append(s.probes[p], r.Method+" "+r.Header.Get("Range"))
	} else {
		s.reqs[p]++
		s.cookies[p] = append(s.cookies[p], r.Header.Get("Cookie"))
	}
	s.mu.Unlock()
	if !ok || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	if !probe && !s.pass(r) {
		return
	}
	w.Header().Set("Content-Type", f.ctype)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.data))
	if r.Context().Err() == nil && !probe {
		s.mu.Lock()
		s.served[p]++
		s.mu.Unlock()
	}
}

// pass waits while the gate holds r back; false when the client gave up.
func (s *streamServer) pass(r *http.Request) bool {
	for {
		s.mu.Lock()
		gate, wake := s.gate, s.wake
		s.mu.Unlock()
		if gate == nil || !gate(r.URL.Path) {
			return true
		}
		select {
		case <-wake:
		case <-r.Context().Done():
			return false
		}
	}
}

// setGate holds back the requests whose path gate approves (nil: none).
func (s *streamServer) setGate(gate func(path string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gate = gate
	close(s.wake)
	s.wake = make(chan struct{})
}

// waitClosed waits until the server has no open connection. Called after the client
// was killed and before the gate changes: the server learns of the closed connections
// only some time after the process exited, and until it does, a request of the dead
// client that the gate held back would be let through by the new gate, answered into
// the closed connection and counted as a complete response.
func (s *streamServer) waitClosed(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(exitTimeout)
	for {
		s.mu.Lock()
		n := s.conns
		s.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d connections to the stream server still open after %v", n, exitTimeout)
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *streamServer) requests(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqs[path]
}

func (s *streamServer) servedCount(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.served[path]
}

// probesOf returns the probe requests of path ("<method> <Range>"), in order.
func (s *streamServer) probesOf(path string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.probes[path])
}

// requestedPaths returns the paths requested at least once, by a job or a probe, sorted.
func (s *streamServer) requestedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for p := range s.reqs {
		out = append(out, p)
	}
	for p := range s.probes {
		if _, ok := s.reqs[p]; !ok {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

func (s *streamServer) cookiesOf(path string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.cookies[path])
}

// segmentNumber returns N of a path ending in "seg-N.<ext>", or -1.
func segmentNumber(path string) int {
	i := strings.LastIndex(path, "seg-")
	j := strings.LastIndex(path, ".")
	if i < 0 || j < i {
		return -1
	}
	n, err := strconv.Atoi(path[i+len("seg-") : j])
	if err != nil {
		return -1
	}
	return n
}

// ---- generated fragmented MP4 --------------------------------------------------------

// bitWriter writes the RBSP of H.264 parameter sets (ISO/IEC 14496-10 §7.3.2.1).
type bitWriter struct {
	buf []byte
	n   uint
}

func (w *bitWriter) bit(b uint64) {
	if w.n%8 == 0 {
		w.buf = append(w.buf, 0)
	}
	if b&1 != 0 {
		w.buf[len(w.buf)-1] |= 0x80 >> (w.n % 8)
	}
	w.n++
}

func (w *bitWriter) bits(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v >> uint(i))
	}
}

// ue writes an unsigned Exp-Golomb code (§9.1).
func (w *bitWriter) ue(v uint64) {
	n := bits.Len64(v + 1)
	w.bits(0, n-1)
	w.bits(v+1, n)
}

// nal ends the RBSP and returns the NAL unit with emulation prevention (§7.4.1).
func (w *bitWriter) nal(header byte) []byte {
	w.bit(1) // rbsp_stop_one_bit
	for w.n%8 != 0 {
		w.bit(0)
	}
	out := []byte{header}
	zeros := 0
	for _, b := range w.buf {
		if zeros >= 2 && b <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// testSPS is a Main profile sequence parameter set for width x height frames (both
// multiples of 16).
func testSPS(width, height int) []byte {
	var w bitWriter
	w.bits(77, 8)               // profile_idc
	w.bits(0, 8)                // constraint flags
	w.bits(30, 8)               // level_idc
	w.ue(0)                     // seq_parameter_set_id
	w.ue(0)                     // log2_max_frame_num_minus4
	w.ue(0)                     // pic_order_cnt_type
	w.ue(2)                     // log2_max_pic_order_cnt_lsb_minus4
	w.ue(1)                     // max_num_ref_frames
	w.bit(0)                    // gaps_in_frame_num_value_allowed_flag
	w.ue(uint64(width/16 - 1))  // pic_width_in_mbs_minus1
	w.ue(uint64(height/16 - 1)) // pic_height_in_map_units_minus1
	w.bit(1)                    // frame_mbs_only_flag
	w.bit(1)                    // direct_8x8_inference_flag
	w.bit(0)                    // frame_cropping_flag
	w.bit(0)                    // vui_parameters_present_flag
	return w.nal(0x67)
}

func testPPS() []byte {
	var w bitWriter
	w.ue(0)      // pic_parameter_set_id
	w.ue(0)      // seq_parameter_set_id
	w.bit(0)     // entropy_coding_mode_flag
	w.bit(0)     // bottom_field_pic_order_in_frame_present_flag
	w.ue(0)      // num_slice_groups_minus1
	w.ue(0)      // num_ref_idx_l0_default_active_minus1
	w.ue(0)      // num_ref_idx_l1_default_active_minus1
	w.bit(0)     // weighted_pred_flag
	w.bits(0, 2) // weighted_bipred_idc
	w.ue(0)      // pic_init_qp_minus26
	w.ue(0)      // pic_init_qs_minus26
	w.ue(0)      // chroma_qp_index_offset
	w.bit(1)     // deblocking_filter_control_present_flag
	w.bit(0)     // constrained_intra_pred_flag
	w.bit(0)     // redundant_pic_cnt_present_flag
	return w.nal(0x68)
}

// Track timing of the generated streams: 25 fps video at 90 kHz, AAC at 48 kHz.
const (
	videoTimescale = 90000
	videoSampleDur = 3600
	videoPerSeg    = 25 // 1 s segments
	audioTimescale = 48000
	audioSampleDur = 1024
	audioPerSeg    = 47 // ~1 s segments
)

// genTrack is a generated fMP4 track: an initialization segment (ftyp+moov) and media
// segments (moof+mdat), one sync sample at the start of each video segment.
type genTrack struct {
	video   bool
	width   int
	height  int
	samples int    // in all segments
	init    []byte // initialization segment
	segs    [][]byte
	payload []byte // the sample data of every segment, in order
}

// newGenTrack generates a track of segs segments. protected adds a 'pssh' box to the
// initialization segment, as encrypted content carries.
func newGenTrack(t *testing.T, video bool, width, height, segs int, seed int64, protected bool) *genTrack {
	t.Helper()
	return newSizedTrack(t, video, width, height, segs, seed, protected, 40, 160)
}

// newSizedTrack is newGenTrack with samples of minSample to minSample+spread-1 bytes.
func newSizedTrack(t *testing.T, video bool, width, height, segs int, seed int64, protected bool, minSample, spread int) *genTrack {
	t.Helper()
	g := &genTrack{video: video, width: width, height: height}
	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = 1000
	timescale, dur, perSeg := uint32(audioTimescale), uint32(audioSampleDur), audioPerSeg
	if video {
		timescale, dur, perSeg = videoTimescale, videoSampleDur, videoPerSeg
		trak := init.AddEmptyTrack(timescale, "video", "und")
		if err := trak.SetAVCDescriptor("avc1", [][]byte{testSPS(width, height)}, [][]byte{testPPS()}, true); err != nil {
			t.Fatal(err)
		}
	} else {
		trak := init.AddEmptyTrack(timescale, "audio", "eng")
		if err := trak.SetAACDescriptor(aac.AAClc, audioTimescale); err != nil {
			t.Fatal(err)
		}
	}
	if protected {
		init.Moov.AddChild(&mp4.PsshBox{SystemID: make(mp4.UUID, 16), Data: []byte("e2e")})
	}
	g.init = encodeMP4(t, init)

	rng := rand.New(rand.NewSource(seed))
	var dt uint64
	for k := range segs {
		frag, err := mp4.CreateFragment(uint32(k+1), 1)
		if err != nil {
			t.Fatal(err)
		}
		for i := range perSeg {
			data := make([]byte, minSample+rng.Intn(spread))
			rng.Read(data)
			flags := mp4.SyncSampleFlags
			if video && i > 0 {
				flags = mp4.NonSyncSampleFlags | mp4.SampleDependsOn1
			}
			frag.AddFullSample(mp4.FullSample{
				Sample:     mp4.Sample{Flags: flags, Dur: dur, Size: uint32(len(data))},
				DecodeTime: dt,
				Data:       data,
			})
			dt += uint64(dur)
			g.payload = append(g.payload, data...)
			g.samples++
		}
		g.segs = append(g.segs, encodeMP4(t, frag))
	}
	return g
}

func encodeMP4(t *testing.T, b interface{ Encode(w io.Writer) error }) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := b.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// serve publishes the track at dir+"init.mp4" and dir+"seg-<n>.m4s" (n from 1).
func (g *genTrack) serve(s *streamServer, dir string) {
	s.add(dir+"init.mp4", g.init, "video/mp4")
	for i, seg := range g.segs {
		s.add(fmt.Sprintf("%sseg-%d.m4s", dir, i+1), seg, "video/iso.segment")
	}
}

// checkMP4 decodes a progressive MP4 and compares its tracks with want, in order:
// handler, sample count, picture size and the sample data.
func checkMP4(t *testing.T, path string, want ...*genTrack) {
	t.Helper()
	fd, err := os.Open(path)
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	defer fd.Close()
	f, err := mp4.DecodeFile(fd, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if f.IsFragmented() || f.Moov == nil {
		t.Fatalf("%s is not a progressive MP4", path)
	}
	var top []string
	for _, c := range f.Children {
		top = append(top, c.Type())
	}
	if !slices.Equal(top, []string{"ftyp", "moov", "mdat"}) {
		t.Errorf("top-level boxes %v, want ftyp moov mdat", top)
	}
	if len(f.Moov.Traks) != len(want) {
		t.Fatalf("%s has %d tracks, want %d", path, len(f.Moov.Traks), len(want))
	}
	for i, g := range want {
		trak := f.Moov.Traks[i]
		handler, ts := "soun", uint32(audioTimescale)
		if g.video {
			handler, ts = "vide", videoTimescale
		}
		if h := trak.Mdia.Hdlr.HandlerType; h != handler {
			t.Errorf("track %d: handler %q, want %q", i+1, h, handler)
		}
		if got := trak.Mdia.Mdhd.Timescale; got != ts {
			t.Errorf("track %d: timescale %d, want %d", i+1, got, ts)
		}
		n := trak.Mdia.Minf.Stbl.Stsz.GetNrSamples()
		if int(n) != g.samples {
			t.Fatalf("track %d: %d samples, want %d", i+1, n, g.samples)
		}
		if g.video {
			w, h := int(trak.Tkhd.Width>>16), int(trak.Tkhd.Height>>16)
			avc := trak.Mdia.Minf.Stbl.Stsd.AvcX
			if w != g.width || h != g.height || avc == nil || int(avc.Width) != g.width || int(avc.Height) != g.height {
				t.Errorf("track %d: picture %dx%d (sample entry %v), want %dx%d", i+1, w, h, avc, g.width, g.height)
			}
		}
		data, err := f.ReadSampleData(trak, 1, n, fd, nil)
		if err != nil {
			t.Fatalf("track %d: read samples: %v", i+1, err)
		}
		if !bytes.Equal(data, g.payload) {
			t.Errorf("track %d: sample data differs from the served segments", i+1)
		}
	}
}

// ---- manifests -----------------------------------------------------------------------

// dashManifest is a static MPD with SegmentTimeline templates: one video
// AdaptationSet (videoReps: id → "WxH") and one audio Representation "a128", segs
// segments each, served below the MPD's directory as video/<id>/… and audio/….
func dashManifest(segs int, videoReps ...[3]string) string {
	var reps strings.Builder
	for _, r := range videoReps {
		w, h, _ := strings.Cut(r[1], "x")
		fmt.Fprintf(&reps, "      <Representation id=%q bandwidth=%q width=%q height=%q frameRate=\"25\" codecs=\"avc1.4d401e\"/>\n",
			r[0], r[2], w, h)
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT%dS" minBufferTime="PT2S"
     profiles="urn:mpeg:dash:profile:isoff-live:2011">
  <Period id="p0">
    <AdaptationSet mimeType="video/mp4" contentType="video" segmentAlignment="true">
      <SegmentTemplate timescale="%d" initialization="video/$RepresentationID$/init.mp4"
                       media="video/$RepresentationID$/seg-$Number$.m4s" startNumber="1">
        <SegmentTimeline><S t="0" d="%d" r="%d"/></SegmentTimeline>
      </SegmentTemplate>
%s    </AdaptationSet>
    <AdaptationSet mimeType="audio/mp4" contentType="audio" lang="en">
      <SegmentTemplate timescale="%d" initialization="audio/init.mp4" media="audio/seg-$Number$.m4s" startNumber="1">
        <SegmentTimeline><S t="0" d="%d" r="%d"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="a128" bandwidth="128000" codecs="mp4a.40.2" audioSamplingRate="48000"/>
    </AdaptationSet>
  </Period>
</MPD>
`, segs, videoTimescale, videoPerSeg*videoSampleDur, segs-1, reps.String(),
		audioTimescale, audioPerSeg*audioSampleDur, segs-1)
}

// hlsMediaPlaylist is a VOD media playlist of segs segments "seg-<n>.<ext>" of the
// given duration, with an EXT-X-MAP when mapURI is set; extra lines go before the
// first segment.
func hlsMediaPlaylist(segs int, dur float64, ext, mapURI string, extra ...string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	if mapURI != "" {
		fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", mapURI)
	}
	for _, l := range extra {
		b.WriteString(l + "\n")
	}
	for i := 1; i <= segs; i++ {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg-%d.%s\n", dur, i, ext)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// ---- generated MPEG-TS ---------------------------------------------------------------

// PIDs of the generated transport streams.
const (
	tsVideoPID = 0x100
	tsAudioPID = 0x101
)

// tsSpec describes a generated MPEG-2 transport stream (internal/testmedia): an H.264
// video stream, an AAC (ADTS) stream at 48 kHz, or both multiplexed.
type tsSpec struct {
	video     *testmedia.VideoConfig // nil: no video stream (Width and Height must be set)
	audio     *testmedia.AudioConfig // nil: no audio stream
	audioType byte                   // the audio's stream type; 0: AAC ADTS
	scrambled bool                   // transport_scrambling_control set on the video packets
	seconds   float64                // segment length; 0: 2 s
}

// genTS is a generated transport stream cut into HLS segments, each starting with the
// PAT and PMT: at video sync pictures about every tsSpec.seconds (audio only: after
// that much audio).
type genTS struct {
	width, height int
	aus           []testmedia.AccessUnit
	frames        []testmedia.AudioFrame
	segs          [][]byte
	durs          []float64 // the EXTINF of each segment, in seconds
}

func newGenTS(t *testing.T, s tsSpec) *genTS {
	t.Helper()
	if s.seconds == 0 {
		s.seconds = 2
	}
	g := &genTS{}
	m := &testmedia.Muxer{}
	var lists [][]testmedia.PES
	var end int64 // the decode time the last segment ends at, 90 kHz
	if v := s.video; v != nil {
		if v.Width == 0 || v.Height == 0 || v.Frames == 0 {
			t.Fatalf("tsSpec video %+v needs a size and frames", *v)
		}
		ticks := cmp.Or(v.FrameTicks, 3000)
		g.width, g.height, g.aus = v.Width, v.Height, testmedia.Video(*v)
		m.Streams = append(m.Streams, testmedia.Stream{PID: tsVideoPID, Type: testmedia.TypeH264})
		units := testmedia.VideoPES(tsVideoPID, g.aus)
		for i := range units {
			units[i].TSScrambled = s.scrambled
		}
		lists = append(lists, units)
		end = g.aus[len(g.aus)-1].DTS + ticks
	}
	if a := s.audio; a != nil {
		if a.SampleRate != 0 && a.SampleRate != audioTimescale {
			t.Fatalf("tsSpec audio at %d Hz: checkMP4 expects %d", a.SampleRate, audioTimescale)
		}
		g.frames = testmedia.AAC(*a)
		m.Streams = append(m.Streams, testmedia.Stream{PID: tsAudioPID, Type: cmp.Or(s.audioType, testmedia.TypeAAC)})
		lists = append(lists, testmedia.AudioPES(tsAudioPID, g.frames, 5))
		end = max(end, g.frames[len(g.frames)-1].PTS+1024*90000/audioTimescale)
	}
	units := testmedia.Interleave(lists...)
	cut := func() func(int, testmedia.PES) bool {
		if s.video != nil {
			return testmedia.SyncCut(tsVideoPID, s.seconds)
		}
		per := max(1, int(s.seconds*audioTimescale/audioSampleDur/5)) // 5 frames per PES
		return func(i int, _ testmedia.PES) bool { return i%per == 0 }
	}
	g.segs = m.Segments(units, cut())
	g.durs = testmedia.Durations(units, cut(), end)
	return g
}

// tsTracks returns the tracks that converting parts (one after the other) saves, as
// checkMP4 compares them: the video with every access unit's MP4 sample, then the
// audio with every raw AAC frame.
func tsTracks(parts ...*genTS) []*genTrack {
	video, audio := &genTrack{video: true}, &genTrack{}
	for _, g := range parts {
		if len(g.aus) > 0 {
			video.width, video.height = g.width, g.height
		}
		for _, au := range g.aus {
			video.payload = append(video.payload, au.Sample...)
			video.samples++
		}
		for _, f := range g.frames {
			audio.payload = append(audio.payload, f.Raw...)
			audio.samples++
		}
	}
	var out []*genTrack
	if video.samples > 0 {
		out = append(out, video)
	}
	if audio.samples > 0 {
		out = append(out, audio)
	}
	return out
}

// serveTS publishes parts as one HLS media playlist dir+"index.m3u8" of the segments
// dir+"seg-<n>.ts" (n from 1, in order), with EXT-X-DISCONTINUITY before the first
// segment of every part but the first, and returns the segments in order.
func serveTS(s *streamServer, dir string, parts ...*genTS) [][]byte {
	var list []testmedia.Segment
	var segs [][]byte
	for k, g := range parts {
		for i, seg := range g.segs {
			segs = append(segs, seg)
			uri := fmt.Sprintf("seg-%d.ts", len(segs))
			list = append(list, testmedia.Segment{URI: uri, Duration: g.durs[i], Discontinuity: k > 0 && i == 0})
			s.add(dir+uri, seg, "video/mp2t")
		}
	}
	s.add(dir+"index.m3u8", []byte(testmedia.MediaPlaylist(list)), hlsMIME)
	return segs
}

// checkTiming checks the timing of the progressive MP4 at path: the movie lasts
// duration seconds (within one frame at 30 fps), and the first sample of each track is
// presented at its starts entry (within 1 ms) on the movie timeline: after the track's
// empty edits, at its composition time less the media edit's start.
func checkTiming(t *testing.T, path string, duration float64, starts ...float64) {
	t.Helper()
	fd, err := os.Open(path)
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	defer fd.Close()
	f, err := mp4.DecodeFile(fd, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil || f.Moov == nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	movie := float64(f.Moov.Mvhd.Timescale)
	if got := float64(f.Moov.Mvhd.Duration) / movie; math.Abs(got-duration) > 1.0/30 {
		t.Errorf("%s lasts %.3f s, want %.3f s", path, got, duration)
	}
	if len(f.Moov.Traks) != len(starts) {
		t.Fatalf("%s has %d tracks, want %d", path, len(f.Moov.Traks), len(starts))
	}
	for i, trak := range f.Moov.Traks {
		stbl := trak.Mdia.Minf.Stbl
		n := stbl.Stsz.GetNrSamples()
		durs, err := stbl.Stts.SampleDurations(n)
		if err != nil {
			t.Fatalf("track %d: %v", i+1, err)
		}
		var ctos []int32
		if stbl.Ctts != nil {
			if ctos, err = stbl.Ctts.CompositionTimeOffsets(n); err != nil {
				t.Fatalf("track %d: %v", i+1, err)
			}
		}
		ts := float64(trak.Mdia.Mdhd.Timescale)
		var empty, mediaTime float64
		if trak.Edts != nil && len(trak.Edts.Elst) > 0 {
			for _, e := range trak.Edts.Elst[0].Entries {
				if e.MediaTime == -1 {
					empty += float64(e.SegmentDuration) / movie
					continue
				}
				mediaTime = float64(e.MediaTime) / ts
				break
			}
		}
		first, dts := math.Inf(1), uint64(0)
		for k := range n {
			c := 0.0
			if ctos != nil {
				c = float64(ctos[k])
			}
			first = min(first, (float64(dts)+c)/ts)
			dts += uint64(durs[k])
		}
		if got := empty + first - mediaTime; math.Abs(got-starts[i]) > 0.001 {
			t.Errorf("track %d starts at %.4f s, want %.4f s", i+1, got, starts[i])
		}
	}
}

// ---- views ---------------------------------------------------------------------------

// mediaEntry is the part of "downex info --json" (read from the database) that the
// media tests read.
type mediaEntry struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
	Kind  string `json:"kind"`
	Path  string `json:"path"`
	Media *struct {
		FragmentsTotal int    `json:"fragmentsTotal"`
		FragmentsDone  int    `json:"fragmentsDone"`
		SelectedVideo  string `json:"selectedVideo"`
		SelectedAudio  string `json:"selectedAudio"`
	} `json:"media"`
}

func (h *harness) mediaInfo(id int64) mediaEntry {
	h.t.Helper()
	r := h.cli("info", idArg(id), "--json").ok(h.t)
	var v mediaEntry
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.Media == nil {
		h.t.Fatalf("info --json: %v: %v", err, r)
	}
	return v
}

// waitFragments waits until running media download id reports at least n durable
// fragments, and (stored) until its stored checkpoint covers them too.
func (h *harness) waitFragments(id int64, n int, stored bool) {
	h.t.Helper()
	deadline := time.Now().Add(progressTimeout)
	for {
		d := h.get(id)
		done := int(d.GetMedia().GetFragmentsDone())
		switch {
		case done > n:
			h.t.Fatalf("download %d has %d fragments, more than the %d the server let through: %v", id, done, n, d)
		case done == n && (!stored || h.mediaInfo(id).Media.FragmentsDone == n):
			return
		case !running(d.GetState()) && d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_QUEUED:
			h.t.Fatalf("download %d stopped at %d of %d fragments: %v", id, done, n, d)
		case time.Now().After(deadline):
			h.t.Fatalf("download %d did not reach %d fragments within %v: %v", id, n, progressTimeout, d)
		}
		time.Sleep(pollInterval)
	}
}

// waitState waits until download id is in state s.
func (h *harness) waitState(id int64, s downexv1.DownloadState) *downexv1.Download {
	h.t.Helper()
	deadline := time.Now().Add(progressTimeout)
	for {
		d := h.get(id)
		if d.GetState() == s {
			return d
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("download %d is not %v within %v: %v", id, s, progressTimeout, d)
		}
		time.Sleep(pollInterval)
	}
}

func noMediaLeftovers(t *testing.T, dir string) {
	t.Helper()
	noPartFiles(t, dir)
	if m, _ := filepath.Glob(filepath.Join(dir, "*.dxparts")); len(m) > 0 {
		t.Errorf("leftover part directories: %v", m)
	}
}

// ---- tests ---------------------------------------------------------------------------

// (a) A DASH presentation with separate fMP4 video and audio: "downex media --wait"
// downloads the default pick (the best video and the audio) and joins them into one
// progressive MP4 holding both tracks with every sample.
func TestMediaDASHVideoAudio(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := newStreamServer(t)
	const segs = 6
	v720 := newGenTrack(t, true, 1280, 720, segs, 11, false)
	v352 := newGenTrack(t, true, 640, 352, segs, 12, false)
	audio := newGenTrack(t, false, 0, 0, segs, 13, false)
	s.add("/dash/clip/manifest.mpd", []byte(dashManifest(segs, [3]string{"v720", "1280x720", "1500000"},
		[3]string{"v352", "640x352", "600000"})), dashMIME)
	v720.serve(s, "/dash/clip/video/v720/")
	v352.serve(s, "/dash/clip/video/v352/")
	audio.serve(s, "/dash/clip/audio/")
	d := h.startDaemon()
	h.waitReady(d)

	r := h.cli("media", s.url("/dash/clip/manifest.mpd"), "--list", "--json").ok(t)
	var list struct {
		Format        string `json:"format"`
		Container     string `json:"container"`
		SelectedVideo string `json:"selectedVideo"`
		SelectedAudio string `json:"selectedAudio"`
		Tracks        []struct {
			ID string `json:"id"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &list); err != nil {
		t.Fatalf("media --list --json: %v: %v", err, r)
	}
	if list.Format != "dash" || list.Container != "mp4" || list.SelectedVideo != "v720" || list.SelectedAudio != "a128" ||
		len(list.Tracks) != 3 {
		t.Fatalf("media --list = %+v", list)
	}

	r = h.cli("media", s.url("/dash/clip/manifest.mpd"), "-o", h.out, "--wait").ok(t)
	id, err := strconv.ParseInt(strings.TrimSpace(r.stdout), 10, 64)
	if err != nil {
		t.Fatalf("no download id on stdout: %v", r)
	}
	path := filepath.Join(h.out, "clip.mp4")
	if !strings.Contains(r.stderr, "Saved "+path) {
		t.Errorf("media --wait did not report the saved file: %v", r)
	}
	checkMP4(t, path, v720, audio)
	noMediaLeftovers(t, h.out)

	dl := h.get(id)
	m := dl.GetMedia()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if dl.GetKind() != downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA || dl.GetSize() != fi.Size() ||
		m.GetFormat() != "dash" || m.GetContainer() != "mp4" || m.GetSelectedVideo() != "v720" ||
		m.GetSelectedAudio() != "a128" || m.GetFragmentsTotal() != 2*(segs+1) || m.GetFragmentsDone() != 2*(segs+1) ||
		dl.GetCategory() != "Video" {
		t.Fatalf("completed media download = %v", dl)
	}
	for _, p := range s.requestedPaths() {
		if strings.Contains(p, "/v352/") {
			t.Errorf("a track that was not selected was fetched: %s", p)
		} else if p != "/dash/clip/manifest.mpd" && s.requests(p) != 1 {
			t.Errorf("%s was fetched %d times", p, s.requests(p))
		}
	}
	h.stop(d)
}

// A part of a stream (docs/MEDIA-SPEC.md §8): "downex media --from --to" fetches only the
// window's segments (C4: the video's that overlap the part, the audio's from the start of the
// video window), names the file by the part (C19), cuts the MP4 from the keyframe at or before
// From with an edit list that shows exactly From–To (C6–C8, anchored on the video, C3) and
// records what the part became (C20).
func TestMediaClipDASH(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := newStreamServer(t)
	const segs = 8
	video := newGenTrack(t, true, 640, 352, segs, 21, false)
	audio := newGenTrack(t, false, 0, 0, segs, 22, false)
	s.add("/dash/talk/manifest.mpd", []byte(dashManifest(segs, [3]string{"v352", "640x352", "600000"})), dashMIME)
	video.serve(s, "/dash/talk/video/v352/")
	audio.serve(s, "/dash/talk/audio/")
	d := h.startDaemon()
	h.waitReady(d)

	r := h.cli("media", s.url("/dash/talk/manifest.mpd"), "-o", h.out, "--from", "2.5", "--to", "4.5", "--wait").ok(t)
	id, err := strconv.ParseInt(strings.TrimSpace(r.stdout), 10, 64)
	if err != nil {
		t.Fatalf("no download id on stdout: %v", r)
	}
	path := filepath.Join(h.out, "talk (00m02s-00m05s).mp4")
	if !strings.Contains(r.stderr, "Saved "+path) {
		t.Errorf("media --wait did not report the part's file: %v", r)
	}
	// Video segments 3-5 hold 2-5 s; audio segments (1.0027 s each) from the one that holds
	// 2 s, the video window's start, up to the one that holds 4.5 s.
	for n := 1; n <= segs; n++ {
		want := 0
		if n >= 3 && n <= 5 {
			want = 1
		}
		if got := s.requests(fmt.Sprintf("/dash/talk/video/v352/seg-%d.m4s", n)); got != want {
			t.Errorf("video segment %d fetched %d times, want %d", n, got, want)
		}
		want = 0
		if n >= 2 && n <= 5 {
			want = 1
		}
		if got := s.requests(fmt.Sprintf("/dash/talk/audio/seg-%d.m4s", n)); got != want {
			t.Errorf("audio segment %d fetched %d times, want %d", n, got, want)
		}
	}
	noMediaLeftovers(t, h.out)

	fd, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	f, err := mp4.DecodeFile(fd, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil || f.IsFragmented() || f.Moov == nil || len(f.Moov.Traks) != 2 {
		t.Fatalf("output %s: %v", path, err)
	}
	vt, at := f.Moov.Traks[0], f.Moov.Traks[1]
	// Video from the keyframe at 2 s (decode times 2.00-4.48 s: 63 frames), shown from 2.5 s
	// for 2 s.
	if n := vt.Mdia.Minf.Stbl.Stsz.GetNrSamples(); n != 63 {
		t.Errorf("%d video samples, want 63", n)
	}
	edit := func(trak *mp4.TrakBox) (media, dur float64) {
		if trak.Edts == nil || len(trak.Edts.Elst) == 0 {
			t.Fatalf("track %d has no edit list", trak.Tkhd.TrackID)
		}
		for _, e := range trak.Edts.Elst[0].Entries {
			if e.MediaTime >= 0 {
				return float64(e.MediaTime) / float64(trak.Mdia.Mdhd.Timescale), float64(e.SegmentDuration) / float64(f.Moov.Mvhd.Timescale)
			}
		}
		t.Fatalf("track %d has no media edit", trak.Tkhd.TrackID)
		return 0, 0
	}
	if mt, dur := edit(vt); math.Abs(mt-0.5) > 1e-6 || math.Abs(dur-2) > 0.002 {
		t.Errorf("video edit: media time %v s, duration %v s; want 0.5 and 2", mt, dur)
	}
	// Audio from the frame that holds 2 s; the tracks are placed on the movie's 1 ms grid.
	frame := float64(audioSampleDur) / audioTimescale
	first := math.Floor(2 / frame)
	if mt, dur := edit(at); math.Abs(mt-(2.5-first*frame)) > 0.001 || math.Abs(dur-2) > 0.002 {
		t.Errorf("audio edit: media time %v s, duration %v s; want %v and 2", mt, dur, 2.5-first*frame)
	}

	c := h.get(id).GetMedia().GetClip()
	if c.GetStartMs() != 2500 || c.GetEndMs() != 4500 || c.GetKeyframeMs() != 2000 || c.GetFetchStartMs() != 2000 ||
		c.GetFetchEndMs() != 5000 || c.GetFetchedWhole() {
		t.Errorf("stored part %v", c)
	}
	if info := h.cli("info", strconv.FormatInt(id, 10)).ok(t); !regexp.MustCompile(`(?m)^Part: +0:02\.5 – 0:04\.5 \(keyframe 0:02\)$`).MatchString(info.stdout) {
		t.Errorf("info has no Part line: %v", info)
	}
	h.stop(d)
}

// (b) An HLS master playlist of MPEG-TS variants (H.264 with B-frames and AAC, muxed,
// with timestamps that wrap past 2^33 after 1 s; docs/VIDEO-DETECT-SPEC.md §8.3 #1):
// the best variant's segments are fetched once each and converted into one progressive
// MP4, "episode.mp4", holding every access unit and AAC frame, as long as the stream.
func TestMediaHLSTransportStream(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := newStreamServer(t)
	const start = 1<<33 - 90000 // decode time of the first picture: the wrap comes after 1 s
	variant := func(width, height int, seed int64) *genTS {
		return newGenTS(t, tsSpec{
			video: &testmedia.VideoConfig{Width: width, Height: height, Frames: 288, BFrames: true, StartDTS: start, Seed: seed},
			// The first picture is presented one frame after its decode time.
			audio: &testmedia.AudioConfig{Frames: 450, StartPTS: start + 3000, Seed: seed + 1},
		})
	}
	lo, hi := variant(320, 180, 51), variant(640, 360, 53)
	s.add("/hls/show/master.m3u8", []byte(testmedia.MasterPlaylist([]testmedia.Variant{
		{URI: "lo/index.m3u8", Bandwidth: 400000, Codecs: "avc1.4d401f,mp4a.40.2", Resolution: "320x180"},
		{URI: "hi/index.m3u8", Bandwidth: 1200000, Codecs: "avc1.4d401f,mp4a.40.2", Resolution: "640x360"},
	}, nil)), hlsMIME)
	serveTS(s, "/hls/show/lo/", lo)
	serveTS(s, "/hls/show/hi/", hi)
	d := h.startDaemon()
	h.waitReady(d)

	r := h.cli("media", s.url("/hls/show/master.m3u8"), "-o", h.out, "--name", "episode", "--wait").ok(t)
	id := r.id(t)
	path := filepath.Join(h.out, "episode.mp4")
	if !strings.Contains(r.stderr, "Saved "+path) {
		t.Errorf("media --wait did not report the saved file: %v", r)
	}
	checkMP4(t, path, tsTracks(hi)...)
	// 288 pictures at 30 fps and 450 AAC frames of 1024 samples at 48 kHz: 9.6 s each,
	// both presented from the start.
	checkTiming(t, path, 9.6, 0, 0)
	noMediaLeftovers(t, h.out)
	if m, _ := filepath.Glob(filepath.Join(h.out, "*.ts")); len(m) > 0 {
		t.Errorf("transport stream files were saved: %v", m)
	}
	for i := 1; i <= len(hi.segs); i++ {
		if n := s.requests(fmt.Sprintf("/hls/show/hi/seg-%d.ts", i)); n != 1 {
			t.Errorf("segment %d fetched %d times", i, n)
		}
	}
	for i := 1; i <= len(lo.segs); i++ {
		if n := s.requests(fmt.Sprintf("/hls/show/lo/seg-%d.ts", i)); n != 0 {
			t.Errorf("segment %d of the variant that was not selected was fetched", i)
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if dl := h.get(id); dl.GetFilename() != "episode.mp4" || dl.GetMedia().GetContainer() != "mp4" ||
		dl.GetMedia().GetSelectedVideo() != "v1" || dl.GetSize() != fi.Size() || dl.GetCategory() != "Video" {
		t.Fatalf("completed media download = %v", dl)
	}
	h.stop(d)
}

// (c) Encrypted content is refused: an HLS playlist with EXT-X-KEY (AES-128) when it
// is added, through the CLI, the API and the browser relay alike (error code
// "encrypted"; nothing is recorded, no key or segment is fetched); a live playlist
// likewise ("live"). A DASH stream whose initialization segment carries a 'pssh'
// box fails with error_code "encrypted" before any media segment is fetched.
func TestMediaEncryptedRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := newStreamServer(t)
	key := `#EXT-X-KEY:METHOD=AES-128,URI="key.bin",IV=0x00000000000000000000000000000001`
	s.add("/enc/index.m3u8", []byte(hlsMediaPlaylist(3, 2, "ts", "", key)), hlsMIME)
	s.add("/enc/master.m3u8", []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=900000,CODECS=\"avc1.4d401e,mp4a.40.2\"\nindex.m3u8\n"), hlsMIME)
	s.add("/enc/key.bin", make([]byte, 16), "application/octet-stream")
	enc := newGenTS(t, tsSpec{video: &testmedia.VideoConfig{Width: 320, Height: 180, Frames: 180, Seed: 61},
		audio: &testmedia.AudioConfig{Frames: 280, Seed: 62}})
	for i := 1; i <= 3; i++ {
		s.add(fmt.Sprintf("/enc/seg-%d.ts", i), enc.segs[(i-1)%len(enc.segs)], "video/mp2t")
	}
	// No EXT-X-ENDLIST and no PLAYLIST-TYPE: the server may still add segments.
	live := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:40\n#EXTINF:2.0,\nseg-1.ts\n#EXTINF:2.0,\nseg-2.ts\n"
	s.add("/live/index.m3u8", []byte(live), hlsMIME)
	inst := h.newDesktopInstall()
	d := h.startDaemonFrom(inst.downexd, inst.env)
	h.waitReady(d)
	h.setDefaultDir(h.out)

	for _, p := range []string{"/enc/index.m3u8", "/enc/master.m3u8"} {
		r := h.cli("media", s.url(p), "-o", h.out)
		if r.code == 0 || !strings.Contains(r.stderr, "encrypted or DRM-protected") {
			t.Errorf("media %s: %v", p, r)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*rpcCallTimeout)
	defer cancel()
	for p, code := range map[string]string{"/enc/master.m3u8": "encrypted", "/live/index.m3u8": "live"} {
		_, err := h.dl.AddMedia(ctx, connect.NewRequest(&downexv1.AddMediaRequest{ManifestUrl: s.url(p), Dir: h.out}))
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != connect.CodeInvalidArgument || ce.Meta().Get("Downex-Error-Code") != code ||
			!strings.HasPrefix(ce.Message(), code+": ") {
			t.Errorf("AddMedia %s: %v, want InvalidArgument with code %s", p, err, code)
		}
	}
	// The browser relay answers "rejected" with the code first in the message.
	p := h.startNMHost(h.env)
	p.send(map[string]any{"id": "1", "type": "hello", "protocol": 1, "browser": "chrome", "extVersion": "0.1.0"})
	if w := p.recv(); w["type"] != "welcome" {
		t.Fatalf("hello answered %v", w)
	}
	p.send(map[string]any{"id": "2", "type": "addMedia", "manifestUrl": s.url("/enc/index.m3u8"),
		"headers": [][]string{{"Cookie", "sid=e2e"}}, "title": "Protected", "deadlineMs": 15000})
	a := p.recv()
	e, _ := a["error"].(map[string]any)
	if a["ok"] != false || e["code"] != "rejected" || !strings.HasPrefix(fmt.Sprint(e["message"]), "encrypted") {
		t.Errorf("addMedia of an encrypted stream answered %v", a)
	}
	p.closeAndWait()
	if list := h.ls(); len(list) != 0 {
		t.Fatalf("refused streams were recorded: %+v", list)
	}
	for _, p := range s.requestedPaths() {
		if !strings.HasSuffix(p, ".m3u8") {
			t.Errorf("%s was fetched for a refused stream", p)
		}
	}

	// Protection found only in the initialization segment fails the download.
	const segs = 3
	prot := newGenTrack(t, true, 640, 352, segs, 21, true)
	audio := newGenTrack(t, false, 0, 0, segs, 22, false)
	s.add("/drm/movie.mpd", []byte(dashManifest(segs, [3]string{"v1", "640x352", "600000"})), dashMIME)
	prot.serve(s, "/drm/video/v1/")
	audio.serve(s, "/drm/audio/")
	r := h.cli("media", s.url("/drm/movie.mpd"), "-o", h.out, "--wait")
	if r.code == 0 || !strings.Contains(r.stderr, "encrypted or DRM-protected") {
		t.Errorf("media --wait of a stream with a protected init segment: %v", r)
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.stdout), 10, 64)
	if err != nil {
		t.Fatalf("no download id on stdout: %v", r)
	}
	dl := h.get(id)
	if dl.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_FAILED || dl.GetErrorCode() != "encrypted" {
		t.Fatalf("download with a protected init segment = %v", dl)
	}
	for i := 1; i <= segs; i++ {
		for _, dir := range []string{"/drm/video/v1/", "/drm/audio/"} {
			if n := s.requests(fmt.Sprintf("%sseg-%d.m4s", dir, i)); n != 0 {
				t.Errorf("media segment %sseg-%d.m4s was fetched although the init segment is protected", dir, i)
			}
		}
	}
	if m, _ := filepath.Glob(filepath.Join(h.out, "*.mp4")); len(m) > 0 {
		t.Errorf("output written for protected content: %v", m)
	}
	noPartFiles(t, h.out)
	h.stop(d)
}

// (d) A media download paused midway keeps its durable fragments across a hard kill of
// the daemon: resumed after a restart it fetches only the others; killed again while
// running, the restarted daemon requeues it from its last checkpoint, and it
// completes. No fragment is downloaded twice.
func TestMediaPauseKillResume(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := newStreamServer(t)
	const segs = 12
	video := newGenTrack(t, true, 640, 352, segs, 31, false)
	audio := newGenTrack(t, false, 0, 0, segs, 32, false)
	s.add("/r/talk/manifest.mpd", []byte(dashManifest(segs, [3]string{"v1", "640x352", "600000"})), dashMIME)
	video.serve(s, "/r/talk/video/v1/")
	audio.serve(s, "/r/talk/audio/")
	var paths []string // every fragment of the presentation
	for _, dir := range []string{"/r/talk/video/v1/", "/r/talk/audio/"} {
		paths = append(paths, dir+"init.mp4")
		for i := 1; i <= segs; i++ {
			paths = append(paths, fmt.Sprintf("%sseg-%d.m4s", dir, i))
		}
	}
	upTo := func(n int) func(string) bool { // holds back segments after the n-th
		return func(p string) bool { return segmentNumber(p) > n }
	}

	// 1. Pause after the init segments and 4 segments of each track.
	s.setGate(upTo(4))
	d := h.startDaemon()
	h.waitReady(d)
	r := h.cli("media", s.url("/r/talk/manifest.mpd"), "-o", h.out, "--name", "talk").ok(t)
	id := r.id(t)
	h.waitFragments(id, 2*(1+4), false)
	h.cli("pause", idArg(id)).ok(t)
	h.waitState(id, downexv1.DownloadState_DOWNLOAD_STATE_PAUSED)
	if e := h.mediaInfo(id); e.State != "paused" || e.Media.FragmentsDone != 10 || e.Media.FragmentsTotal != 2*(segs+1) {
		t.Fatalf("paused download in the database = %+v %+v", e, *e.Media)
	}
	d.kill(t)
	s.waitClosed(t)
	atPause := map[string]bool{}
	for _, p := range paths {
		if segmentNumber(p) <= 4 {
			atPause[p] = true
		}
	}

	// 2. Resume after a restart; kill the daemon once 8 segments per track are stored.
	s.setGate(upTo(8))
	d = h.startDaemon()
	h.waitReady(d)
	if got := h.get(id); got.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_PAUSED {
		t.Fatalf("after the restart: %v", got)
	}
	h.cli("resume", idArg(id)).ok(t)
	h.waitFragments(id, 2*(1+8), true)
	d.kill(t)
	s.waitClosed(t)

	// 3. The restarted daemon requeues it from the stored checkpoint.
	s.setGate(nil)
	d = h.startDaemon()
	h.waitReady(d)
	dl := h.waitCompleted(id)
	path := filepath.Join(h.out, "talk.mp4")
	if dl.GetFilename() != "talk.mp4" {
		t.Fatalf("completed as %q", dl.GetFilename())
	}
	checkMP4(t, path, video, audio)
	noMediaLeftovers(t, h.out)
	for _, p := range paths {
		if n := s.servedCount(p); n != 1 {
			t.Errorf("%s was downloaded %d times", p, n)
		}
		if n := s.requests(p); atPause[p] && n != 1 {
			t.Errorf("%s, durable when the download was paused, was requested %d times", p, n)
		}
	}
	h.stop(d)
}

// (e) A stream handed off by the browser ("addMedia" through downex-nmhost) with
// confirmation on is held, awaiting confirmation, with its tracks; "downex confirm
// --video" picks another quality, and the output has that track's picture size.
func TestMediaBrowserHandOffConfirm(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := newStreamServer(t)
	const segs = 4
	v720 := newGenTrack(t, true, 1280, 720, segs, 41, false)
	v352 := newGenTrack(t, true, 640, 352, segs, 42, false)
	audio := newGenTrack(t, false, 0, 0, segs, 43, false)
	s.add("/b/master.m3u8", []byte(`#EXTM3U
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,AUTOSELECT=YES,URI="audio/index.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1500000,RESOLUTION=1280x720,CODECS="avc1.4d401e,mp4a.40.2",AUDIO="aud"
v720/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=600000,RESOLUTION=640x352,CODECS="avc1.4d401e,mp4a.40.2",AUDIO="aud"
v352/index.m3u8
`), hlsMIME)
	for dir, g := range map[string]*genTrack{"/b/v720/": v720, "/b/v352/": v352, "/b/audio/": audio} {
		dur := float64(videoPerSeg*videoSampleDur) / videoTimescale
		if !g.video {
			dur = float64(audioPerSeg*audioSampleDur) / audioTimescale
		}
		s.add(dir+"index.m3u8", []byte(hlsMediaPlaylist(segs, dur, "m4s", "init.mp4")), hlsMIME)
		g.serve(s, dir)
	}
	inst := h.newDesktopInstall()
	d := h.startDaemonFrom(inst.downexd, inst.env)
	h.waitReady(d)
	h.setDefaultDir(h.out)

	p := h.startNMHost(h.env)
	p.send(map[string]any{"id": "1", "type": "hello", "protocol": 1, "browser": "chrome", "extVersion": "0.1.0"})
	if w := p.recv(); w["type"] != "welcome" {
		t.Fatalf("hello answered %v", w)
	}
	p.send(map[string]any{"id": "2", "type": "addMedia", "manifestUrl": s.url("/b/master.m3u8"),
		"headers": [][]string{{"Cookie", "sid=e2e-media"}, {"Referer", "https://video.example.test/watch"}, {"User-Agent", "E2E/1"}},
		"pageUrl": "https://video.example.test/watch?v=1", "title": "Morning Talk", "deadlineMs": 15000})
	a := p.recv()
	fid, ok := a["downloadId"].(float64)
	if a["ok"] != true || a["type"] != "added" || !ok || fid <= 0 {
		t.Fatalf("addMedia answered %v\nrelay stderr: %s", a, p.stderr.String())
	}
	p.closeAndWait()
	id := int64(fid)

	videoDir := filepath.Join(h.out, "Video")
	dl := h.get(id)
	m := dl.GetMedia()
	if dl.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION ||
		dl.GetKind() != downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA || dl.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER ||
		dl.GetCategory() != "Video" || dl.GetDir() != videoDir || dl.GetFilename() != "Morning Talk.mp4" ||
		m.GetFormat() != "hls" || m.GetTitle() != "Morning Talk" || len(m.GetTracks()) != 3 ||
		m.GetSelectedVideo() != "v0" || m.GetSelectedAudio() != "r0" {
		t.Fatalf("handed-off stream = %v", dl)
	}
	call := inst.waitUICall(t)
	if !slices.Equal(call.Args, []string{"--confirm", idArg(id), "--raise"}) {
		t.Errorf("confirmation window started with %q", call.Args)
	}
	for _, p := range s.requestedPaths() {
		if !strings.HasSuffix(p, ".m3u8") {
			t.Errorf("%s was fetched before the confirmation", p)
		}
	}
	if c := s.cookiesOf("/b/master.m3u8"); len(c) == 0 || c[0] != "sid=e2e-media" {
		t.Errorf("the manifest was fetched with cookies %q, want the page's", c)
	}

	r := h.cli("confirm", idArg(id), "--video", "v1").ok(t)
	want := filepath.Join(videoDir, "Morning Talk.mp4")
	if !strings.Contains(r.stdout, fmt.Sprintf("Confirmed download %d", id)) || !strings.Contains(r.stdout, want) {
		t.Errorf("confirm: %v", r)
	}
	dl = h.waitCompleted(id)
	if m := dl.GetMedia(); m.GetSelectedVideo() != "v1" || m.GetSelectedAudio() != "r0" {
		t.Fatalf("completed with tracks %q + %q, want v1 + r0", m.GetSelectedVideo(), m.GetSelectedAudio())
	}
	checkMP4(t, want, v352, audio)
	noMediaLeftovers(t, videoDir)
	for _, p := range s.requestedPaths() {
		if strings.HasPrefix(p, "/b/v720/") && p != "/b/v720/index.m3u8" {
			t.Errorf("%s of the default quality was fetched after another was chosen", p)
		}
	}
	if c := s.cookiesOf("/b/v352/seg-1.m4s"); len(c) != 1 || c[0] != "sid=e2e-media" {
		t.Errorf("a segment was fetched with cookies %q, want the page's", c)
	}
	h.stop(d)
}
