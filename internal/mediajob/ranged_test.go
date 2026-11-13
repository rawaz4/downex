package mediajob

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math/bits"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/limiter"
	"downex/internal/media"
	"downex/internal/transport"
)

// ---- fixtures: single-file fragmented MP4 tracks built with mp4ff ---------------------

// rbsp writes the RBSP of H.264 parameter sets (ISO/IEC 14496-10 §7.3.2).
type rbsp struct {
	buf []byte
	n   uint
}

func (w *rbsp) bit(b uint64) {
	if w.n%8 == 0 {
		w.buf = append(w.buf, 0)
	}
	if b&1 != 0 {
		w.buf[len(w.buf)-1] |= 0x80 >> (w.n % 8)
	}
	w.n++
}

func (w *rbsp) bits(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v >> uint(i))
	}
}

// ue writes an unsigned Exp-Golomb code (§9.1).
func (w *rbsp) ue(v uint64) {
	n := bits.Len64(v + 1)
	w.bits(0, n-1)
	w.bits(v+1, n)
}

func (w *rbsp) nal(header byte) []byte {
	w.bit(1) // rbsp_stop_one_bit
	for w.n%8 != 0 {
		w.bit(0)
	}
	out := []byte{header}
	zeros := 0
	for _, b := range w.buf { // emulation prevention (§7.4.1)
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

// avcSPS is a Main profile sequence parameter set for 640x352 frames.
func avcSPS() []byte {
	var w rbsp
	w.bits(77, 8) // profile_idc
	w.bits(0, 8)  // constraint flags
	w.bits(30, 8) // level_idc
	w.ue(0)       // seq_parameter_set_id
	w.ue(0)       // log2_max_frame_num_minus4
	w.ue(0)       // pic_order_cnt_type
	w.ue(2)       // log2_max_pic_order_cnt_lsb_minus4
	w.ue(2)       // max_num_ref_frames
	w.bit(0)      // gaps_in_frame_num_value_allowed_flag
	w.ue(39)      // pic_width_in_mbs_minus1
	w.ue(21)      // pic_height_in_map_units_minus1
	w.bit(1)      // frame_mbs_only_flag
	w.bit(1)      // direct_8x8_inference_flag
	w.bit(0)      // frame_cropping_flag
	w.bit(0)      // vui_parameters_present_flag
	return w.nal(0x67)
}

func avcPPS() []byte {
	var w rbsp
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

func encodeMP4(t *testing.T, b interface{ Encode(w io.Writer) error }) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := b.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// siteTrack is a single-file fragmented MP4 track as a site serves it (YouTube's DASH
// formats): ftyp, moov with mvex, sidx, then moof/mdat pairs.
type siteTrack struct {
	data    []byte
	samples [][]byte
}

// buildTrack builds a track of n samples of size bytes, perFrag per movie fragment.
// When boundary > 0, a 'free' box after the 'sidx' moves the fragments so that byte
// boundary (a chunk boundary) lies inside a 'moof'.
func buildTrack(t *testing.T, video bool, n, size, perFrag int, seed uint64, boundary int64) siteTrack {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = 1000
	var dur uint32
	if video {
		dur = 3000
		trak := init.AddEmptyTrack(90000, "video", "und")
		if err := trak.SetAVCDescriptor("avc1", [][]byte{avcSPS()}, [][]byte{avcPPS()}, true); err != nil {
			t.Fatal(err)
		}
	} else {
		dur = 1024
		trak := init.AddEmptyTrack(48000, "audio", "und")
		if err := trak.SetAACDescriptor(aac.AAClc, 48000); err != nil {
			t.Fatal(err)
		}
	}
	head := append(encodeMP4(t, init), box("sidx", make([]byte, 24))...)
	rng := rand.New(rand.NewPCG(seed, 3))
	var st siteTrack
	var frags [][]byte
	dt := uint64(0)
	for i := 0; i < n; i += perFrag {
		frag, err := mp4.CreateFragment(uint32(len(frags)+1), 1)
		if err != nil {
			t.Fatal(err)
		}
		for range min(perFrag, n-i) {
			data := make([]byte, size)
			for k := range data {
				data[k] = byte(rng.Uint32())
			}
			st.samples = append(st.samples, data)
			frag.AddFullSample(mp4.FullSample{
				Sample:     mp4.Sample{Flags: mp4.SyncSampleFlags, Dur: dur, Size: uint32(size)},
				DecodeTime: dt,
				Data:       data,
			})
			dt += uint64(dur)
		}
		frags = append(frags, encodeMP4(t, frag))
	}
	pad := int64(0)
	if boundary > 0 {
		inside := func(shift int64) bool {
			off := int64(len(head)) + shift
			for _, f := range frags {
				if moof := int64(binary.BigEndian.Uint32(f)); off < boundary && boundary < off+moof {
					return true
				}
				off += int64(len(f))
			}
			return false
		}
		if !inside(0) {
			off := int64(len(head))
			for _, f := range frags {
				if mid := off + int64(binary.BigEndian.Uint32(f))/2; mid+8 <= boundary {
					pad = boundary - mid
				}
				off += int64(len(f))
			}
			if pad < 8 || !inside(pad) {
				t.Fatal("cannot place the chunk boundary inside a moof")
			}
		}
	}
	st.data = head
	if pad > 0 {
		st.data = append(st.data, box("free", make([]byte, pad-8))...)
	}
	for _, f := range frags {
		st.data = append(st.data, f...)
	}
	return st
}

var (
	pairOnce             sync.Once
	pairVideo, pairAudio siteTrack
)

// fixturePair returns a video track of 3 chunks and an audio track of 2 chunks (of
// media.MinChunkBytes), each with a chunk boundary inside a 'moof'.
func fixturePair(t *testing.T) (video, audio siteTrack) {
	t.Helper()
	pairOnce.Do(func() {
		pairVideo = buildTrack(t, true, 160, 16000, 16, 1, media.MinChunkBytes)
		pairAudio = buildTrack(t, false, 400, 3000, 50, 2, media.MinChunkBytes)
	})
	if len(pairVideo.data) == 0 {
		t.Fatal("fixtures were not built")
	}
	return pairVideo, pairAudio
}

// checkMP4 decodes a progressive MP4 with mp4ff and compares the sample data of its
// tracks with want.
func checkMP4(t *testing.T, path string, want ...[][]byte) {
	t.Helper()
	fd, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	f, err := mp4.DecodeFile(fd, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if f.IsFragmented() {
		t.Fatal("output is fragmented")
	}
	if len(f.Moov.Traks) != len(want) {
		t.Fatalf("%d tracks, want %d", len(f.Moov.Traks), len(want))
	}
	for i, samples := range want {
		trak := f.Moov.Traks[i]
		n := trak.Mdia.Minf.Stbl.Stsz.GetNrSamples()
		if int(n) != len(samples) {
			t.Fatalf("track %d: %d samples, want %d", i, n, len(samples))
		}
		data, err := f.ReadSampleData(trak, 1, n, fd, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, bytes.Join(samples, nil)) {
			t.Fatalf("track %d: sample data differs", i)
		}
	}
}

// ---- a site's file server ------------------------------------------------------------

// siteServer serves files with byte ranges (http.ServeContent) and records every
// request. intercept (may be nil) sees each request first with its number for the path
// (1-based) and answers it itself by returning true; it may also remove the Range
// header, which makes the server send the whole file.
type siteServer struct {
	ts *httptest.Server

	mu        sync.Mutex
	files     map[string][]byte
	reqs      []siteReq
	active    int
	peak      int
	intercept func(w http.ResponseWriter, r *http.Request, n int) bool
}

type siteReq struct {
	path, rng string
	at        time.Time
}

func newSiteServer(t *testing.T) *siteServer {
	t.Helper()
	s := &siteServer{files: make(map[string][]byte)}
	s.ts = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.ts.Close)
	return s
}

func (s *siteServer) url(path string) string { return s.ts.URL + path }
func (s *siteServer) host() string           { return strings.TrimPrefix(s.ts.URL, "http://") }

func (s *siteServer) add(path string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = data
}

func (s *siteServer) setIntercept(f func(w http.ResponseWriter, r *http.Request, n int) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.intercept = f
}

// requests returns the requests for path ("" = every path).
func (s *siteServer) requests(path string) []siteReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []siteReq
	for _, q := range s.reqs {
		if path == "" || q.path == path {
			out = append(out, q)
		}
	}
	return out
}

func (s *siteServer) maxActive() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

func (s *siteServer) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	s.mu.Lock()
	n := 1
	for _, q := range s.reqs {
		if q.path == p {
			n++
		}
	}
	s.reqs = append(s.reqs, siteReq{path: p, rng: r.Header.Get("Range"), at: time.Now()})
	s.active++
	s.peak = max(s.peak, s.active)
	data, ok := s.files[p]
	intercept := s.intercept
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	if intercept != nil && intercept(w, r, n) {
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// parseRange parses "bytes=a-b".
func parseRange(t *testing.T, v string) (int64, int64) {
	t.Helper()
	spec, ok := strings.CutPrefix(v, "bytes=")
	first, last, ok2 := strings.Cut(spec, "-")
	a, err1 := strconv.ParseInt(first, 10, 64)
	b, err2 := strconv.ParseInt(last, 10, 64)
	if !ok || !ok2 || err1 != nil || err2 != nil {
		t.Fatalf("unexpected Range %q", v)
	}
	return a, b
}

func rangesOf(reqs []siteReq) []string {
	out := make([]string, len(reqs))
	for i, q := range reqs {
		out[i] = q.path + " " + q.rng
	}
	return out
}

// chunkRange is the Range header of chunk i of a track of size bytes in chunks of c.
func chunkRange(i int, c, size int64) string {
	start := int64(i) * c
	return "bytes=" + strconv.FormatInt(start, 10) + "-" + strconv.FormatInt(min(start+c, size)-1, 10)
}

// ---- host rules and limiter stand-ins ----------------------------------------------------

// testRules is a HostRules with request caps, keyed by host.
type testRules struct {
	conns map[string]int
	caps  map[string]int64
	lims  map[string]limiter.Limiter
}

func (r *testRules) HostMaxConnections(host string) int      { return r.conns[host] }
func (r *testRules) HostMaxRequestBytes(host string) int64   { return r.caps[host] }
func (r *testRules) HostLimiter(host string) limiter.Limiter { return r.lims[host] }

// countLimiter counts the bytes it is asked for and never waits.
type countLimiter struct{ n atomic.Int64 }

func (l *countLimiter) WaitN(ctx context.Context, n int) error {
	l.n.Add(int64(n))
	return ctx.Err()
}
func (l *countLimiter) SetLimit(int64)              {}
func (l *countLimiter) Limit() int64                { return 0 }
func (l *countLimiter) Child(int64) limiter.Limiter { return l }

// ---- a fake clock ------------------------------------------------------------------------

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c       chan time.Time
	at      time.Time
	stopped bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }

func (t *fakeTimer) Stop() bool {
	was := !t.stopped
	t.stopped = true
	return was
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: make(chan time.Time, 1), at: c.now.Add(d)}
	if d <= 0 {
		t.c <- c.now
		t.stopped = true
		return t
	}
	c.timers = append(c.timers, t)
	return t
}

// advance moves the clock and fires the timers that are due.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.timers[:0]
	for _, t := range c.timers {
		switch {
		case t.stopped:
		case !t.at.After(c.now):
			t.stopped = true
			t.c <- c.now
		default:
			keep = append(keep, t)
		}
	}
	c.timers = keep
}

// pending counts the timers that have not fired or been stopped.
func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.stopped {
			n++
		}
	}
	return n
}

// ---- helpers -----------------------------------------------------------------------------

func rangedTrack(id string, kind media.TrackKind, ext, url string, size int64) media.RangedTrack {
	return media.RangedTrack{ID: id, Key: "s1-" + id, Kind: kind, Ext: ext, URL: url, Size: size, ChunkBytes: media.MinChunkBytes}
}

// pairSelection serves the fixture pair under prefix and selects it for remuxing.
func pairSelection(t *testing.T, s *siteServer, prefix string) (RangedSelection, siteTrack, siteTrack) {
	t.Helper()
	v, a := fixturePair(t)
	s.add(prefix+"v.mp4", v.data)
	s.add(prefix+"a.m4a", a.data)
	return RangedSelection{Output: OutputRemux, Tracks: []media.RangedTrack{
		rangedTrack("137", media.KindVideo, "mp4", s.url(prefix+"v.mp4"), int64(len(v.data))),
		rangedTrack("140", media.KindAudio, "m4a", s.url(prefix+"a.m4a"), int64(len(a.data))),
	}}, v, a
}

func withURLs(sel RangedSelection, s *siteServer, prefix string) []media.RangedTrack {
	out := slices.Clone(sel.Tracks)
	for i := range out {
		out[i].URL = s.url(prefix + filepath.Base(out[i].URL))
	}
	return out
}

func rangedOptions(t *testing.T) Options {
	t.Helper()
	o := testOptions(t, nil)
	o.OutPath = filepath.Join(filepath.Dir(o.OutPath), "video.mp4")
	return o
}

func ftyp() []byte { return box("ftyp", []byte("isom\x00\x00\x02\x00isomiso6")) }

func filler(n int, seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, 9))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Uint32())
	}
	return b
}

// ---- tests ---------------------------------------------------------------------------------

func TestRunRangedRemuxSingleFile(t *testing.T) {
	s := newSiteServer(t)
	sel, v, a := pairSelection(t, s, "/")
	opts := rangedOptions(t)
	var rec recorder
	container, err := RunRanged(testContext(t), opts, sel, nil, rec.onProgress, rec.onCheckpoint)
	if err != nil || container != "mp4" {
		t.Fatalf("RunRanged = %q, %v", container, err)
	}
	checkMP4(t, opts.OutPath, v.samples, a.samples)

	reqs := s.requests("")
	first := map[string]string{reqs[0].path: reqs[0].rng, reqs[1].path: reqs[1].rng}
	if want := chunkRange(0, media.MinChunkBytes, 1<<40); first["/v.mp4"] != want || first["/a.m4a"] != want {
		t.Fatalf("first requests %v: every first chunk must come before the others", rangesOf(reqs[:2]))
	}
	for _, path := range []string{"/v.mp4", "/a.m4a"} {
		got := rangesOf(s.requests(path))
		size := int64(len(v.data))
		if path == "/a.m4a" {
			size = int64(len(a.data))
		}
		var want []string
		for i := range int((size + media.MinChunkBytes - 1) / media.MinChunkBytes) {
			want = append(want, path+" "+chunkRange(i, media.MinChunkBytes, size))
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: requests %v, want each chunk once: %v", path, got, want)
		}
	}

	last := rec.progress[len(rec.progress)-1]
	total := int64(len(v.data) + len(a.data))
	if last.FragmentsTotal != 5 || last.FragmentsDone != 5 || last.BytesDone != total || last.BytesTotal != total {
		t.Errorf("final progress %+v, want 5/5 parts and %d bytes", last, total)
	}
	for _, p := range rec.progress {
		if p.BytesTotal != total {
			t.Fatalf("progress BytesTotal %d, want %d from the start", p.BytesTotal, total)
		}
	}
	cp := rec.last()
	if cp == nil || len(cp.Tracks) != 2 {
		t.Fatalf("checkpoint %+v", cp)
	}
	for k, tc := range cp.Tracks {
		if tc.Key != sel.Tracks[k].Key || tc.TrackID != sel.Tracks[k].ID || tc.ChunkBytes != media.MinChunkBytes {
			t.Errorf("checkpoint track %+v", tc)
		}
		for i := range tc.Total {
			if !bitSet(tc.Done, i) {
				t.Errorf("track %s part %d not durable", tc.TrackID, i)
			}
		}
	}

	// The output is recorded: a second call completes without a request.
	n := len(s.requests(""))
	container, err = RunRanged(testContext(t), opts, sel, cp, nil, nil)
	if err != nil || container != "mp4" || len(s.requests("")) != n {
		t.Fatalf("second RunRanged = %q, %v with %d new requests", container, err, len(s.requests(""))-n)
	}
}

func TestRunRangedRefusesAfterTheFirstChunk(t *testing.T) {
	mvex := box("mvex", box("trex", make([]byte, 24)))
	mvhd := box("mvhd", make([]byte, 100))
	trak := box("trak", box("tkhd", make([]byte, 84)))
	body := box("mdat", filler(2<<20, 4))
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"progressive", slices.Concat(ftyp(), box("moov", mvhd, trak), body), media.ErrUnsupported},
		{"protected", slices.Concat(ftyp(), box("moov", mvhd, mvex, box("pssh", make([]byte, 24))), body), media.ErrEncrypted},
		{"moov too long", slices.Concat(ftyp(), box("moov", mvhd, mvex, box("free", make([]byte, 3<<19))), body), media.ErrUnsupported},
		{"no moov", slices.Concat(ftyp(), body), media.ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSiteServer(t)
			s.add("/v.mp4", tc.data)
			opts := rangedOptions(t)
			sel := RangedSelection{Output: OutputRemux, Tracks: []media.RangedTrack{
				rangedTrack("137", media.KindVideo, "mp4", s.url("/v.mp4"), int64(len(tc.data))),
			}}
			_, err := RunRanged(testContext(t), opts, sel, nil, nil, nil)
			if !errors.Is(err, tc.want) {
				t.Fatalf("RunRanged = %v, want %v", err, tc.want)
			}
			if got := rangesOf(s.requests("")); len(got) != 1 || got[0] != "/v.mp4 "+chunkRange(0, media.MinChunkBytes, 1<<40) {
				t.Fatalf("requests %v, want only the first chunk", got)
			}
			if _, err := os.Stat(opts.OutPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("an output was written")
			}
		})
	}
}

func TestRunRangedCopy(t *testing.T) {
	mvhd := box("mvhd", make([]byte, 100))
	trak := box("trak", box("tkhd", make([]byte, 84)))
	for _, tc := range []struct {
		name     string
		data     []byte
		want     error
		requests int
	}{
		{"moov at the end", slices.Concat(ftyp(), box("mdat", filler(2200<<10, 5)), box("moov", mvhd, trak)), nil, 3},
		{"moov at the front", slices.Concat(ftyp(), box("moov", mvhd, trak), box("mdat", filler(1500<<10, 6))), nil, 2},
		{"protected moov at the end", slices.Concat(ftyp(), box("mdat", filler(2200<<10, 7)), box("moov", mvhd, box("pssh", make([]byte, 24)))), media.ErrEncrypted, 3},
		{"protected moov at the front", slices.Concat(ftyp(), box("moov", mvhd, box("pssh", make([]byte, 24))), box("mdat", filler(2200<<10, 8))), media.ErrEncrypted, 1},
		{"not an MP4", filler(1500<<10, 9), media.ErrUnsupported, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSiteServer(t)
			s.add("/18.mp4", tc.data)
			opts := rangedOptions(t)
			sel := RangedSelection{Output: OutputCopy, Tracks: []media.RangedTrack{
				rangedTrack("18", media.KindMuxed, "mp4", s.url("/18.mp4"), int64(len(tc.data))),
			}}
			container, err := RunRanged(testContext(t), opts, sel, nil, nil, nil)
			if n := len(s.requests("")); n != tc.requests {
				t.Errorf("%d requests, want %d", n, tc.requests)
			}
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("RunRanged = %v, want %v", err, tc.want)
				}
				if _, err := os.Stat(opts.OutPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("an output was written")
				}
				return
			}
			if err != nil || container != "mp4" {
				t.Fatalf("RunRanged = %q, %v", container, err)
			}
			if !bytes.Equal(readFile(t, opts.OutPath), tc.data) {
				t.Fatal("the copied output differs from the served file")
			}
			// Moved, not copied: the track's file is gone from the part directory.
			if _, err := os.Stat(filepath.Join(opts.PartDir, "muxed-18", dataName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("track file still present: %v", err)
			}
		})
	}
}

func TestRunRangedServerIgnoresRanges(t *testing.T) {
	s := newSiteServer(t)
	sel, v, a := pairSelection(t, s, "/")
	var once atomic.Bool
	s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		switch {
		case r.URL.Path == "/a.m4a":
			r.Header.Del("Range") // never ranges: the first chunk gets the whole track
		case r.URL.Path == "/v.mp4" && strings.HasPrefix(r.Header.Get("Range"), "bytes=1048576-") && once.CompareAndSwap(false, true):
			r.Header.Del("Range") // a 200 to a later chunk: retried
		}
		return false
	})
	opts := rangedOptions(t)
	var rec recorder
	if _, err := RunRanged(testContext(t), opts, sel, nil, rec.onProgress, rec.onCheckpoint); err != nil {
		t.Fatal(err)
	}
	checkMP4(t, opts.OutPath, v.samples, a.samples)
	if n := len(s.requests("/a.m4a")); n != 1 {
		t.Errorf("%d requests for the audio track, want 1 (the whole track)", n)
	}
	if n := len(s.requests("/v.mp4")); n != 4 {
		t.Errorf("%d requests for the video track, want 3 chunks + 1 retry", n)
	}
	cp := rec.last()
	if a := cp.Tracks[1]; a.Total != 1 || a.ChunkBytes != 0 || !bitSet(a.Done, 0) {
		t.Errorf("audio checkpoint %+v, want one whole-resource part", a)
	}
	if last := rec.progress[len(rec.progress)-1]; last.FragmentsTotal != 4 || last.FragmentsDone != 4 {
		t.Errorf("final progress %+v, want 4/4 parts", last)
	}
}

func TestRunRangedRefreshOn403(t *testing.T) {
	s := newSiteServer(t)
	sel, v, a := pairSelection(t, s, "/old/")
	s.add("/new/v.mp4", v.data)
	s.add("/new/a.m4a", a.data)
	s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		if r.URL.Path == "/old/v.mp4" && n > 1 {
			w.WriteHeader(http.StatusForbidden) // the address expired
			return true
		}
		return false
	})
	var refreshes atomic.Int32
	sel.Refresh = func(ctx context.Context) ([]media.RangedTrack, error) {
		refreshes.Add(1)
		return withURLs(sel, s, "/new/"), nil
	}
	opts := rangedOptions(t)
	if _, err := RunRanged(testContext(t), opts, sel, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkMP4(t, opts.OutPath, v.samples, a.samples)
	if n := refreshes.Load(); n != 1 {
		t.Errorf("%d renewals, want 1", n)
	}
	if len(s.requests("/new/v.mp4")) == 0 {
		t.Error("the renewed address was never used")
	}
}

// A chunk refused again after the renewal fails the run; the chunks completed before
// the failure stay durable. The other chunks may start while the first ones still
// arrive (§2.4), and a failure cancels whatever is in flight, a first chunk included:
// so the third video chunk is refused only once the first chunks are checkpointed.
func TestRunRangedFailsOn403AfterRefresh(t *testing.T) {
	s := newSiteServer(t)
	sel, _, _ := pairSelection(t, s, "/old/")
	var rec syncRecorder
	heads := func() bool {
		cps := rec.all()
		return len(cps) > 0 && bitSet(cps[len(cps)-1].Tracks[0].Done, 0) && bitSet(cps[len(cps)-1].Tracks[1].Done, 0)
	}
	s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		a, _ := parseRange(t, r.Header.Get("Range"))
		if r.URL.Path == "/old/v.mp4" && a >= 2*media.MinChunkBytes {
			for deadline := time.Now().Add(10 * time.Second); !heads() && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
		}
		if strings.HasPrefix(r.URL.Path, "/new/") || r.URL.Path == "/old/v.mp4" && a >= 2*media.MinChunkBytes {
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		return false
	})
	var refreshes atomic.Int32
	sel.Refresh = func(ctx context.Context) ([]media.RangedTrack, error) {
		refreshes.Add(1)
		return withURLs(sel, s, "/new/"), nil
	}
	opts := rangedOptions(t)
	opts.CheckpointInterval = 10 * time.Millisecond
	_, err := RunRanged(testContext(t), opts, sel, nil, nil, rec.onCheckpoint)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusForbidden {
		t.Fatalf("RunRanged = %v, want HTTP 403", err)
	}
	if strings.Contains(err.Error(), s.ts.URL) {
		t.Errorf("error repeats the URL: %v", err)
	}
	if n := refreshes.Load(); n != 1 {
		t.Errorf("%d renewals, want 1", n)
	}
	if !heads() {
		t.Fatalf("checkpoints %+v: the first chunks must stay durable", rec.all())
	}
}

func TestRunRangedRenewsBeforeExpiry(t *testing.T) {
	s := newSiteServer(t)
	sel, v, a := pairSelection(t, s, "/old/")
	s.add("/new/v.mp4", v.data)
	s.add("/new/a.m4a", a.data)
	// Slow answers, so that the progress tick sees the near expiry during a phase.
	s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		select {
		case <-time.After(400 * time.Millisecond):
		case <-r.Context().Done():
			return true
		}
		return false
	})
	for i := range sel.Tracks {
		sel.Tracks[i].Expires = time.Now().Add(time.Minute)
	}
	var refreshes atomic.Int32
	sel.Refresh = func(ctx context.Context) ([]media.RangedTrack, error) {
		refreshes.Add(1)
		fresh := withURLs(sel, s, "/new/")
		for i := range fresh {
			fresh[i].Expires = time.Now().Add(6 * time.Hour)
		}
		return fresh, nil
	}
	opts := rangedOptions(t)
	if _, err := RunRanged(testContext(t), opts, sel, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkMP4(t, opts.OutPath, v.samples, a.samples)
	if n := refreshes.Load(); n != 1 {
		t.Errorf("%d renewals, want 1", n)
	}
	if len(s.requests("/new/v.mp4"))+len(s.requests("/new/a.m4a")) == 0 {
		t.Error("the renewed addresses were never used")
	}
}

// interrupted runs sel until the server has seen its fourth request, which it holds
// until a checkpoint records the first chunk of every track as durable and then
// cancels the run, and returns the checkpoint the run saved. (The other chunks may start
// while the first ones still arrive, §2.4, so the fourth request alone says nothing
// about them.) The run asks a server of its own with the files of s: a request it sent
// just before the cancel can reach a server after RunRanged returned (the client gave
// up on it, the server reads it later), and s records only the requests of the run that
// resumes.
func interrupted(t *testing.T, s *siteServer, opts Options, sel RangedSelection) *Checkpoint {
	t.Helper()
	first := newSiteServer(t)
	s.mu.Lock()
	for p, data := range s.files {
		first.add(p, data)
	}
	s.mu.Unlock()
	sel.Tracks = slices.Clone(sel.Tracks)
	for i := range sel.Tracks {
		sel.Tracks[i].URL = first.url(strings.TrimPrefix(sel.Tracks[i].URL, s.ts.URL))
	}
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	var rec syncRecorder
	heads := func() bool {
		cps := rec.all()
		if len(cps) == 0 {
			return false
		}
		for _, tc := range cps[len(cps)-1].Tracks {
			if !bitSet(tc.Done, 0) {
				return false
			}
		}
		return true
	}
	var seen atomic.Int32
	first.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		if seen.Add(1) == 4 {
			for deadline := time.Now().Add(10 * time.Second); !heads() && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
			cancel()
			<-r.Context().Done()
			return true
		}
		return false
	})
	o := opts
	o.CheckpointInterval = 10 * time.Millisecond
	if _, err := RunRanged(ctx, o, sel, nil, nil, rec.onCheckpoint); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted run = %v, want context.Canceled", err)
	}
	cps := rec.all()
	if len(cps) == 0 {
		t.Fatal("no checkpoint")
	}
	return &cps[len(cps)-1]
}

func TestRunRangedResume(t *testing.T) {
	ref := newSiteServer(t)
	refSel, _, _ := pairSelection(t, ref, "/")
	refOpts := rangedOptions(t)
	if _, err := RunRanged(testContext(t), refOpts, refSel, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	want := readFile(t, refOpts.OutPath)

	t.Run("only chunks without bits", func(t *testing.T) {
		s := newSiteServer(t)
		sel, v, a := pairSelection(t, s, "/")
		opts := rangedOptions(t)
		cp := interrupted(t, s, opts, sel)
		// Each chunk without its bit is asked for after its durable prefix (§2.5).
		var missing []string
		for k, tc := range cp.Tracks {
			size := []int64{int64(len(v.data)), int64(len(a.data))}[k]
			for i := range tc.Total {
				if !bitSet(tc.Done, i) {
					start := int64(i)*media.MinChunkBytes + tc.Partial[i]
					end := min(int64(i+1)*media.MinChunkBytes, size) - 1
					missing = append(missing, filepath.Base(sel.Tracks[k].URL)+" bytes="+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10))
				}
			}
		}
		if len(missing) == 0 || len(missing) == 5 {
			t.Fatalf("the interruption left %d of 5 chunks missing", len(missing))
		}
		if _, err := RunRanged(testContext(t), opts, sel, cp, nil, nil); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, q := range s.requests("") {
			got = append(got, strings.TrimPrefix(q.path, "/")+" "+q.rng)
		}
		slices.Sort(got)
		slices.Sort(missing)
		if !slices.Equal(got, missing) {
			t.Errorf("resumed requests %v, want exactly the chunks without bits (after their prefixes) %v", got, missing)
		}
		if !bytes.Equal(readFile(t, opts.OutPath), want) {
			t.Error("resumed output differs from an uninterrupted run")
		}
	})

	t.Run("changed chunk size restarts that track only", func(t *testing.T) {
		s := newSiteServer(t)
		sel, v, a := pairSelection(t, s, "/")
		opts := rangedOptions(t)
		cp := interrupted(t, s, opts, sel)
		videoMissing := 0
		for i := range cp.Tracks[0].Total {
			if !bitSet(cp.Tracks[0].Done, i) {
				videoMissing++
			}
		}
		sel.Tracks[1].ChunkBytes = 2 * media.MinChunkBytes // the audio track is one chunk now
		if _, err := RunRanged(testContext(t), opts, sel, cp, nil, nil); err != nil {
			t.Fatal(err)
		}
		var audio []string
		video := 0
		for _, q := range s.requests("") {
			if q.path == "/a.m4a" {
				audio = append(audio, q.rng)
			} else {
				video++
			}
		}
		if wantAudio := chunkRange(0, 2*media.MinChunkBytes, int64(len(a.data))); !slices.Equal(audio, []string{wantAudio}) {
			t.Errorf("audio requests %v, want the whole track again: %s", audio, wantAudio)
		}
		if video != videoMissing {
			t.Errorf("%d video requests, want the %d missing chunks", video, videoMissing)
		}
		checkMP4(t, opts.OutPath, v.samples, a.samples)
	})

	t.Run("a file of another size restarts that track only", func(t *testing.T) {
		s := newSiteServer(t)
		sel, v, a := pairSelection(t, s, "/")
		opts := rangedOptions(t)
		cp := interrupted(t, s, opts, sel)
		if err := os.Truncate(filepath.Join(opts.PartDir, "video-137", dataName), 100); err != nil {
			t.Fatal(err)
		}
		audioMissing := 0
		for i := range cp.Tracks[1].Total {
			if !bitSet(cp.Tracks[1].Done, i) {
				audioMissing++
			}
		}
		if _, err := RunRanged(testContext(t), opts, sel, cp, nil, nil); err != nil {
			t.Fatal(err)
		}
		if n := len(s.requests("")) - audioMissing; n != 3 {
			t.Errorf("%d video requests, want all 3 chunks again", n)
		}
		checkMP4(t, opts.OutPath, v.samples, a.samples)
	})
}

func TestRunRangedHostCaps(t *testing.T) {
	sv, sa := newSiteServer(t), newSiteServer(t)
	v, a := fixturePair(t)
	sv.add("/v.mp4", v.data)
	sa.add("/a.m4a", a.data)
	const capBytes = 256 << 10
	audioLim := &countLimiter{}
	opts := rangedOptions(t)
	opts.MaxConnections = 4
	opts.HostRules = &testRules{
		conns: map[string]int{sv.host(): 1},
		caps:  map[string]int64{sv.host(): capBytes},
		lims:  map[string]limiter.Limiter{sa.host(): audioLim},
	}
	sel := RangedSelection{Output: OutputRemux, Tracks: []media.RangedTrack{
		rangedTrack("137", media.KindVideo, "mp4", sv.url("/v.mp4"), int64(len(v.data))),
		rangedTrack("140", media.KindAudio, "m4a", sa.url("/a.m4a"), int64(len(a.data))),
	}}
	if _, err := RunRanged(testContext(t), opts, sel, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	checkMP4(t, opts.OutPath, v.samples, a.samples)
	for _, q := range sv.requests("") {
		if a, b := parseRange(t, q.rng); b-a+1 > capBytes {
			t.Errorf("video request %s exceeds the host's %d-byte cap", q.rng, capBytes)
		}
	}
	if n := len(sv.requests("")); n < int(int64(len(v.data))/capBytes) {
		t.Errorf("%d video requests under a %d-byte cap", n, capBytes)
	}
	if p := sv.maxActive(); p != 1 {
		t.Errorf("%d parallel requests to a host capped at 1", p)
	}
	if got := audioLim.n.Load(); got < int64(len(a.data)) {
		t.Errorf("the audio host's limiter saw %d bytes, want ≥ %d", got, len(a.data))
	}
}

func TestRunRangedThrottle(t *testing.T) {
	o, err := Options{Factory: testFactory(t), PartDir: "p", OutPath: "o", MaxConnections: 8,
		HostRules: &testRules{conns: map[string]int{"h": 4}}}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	r := newRangedRunner(o, RangedSelection{}, nil, nil)
	defer r.close()
	if c := r.hostCap("h"); c != 4 {
		t.Fatalf("cap %d, want the host rule's 4", c)
	}
	r.active["h"] = 4
	r.halve("h")
	if c := r.hostCap("h"); c != 2 {
		t.Fatalf("cap after a 429 = %d, want 2", c)
	}
	r.halve("h")
	r.halve("h")
	if c := r.hostCap("h"); c != 1 {
		t.Fatalf("cap after three 429s = %d, want 1", c)
	}
	if c := r.hostCap("other"); c != 8 {
		t.Fatalf("another host's cap = %d, want 8", c)
	}

	// A 429 with Retry-After is waited out and the download completes.
	s := newSiteServer(t)
	sel, v, a := pairSelection(t, s, "/")
	s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		if r.URL.Path == "/v.mp4" && n == 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return true
		}
		return false
	})
	opts := rangedOptions(t)
	start := time.Now()
	if _, err := RunRanged(testContext(t), opts, sel, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < time.Second {
		t.Errorf("finished after %v: Retry-After was not honoured", d)
	}
	checkMP4(t, opts.OutPath, v.samples, a.samples)
}

func TestRunRangedNotBefore(t *testing.T) {
	s := newSiteServer(t)
	sel, _, _ := pairSelection(t, s, "/")
	sel.NotBefore = time.Now().Add(300 * time.Millisecond)
	opts := rangedOptions(t)
	var firstProgress atomic.Int64
	onProgress := func(p Progress) { firstProgress.CompareAndSwap(0, time.Now().UnixNano()) }
	if _, err := RunRanged(testContext(t), opts, sel, nil, onProgress, nil); err != nil {
		t.Fatal(err)
	}
	if first := s.requests("")[0].at; first.Before(sel.NotBefore) {
		t.Errorf("first request %v before NotBefore", sel.NotBefore.Sub(first))
	}
	if p := time.Unix(0, firstProgress.Load()); p.Before(sel.NotBefore) {
		t.Errorf("progress reported %v before NotBefore", sel.NotBefore.Sub(p))
	}

	far := newSiteServer(t)
	sel, _, _ = pairSelection(t, far, "/")
	sel.NotBefore = time.Now().Add(11 * time.Minute)
	if _, err := RunRanged(testContext(t), rangedOptions(t), sel, nil, nil, nil); !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("NotBefore 11 min ahead = %v, want ErrInvalidSelection", err)
	}
	if n := len(far.requests("")); n != 0 {
		t.Fatalf("%d requests before an unusable NotBefore", n)
	}
}

func TestRunRangedUnknownSize(t *testing.T) {
	s := newSiteServer(t)
	sel, v, a := pairSelection(t, s, "/")
	sel.Tracks[0].Size = 0 // ranges: the size comes from bytes=0-0
	sel.Tracks[1].Size = 0 // no ranges: one whole request
	s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		if r.URL.Path == "/a.m4a" {
			r.Header.Del("Range")
		}
		return false
	})
	opts := rangedOptions(t)
	var rec recorder
	if _, err := RunRanged(testContext(t), opts, sel, nil, rec.onProgress, rec.onCheckpoint); err != nil {
		t.Fatal(err)
	}
	checkMP4(t, opts.OutPath, v.samples, a.samples)
	vr := rangesOf(s.requests("/v.mp4"))
	if len(vr) != 4 || vr[0] != "/v.mp4 bytes=0-0" {
		t.Errorf("video requests %v, want bytes=0-0 then 3 chunks", vr)
	}
	if n := len(s.requests("/a.m4a")); n != 2 {
		t.Errorf("%d audio requests, want the probe and one whole request", n)
	}
	// The probes found both sizes (the audio's from the Content-Length of its 200).
	total := int64(len(v.data) + len(a.data))
	if first, last := rec.progress[0], rec.progress[len(rec.progress)-1]; first.BytesTotal != total || last.BytesTotal != total {
		t.Errorf("BytesTotal %d first, %d last; want %d", first.BytesTotal, last.BytesTotal, total)
	}
	if a := rec.last().Tracks[1]; a.Total != 1 || a.ChunkBytes != 0 {
		t.Errorf("audio checkpoint %+v, want one whole-resource part", a)
	}

	// No size anywhere: a chunked 200 without Content-Length to every request.
	data := slices.Concat(ftyp(), box("mdat", filler(1500<<10, 10)), box("moov", box("mvhd", make([]byte, 100))))
	s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		w.Header().Set("Content-Type", "video/mp4")
		w.(http.Flusher).Flush() // headers out now: the body is chunked
		_, _ = w.Write(data)
		return true
	})
	opts = rangedOptions(t)
	rec = recorder{}
	copySel := RangedSelection{Output: OutputCopy, Tracks: []media.RangedTrack{rangedTrack("18", media.KindMuxed, "mp4", s.url("/18.mp4"), 0)}}
	if _, err := RunRanged(testContext(t), opts, copySel, nil, rec.onProgress, rec.onCheckpoint); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, opts.OutPath), data) {
		t.Fatal("output differs")
	}
	if first, last := rec.progress[0], rec.progress[len(rec.progress)-1]; first.BytesTotal != 0 || last.BytesTotal != int64(len(data)) {
		t.Errorf("BytesTotal %d first (size unknown), %d last; want 0 and %d", first.BytesTotal, last.BytesTotal, len(data))
	}
	if n := len(s.requests("/18.mp4")); n != 2 {
		t.Errorf("%d requests, want the probe and one whole request", n)
	}
}

func TestRunRangedSelectionChecks(t *testing.T) {
	s := newSiteServer(t)
	video := rangedTrack("137", media.KindVideo, "mp4", s.url("/v"), 5<<20)
	audio := rangedTrack("140", media.KindAudio, "m4a", s.url("/a"), 1<<20)
	muxed := rangedTrack("18", media.KindMuxed, "mp4", s.url("/m"), 5<<20)
	webm := rangedTrack("248", media.KindVideo, "webm", s.url("/w"), 5<<20)
	for _, tc := range []struct {
		name string
		sel  RangedSelection
		want error
	}{
		{"no track", RangedSelection{Output: OutputRemux}, ErrInvalidSelection},
		{"audio alone", RangedSelection{Output: OutputRemux, Tracks: []media.RangedTrack{audio}}, ErrInvalidSelection},
		{"audio first", RangedSelection{Output: OutputRemux, Tracks: []media.RangedTrack{audio, video}}, ErrInvalidSelection},
		{"three tracks", RangedSelection{Output: OutputRemux, Tracks: []media.RangedTrack{video, audio, audio}}, ErrInvalidSelection},
		{"muxed plus audio", RangedSelection{Output: OutputRemux, Tracks: []media.RangedTrack{muxed, audio}}, ErrInvalidSelection},
		{"WebM", RangedSelection{Output: OutputRemux, Tracks: []media.RangedTrack{webm, audio}}, media.ErrUnsupported},
		{"copy of a pair", RangedSelection{Output: OutputCopy, Tracks: []media.RangedTrack{video, audio}}, ErrInvalidSelection},
		{"copy of a video track", RangedSelection{Output: OutputCopy, Tracks: []media.RangedTrack{video}}, ErrInvalidSelection},
		{"unknown output", RangedSelection{Output: "mkv", Tracks: []media.RangedTrack{muxed}}, ErrInvalidSelection},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RunRanged(testContext(t), rangedOptions(t), tc.sel, nil, nil, nil); !errors.Is(err, tc.want) {
				t.Fatalf("RunRanged = %v, want %v", err, tc.want)
			}
		})
	}
	if n := len(s.requests("")); n != 0 {
		t.Fatalf("%d requests for refused selections", n)
	}
	// A plan media.PlanRanged refuses: a chunk size below the minimum.
	bad := video
	bad.ChunkBytes = 4096
	if _, err := RunRanged(testContext(t), rangedOptions(t), RangedSelection{Output: OutputRemux, Tracks: []media.RangedTrack{bad}}, nil, nil, nil); !errors.Is(err, media.ErrMalformed) {
		t.Fatalf("chunk size 4096 = %v, want media.ErrMalformed", err)
	}
}

func TestRunRangedFreeSpace(t *testing.T) {
	s := newSiteServer(t)
	sel, v, a := pairSelection(t, s, "/")
	opts := rangedOptions(t)
	var asked string
	opts.FreeSpace = func(dir string) (int64, error) {
		asked = dir
		return 10 << 20, nil
	}
	_, err := RunRanged(testContext(t), opts, sel, nil, nil, nil)
	if !errors.Is(err, ErrDiskFull) {
		t.Fatalf("RunRanged = %v, want ErrDiskFull", err)
	}
	if asked != opts.PartDir || len(s.requests("")) != 0 {
		t.Fatalf("free space asked for %q, %d requests", asked, len(s.requests("")))
	}
	if _, err := os.Stat(filepath.Join(opts.PartDir, "video-137", dataName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a track file was allocated")
	}
	// Twice the tracks plus 64 MiB fit.
	need := 2*int64(len(v.data)+len(a.data)) + freeSpaceMargin
	opts.FreeSpace = func(string) (int64, error) { return need, nil }
	if _, err := RunRanged(testContext(t), opts, sel, nil, nil, nil); err != nil {
		t.Fatalf("with %d bytes free: %v", need, err)
	}
}

func TestRangedRefreshRules(t *testing.T) {
	clk := newFakeClock()
	o, err := Options{Factory: testFactory(t), PartDir: "p", OutPath: "o", Clock: clk}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	sel := RangedSelection{Output: OutputRemux, Tracks: []media.RangedTrack{
		rangedTrack("137", media.KindVideo, "mp4", "https://a.example/v", 5<<20),
	}}
	sel.Refresh = func(context.Context) ([]media.RangedTrack, error) {
		n := calls.Add(1)
		return []media.RangedTrack{{ID: "137", Key: "s1-137", URL: "https://b.example/v" + strconv.Itoa(int(n)), Network: "tcp6"}}, nil
	}
	r := newRangedRunner(o, sel, nil, nil)
	defer r.close()
	ctx := testContext(t)

	if err := r.refresh(ctx, 0); err != nil || calls.Load() != 1 || r.generation() != 1 {
		t.Fatalf("first renewal: %v, %d calls, generation %d", err, calls.Load(), r.generation())
	}
	if src := r.source(r.tracks[0]); src.url != "https://b.example/v1" || src.network != "tcp6" || src.host != "b.example" {
		t.Fatalf("renewed source %+v", src)
	}
	// A caller that saw the old generation takes the renewal that happened.
	if err := r.refresh(ctx, 0); err != nil || calls.Load() != 1 {
		t.Fatalf("stale caller renewed again: %v, %d calls", err, calls.Load())
	}
	// The next renewal waits until a minute after the last one.
	for gen := uint64(1); gen < maxRefreshes; gen++ {
		done := make(chan error, 1)
		go func() { done <- r.refresh(ctx, gen) }()
		waitUntil(t, func() bool { return clk.pending() == 1 })
		clk.advance(refreshGap - time.Second)
		select {
		case err := <-done:
			t.Fatalf("renewal %d did not wait a minute: %v", gen+1, err)
		case <-time.After(20 * time.Millisecond):
		}
		clk.advance(time.Second)
		if err := <-done; err != nil || calls.Load() != int32(gen+1) {
			t.Fatalf("renewal %d: %v, %d calls", gen+1, err, calls.Load())
		}
	}
	clk.advance(time.Hour)
	if err := r.refresh(ctx, maxRefreshes); !errors.Is(err, errRefreshLimit) {
		t.Fatalf("renewal %d = %v, want errRefreshLimit", maxRefreshes+1, err)
	}

	// A failed renewal fails every later call; a missing key is an invalid selection.
	failing := errors.New("site_unavailable: this video is not available")
	sel.Refresh = func(context.Context) ([]media.RangedTrack, error) { calls.Add(1); return nil, failing }
	r2 := newRangedRunner(o, sel, nil, nil)
	defer r2.close()
	before := calls.Load()
	if err := r2.refresh(ctx, 0); !errors.Is(err, failing) {
		t.Fatalf("failed renewal = %v", err)
	}
	if err := r2.refresh(ctx, 0); !errors.Is(err, failing) || calls.Load() != before+1 {
		t.Fatalf("second call after a failure = %v, %d calls", err, calls.Load()-before)
	}
	sel.Refresh = func(context.Context) ([]media.RangedTrack, error) {
		return []media.RangedTrack{{ID: "137", Key: "s1-other", URL: "https://b.example/v"}}, nil
	}
	r3 := newRangedRunner(o, sel, nil, nil)
	defer r3.close()
	if err := r3.refresh(ctx, 0); !errors.Is(err, ErrInvalidSelection) || !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("renewal without the key = %v, want ErrInvalidSelection", err)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestChunkSize(t *testing.T) {
	rules := &testRules{caps: map[string]int64{"rr1.googlevideo.com": 10 << 20, "tiny.example": 4096}}
	for _, tc := range []struct {
		url         string
		formatChunk int64
		want        int64
	}{
		{"https://rr1.googlevideo.com/videoplayback", 0, 10 << 20},
		{"https://rr1.googlevideo.com/videoplayback", 4 << 20, 4 << 20},
		{"https://rr1.googlevideo.com/videoplayback", 20 << 20, 10 << 20},
		{"https://other.example/v", 0, media.DefaultChunkBytes},
		{"https://other.example/v", 100 << 20, media.MaxChunkBytes},
		{"https://other.example/v", 64 << 10, media.MinChunkBytes},
		{"https://tiny.example/v", 0, media.MinChunkBytes},
		{"https://other.example/v", -5, media.DefaultChunkBytes},
	} {
		if got := ChunkSize(rules, tc.url, tc.formatChunk); got != tc.want {
			t.Errorf("ChunkSize(%s, %d) = %d, want %d", tc.url, tc.formatChunk, got, tc.want)
		}
	}
	if got := ChunkSize(nil, "https://x.example/", 0); got != media.DefaultChunkBytes {
		t.Errorf("ChunkSize without rules = %d", got)
	}
}

func TestTopBoxes(t *testing.T) {
	moov := box("moov", box("mvhd", make([]byte, 100)), box("mvex", box("trex", make([]byte, 24))))
	data := slices.Concat(ftyp(), moov, box("mdat", make([]byte, 50)))
	boxes, partial, err := topBoxes(data[:len(data)-10], int64(len(data)))
	if err != nil || len(boxes) != 2 || partial == nil || partial.typ != "mdat" {
		t.Fatalf("topBoxes = %+v, %+v, %v", boxes, partial, err)
	}
	if boxes[1].typ != "moov" || boxes[1].off != int64(len(ftyp())) || boxes[1].end() != int64(len(ftyp())+len(moov)) {
		t.Fatalf("moov box %+v", boxes[1])
	}
	if !hasChildBox(moov[8:], "mvex") || hasChildBox(moov[8:], "pssh") {
		t.Fatal("hasChildBox")
	}
	// A 64-bit size, and a size of 0 (to the end of a file of known size).
	large := slices.Concat(be32(1), []byte("mdat"), binary.BigEndian.AppendUint64(nil, 26), make([]byte, 10))
	toEnd := slices.Concat(be32(0), []byte("mdat"), make([]byte, 12))
	for _, tc := range []struct {
		data []byte
		size int64
		n    int
	}{
		{large, int64(len(large)), 1},
		{toEnd, int64(len(toEnd)), 1},
		{toEnd, -1, 0},
	} {
		boxes, _, err := topBoxes(tc.data, tc.size)
		if err != nil || len(boxes) != tc.n {
			t.Errorf("topBoxes(%x) = %+v, %v; want %d boxes", tc.data[:8], boxes, err, tc.n)
		}
	}
	if _, _, err := topBoxes(slices.Concat(be32(4), []byte("free")), -1); !errors.Is(err, media.ErrMalformed) {
		t.Errorf("a box smaller than its header = %v", err)
	}
}

func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

// progressiveFile is a faststart progressive MP4 of about size bytes: ftyp, a small
// moov, then one mdat of random bytes.
func progressiveFile(size int, seed uint64) []byte {
	head := slices.Concat(ftyp(), box("moov", box("mvhd", make([]byte, 100)), box("trak", box("tkhd", make([]byte, 84)))))
	return slices.Concat(head, box("mdat", filler(size-len(head)-8, seed)))
}

// A 40 MiB track in chunks fitted to 8 connections (FitChunk: 2.5 MiB) keeps all 8
// busy once its first chunk is checked; with the 16 MiB chunks of before it had 3.
func TestRunRangedFittedChunksUseEveryConnection(t *testing.T) {
	const size = 40 << 20
	data := progressiveFile(size, 21)
	s := newSiteServer(t)
	s.add("/18.mp4", data)
	// Slow answers, so that the requests of one dispatch overlap.
	s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		select {
		case <-time.After(150 * time.Millisecond):
		case <-r.Context().Done():
			return true
		}
		return false
	})
	opts := rangedOptions(t)
	opts.MaxConnections = 8
	url := s.url("/18.mp4")
	chunk := FitChunk(nil, url, ChunkSize(nil, url, 0), size, opts.MaxConnections)
	if chunk != 5<<19 {
		t.Fatalf("fitted chunk %d, want 2.5 MiB", chunk)
	}
	tr := rangedTrack("18", media.KindMuxed, "mp4", url, size)
	tr.ChunkBytes = chunk
	if _, err := RunRanged(testContext(t), opts, RangedSelection{Output: OutputCopy, Tracks: []media.RangedTrack{tr}}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, opts.OutPath), data) {
		t.Fatal("the output differs from the served file")
	}
	if n := len(s.requests("")); n != 16 {
		t.Errorf("%d requests, want 16 chunks", n)
	}
	if p := s.maxActive(); p != 8 {
		t.Errorf("peak of %d requests at once, want 8", p)
	}
}

// A checkpoint written with 16 MiB chunks (a download started before chunks were
// fitted) resumes with that size: only its missing chunks are fetched, and the output
// is byte-exact.
func TestRunRangedResumesOldChunkSize(t *testing.T) {
	const size = 40 << 20
	data := progressiveFile(size, 22)
	s := newSiteServer(t)
	s.add("/18.mp4", data)
	opts := rangedOptions(t)
	opts.MaxConnections = 8
	tr := rangedTrack("18", media.KindMuxed, "mp4", s.url("/18.mp4"), size)
	tr.ChunkBytes = media.DefaultChunkBytes
	sel := RangedSelection{Output: OutputCopy, Tracks: []media.RangedTrack{tr}}

	// The first run: the last chunk is held until the run is cancelled after the others
	// are durable.
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	last := chunkRange(2, media.DefaultChunkBytes, size)
	s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		if r.Header.Get("Range") == last {
			<-r.Context().Done()
			return true
		}
		return false
	})
	var rec syncRecorder
	onCheckpoint := func(cp Checkpoint) error {
		if bitSet(cp.Tracks[0].Done, 0) && bitSet(cp.Tracks[0].Done, 1) {
			cancel()
		}
		return rec.onCheckpoint(cp)
	}
	first := opts
	first.CheckpointInterval = 20 * time.Millisecond
	if _, err := RunRanged(ctx, first, sel, nil, nil, onCheckpoint); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted run = %v, want context.Canceled", err)
	}
	cps := rec.all()
	cp := cps[len(cps)-1]
	if tc := cp.Tracks[0]; tc.ChunkBytes != media.DefaultChunkBytes || tc.Total != 3 || !bitSet(tc.Done, 0) || !bitSet(tc.Done, 1) || bitSet(tc.Done, 2) {
		t.Fatalf("checkpoint %+v, want chunks 0 and 1 of 3 durable at 16 MiB", tc)
	}

	s.setIntercept(nil)
	before := len(s.requests(""))
	if _, err := RunRanged(testContext(t), opts, sel, &cp, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, opts.OutPath), data) {
		t.Fatal("the resumed output differs from the served file")
	}
	got := rangesOf(s.requests("")[before:])
	if !slices.Equal(got, []string{"/18.mp4 " + last}) {
		t.Errorf("resumed requests %v, want only the missing 16 MiB chunk", got)
	}
}

// A chunk whose response stops sending is closed by the copier's watchdog after
// mediaStallTimeout on the job's clock (docs/MEDIA-SPEC.md §7) and fetched again from
// where it stopped (docs/SITE-MEDIA-SPEC.md §2.4): one extra request for the rest of the
// chunk, the progress never goes back, and the output is byte-exact.
func TestRunRangedStalledChunkIsRetried(t *testing.T) {
	data := progressiveFile(3<<20, 23)
	s := newSiteServer(t)
	s.add("/18.mp4", data)
	stalled := make(chan struct{})
	var once sync.Once
	var closes atomic.Int32
	var closedBeforeRetry atomic.Int32 // Close calls when the retry arrived
	closedBeforeRetry.Store(-1)
	s.setIntercept(func(w http.ResponseWriter, r *http.Request, n int) bool {
		if n == 2 {
			closedBeforeRetry.Store(closes.Load())
		}
		if n != 1 {
			return false
		}
		// The first response sends 512 KiB of its range, then nothing.
		a, b := parseRange(t, r.Header.Get("Range"))
		w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(a, 10)+"-"+strconv.FormatInt(b, 10)+"/"+strconv.Itoa(len(data)))
		w.Header().Set("Content-Length", strconv.FormatInt(b-a+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[a : a+512<<10])
		w.(http.Flusher).Flush()
		once.Do(func() { close(stalled) })
		<-r.Context().Done()
		return true
	})
	clk := newFakeClock()
	opts := rangedOptions(t)
	opts.Clock = clk
	opts.MaxConnections = 1 // nothing else is read while the clock jumps
	opts.Factory = closeCounter{opts.Factory, &closes}
	tr := rangedTrack("18", media.KindMuxed, "mp4", s.url("/18.mp4"), int64(len(data)))
	done := make(chan error, 1)
	var log progressLog
	go func() {
		_, err := RunRanged(testContext(t), opts, RangedSelection{Output: OutputCopy, Tracks: []media.RangedTrack{tr}}, nil, log.onProgress, nil)
		done <- err
	}()
	<-stalled
	// Move the job's clock until the chunk is asked for again; real time stays short.
	deadline := time.Now().Add(10 * time.Second)
	for len(s.requests("")) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the stalled chunk was not requested again: the watchdog does not run on the job's clock")
		}
		clk.advance(time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, opts.OutPath), data) {
		t.Fatal("the output differs from the served file")
	}
	got := rangesOf(s.requests(""))
	first := "/18.mp4 " + chunkRange(0, media.MinChunkBytes, int64(len(data)))
	rest := "/18.mp4 bytes=" + strconv.Itoa(512<<10) + "-" + strconv.Itoa(media.MinChunkBytes-1)
	if len(got) != 4 || got[0] != first || got[1] != rest {
		t.Errorf("requests %v, want the first chunk, the rest of it after its first 512 KiB, then the other 2", got)
	}
	checkNeverBack(t, "stalled run", log.all())
	// As in Run: the retry dials anew rather than reuse an idle connection that may be as
	// dead as the stalled one (an HTTP/2 connection outlives its reset stream).
	if n := closedBeforeRetry.Load(); n < 1 {
		t.Errorf("the slot's idle connections were dropped %d times before the retry, want at least 1", n)
	}
}

// closeCounter counts the Close calls of the fetchers its factory makes.
type closeCounter struct {
	transport.Factory
	n *atomic.Int32
}

func (c closeCounter) NewSlot() transport.Fetcher {
	return closeCountingFetcher{c.Factory.NewSlot(), c.n}
}

type closeCountingFetcher struct {
	transport.Fetcher
	n *atomic.Int32
}

func (f closeCountingFetcher) Close() {
	f.n.Add(1)
	f.Fetcher.Close()
}
