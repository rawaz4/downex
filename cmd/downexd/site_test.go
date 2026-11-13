package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/daemon"
	"downex/internal/limiter"
	"downex/internal/media"
	"downex/internal/mediajob"
	"downex/internal/model"
	"downex/internal/sitejob"
	"downex/internal/sitemedia"
	"downex/internal/testserver"
	"downex/internal/transport"
)

// ---- fixtures ---------------------------------------------------------------------

// H.264 parameter sets of a Main profile stream of 640x352 frames.
var testSPS, testPPS = mustHex("674d001eed81405b20"), mustHex("68ce3c80")

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// fmp4Track is a fragmented MP4 track as a site serves it in one file: the init segment
// (ftyp, moov with mvex) and the movie fragments, with the samples they carry.
type fmp4Track struct {
	init    []byte
	frags   [][]byte
	samples [][]byte
}

func (f fmp4Track) file() []byte { return bytes.Join(append([][]byte{f.init}, f.frags...), nil) }

func encodeBox(t *testing.T, b interface{ Encode(w io.Writer) error }) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := b.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// buildFMP4 builds a video (H.264) or audio (AAC) track of n samples of size random
// bytes, perFrag per movie fragment.
func buildFMP4(t *testing.T, video bool, n, size, perFrag int, seed uint64) fmp4Track {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = 1000
	dur := uint32(1024)
	if video {
		dur = 3000
		if err := init.AddEmptyTrack(90000, "video", "und").SetAVCDescriptor("avc1", [][]byte{testSPS}, [][]byte{testPPS}, true); err != nil {
			t.Fatal(err)
		}
	} else if err := init.AddEmptyTrack(48000, "audio", "und").SetAACDescriptor(aac.AAClc, 48000); err != nil {
		t.Fatal(err)
	}
	tr := fmp4Track{init: encodeBox(t, init)}
	rng := rand.New(rand.NewPCG(seed, 7))
	dt := uint64(0)
	for i := 0; i < n; i += perFrag {
		frag, err := mp4.CreateFragment(uint32(len(tr.frags)+1), 1)
		if err != nil {
			t.Fatal(err)
		}
		for range min(perFrag, n-i) {
			data := make([]byte, size)
			for k := range data {
				data[k] = byte(rng.Uint32())
			}
			tr.samples = append(tr.samples, data)
			frag.AddFullSample(mp4.FullSample{Sample: mp4.Sample{Flags: mp4.SyncSampleFlags, Dur: dur, Size: uint32(size)},
				DecodeTime: dt, Data: data})
			dt += uint64(dur)
		}
		tr.frags = append(tr.frags, encodeBox(t, frag))
	}
	return tr
}

var (
	pairOnce             sync.Once
	pairVideo, pairAudio fmp4Track
)

// testPair returns a video track of about 2.5 MB and an audio track of about 1.2 MB.
func testPair(t *testing.T) (video, audio fmp4Track) {
	t.Helper()
	pairOnce.Do(func() {
		pairVideo = buildFMP4(t, true, 160, 16000, 16, 1)
		pairAudio = buildFMP4(t, false, 400, 3000, 50, 2)
	})
	if pairVideo.init == nil {
		t.Fatal("fixtures were not built")
	}
	return pairVideo, pairAudio
}

// checkOutput decodes a progressive MP4 and compares the samples of its tracks with
// want's.
func checkOutput(t *testing.T, path string, want ...fmp4Track) {
	t.Helper()
	fd, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	f, err := mp4.DecodeFile(fd, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		t.Fatalf("decode the output: %v", err)
	}
	if f.IsFragmented() || f.Moov == nil || len(f.Moov.Traks) != len(want) {
		t.Fatalf("output: fragmented %v, %d tracks, want %d", f.IsFragmented(), len(f.Moov.Traks), len(want))
	}
	for i, tr := range want {
		trak := f.Moov.Traks[i]
		n := trak.Mdia.Minf.Stbl.Stsz.GetNrSamples()
		data, err := f.ReadSampleData(trak, 1, n, fd, nil)
		if err != nil || int(n) != len(tr.samples) || !bytes.Equal(data, bytes.Join(tr.samples, nil)) {
			t.Fatalf("track %d: %d samples (want %d), data differs or %v", i, n, len(tr.samples), err)
		}
	}
}

// fileServer serves files with byte ranges (http.ServeContent), answers the paths of
// deny with their status, and records every request.
type fileServer struct {
	ts *httptest.Server

	mu     sync.Mutex
	files  map[string][]byte
	deny   map[string]int
	reqs   []fileReq
	active int
	peak   int
}

type fileReq struct{ path, rng string }

func newFileServer(t *testing.T) *fileServer {
	t.Helper()
	s := &fileServer{files: make(map[string][]byte), deny: make(map[string]int)}
	s.ts = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.ts.Close)
	return s
}

func (s *fileServer) url(path string) string { return s.ts.URL + path }

func (s *fileServer) add(path string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = data
}

func (s *fileServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, fileReq{r.URL.Path, r.Header.Get("Range")})
	data, ok := s.files[r.URL.Path]
	status := s.deny[r.URL.Path]
	s.active++
	s.peak = max(s.peak, s.active)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	switch {
	case status != 0:
		w.WriteHeader(status)
	case !ok:
		http.NotFound(w, r)
	default:
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	}
}

func (s *fileServer) requests() []fileReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reqs)
}

// rangeLength returns the length of a "bytes=a-b" Range (-1 for any other).
func rangeLength(v string) int64 {
	a, b, ok := strings.Cut(strings.TrimPrefix(v, "bytes="), "-")
	first, err1 := strconv.ParseInt(a, 10, 64)
	last, err2 := strconv.ParseInt(b, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return -1
	}
	return last - first + 1
}

// fakeResolver answers with the yt-dlp outputs of pages in turn (the last repeats) and
// records the requests.
type fakeResolver struct {
	mu    sync.Mutex
	pages []string
	err   error
	reqs  []sitejob.Request
	block bool // wait for the caller's end
}

func (f *fakeResolver) Resolve(ctx context.Context, req sitejob.Request) (*sitemedia.Info, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	page := f.pages[min(len(f.reqs)-1, len(f.pages)-1)]
	err, block := f.err, f.block
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, fmt.Errorf("%w: %w", sitemedia.ErrSiteResolving, ctx.Err())
	}
	if err != nil {
		return nil, err
	}
	return sitemedia.Parse([]byte(page), time.Now())
}

func (f *fakeResolver) requests() []sitejob.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

// freshness returns the Fresh flag of each request.
func (f *fakeResolver) freshness() []bool {
	var out []bool
	for _, r := range f.requests() {
		out = append(out, r.Fresh)
	}
	return out
}

// testRules caps every host: connections and request length.
type testRules struct {
	conns    int
	maxBytes int64
}

func (r testRules) HostMaxConnections(string) int      { return r.conns }
func (r testRules) HostLimiter(string) limiter.Limiter { return nil }
func (r testRules) HostMaxRequestBytes(string) int64   { return r.maxBytes }

func siteFactory(t *testing.T) transport.Factory {
	t.Helper()
	o := transport.DefaultHTTPOptions()
	o.Proxy = "direct"
	f, err := transport.NewHTTPFactory(o)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func siteContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// format is one yt-dlp format of a test page.
type format map[string]any

func videoFormat(id, url string, size int) format {
	return format{"format_id": id, "protocol": "http", "url": url, "ext": "mp4", "container": "mp4_dash",
		"vcodec": "avc1.4d401e", "acodec": "none", "width": 640, "height": 352, "fps": 30, "tbr": 1200, "filesize": size}
}

func audioFormat(id, url string, approx int) format {
	return format{"format_id": id, "protocol": "http", "url": url, "ext": "m4a", "container": "m4a_dash",
		"vcodec": "none", "acodec": "mp4a.40.2", "abr": 128, "asr": 48000, "filesize_approx": approx}
}

// pageJSON is yt-dlp's -J output for a page with formats.
func pageJSON(t *testing.T, formats ...format) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"id": "vid00000001", "extractor_key": "Youtube", "title": "Test video",
		"duration": 16.5, "formats": formats})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// keyOf returns the key a page gives format id.
func keyOf(t *testing.T, page, id string) string {
	t.Helper()
	info, err := sitemedia.Parse([]byte(page), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if f := info.Format(id); f != nil {
		return f.Key
	}
	t.Fatalf("no format %s", id)
	return ""
}

// runOptions completes o (its host rules) for a run that saves "video.mp4" in a new
// folder.
func runOptions(t *testing.T, o daemon.MediaRunOptions) daemon.MediaRunOptions {
	t.Helper()
	dir := t.TempDir()
	o.Factory = siteFactory(t)
	o.MaxConnections = 8
	o.PartDir = daemon.MediaPartsDir(dir, "video.mp4")
	o.OutPath = filepath.Join(dir, "video.mp4")
	return o
}

// pairPage serves the test pair under prefix and returns the page offering it.
func pairPage(t *testing.T, s *fileServer, prefix string) string {
	t.Helper()
	video, audio := testPair(t)
	s.add(prefix+"/v.mp4", video.file())
	s.add(prefix+"/a.m4a", audio.file())
	return pageJSON(t, videoFormat("137", s.url(prefix+"/v.mp4"), len(video.file())),
		audioFormat("140", s.url(prefix+"/a.m4a"), len(audio.file())))
}

// ---- probe ------------------------------------------------------------------------

// A probe offers one MP4 video per quality class and the default pick (or the tracks
// named; a video the class rule left out is then listed in place of its class's), with
// nothing of WebM, and passes the page's cookie, Referer and proxy to the resolver.
func TestSiteProbe(t *testing.T) {
	srv := newFileServer(t)
	page := srv.url("/watch?v=vid00000001")
	low := videoFormat("136", srv.url("/v136"), 500)
	low["height"], low["width"] = 240, 426
	av1 := videoFormat("396", srv.url("/v396"), 800)
	av1["vcodec"] = "av01.0.04M.08"
	res := &fakeResolver{pages: []string{pageJSON(t,
		videoFormat("137", srv.url("/v137"), 1000), av1, low, audioFormat("140", srv.url("/a140"), 300),
		format{"format_id": "248", "protocol": "http", "url": srv.url("/v248"), "ext": "webm", "container": "webm_dash",
			"vcodec": "vp9", "acodec": "none", "height": 1080},
		format{"format_id": "251", "protocol": "http", "url": srv.url("/a251"), "ext": "webm", "container": "webm_dash",
			"vcodec": "none", "acodec": "opus"},
	)}}
	s := newSiteMedia(res)
	sel := daemon.SiteSelection{PageURL: page, Cookie: "SID=1", Referer: "https://blog.example/post",
		Proxy: daemon.SiteProxy{URL: "http://proxy.lan:3128", Username: "me", Password: "pw"}}
	p, err := s.Probe(siteContext(t), siteFactory(t), sel)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tr := range p.Tracks {
		ids = append(ids, tr.ID)
	}
	if p.Format != "http" || p.Container != "mp4" || p.Site != "YouTube" || p.Title != "Test video" ||
		p.Duration != 16500*time.Millisecond || p.FragmentsTotal != 0 || p.DefaultVideo != "137" || p.DefaultAudio != "140" ||
		!slices.Equal(ids, []string{"137", "136", "140"}) {
		t.Errorf("probe = %+v (tracks %v)", p, ids)
	}
	want := sitejob.Request{PageURL: page, Cookie: "SID=1", Referer: "https://blog.example/post",
		Proxy: sitejob.Proxy{URL: "http://proxy.lan:3128", Username: "me", Password: "pw"}}
	if got := res.requests(); len(got) != 1 || got[0] != want {
		t.Errorf("resolver requests %+v, want %+v", got, want)
	}

	sel.VideoTrack = "136"
	if p, err := s.Probe(siteContext(t), siteFactory(t), sel); err != nil || p.DefaultVideo != "136" || p.DefaultAudio != "140" {
		t.Errorf("named lower quality: %+v, %v", p, err)
	}
	sel.VideoTrack = "396"
	p, err = s.Probe(siteContext(t), siteFactory(t), sel)
	ids = ids[:0]
	for _, tr := range p.Tracks {
		ids = append(ids, tr.ID)
	}
	if err != nil || p.DefaultVideo != "396" || p.DefaultAudio != "140" || !slices.Equal(ids, []string{"396", "136", "140"}) {
		t.Errorf("named left-out quality: %+v (tracks %v), %v", p, ids, err)
	}
	for _, c := range []struct {
		video, audio string
		want         error
	}{{"248", "", media.ErrNotFound}, {"137", "251", media.ErrNotFound}, {"140", "", media.ErrMalformed}} {
		sel.VideoTrack, sel.AudioTrack = c.video, c.audio
		if _, err := s.Probe(siteContext(t), siteFactory(t), sel); !errors.Is(err, c.want) {
			t.Errorf("probe %s + %s: %v, want %v", c.video, c.audio, err, c.want)
		}
	}
	res.err = fmt.Errorf("%w: Sign in to confirm your age", sitemedia.ErrAuthRequired)
	if _, err := s.Probe(siteContext(t), siteFactory(t), sel); !errors.Is(err, sitemedia.ErrAuthRequired) {
		t.Errorf("resolver error: %v", err)
	}
}

// A look-ahead's probe (docs/SITE-MEDIA-SPEC.md §5.6) reaches the resolver as one, with
// the request of a probe otherwise (so it fills the cache entry of the page), and its
// answer is a probe's; its busy refusal passes as it is.
func TestSiteProbePrefetch(t *testing.T) {
	srv := newFileServer(t)
	page := srv.url("/watch?v=vid00000001")
	res := &fakeResolver{pages: []string{pairPage(t, srv, "/p")}}
	s := newSiteMedia(res)
	sel := daemon.SiteSelection{PageURL: page, Cookie: "SID=1", Prefetch: true}
	p, err := s.Probe(siteContext(t), siteFactory(t), sel)
	if err != nil || p.Format != "http" || p.DefaultVideo != "137" || p.DefaultAudio != "140" {
		t.Fatalf("look-ahead = %+v, %v", p, err)
	}
	want := sitejob.Request{PageURL: page, Cookie: "SID=1", Prefetch: true}
	if got := res.requests(); len(got) != 1 || got[0] != want {
		t.Errorf("resolver requests %+v, want %+v", got, want)
	}
	res.err = sitejob.ErrBusy
	if _, err := s.Probe(siteContext(t), siteFactory(t), sel); !errors.Is(err, sitemedia.ErrSiteResolving) {
		t.Errorf("busy look-ahead = %v, want site_resolving", err)
	}
}

// hlsFiles serves an HLS master playlist of fragmented MP4 (master.m3u8) and one of
// MPEG-TS segments (ts.m3u8), and returns the track the fMP4 one carries.
func hlsFiles(t *testing.T, s *fileServer) fmp4Track {
	t.Helper()
	video, _ := testPair(t)
	var pl strings.Builder
	pl.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-MAP:URI=\"init.mp4\"\n")
	for i, f := range video.frags {
		s.add(fmt.Sprintf("/hls/seg%d.m4s", i), f)
		fmt.Fprintf(&pl, "#EXTINF:0.533,\nseg%d.m4s\n", i)
	}
	pl.WriteString("#EXT-X-ENDLIST\n")
	s.add("/hls/init.mp4", video.init)
	s.add("/hls/v.m3u8", []byte(pl.String()))
	s.add("/hls/master.m3u8", []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1200000,RESOLUTION=640x352,CODECS=\"avc1.4d401e\"\nv.m3u8\n"))
	s.add("/hls/ts.m3u8", []byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:4.0,\nseg0.ts\n#EXT-X-ENDLIST\n"))
	return video
}

func hlsPage(t *testing.T, s *fileServer, manifest string) string {
	t.Helper()
	return pageJSON(t, format{"format_id": "hls-1200", "protocol": "m3u8_native", "url": s.url("/hls/v.m3u8"),
		"manifest_url": s.url(manifest), "ext": "mp4", "vcodec": "avc1.4d401e", "acodec": "none", "height": 352})
}

// A page with only an HLS manifest is probed and downloaded as M5 does it when the
// manifest is fragmented MP4; an MPEG-TS manifest is offered too, as an MP4 (it is
// converted, docs/VIDEO-DETECT-SPEC.md §3.3). Of its media the probe reads at most the
// start of a segment, for the size and picture its bare playlist does not declare
// (mediajob.ProbeMP4's bounded requests), and a look-ahead's probe nothing at all.
func TestSiteManifestFallback(t *testing.T) {
	srv := newFileServer(t)
	video := hlsFiles(t, srv)
	page := srv.url("/watch?v=vid00000001")
	res := &fakeResolver{pages: []string{hlsPage(t, srv, "/hls/master.m3u8")}}
	s := newSiteMedia(res)
	p, err := s.Probe(siteContext(t), siteFactory(t), daemon.SiteSelection{PageURL: page})
	if err != nil {
		t.Fatal(err)
	}
	if p.Format != "hls" || p.Container != "mp4" || p.DefaultVideo == "" || p.Site != "YouTube" || p.Title != "Test video" ||
		p.FragmentsTotal != len(video.frags)+1 {
		t.Fatalf("probe = %+v", p)
	}
	var key string
	for _, tr := range p.Tracks {
		if tr.ID == p.DefaultVideo {
			key = tr.Key
		}
	}
	opts := runOptions(t, daemon.MediaRunOptions{})
	sel := daemon.SiteSelection{PageURL: page, Format: "hls", VideoTrack: p.DefaultVideo, VideoKey: key}
	container, err := s.Run(siteContext(t), opts, sel, nil, nil, nil)
	if err != nil || container != "mp4" {
		t.Fatalf("Run = %q, %v", container, err)
	}
	checkOutput(t, opts.OutPath, video)

	res.pages = []string{hlsPage(t, srv, "/hls/ts.m3u8")}
	res.reqs = nil
	p, err = s.Probe(siteContext(t), siteFactory(t), daemon.SiteSelection{PageURL: page})
	if err != nil || p.Format != "hls" || p.Container != "mp4" || len(p.Tracks) != 1 || p.DefaultVideo != p.Tracks[0].ID ||
		p.Tracks[0].Kind != media.KindMuxed || p.FragmentsTotal != 1 {
		t.Fatalf("MPEG-TS manifest: %+v, %v", p, err)
	}
	for _, r := range srv.requests() {
		if n := rangeLength(r.rng); strings.HasSuffix(r.path, ".ts") && (n < 1 || n > 512<<10) {
			t.Errorf("the probe fetched a segment: %s (Range %q)", r.path, r.rng)
		}
	}

	before := len(srv.requests())
	if _, err := s.Probe(siteContext(t), siteFactory(t), daemon.SiteSelection{PageURL: page, Prefetch: true}); err != nil {
		t.Fatalf("look-ahead of the MPEG-TS manifest: %v", err)
	}
	for _, r := range srv.requests()[before:] {
		if !strings.HasSuffix(r.path, ".m3u8") {
			t.Errorf("the look-ahead fetched %s (Range %q)", r.path, r.rng)
		}
	}
}

// mixedHLS is a master playlist of an MPEG-TS variant whose CODECS name AC-3 (not
// converted), an MPEG-TS variant of the same quality with AAC, and the fragmented MP4
// video of hlsFiles.
const mixedHLS = "#EXTM3U\n" +
	"#EXT-X-STREAM-INF:BANDWIDTH=300000,RESOLUTION=320x176,CODECS=\"avc1.4d400d,ac-3\"\nts.m3u8\n" +
	"#EXT-X-STREAM-INF:BANDWIDTH=200000,RESOLUTION=320x176,CODECS=\"avc1.4d400d,mp4a.40.2\"\ntsaac.m3u8\n" +
	"#EXT-X-STREAM-INF:BANDWIDTH=1200000,RESOLUTION=640x352,CODECS=\"avc1.4d401e\"\nv.m3u8\n"

// The manifest fallback offers only what is saved as an MP4: the fragmented MP4 video
// and the MPEG-TS variant with AAC, which takes the place of the AC-3 one of its quality
// (the second offer round). A run of the AC-3 variant (a key named anyway) is refused
// before any segment is fetched, never saved as ".mp4".
func TestSiteManifestFallbackOffersMP4Only(t *testing.T) {
	srv := newFileServer(t)
	hlsFiles(t, srv)
	srv.add("/hls/seg0.ts", bytes.Repeat(append([]byte{0x47, 0x1f, 0xff, 0x10}, make([]byte, 184)...), 3))
	srv.add("/hls/tsaac.m3u8", []byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:4.0,\nseg0.ts\n#EXT-X-ENDLIST\n"))
	srv.add("/hls/mixed.m3u8", []byte(mixedHLS))
	page := srv.url("/watch?v=vid00000001")
	s := newSiteMedia(&fakeResolver{pages: []string{hlsPage(t, srv, "/hls/mixed.m3u8")}})
	p, err := s.Probe(siteContext(t), siteFactory(t), daemon.SiteSelection{PageURL: page})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tracks) != 2 || p.Tracks[0].Height != 352 || p.Tracks[1].Height != 176 || p.Tracks[1].Codecs != "avc1.4d400d,mp4a.40.2" ||
		p.DefaultVideo != p.Tracks[0].ID || p.Container != "mp4" {
		t.Fatalf("probe = %+v", p)
	}
	pl, err := media.ParseHLS(nil, []byte(mixedHLS))
	if err != nil {
		t.Fatal(err)
	}
	var ac3 media.Track
	for _, tr := range pl.Options() {
		if strings.Contains(tr.Codecs, "ac-3") {
			ac3 = tr
		}
	}
	if _, err := s.Probe(siteContext(t), siteFactory(t), daemon.SiteSelection{PageURL: page, VideoTrack: ac3.ID,
		VideoKey: ac3.Key}); !errors.Is(err, media.ErrNotFound) {
		t.Errorf("probe of the AC-3 variant: %v", err)
	}
	opts := runOptions(t, daemon.MediaRunOptions{})
	sel := daemon.SiteSelection{PageURL: page, Format: "hls", VideoTrack: ac3.ID, VideoKey: ac3.Key}
	if c, err := s.Run(siteContext(t), opts, sel, nil, nil, nil); !errors.Is(err, media.ErrUnsupported) {
		t.Fatalf("run of the AC-3 variant = %q, %v", c, err)
	}
	if _, err := os.Stat(opts.OutPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an output was written: %v", err)
	}
	for _, r := range srv.requests() {
		if r.path == "/hls/seg0.ts" {
			t.Error("a segment of the AC-3 variant was fetched")
		}
	}
}

// The DASH fallback leaves out WebM (VP9, Opus) Representations and lists the MP4 ones
// best first.
func TestSiteManifestFallbackDASHOrder(t *testing.T) {
	srv := newFileServer(t)
	const tmpl = `<SegmentTemplate initialization="$RepresentationID$-init" media="$RepresentationID$-$Number$" duration="2" timescale="1" startNumber="1"/>`
	srv.add("/m.mpd", []byte(`<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S" minBufferTime="PT2S">
 <Period>
  <AdaptationSet mimeType="video/webm" contentType="video">`+tmpl+`
   <Representation id="vp9-1080" codecs="vp09.00.40.08" width="1920" height="1080" bandwidth="4000000"/>
  </AdaptationSet>
  <AdaptationSet mimeType="video/mp4" contentType="video">`+tmpl+`
   <Representation id="avc-360" codecs="avc1.4d401e" width="640" height="360" bandwidth="800000"/>
   <Representation id="avc-720" codecs="avc1.4d401f" width="1280" height="720" bandwidth="2000000"/>
  </AdaptationSet>
  <AdaptationSet mimeType="audio/webm" contentType="audio" lang="en">`+tmpl+`
   <Representation id="opus" codecs="opus" bandwidth="160000"/>
  </AdaptationSet>
  <AdaptationSet mimeType="audio/mp4" contentType="audio" lang="en">`+tmpl+`
   <Representation id="aac" codecs="mp4a.40.2" bandwidth="128000"/>
  </AdaptationSet>
 </Period>
</MPD>`))
	m := srv.url("/m.mpd")
	page := pageJSON(t,
		format{"format_id": "dash-avc-720", "protocol": "http_dash_segments", "url": m, "manifest_url": m, "ext": "mp4",
			"vcodec": "avc1.4d401f", "acodec": "none", "height": 720},
		format{"format_id": "dash-aac", "protocol": "http_dash_segments", "url": m, "manifest_url": m, "ext": "m4a",
			"vcodec": "none", "acodec": "mp4a.40.2"})
	s := newSiteMedia(&fakeResolver{pages: []string{page}})
	p, err := s.Probe(siteContext(t), siteFactory(t), daemon.SiteSelection{PageURL: srv.url("/watch?v=vid00000001")})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tr := range p.Tracks {
		ids = append(ids, tr.ID)
	}
	if !slices.Equal(ids, []string{"avc-720", "avc-360", "aac"}) || p.DefaultVideo != "avc-720" || p.DefaultAudio != "aac" ||
		p.Format != "dash" || p.Container != "mp4" {
		t.Errorf("probe = %+v (tracks %v)", p, ids)
	}
}

// ---- run --------------------------------------------------------------------------

// A run finds the chosen tracks by key and downloads them in chunks of the host's
// request cap over at most its connections, reports their total size, records the chunk
// size in its checkpoints and joins them into one MP4.
func TestSiteRunPair(t *testing.T) {
	srv := newFileServer(t)
	page := pairPage(t, srv, "/p")
	res := &fakeResolver{pages: []string{page}}
	s := newSiteMedia(res)
	sel := daemon.SiteSelection{PageURL: srv.url("/watch?v=vid00000001"), Format: "http", VideoTrack: "137",
		AudioTrack: "140", VideoKey: keyOf(t, page, "137"), AudioKey: keyOf(t, page, "140")}
	opts := runOptions(t, daemon.MediaRunOptions{HostRules: testRules{conns: 2, maxBytes: 1 << 20}})
	var last daemon.MediaProgress
	var chunks []int64
	container, err := s.Run(siteContext(t), opts, sel, nil, func(p daemon.MediaProgress) { last = p },
		func(c model.MediaCheckpoint) error {
			for _, tr := range c.Tracks {
				chunks = append(chunks, tr.ChunkBytes)
			}
			return nil
		})
	if err != nil || container != "mp4" {
		t.Fatalf("Run = %q, %v", container, err)
	}
	video, audio := testPair(t)
	checkOutput(t, opts.OutPath, video, audio)
	if want := int64(len(video.file()) + len(audio.file())); last.BytesTotal != want {
		t.Errorf("BytesTotal %d, want %d", last.BytesTotal, want)
	}
	if len(chunks) == 0 || slices.ContainsFunc(chunks, func(c int64) bool { return c != 1<<20 }) {
		t.Errorf("checkpoint chunk sizes %v, want 1 MiB", chunks)
	}
	for _, r := range srv.requests() {
		if n := rangeLength(r.rng); n < 1 || n > 1<<20 {
			t.Errorf("request %s Range %q", r.path, r.rng)
		}
	}
	if srv.peak > 2 {
		t.Errorf("%d requests at once, the host allows 2", srv.peak)
	}
	if got := res.freshness(); !slices.Equal(got, []bool{false}) {
		t.Errorf("resolutions %v, want one from the cache", got)
	}
}

// A 403 renews the addresses past the cache, and the run completes with the new ones.
func TestSiteRunRefreshOn403(t *testing.T) {
	srv := newFileServer(t)
	old := pairPage(t, srv, "/old")
	srv.deny["/old/v.mp4"], srv.deny["/old/a.m4a"] = http.StatusForbidden, http.StatusForbidden
	res := &fakeResolver{pages: []string{old, pairPage(t, srv, "/new")}}
	s := newSiteMedia(res)
	sel := daemon.SiteSelection{PageURL: srv.url("/watch?v=vid00000001"), Format: "http", VideoTrack: "137",
		AudioTrack: "140", VideoKey: keyOf(t, old, "137"), AudioKey: keyOf(t, old, "140")}
	opts := runOptions(t, daemon.MediaRunOptions{})
	if _, err := s.Run(siteContext(t), opts, sel, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	video, audio := testPair(t)
	checkOutput(t, opts.OutPath, video, audio)
	if got := res.freshness(); !slices.Equal(got, []bool{false, true}) {
		t.Errorf("resolutions %v, want the run's and one renewal past the cache", got)
	}
}

// A chosen key the page no longer has is resolved once more past the cache, after the
// retry delay; still missing, the run fails as media_invalid, never picking another
// quality. A key that is back on that resolution runs.
func TestSiteRunTrackGone(t *testing.T) {
	srv := newFileServer(t)
	page := pairPage(t, srv, "/p")
	reencoded := strings.Replace(page, `"filesize":`+strconv.Itoa(len(pairVideo.file())), `"filesize":123456`, 1)
	if reencoded == page {
		t.Fatal("cannot change the video's size")
	}
	sel := daemon.SiteSelection{PageURL: srv.url("/watch?v=vid00000001"), Format: "http", VideoTrack: "137",
		AudioTrack: "140", VideoKey: keyOf(t, page, "137"), AudioKey: keyOf(t, page, "140")}
	res := &fakeResolver{pages: []string{reencoded}}
	s := newSiteMedia(res)
	s.retryDelay = 50 * time.Millisecond
	start := time.Now()
	_, err := s.Run(siteContext(t), runOptions(t, daemon.MediaRunOptions{}), sel, nil, nil, nil)
	if !errors.Is(err, mediajob.ErrInvalidSelection) || !errors.Is(err, media.ErrNotFound) ||
		!strings.Contains(err.Error(), "the chosen quality is no longer offered") {
		t.Fatalf("Run = %v, want media_invalid", err)
	}
	if d := time.Since(start); d < s.retryDelay {
		t.Errorf("resolved again after %v, before the retry delay", d)
	}
	if got := res.freshness(); !slices.Equal(got, []bool{false, true}) {
		t.Errorf("resolutions %v", got)
	}
	if n := len(srv.requests()); n != 0 {
		t.Errorf("%d requests for a quality that is gone", n)
	}

	res = &fakeResolver{pages: []string{reencoded, page}}
	s = newSiteMedia(res)
	s.retryDelay = time.Millisecond
	opts := runOptions(t, daemon.MediaRunOptions{})
	if _, err := s.Run(siteContext(t), opts, sel, nil, nil, nil); err != nil {
		t.Fatalf("key back on the second resolution: %v", err)
	}
	checkOutput(t, opts.OutPath, pairVideo, pairAudio)
}

// A page that now offers its video as a manifest instead of the recorded direct formats
// fails the run (after one more resolution).
func TestSiteRunFormatChanged(t *testing.T) {
	srv := newFileServer(t)
	page := pairPage(t, srv, "/p")
	res := &fakeResolver{pages: []string{hlsPage(t, srv, "/hls/master.m3u8")}}
	s := newSiteMedia(res)
	s.retryDelay = time.Millisecond
	sel := daemon.SiteSelection{PageURL: srv.url("/watch?v=vid00000001"), Format: "http", VideoTrack: "137",
		AudioTrack: "140", VideoKey: keyOf(t, page, "137"), AudioKey: keyOf(t, page, "140")}
	_, err := s.Run(siteContext(t), runOptions(t, daemon.MediaRunOptions{}), sel, nil, nil, nil)
	if !errors.Is(err, mediajob.ErrInvalidSelection) || !strings.Contains(err.Error(), "offers hls instead of http") {
		t.Fatalf("Run = %v", err)
	}
	if got := res.freshness(); !slices.Equal(got, []bool{false, true}) {
		t.Errorf("resolutions %v", got)
	}
}

// A cached resolution whose chosen addresses expire within 30 minutes is not used: the
// run resolves the page past the cache.
func TestSiteRunFreshWhenExpiring(t *testing.T) {
	srv := newFileServer(t)
	video, audio := testPair(t)
	srv.add("/p/v.mp4", video.file())
	srv.add("/p/a.m4a", audio.file())
	pageAt := func(expire time.Time) string {
		q := "?expire=" + strconv.FormatInt(expire.Unix(), 10)
		return pageJSON(t, videoFormat("137", srv.url("/p/v.mp4"+q), len(video.file())),
			audioFormat("140", srv.url("/p/a.m4a"+q), len(audio.file())))
	}
	soon, later := pageAt(time.Now().Add(10*time.Minute)), pageAt(time.Now().Add(6*time.Hour))
	res := &fakeResolver{pages: []string{soon, later}}
	s := newSiteMedia(res)
	sel := daemon.SiteSelection{PageURL: srv.url("/watch?v=vid00000001"), Format: "http", VideoTrack: "137",
		AudioTrack: "140", VideoKey: keyOf(t, soon, "137"), AudioKey: keyOf(t, soon, "140")}
	if _, err := s.Run(siteContext(t), runOptions(t, daemon.MediaRunOptions{}), sel, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := res.freshness(); !slices.Equal(got, []bool{false, true}) {
		t.Errorf("resolutions %v, want a second one past the cache", got)
	}
}

// One muxed MP4 file (not fragmented) is saved as it is.
func TestSiteRunCopy(t *testing.T) {
	srv := newFileServer(t)
	init := mp4.CreateEmptyInit()
	if err := init.AddEmptyTrack(48000, "audio", "und").SetAACDescriptor(aac.AAClc, 48000); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("downex"), 300_000)
	mdat := append([]byte{0, 0, 0, 0, 'm', 'd', 'a', 't'}, payload...)
	n := len(mdat)
	mdat[0], mdat[1], mdat[2], mdat[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
	file := append(encodeBox(t, init), mdat...)
	srv.add("/m.mp4", file)
	page := pageJSON(t, format{"format_id": "18", "protocol": "http", "url": srv.url("/m.mp4"), "ext": "mp4",
		"vcodec": "avc1.42001E", "acodec": "mp4a.40.2", "height": 360, "filesize": len(file)})
	s := newSiteMedia(&fakeResolver{pages: []string{page}})
	sel := daemon.SiteSelection{PageURL: srv.url("/watch?v=vid00000001"), Format: "http", VideoTrack: "18",
		VideoKey: keyOf(t, page, "18")}
	opts := runOptions(t, daemon.MediaRunOptions{HostRules: testRules{maxBytes: 1 << 20}})
	container, err := s.Run(siteContext(t), opts, sel, nil, nil, nil)
	if err != nil || container != "mp4" {
		t.Fatalf("Run = %q, %v", container, err)
	}
	if got, err := os.ReadFile(opts.OutPath); err != nil || !bytes.Equal(got, file) {
		t.Errorf("the saved file differs from the served one (%v)", err)
	}
}

// A served file that is not an MP4 is refused after its first chunk
// (internal/testserver's content), before anything else is requested.
func TestSiteRunRefusesWhatIsNotMP4(t *testing.T) {
	ts := testserver.New(testserver.Options{Size: 3 << 20, Seed: 9})
	defer ts.Close()
	page := pageJSON(t, format{"format_id": "18", "protocol": "http", "url": ts.URL + "/video.mp4", "ext": "mp4",
		"vcodec": "avc1.42001E", "acodec": "mp4a.40.2", "height": 360, "filesize": 3 << 20})
	s := newSiteMedia(&fakeResolver{pages: []string{page}})
	sel := daemon.SiteSelection{PageURL: ts.URL + "/watch?v=vid00000001", Format: "http", VideoTrack: "18",
		VideoKey: keyOf(t, page, "18")}
	opts := runOptions(t, daemon.MediaRunOptions{HostRules: testRules{maxBytes: 1 << 20}})
	if _, err := s.Run(siteContext(t), opts, sel, nil, nil, nil); !errors.Is(err, media.ErrUnsupported) {
		t.Fatalf("Run = %v, want media_unsupported", err)
	}
	reqs := ts.Requests()
	if len(reqs) == 0 {
		t.Fatal("nothing was requested")
	}
	for _, r := range reqs {
		if r.Range != "bytes=0-1048575" {
			t.Errorf("request beyond the first chunk: Range %q", r.Range)
		}
	}
	if _, err := os.Stat(opts.OutPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an output was written: %v", err)
	}
}

// A volume without room for the tracks and the output refuses the run before anything
// is allocated.
func TestSiteRunChecksFreeSpace(t *testing.T) {
	srv := newFileServer(t)
	page := pairPage(t, srv, "/p")
	s := newSiteMedia(&fakeResolver{pages: []string{page}})
	s.freeSpace = func(string) (int64, error) { return 1 << 20, nil }
	sel := daemon.SiteSelection{PageURL: srv.url("/watch?v=vid00000001"), Format: "http", VideoTrack: "137",
		AudioTrack: "140", VideoKey: keyOf(t, page, "137"), AudioKey: keyOf(t, page, "140")}
	opts := runOptions(t, daemon.MediaRunOptions{})
	if _, err := s.Run(siteContext(t), opts, sel, nil, nil, nil); !errors.Is(err, mediajob.ErrDiskFull) {
		t.Fatalf("Run = %v, want disk full", err)
	}
	if _, err := os.Stat(opts.OutPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an output was written: %v", err)
	}
}

// A pause while the page is being resolved stops the run with the context's error.
func TestSiteRunPausedWhileResolving(t *testing.T) {
	s := newSiteMedia(&fakeResolver{pages: []string{"{}"}, block: true})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := s.Run(ctx, runOptions(t, daemon.MediaRunOptions{}), daemon.SiteSelection{PageURL: "https://www.youtube.com/watch?v=x",
		Format: "http"}, nil, nil, nil)
	if !errors.Is(err, context.Canceled) || errors.Is(err, sitemedia.ErrSiteResolving) {
		t.Errorf("Run = %v, want the context's error", err)
	}
}

// The chunk size is the smaller of the format's own and the host's request cap, lowered
// for a track of known size to about two chunks per connection (mediajob.FitChunk);
// a checkpoint's recorded size wins for its track, and renewals keep the sizes.
func TestRangedTracksChunkSize(t *testing.T) {
	const mib = 1 << 20
	v := videoFormat("137", "https://rr1.googlevideo.com/v", 50*mib)
	v["downloader_options"] = map[string]any{"http_chunk_size": 4 * mib}
	pageText := pageJSON(t, v, audioFormat("140", "https://rr1.googlevideo.com/a", mib))
	info, err := sitemedia.Parse([]byte(pageText), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	off, err := sitemedia.Offers(info, "www.youtube.com")
	if err != nil {
		t.Fatal(err)
	}
	pick, err := sitemedia.Select(info, off, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	page := &sitePage{info: info, off: off, pick: pick}
	sizes := func(rules daemon.MediaRunOptions, resume *model.MediaCheckpoint, maxConns int) []int64 {
		var out []int64
		for _, tr := range rangedTracks(page, rules.HostRules, resume, maxConns) {
			out = append(out, tr.ChunkBytes)
		}
		return out
	}
	// The 50 MiB video over 8 connections: ceil(50 MiB / 16) = 3.125 MiB, below the
	// format's 4 MiB. The audio's size is approximate (unknown): its chunk stays.
	fitted := int64(25 * mib / 8)
	if got := sizes(daemon.MediaRunOptions{}, nil, 8); !slices.Equal(got, []int64{fitted, 16 * mib}) {
		t.Errorf("no host cap, 8 connections: %v", got)
	}
	if got := sizes(daemon.MediaRunOptions{}, nil, 1); !slices.Equal(got, []int64{4 * mib, 16 * mib}) {
		t.Errorf("no host cap, 1 connection: %v", got)
	}
	capped := daemon.MediaRunOptions{HostRules: testRules{maxBytes: 10 * mib}}
	if got := sizes(capped, nil, 8); !slices.Equal(got, []int64{fitted, 10 * mib}) {
		t.Errorf("10 MiB host cap: %v", got)
	}
	// A host of 2 connections: ceil(50 MiB / 4) = 12.5 MiB, so the format's 4 MiB stays.
	twoConns := daemon.MediaRunOptions{HostRules: testRules{conns: 2, maxBytes: 10 * mib}}
	if got := sizes(twoConns, nil, 8); !slices.Equal(got, []int64{4 * mib, 10 * mib}) {
		t.Errorf("a host of 2 connections: %v", got)
	}
	resume := &model.MediaCheckpoint{Tracks: []model.MediaTrackCheckpoint{
		{TrackID: "137", Key: pick.Tracks[0].Key, ChunkBytes: 16 * mib},
		{TrackID: "140", Key: pick.Tracks[1].Key, ChunkBytes: 0},
	}}
	if got := sizes(capped, resume, 8); !slices.Equal(got, []int64{16 * mib, 10 * mib}) {
		t.Errorf("with a checkpoint: %v, want its 16 MiB kept for the video", got)
	}
	if pick.Tracks[0].ChunkBytes != 0 {
		t.Error("rangedTracks changed the pick")
	}

	// A renewal returns the same tracks with their chunk sizes unchanged.
	tracks := rangedTracks(page, capped.HostRules, nil, 8)
	s := newSiteMedia(&fakeResolver{pages: []string{pageText}})
	refresh := s.refresher(daemon.SiteSelection{PageURL: "https://www.youtube.com/watch?v=vid00000001", Format: "http"}, tracks)
	fresh, err := refresh(siteContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != len(tracks) {
		t.Fatalf("renewal returned %d tracks, want %d", len(fresh), len(tracks))
	}
	for i := range fresh {
		if fresh[i].Key != tracks[i].Key || fresh[i].ChunkBytes != tracks[i].ChunkBytes {
			t.Errorf("renewed track %d: key %q chunk %d, want %q and %d", i, fresh[i].Key, fresh[i].ChunkBytes,
				tracks[i].Key, tracks[i].ChunkBytes)
		}
	}
}

// A run passes the download's connection count to the chunk fit: a 6 MiB video over 8
// connections is fetched in 2 MiB chunks, not as one 16 MiB chunk, and the checkpoint
// records that size.
func TestSiteRunFitsChunks(t *testing.T) {
	srv := newFileServer(t)
	video := buildFMP4(t, true, 400, 16000, 16, 3)
	_, audio := testPair(t)
	srv.add("/f/v.mp4", video.file())
	srv.add("/f/a.m4a", audio.file())
	page := pageJSON(t, videoFormat("137", srv.url("/f/v.mp4"), len(video.file())),
		audioFormat("140", srv.url("/f/a.m4a"), len(audio.file())))
	s := newSiteMedia(&fakeResolver{pages: []string{page}})
	sel := daemon.SiteSelection{PageURL: srv.url("/watch?v=vid00000001"), Format: "http", VideoTrack: "137",
		AudioTrack: "140", VideoKey: keyOf(t, page, "137"), AudioKey: keyOf(t, page, "140")}
	opts := runOptions(t, daemon.MediaRunOptions{})
	var chunks []int64
	if _, err := s.Run(siteContext(t), opts, sel, nil, nil, func(c model.MediaCheckpoint) error {
		chunks = append(chunks, c.Tracks[0].ChunkBytes)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	checkOutput(t, opts.OutPath, video, audio)
	if len(chunks) == 0 || slices.ContainsFunc(chunks, func(c int64) bool { return c != 2<<20 }) {
		t.Errorf("video checkpoint chunk sizes %v, want 2 MiB", chunks)
	}
	n := 0
	for _, r := range srv.requests() {
		if r.path == "/f/v.mp4" {
			n++
			if l := rangeLength(r.rng); l < 1 || l > 2<<20 {
				t.Errorf("video request Range %q, want at most 2 MiB", r.rng)
			}
		}
	}
	if want := (len(video.file()) + 2<<20 - 1) / (2 << 20); n != want {
		t.Errorf("%d video requests, want %d chunks", n, want)
	}
}
