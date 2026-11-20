package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/model"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/store"
	"downex/internal/testmedia"
)

// Video detection on every site (docs/VIDEO-DETECT-SPEC.md §8.3), with real executables:
// MPEG-TS streams converted to MP4 by downexd (separate audio, discontinuities, early
// refusals, legacy ".ts" records, crash and resume), the relay's stream probe and
// track-carrying stream hand-off, a direct MP4 handed off as an ordinary download,
// MP4-only manifests, and a site whose only formats are MPEG-TS HLS. §8.3 #1 is
// TestMediaHLSTransportStream (media_test.go).

// muxedTS is a muxed H.264 + AAC stream of the given size and length in frames at 30
// fps (frames × 25/16 AAC frames: the same length when frames is a multiple of 16).
func muxedTS(t *testing.T, width, height, frames int, start, seed int64) *genTS {
	t.Helper()
	return newGenTS(t, tsSpec{
		video: &testmedia.VideoConfig{Width: width, Height: height, Frames: frames, StartDTS: start, Seed: seed},
		audio: &testmedia.AudioConfig{Frames: frames * 25 / 16, StartPTS: start, Seed: seed + 1},
	})
}

// concatSegments returns segs joined in order: a legacy ".ts" download's output.
func concatSegments(segs [][]byte) []byte { return bytes.Join(segs, nil) }

// §8.3 #2: a TS video variant with a separate TS AAC rendition (EXT-X-MEDIA) is saved as
// one MP4 holding both tracks, the audio starting 0.5 s after the picture as generated;
// also when the video starts just before the 33-bit timestamp wrap and the audio after
// it (each rendition is unwrapped on its own).
func TestMediaTSAudioRendition(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := newStreamServer(t)
	d := h.startDaemon()
	h.waitReady(d)
	for _, tc := range []struct {
		name  string
		start int64
	}{
		{"rendition", 10 * 90000},
		{"wrapped", 1<<33 - 9000},
	} {
		video := newGenTS(t, tsSpec{video: &testmedia.VideoConfig{Width: 640, Height: 360, Frames: 240, StartDTS: tc.start, Seed: 81}})
		audio := newGenTS(t, tsSpec{audio: &testmedia.AudioConfig{Frames: 352, StartPTS: tc.start + 45000, Seed: 82}})
		dir := "/" + tc.name + "/"
		s.add(dir+"master.m3u8", []byte(testmedia.MasterPlaylist(
			[]testmedia.Variant{{URI: "video/index.m3u8", Bandwidth: 1200000, Codecs: "avc1.4d401f,mp4a.40.2", Resolution: "640x360", Audio: "aud"}},
			[]testmedia.Rendition{{Group: "aud", Name: "English", Language: "eng", URI: "audio/index.m3u8", Default: true}})), hlsMIME)
		serveTS(s, dir+"video/", video)
		serveTS(s, dir+"audio/", audio)

		r := h.cli("media", s.url(dir+"master.m3u8"), "-o", h.out, "--name", tc.name, "--wait").ok(t)
		path := filepath.Join(h.out, tc.name+".mp4")
		checkMP4(t, path, tsTracks(video, audio)...)
		// 8 s of video from 0; 352 AAC frames (7.51 s) from 0.5 s.
		checkTiming(t, path, 8.01, 0, 0.5)
		dl := h.get(r.id(t))
		if m := dl.GetMedia(); m.GetContainer() != "mp4" || m.GetSelectedVideo() == "" || m.GetSelectedAudio() == "" ||
			m.GetFragmentsTotal() != int32(len(video.segs)+len(audio.segs)) {
			t.Fatalf("%s: completed media download = %v", tc.name, dl)
		}
	}
	noMediaLeftovers(t, h.out)
	for _, p := range s.requestedPaths() {
		if strings.HasSuffix(p, ".ts") && s.requests(p) != 1 {
			t.Errorf("%s was fetched %d times", p, s.requests(p))
		}
	}
	h.stop(d)
}

// §8.3 #3: three discontinuity domains whose timestamps jump forward and back become one
// continuous MP4 as long as the domains together, with every picture and AAC frame.
func TestMediaTSDiscontinuities(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := newStreamServer(t)
	// 128, 96 and 64 frames (4.267 s, 3.2 s and 2.133 s) starting at 10 s, 100 s and 20 s.
	parts := []*genTS{
		muxedTS(t, 640, 360, 128, 10*90000, 91),
		muxedTS(t, 640, 360, 96, 100*90000, 93),
		muxedTS(t, 640, 360, 64, 20*90000, 95),
	}
	serveTS(s, "/disc/", parts...)
	d := h.startDaemon()
	h.waitReady(d)

	r := h.cli("media", s.url("/disc/index.m3u8"), "-o", h.out, "--name", "spliced", "--wait").ok(t)
	path := filepath.Join(h.out, "spliced.mp4")
	checkMP4(t, path, tsTracks(parts...)...)
	checkTiming(t, path, float64(128+96+64)/30, 0, 0)
	noMediaLeftovers(t, h.out)
	if dl := h.get(r.id(t)); dl.GetMedia().GetContainer() != "mp4" {
		t.Fatalf("completed media download = %v", dl)
	}
	h.stop(d)
}

// §8.3 #4: what the converter cannot save is refused early. A muxed TS whose audio is
// AC-3 (no CODECS to tell) fails as media_unsupported, and one with scrambled packets as
// encrypted, each after its first segment only (the probe of its bare playlist reads
// the start of that segment and, seeing a start Run refuses, nothing more); a master
// whose CODECS name AC-3 is refused when it is added, before any segment is fetched
// (its media playlist is read: AC-3 in fragmented MP4 would be saved).
func TestMediaTSEarlyRefusals(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := newStreamServer(t)
	video := func(seed int64) *testmedia.VideoConfig {
		return &testmedia.VideoConfig{Width: 320, Height: 180, Frames: 180, Seed: seed}
	}
	ac3 := newGenTS(t, tsSpec{video: video(101), audio: &testmedia.AudioConfig{Frames: 280, Seed: 102},
		audioType: testmedia.TypeAC3})
	scrambled := newGenTS(t, tsSpec{video: video(103), audio: &testmedia.AudioConfig{Frames: 280, Seed: 104}, scrambled: true})
	serveTS(s, "/ac3/", ac3)
	serveTS(s, "/scr/", scrambled)
	s.add("/ac3master/master.m3u8", []byte(testmedia.MasterPlaylist([]testmedia.Variant{
		{URI: "index.m3u8", Bandwidth: 900000, Codecs: "avc1.4d401f,ac-3", Resolution: "320x180"}}, nil)), hlsMIME)
	serveTS(s, "/ac3master/", ac3)
	d := h.startDaemon()
	h.waitReady(d)

	for dir, code := range map[string]string{"/ac3/": "media_unsupported", "/scr/": "encrypted"} {
		r := h.cli("media", s.url(dir+"index.m3u8"), "-o", h.out, "--wait")
		if r.code == 0 {
			t.Errorf("media --wait of %s succeeded: %v", dir, r)
			continue
		}
		dl := h.waitFailed(r.id(t))
		if dl.GetErrorCode() != code || dl.GetMedia().GetContainer() != "mp4" {
			t.Errorf("download of %s = %v, want error code %s", dir, dl, code)
		}
		if n := s.requests(dir + "seg-1.ts"); n != 1 {
			t.Errorf("%sseg-1.ts was fetched %d times, want once", dir, n)
		}
		if got := s.probesOf(dir + "seg-1.ts"); !slices.Equal(got, []string{"GET bytes=0-524287"}) {
			t.Errorf("%sseg-1.ts was probed with %q, want one request of its first 512 KiB", dir, got)
		}
		for i := 2; i <= 3; i++ {
			p := fmt.Sprintf("%sseg-%d.ts", dir, i)
			if n := s.requests(p) + len(s.probesOf(p)); n != 0 {
				t.Errorf("%s was fetched after the first segment was refused", p)
			}
		}
	}
	if m, _ := filepath.Glob(filepath.Join(h.out, "*.mp4")); len(m) > 0 {
		t.Errorf("outputs written for refused streams: %v", m)
	}

	before := len(h.ls())
	r := h.cli("media", s.url("/ac3master/master.m3u8"), "-o", h.out)
	if r.code == 0 || !strings.Contains(r.stderr, "no MP4 video in this stream") {
		t.Errorf("media of a master with AC-3 audio: %v", r)
	}
	if n := len(h.ls()); n != before {
		t.Errorf("a refused master was recorded (%d downloads, %d before)", n, before)
	}
	for _, p := range s.requestedPaths() {
		if strings.HasPrefix(p, "/ac3master/") && !strings.HasSuffix(p, ".m3u8") {
			t.Errorf("%s was fetched for a master refused by its CODECS", p)
		}
	}
	h.stop(d)
}

// §8.3 #5: records stored before 2026-10-23 as MPEG-TS. One with a checkpoint (paused
// midway, its fragments in "show.ts.dxparts") resumes into the byte-for-byte join of the
// segments under its ".ts" name, fetching only the segments it lacks; one that never
// started (no checkpoint) is saved as "later.mp4".
func TestMediaLegacyTS(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := newStreamServer(t)
	g := muxedTS(t, 320, 180, 240, 0, 111) // 8 s: 4 segments
	segs := serveTS(s, "/old/", g)
	if len(segs) != 4 {
		t.Fatalf("%d segments, want 4", len(segs))
	}
	serveTS(s, "/later/", g) // the same stream for the second record

	// A download paused after 2 segments, as today's Downex stores it...
	s.setGate(func(p string) bool { return segmentNumber(p) > 2 })
	d := h.startDaemon()
	h.waitReady(d)
	id := h.cli("media", s.url("/old/index.m3u8"), "-o", h.out, "--name", "show").ok(t).id(t)
	h.waitFragments(id, 2, true)
	h.cli("pause", idArg(id)).ok(t)
	h.waitState(id, downexv1.DownloadState_DOWNLOAD_STATE_PAUSED)
	h.stop(d)
	s.setGate(nil)

	// ...becomes one stored before the change: container "ts", a ".ts" name and part
	// folder. A second record, never started, has no checkpoint.
	st, err := store.OpenSQLite(h.db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rec, err := st.GetDownload(ctx, model.DownloadID(id))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Filename != "show.mp4" || rec.Media == nil || rec.Media.Checkpoint == nil || rec.Media.Container != "mp4" {
		t.Fatalf("paused record = %+v (media %+v)", rec, rec.Media)
	}
	rec.Filename, rec.Media.Container = "show.ts", "ts"
	if err := st.UpdateDownload(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(h.out, "show.mp4.dxparts"), filepath.Join(h.out, "show.ts.dxparts")); err != nil {
		t.Fatal(err)
	}
	later := model.Download{
		Request: model.Request{URL: s.url("/later/index.m3u8")}, Dir: h.out, Filename: "later.ts", Size: -1, State: model.StateQueued,
		Source: model.SourceCLI, Kind: model.KindMedia,
		Media: &model.Media{Format: rec.Media.Format, Tracks: rec.Media.Tracks, SelectedVideo: rec.Media.SelectedVideo,
			SelectedAudio: rec.Media.SelectedAudio, Duration: rec.Media.Duration, Container: "ts",
			FragmentsTotal: rec.Media.FragmentsTotal},
	}
	if err := st.CreateDownload(ctx, &later); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	d = h.startDaemon()
	h.waitReady(d)
	h.cli("resume", idArg(id)).ok(t)
	dl := h.waitCompleted(id)
	path := filepath.Join(h.out, "show.ts")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := concatSegments(segs); !bytes.Equal(got, want) {
		t.Errorf("%s: %d bytes, not the %d bytes of the joined segments", path, len(got), len(want))
	}
	if dl.GetFilename() != "show.ts" || dl.GetMedia().GetContainer() != "ts" || dl.GetSize() != int64(len(got)) {
		t.Errorf("resumed legacy download = %v", dl)
	}
	for i := 1; i <= 2; i++ {
		if n := s.requests(fmt.Sprintf("/old/seg-%d.ts", i)); n != 1 {
			t.Errorf("segment %d, stored before the pause, was fetched %d times", i, n)
		}
	}

	dl = h.waitCompleted(int64(later.ID))
	if dl.GetFilename() != "later.mp4" || dl.GetMedia().GetContainer() != "mp4" {
		t.Errorf("legacy record without a checkpoint completed as %v", dl)
	}
	checkMP4(t, filepath.Join(h.out, "later.mp4"), tsTracks(g)...)
	noSuchPath(t, filepath.Join(h.out, "later.ts"))
	noMediaLeftovers(t, h.out)
	h.stop(d)
}

// §8.3 #6: a TS download paused, killed and resumed, then killed while running, fetches
// only the segments without a stored bit again, and its MP4 is byte for byte the one of
// an uninterrupted download of the same stream (the conversion is deterministic).
func TestMediaTSPauseKillResume(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := newStreamServer(t)
	g := muxedTS(t, 320, 180, 480, 0, 121) // 16 s: 8 segments
	segs := serveTS(s, "/k/", g)
	paths := make([]string, len(segs))
	for i := range segs {
		paths[i] = fmt.Sprintf("/k/seg-%d.ts", i+1)
	}
	upTo := func(n int) func(string) bool { return func(p string) bool { return segmentNumber(p) > n } }

	s.setGate(upTo(3))
	d := h.startDaemon()
	h.waitReady(d)
	id := h.cli("media", s.url("/k/index.m3u8"), "-o", h.out, "--name", "night").ok(t).id(t)
	h.waitFragments(id, 3, false)
	h.cli("pause", idArg(id)).ok(t)
	h.waitState(id, downexv1.DownloadState_DOWNLOAD_STATE_PAUSED)
	d.kill(t)
	s.waitClosed(t)

	s.setGate(upTo(6))
	d = h.startDaemon()
	h.waitReady(d)
	h.cli("resume", idArg(id)).ok(t)
	h.waitFragments(id, 6, true)
	d.kill(t)
	s.waitClosed(t)

	s.setGate(nil)
	d = h.startDaemon()
	h.waitReady(d)
	dl := h.waitCompleted(id)
	path := filepath.Join(h.out, "night.mp4")
	if dl.GetFilename() != "night.mp4" || dl.GetMedia().GetContainer() != "mp4" {
		t.Fatalf("completed as %v", dl)
	}
	checkMP4(t, path, tsTracks(g)...)
	noMediaLeftovers(t, h.out)
	for i, p := range paths {
		if n := s.servedCount(p); n != 1 {
			t.Errorf("%s was downloaded %d times", p, n)
		}
		if n := s.requests(p); i < 3 && n != 1 {
			t.Errorf("%s, durable when the download was paused, was requested %d times", p, n)
		}
	}

	out2 := filepath.Join(h.root, "out2")
	h.cli("media", s.url("/k/index.m3u8"), "-o", out2, "--name", "night", "--wait").ok(t)
	a, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out2, "night.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("the resumed download (%d bytes) differs from an uninterrupted one (%d bytes)", len(a), len(b))
	}
	h.stop(d)
}

// tsMaster serves two muxed TS variants below dir, 320x180 (lo, listed first: track
// v0) and 640x360 (hi: v1), each 4 s long, and returns them.
func tsMaster(t *testing.T, s *streamServer, dir string, seed int64) (lo, hi *genTS) {
	t.Helper()
	lo, hi = muxedTS(t, 320, 180, 128, 0, seed), muxedTS(t, 640, 360, 128, 0, seed+2)
	s.add(dir+"master.m3u8", []byte(testmedia.MasterPlaylist([]testmedia.Variant{
		{URI: "lo/index.m3u8", Bandwidth: 400000, Codecs: "avc1.4d401f,mp4a.40.2", Resolution: "320x180"},
		{URI: "hi/index.m3u8", Bandwidth: 1200000, Codecs: "avc1.4d401f,mp4a.40.2", Resolution: "640x360"},
	}, nil)), hlsMIME)
	serveTS(s, dir+"lo/", lo)
	serveTS(s, dir+"hi/", hi)
	return lo, hi
}

// helloStreams starts a relay and says hello; the welcome must name the streams feature.
func (h *harness) helloStreams() *nmhostProc {
	h.t.Helper()
	p := h.startNMHost(h.env)
	p.send(map[string]any{"id": "1", "type": "hello", "protocol": 1, "browser": "chrome", "extVersion": "0.1.0"})
	w := p.recv()
	features, _ := w["features"].([]any)
	if w["type"] != "welcome" || !slices.Contains(features, any("streams")) || !slices.Contains(features, any("video")) {
		h.t.Fatalf("hello answered %v", w)
	}
	return p
}

// noAddresses fails when a relay answer holds an address (or the server's host).
func noAddresses(t *testing.T, what string, m map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(m)
	if bytes.Contains(raw, []byte("://")) || bytes.Contains(raw, []byte("127.0.0.1")) {
		t.Errorf("%s holds an address: %s", what, raw)
	}
}

// §8.3 #7: the relay names the streams feature; probeStream answers a master's MP4
// qualities without any address; addMedia with the lower quality is held awaiting
// confirmation (the window starts with --confirm); "downex confirm" then saves that
// quality as an MP4, fetching nothing of the other.
func TestStreamRelayProbeAndAdd(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := newStreamServer(t)
	lo, _ := tsMaster(t, s, "/relay/", 131)
	inst := h.newDesktopInstall()
	d := h.startDaemonFrom(inst.downexd, inst.env)
	h.waitReady(d)
	h.setDefaultDir(h.out)

	p := h.helloStreams()
	headers := [][]string{{"Cookie", "sid=e2e-stream"}, {"Referer", "https://news.example.test/story"}, {"User-Agent", "E2E/1"}}
	p.send(map[string]any{"id": "2", "type": "probeStream", "manifestUrl": s.url("/relay/master.m3u8"), "headers": headers,
		"pageUrl": "https://news.example.test/story", "deadlineMs": 28000})
	info := p.recv()
	tracks, _ := info["tracks"].([]any)
	if info["ok"] != true || info["type"] != "videoInfo" || info["site"] != "" || info["container"] != "mp4" ||
		info["format"] != "hls" || info["selectedVideo"] != "v1" || len(tracks) != 2 {
		t.Fatalf("probeStream answered %v", info)
	}
	noAddresses(t, "probeStream's answer", info)
	low, lowHeight := "", 0.0
	for _, tr := range tracks {
		m, _ := tr.(map[string]any)
		if hgt, _ := m["height"].(float64); low == "" || hgt < lowHeight {
			low, _ = m["id"].(string)
			lowHeight = hgt
		}
	}
	if low != "v0" || lowHeight != 180 {
		t.Fatalf("lowest quality %q (%vp) in %v", low, lowHeight, tracks)
	}
	if n := s.requests("/relay/lo/seg-1.ts") + s.requests("/relay/hi/seg-1.ts"); n != 0 {
		t.Errorf("the probe fetched %d segments", n)
	}

	p.send(map[string]any{"id": "3", "type": "addMedia", "manifestUrl": s.url("/relay/master.m3u8"), "headers": headers,
		"pageUrl": "https://news.example.test/story", "title": "Night Show", "videoTrack": low, "deadlineMs": 15000})
	a := p.recv()
	fid, _ := a["downloadId"].(float64)
	id := int64(fid)
	if a["ok"] != true || a["type"] != "added" || a["awaiting"] != true || id <= 0 {
		t.Fatalf("addMedia answered %v\nrelay stderr: %s", a, p.stderr.String())
	}
	p.closeAndWait()
	if call := inst.waitUICall(t); !slices.Equal(call.Args, []string{"--confirm", idArg(id), "--raise"}) {
		t.Errorf("confirmation window started with %q", call.Args)
	}
	dl := h.get(id)
	if dl.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION || dl.GetFilename() != "Night Show.mp4" ||
		dl.GetMedia().GetSelectedVideo() != low || dl.GetMedia().GetContainer() != "mp4" || dl.GetCategory() != "Video" {
		t.Fatalf("handed-off stream = %v", dl)
	}

	h.cli("confirm", idArg(id)).ok(t)
	dl = h.waitCompleted(id)
	checkMP4(t, filepath.Join(dl.GetDir(), dl.GetFilename()), tsTracks(lo)...)
	noMediaLeftovers(t, dl.GetDir())
	for _, p := range s.requestedPaths() {
		if strings.HasPrefix(p, "/relay/hi/") && p != "/relay/hi/index.m3u8" {
			t.Errorf("%s of the quality not chosen was fetched", p)
		}
	}
	if c := s.cookiesOf("/relay/lo/seg-1.ts"); len(c) != 1 || c[0] != "sid=e2e-stream" {
		t.Errorf("a segment was fetched with cookies %q, want the page's", c)
	}
	h.stop(d)
}

// clipServer stands in for a page's direct video: the origin answers "/v/clip" (only
// with the page's cookie) with a redirect to a signed address on a CDN of another
// origin, which serves the file with ranges, at about 2 MiB/s per response.
type clipServer struct {
	origin, cdn *httptest.Server
	file        []byte
	mu          sync.Mutex
	originReqs  []string // the Cookie of each origin request
	cdnReqs     []cdnRequest
	active, max int
}

type cdnRequest struct {
	Range, Cookie, Query string
}

const clipCookie = "sid=e2e-clip"

func newClipServer(t *testing.T, file []byte) *clipServer {
	t.Helper()
	c := &clipServer{file: file}
	c.cdn = httptest.NewServer(http.HandlerFunc(c.serveCDN))
	t.Cleanup(c.cdn.Close)
	c.origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.originReqs = append(c.originReqs, r.Header.Get("Cookie"))
		c.mu.Unlock()
		if r.URL.Path != "/v/clip" || r.Header.Get("Cookie") != clipCookie {
			http.Error(w, "sign in first", http.StatusForbidden)
			return
		}
		http.Redirect(w, r, c.cdn.URL+"/cdn/clip.mp4?sig=e2e-signature", http.StatusFound)
	}))
	t.Cleanup(c.origin.Close)
	return c
}

func (c *clipServer) serveCDN(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.cdnReqs = append(c.cdnReqs, cdnRequest{Range: r.Header.Get("Range"), Cookie: r.Header.Get("Cookie"), Query: r.URL.RawQuery})
	c.active++
	c.max = max(c.max, c.active)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.active--
		c.mu.Unlock()
	}()
	if r.URL.Path != "/cdn/clip.mp4" || r.URL.Query().Get("sig") == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("ETag", `"clip-1"`)
	http.ServeContent(slowWriter{ResponseWriter: w, r: r}, r, "", time.Time{}, bytes.NewReader(c.file))
}

func (c *clipServer) requests() (origin []string, cdn []cdnRequest, maxActive int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.originReqs), slices.Clone(c.cdnReqs), c.max
}

// slowWriter writes a response body at about 2 MiB/s.
type slowWriter struct {
	http.ResponseWriter
	r *http.Request
}

func (w slowWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		k := min(len(p), 16<<10)
		m, err := w.ResponseWriter.Write(p[:k])
		n += m
		if err != nil {
			return n, err
		}
		p = p[k:]
		select {
		case <-time.After(8 * time.Millisecond):
		case <-w.r.Context().Done():
			return n, w.r.Context().Err()
		}
	}
	return n, nil
}

// §8.3 #8: a direct MP4 handed off with "add" (the page's request headers, the file
// name, video/mp4 and the size) awaits confirmation, then downloads over several ranged
// connections into a byte-identical "Clip.mp4"; the page's cookie reaches only the
// origin, never the CDN the origin redirects to.
func TestDirectMP4HandOff(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	vc := testmedia.VideoConfig{Width: 640, Height: 360, Frames: 300, SliceBytes: 11000, Seed: 141}
	ac := testmedia.AudioConfig{Frames: 470, Seed: 142}
	file, err := testmedia.ProgressiveMP4(vc, testmedia.Video(vc), ac, testmedia.AAC(ac))
	if err != nil {
		t.Fatal(err)
	}
	if len(file) < 5<<20 || len(file) > 8<<20 {
		t.Fatalf("the generated MP4 has %d bytes, want about 6 MiB", len(file))
	}
	c := newClipServer(t, file)
	inst := h.newDesktopInstall()
	d := h.startDaemonFrom(inst.downexd, inst.env)
	h.waitReady(d)
	h.setDefaultDir(h.out)

	p := h.helloStreams()
	p.send(map[string]any{"id": "2", "type": "add", "url": c.origin.URL + "/v/clip", "method": "GET",
		"headers":  [][]string{{"Cookie", clipCookie}, {"Referer", "https://clips.example.test/watch/7"}, {"User-Agent", "E2E/1"}},
		"referrer": "https://clips.example.test/watch/7", "filename": "Clip.mp4", "mime": "video/mp4", "size": len(file),
		"deadlineMs": 15000})
	a := p.recv()
	fid, _ := a["downloadId"].(float64)
	id := int64(fid)
	if a["ok"] != true || a["type"] != "added" || a["awaiting"] != true || id <= 0 {
		t.Fatalf("add answered %v\nrelay stderr: %s", a, p.stderr.String())
	}
	p.closeAndWait()
	if call := inst.waitUICall(t); !slices.Equal(call.Args, []string{"--confirm", idArg(id), "--raise"}) {
		t.Errorf("confirmation window started with %q", call.Args)
	}
	if dl := h.get(id); dl.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION ||
		dl.GetKind() != downexv1.DownloadKind_DOWNLOAD_KIND_FILE || dl.GetCategory() != "Video" ||
		dl.GetExpectedSize() != int64(len(file)) {
		t.Fatalf("handed-off file = %v", dl)
	}
	if _, cdn, _ := c.requests(); len(cdn) != 0 {
		t.Errorf("the file was fetched before the confirmation: %+v", cdn)
	}

	h.cli("confirm", idArg(id)).ok(t)
	dl := h.waitCompleted(id)
	if dl.GetFilename() != "Clip.mp4" || dl.GetSize() != int64(len(file)) {
		t.Fatalf("completed download = %v", dl)
	}
	got, err := os.ReadFile(filepath.Join(dl.GetDir(), "Clip.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, file) {
		t.Errorf("Clip.mp4 (%d bytes) differs from the served file (%d bytes)", len(got), len(file))
	}
	noPartFiles(t, dl.GetDir())
	origin, cdn, maxActive := c.requests()
	if len(origin) == 0 || slices.ContainsFunc(origin, func(c string) bool { return c != clipCookie }) {
		t.Errorf("origin requests with cookies %q, want the page's on each", origin)
	}
	starts := map[string]bool{}
	for _, r := range cdn {
		if r.Cookie != "" {
			t.Errorf("the cookie reached the CDN: %+v", r)
		}
		if !strings.HasPrefix(r.Range, "bytes=") {
			t.Errorf("a CDN request without a byte range: %+v", r)
		}
		start, _, _ := strings.Cut(strings.TrimPrefix(r.Range, "bytes="), "-")
		starts[start] = true
	}
	if len(starts) < 2 || maxActive < 2 {
		t.Errorf("%d CDN requests from %d offsets, at most %d at once; want several ranged connections: %+v",
			len(cdn), len(starts), maxActive, cdn)
	}
	h.stop(d)
}

// mixedMPD is a static DASH presentation of 4 s with WebM (VP9, Opus) and MP4 (H.264,
// AAC) AdaptationSets; the MP4 tracks are served below its directory.
func mixedMPD(segs int) string {
	tmpl := func(dir string, timescale, d int) string {
		return fmt.Sprintf(`<SegmentTemplate timescale="%d" initialization="%s$RepresentationID$/init.mp4" `+
			`media="%s$RepresentationID$/seg-$Number$.m4s" startNumber="1">
        <SegmentTimeline><S t="0" d="%d" r="%d"/></SegmentTimeline>
      </SegmentTemplate>`, timescale, dir, dir, d, segs-1)
	}
	video := tmpl("video/", videoTimescale, videoPerSeg*videoSampleDur)
	audio := tmpl("audio/", audioTimescale, audioPerSeg*audioSampleDur)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT%dS" minBufferTime="PT2S"
     profiles="urn:mpeg:dash:profile:isoff-live:2011">
  <Period id="p0">
    <AdaptationSet mimeType="video/webm" contentType="video">
      %s
      <Representation id="vp9" bandwidth="3000000" width="1280" height="720" frameRate="25" codecs="vp09.00.31.08"/>
    </AdaptationSet>
    <AdaptationSet mimeType="video/mp4" contentType="video">
      %s
      <Representation id="avc" bandwidth="600000" width="640" height="352" frameRate="25" codecs="avc1.4d401e"/>
    </AdaptationSet>
    <AdaptationSet mimeType="audio/webm" contentType="audio" lang="en">
      %s
      <Representation id="opus" bandwidth="160000" codecs="opus" audioSamplingRate="48000"/>
    </AdaptationSet>
    <AdaptationSet mimeType="audio/mp4" contentType="audio" lang="en">
      %s
      <Representation id="aac" bandwidth="128000" codecs="mp4a.40.2" audioSamplingRate="48000"/>
    </AdaptationSet>
  </Period>
</MPD>
`, segs, video, video, audio, audio)
}

// §8.3 #9: manifests list and save MP4 only. A DASH presentation with WebM and MP4
// Representations lists only the MP4 ones (CLI and relay alike) and saves them, fetching
// nothing of the WebM ones; an audio-only HLS stream is media_unsupported through the CLI
// and the relay, and nothing is recorded or fetched for it.
func TestMediaMP4Only(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := newStreamServer(t)
	const segs = 4
	video := newGenTrack(t, true, 640, 352, segs, 151, false)
	audio := newGenTrack(t, false, 0, 0, segs, 152, false)
	s.add("/mix/movie.mpd", []byte(mixedMPD(segs)), dashMIME)
	video.serve(s, "/mix/video/avc/")
	audio.serve(s, "/mix/audio/aac/")
	aud := newGenTS(t, tsSpec{audio: &testmedia.AudioConfig{Frames: 375, Seed: 153}})
	s.add("/radio/master.m3u8", []byte(testmedia.MasterPlaylist([]testmedia.Variant{
		{URI: "index.m3u8", Bandwidth: 128000, Codecs: "mp4a.40.2"}}, nil)), hlsMIME)
	serveTS(s, "/radio/", aud)
	d := h.startDaemon()
	h.waitReady(d)
	h.setDefaultDir(h.out)

	r := h.cli("media", s.url("/mix/movie.mpd"), "--list", "--json").ok(t)
	var list siteMediaView
	if err := json.Unmarshal([]byte(r.stdout), &list); err != nil {
		t.Fatalf("media --list --json: %v: %v", err, r)
	}
	var ids []string
	for _, tr := range list.Tracks {
		ids = append(ids, tr.ID)
	}
	if list.Format != "dash" || list.Container != "mp4" || !slices.Equal(ids, []string{"avc", "aac"}) ||
		list.SelectedVideo != "avc" || list.SelectedAudio != "aac" {
		t.Fatalf("media --list --json = %+v", list)
	}
	r = h.cli("media", s.url("/mix/movie.mpd"), "-o", h.out, "--name", "mixed", "--wait").ok(t)
	checkMP4(t, filepath.Join(h.out, "mixed.mp4"), video, audio)
	for _, p := range s.requestedPaths() {
		if strings.Contains(p, "/vp9/") || strings.Contains(p, "/opus/") {
			t.Errorf("%s of a WebM Representation was fetched", p)
		}
	}

	for _, args := range [][]string{{"--list"}, {"-o", h.out}} {
		r := h.cli(append([]string{"media", s.url("/radio/master.m3u8")}, args...)...)
		if r.code == 0 || !strings.Contains(r.stderr, "no MP4 video in this stream") {
			t.Errorf("media of an audio-only stream %q: %v", args, r)
		}
	}
	p := h.helloStreams()
	p.send(map[string]any{"id": "2", "type": "probeStream", "manifestUrl": s.url("/mix/movie.mpd")})
	info := p.recv()
	tracks, _ := info["tracks"].([]any)
	if info["type"] != "videoInfo" || info["format"] != "dash" || info["container"] != "mp4" || len(tracks) != 2 {
		t.Errorf("probeStream of the DASH presentation answered %v", info)
	}
	noAddresses(t, "probeStream's answer", info)
	for i, typ := range []string{"probeStream", "addMedia"} {
		p.send(map[string]any{"id": fmt.Sprint(3 + i), "type": typ, "manifestUrl": s.url("/radio/master.m3u8"), "deadlineMs": 15000})
		a := p.recv()
		e, _ := a["error"].(map[string]any)
		if a["ok"] != false || e["code"] != "rejected" || e["reason"] != "media_unsupported" {
			t.Errorf("%s of an audio-only stream answered %v", typ, a)
		}
	}
	p.closeAndWait()
	if list := h.ls(); len(list) != 1 {
		t.Errorf("downloads %+v, want only the DASH one", list)
	}
	for _, p := range s.requestedPaths() {
		if strings.HasPrefix(p, "/radio/") && strings.HasSuffix(p, ".ts") {
			t.Errorf("%s of an audio-only stream was fetched", p)
		}
	}
	h.stop(d)
}

// §8.3 #10: a page whose only formats are MPEG-TS HLS (m3u8_native, as on some Twitch
// pages) is offered through the manifest fallback: "media --site --list" names the site
// and lists its MP4 qualities, and the download is saved as one ".mp4" of the best one.
func TestSiteTSFallback(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	site := h.newSite(true, nil)
	s := newStreamServer(t)
	_, hi := tsMaster(t, s, "/vod/", 161)
	master := s.url("/vod/master.m3u8")
	format := func(id, variant string, width, height, tbr int) map[string]any {
		return map[string]any{"format_id": id, "protocol": "m3u8_native", "url": s.url("/vod/" + variant + "/index.m3u8"),
			"manifest_url": master, "ext": "mp4", "vcodec": "avc1.4d401f", "acodec": "mp4a.40.2", "width": width,
			"height": height, "fps": 30, "tbr": tbr}
	}
	b, err := json.Marshal(map[string]any{"id": "v2000000001", "title": "A TS-only VOD", "extractor_key": "TwitchVod",
		"duration": 4.267, "live_status": "not_live",
		"formats": []map[string]any{format("hls-180p", "lo", 320, 180, 400), format("hls-360p", "hi", 640, 360, 1200)}})
	if err != nil {
		t.Fatal(err)
	}
	page := site.page("/videos/2000000001", fakePage{Stdout: string(b) + "\n"})
	d := h.startDaemonFrom(bin.downexdHooks, site.env)
	h.waitReady(d)

	r := h.cli("media", "--site", page, "--list", "--json").ok(t)
	var v siteMediaView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		t.Fatalf("media --site --list --json: %v: %v", err, r)
	}
	if v.Origin != "site" || v.Site != "Twitch" || v.Format != "hls" || v.Container != "mp4" || len(v.Tracks) != 2 ||
		v.Tracks[0].Height != 360 || v.Tracks[1].Height != 180 || v.SelectedVideo != v.Tracks[0].ID {
		t.Fatalf("media --site --list --json = %+v", v)
	}
	for _, p := range s.requestedPaths() {
		if strings.HasSuffix(p, ".ts") {
			t.Errorf("the probe fetched a segment: %s", p)
		}
	}

	id := h.cli("media", "--site", page, "-o", h.out).ok(t).id(t)
	dl := h.waitCompleted(id)
	if dl.GetFilename() != "A TS-only VOD.mp4" || dl.GetMedia().GetOrigin() != downexv1.MediaOrigin_MEDIA_ORIGIN_SITE ||
		dl.GetMedia().GetContainer() != "mp4" || dl.GetMedia().GetSite() != "Twitch" {
		t.Fatalf("completed download = %v", dl)
	}
	checkMP4(t, filepath.Join(h.out, "A TS-only VOD.mp4"), tsTracks(hi)...)
	noMediaLeftovers(t, h.out)
	for _, p := range s.requestedPaths() {
		if strings.HasPrefix(p, "/vod/lo/") && strings.HasSuffix(p, ".ts") {
			t.Errorf("%s of the quality not chosen was fetched", p)
		}
	}
	h.stop(d)
}
