package mediajob

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/media"
	"downex/internal/testmedia"
)

// Bare media playlists (a player that loads an HLS media playlist without a master):
// ProbeMP4 measures what the playlist does not say, a size from a few segments and the
// picture and codecs from the start of the first, within a few bounded requests.

// png1x1 is a 1×1 RGBA PNG of the shape a CDN puts before every TS segment it serves
// as image/png: signature, IHDR, sRGB, gAMA, pHYs, IDAT and IEND, 120 bytes.
func png1x1() []byte {
	chunk := func(typ string, data []byte) []byte {
		b := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
		b = append(append(b, typ...), data...)
		return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[4:]))
	}
	// zlib (default level) of filter type 0 and one transparent white pixel.
	idat := []byte{0x78, 0x9c, 0x63, 0xf8, 0xff, 0xff, 0x3f, 0x03, 0x00, 0x08, 0xfc, 0x02, 0xfe}
	out := []byte("\x89PNG\r\n\x1a\n")
	out = append(out, chunk("IHDR", []byte{0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0})...)
	out = append(out, chunk("sRGB", []byte{0})...)
	out = append(out, chunk("gAMA", []byte{0, 0, 0xb1, 0x8f})...)
	out = append(out, chunk("pHYs", []byte{0, 0, 0x0e, 0xc3, 0, 0, 0x0e, 0xc3, 1})...)
	out = append(out, chunk("IDAT", idat)...)
	return append(out, chunk("IEND", nil)...)
}

// jpegPrefix is a small baseline JPEG header (SOI, APP0 JFIF, a quantisation table)
// and EOI.
func jpegPrefix() []byte {
	b := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0, 0xff, 0xdb, 0, 67, 0}
	for i := range 64 {
		b = append(b, byte(2+i/4))
	}
	return append(b, 0xff, 0xd9)
}

// gifPrefix is a 1×1 GIF89a. Its first byte, 'G', is the TS sync byte 0x47.
func gifPrefix() []byte {
	return []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")
}

const (
	bareVideoPID = 0x100
	bareAudioPID = 0x101
)

// genTS generates seconds of video (codec, width×height, ticks 90 kHz ticks per frame,
// a sync picture about every second) with AAC LC audio (audioType 0; another stream
// type labels the same frames as that type; -1 = no audio), cut into segments of about
// one second, and their durations.
func genTS(t *testing.T, codec testmedia.Codec, width, height int, ticks int64, seconds, audioType int) ([][]byte, []float64) {
	t.Helper()
	frames := int(int64(seconds) * 90000 / ticks)
	aus := testmedia.Video(testmedia.VideoConfig{Codec: codec, Width: width, Height: height, Frames: frames,
		GOP: int(math.Round(90000 / float64(ticks))), FrameTicks: ticks, Seed: int64(width)})
	vtype := byte(testmedia.TypeH264)
	if codec == testmedia.HEVC {
		vtype = testmedia.TypeHEVC
	}
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: bareVideoPID, Type: vtype}}}
	lists := [][]testmedia.PES{testmedia.VideoPES(bareVideoPID, aus)}
	if audioType >= 0 {
		at := byte(testmedia.TypeAAC)
		if audioType > 0 {
			at = byte(audioType)
		}
		m.Streams = append(m.Streams, testmedia.Stream{PID: bareAudioPID, Type: at})
		af := testmedia.AAC(testmedia.AudioConfig{Frames: seconds*48000/1024 + 1, Seed: int64(height)})
		lists = append(lists, testmedia.AudioPES(bareAudioPID, af, 5))
	}
	units := testmedia.Interleave(lists...)
	segs := m.Segments(units, testmedia.SyncCut(bareVideoPID, 1))
	durs := testmedia.Durations(units, testmedia.SyncCut(bareVideoPID, 1), aus[len(aus)-1].DTS+ticks)
	if len(segs) == 0 || len(segs) != len(durs) {
		t.Fatalf("generated %d segments with %d durations", len(segs), len(durs))
	}
	return segs, durs
}

// serveBare serves segs, each after prefix, as the bare media playlist dir+"index.m3u8"
// of the segments dir+"seg<i>.ts", and returns the bytes of all segments.
func serveBare(s *server, dir string, prefix []byte, segs [][]byte, durs []float64) int64 {
	var list []testmedia.Segment
	var total int64
	for i, seg := range segs {
		data := append(slices.Clip(prefix), seg...)
		s.add(fmt.Sprintf("%sseg%d.ts", dir, i), data, "image/png")
		list = append(list, testmedia.Segment{URI: fmt.Sprintf("seg%d.ts", i), Duration: durs[i]})
		total += int64(len(data))
	}
	s.add(dir+"index.m3u8", []byte(testmedia.MediaPlaylist(list)), hlsType)
	return total
}

func TestEstimateSize(t *testing.T) {
	frag := func(d time.Duration, rng ...int64) media.Fragment {
		f := media.Fragment{URL: "https://cdn.example/s", Duration: d}
		if len(rng) == 2 {
			f.ByteRange = &[2]int64{rng[0], rng[1]}
		}
		return f
	}
	plan := func(fs ...media.Fragment) *media.Plan {
		p := &media.Plan{Container: media.ContainerTS, Fragments: fs}
		for i := range p.Fragments {
			p.Fragments[i].Index = i
			p.Duration += p.Fragments[i].Duration
		}
		return p
	}
	ten := make([]media.Fragment, 10)
	for i := range ten {
		ten[i] = frag(4 * time.Second)
	}
	ten[9].Duration = 2 * time.Second
	initFrag := media.Fragment{URL: "https://cdn.example/init", Init: true, ByteRange: &[2]int64{0, 99}}
	for _, c := range []struct {
		name  string
		plan  *media.Plan
		known map[int]int64
		size  int64
	}{
		{"byte ranges", plan(frag(4*time.Second, 0, 999), frag(4*time.Second, 1000, 2999)), nil, 3000},
		{"sampled: bytes per second times the duration", plan(ten...), map[int]int64{0: 4000, 5: 6000, 8: 5000}, 5000.0 / 4 * 38},
		{"every fragment sampled", plan(ten[:3]...), map[int]int64{0: 10, 1: 20, 2: 30}, 60},
		{"an init range counts", plan(append([]media.Fragment{initFrag}, ten[:2]...)...), map[int]int64{1: 4000}, 100 + 8000},
		{"nothing known", plan(ten...), nil, 0},
		{"only the init known", plan(append([]media.Fragment{initFrag}, ten[:2]...)...), nil, 0},
		{"no durations: bytes per fragment", plan(frag(0), frag(0), frag(0), frag(0)), map[int]int64{0: 100, 2: 300}, 800},
		{"too large", plan(ten...), map[int]int64{0: 1 << 40}, 0},
		{"empty sample", plan(ten...), map[int]int64{0: 0}, 0},
	} {
		if size := estimateSize(c.plan, c.known); size != c.size {
			t.Errorf("%s: estimateSize = %d, want %d", c.name, size, c.size)
		}
	}
}

func TestDescribeTS(t *testing.T) {
	h264, _ := genTS(t, testmedia.H264, 1280, 720, 3000, 1, 0)
	hevc, _ := genTS(t, testmedia.HEVC, 1920, 1080, 3600, 1, 0)
	ac3, _ := genTS(t, testmedia.H264, 854, 480, 3000, 1, 0x81)
	silent, _ := genTS(t, testmedia.H264, 640, 360, 1500, 1, -1)
	ctx := testContext(t)

	if got := describeTS(ctx, h264[0]); got != (streamPicture{Width: 1280, Height: 720, FrameRate: 30, Codecs: "avc1.4D001F,mp4a.40.2"}) {
		t.Errorf("H.264 with AAC: %+v", got)
	}
	got := describeTS(ctx, hevc[0])
	if got.Width != 1920 || got.Height != 1080 || got.FrameRate != 25 || !strings.HasPrefix(got.Codecs, "hvc1.") ||
		!strings.HasSuffix(got.Codecs, ",mp4a.40.2") || !media.TSConvertible(got.Codecs) {
		t.Errorf("HEVC with AAC: %+v", got)
	}
	// Audio the remuxer does not convert: the picture, but no codecs.
	if got := describeTS(ctx, ac3[0]); got != (streamPicture{Width: 854, Height: 480, FrameRate: 30}) {
		t.Errorf("H.264 with AC-3: %+v", got)
	}
	if got := describeTS(ctx, silent[0]); got != (streamPicture{Width: 640, Height: 360, FrameRate: 60, Codecs: "avc1.4D001F"}) {
		t.Errorf("video only: %+v", got)
	}
	// Not a transport stream: nothing.
	for _, b := range [][]byte{tsSegment(9, 400), []byte("<html>no video</html>"), nil} {
		if got := describeTS(ctx, b); got != (streamPicture{}) {
			t.Errorf("%d bytes: %+v", len(b), got)
		}
	}
	// Cut short or damaged: what is found, never a panic.
	damaged := slices.Clone(h264[0])
	for i := 188 * 3; i < len(damaged); i += 97 {
		damaged[i] ^= 0x5a
	}
	for _, b := range [][]byte{h264[0][:188*3], h264[0][:5000], damaged} {
		if got := describeTS(ctx, b); got.Width != 0 && (got.Width != 1280 || got.Height != 720) {
			t.Errorf("%d bytes: %+v", len(b), got)
		}
	}
}

// A bare media playlist whose every segment is a TS behind a 1×1 PNG (served as
// image/png) is listed with its picture, frame rate, codecs, a name and an estimated
// size: three segments sampled (the first read up to 512 KiB, the middle and the last
// but one asked for one byte), each request with the headers its segment fetch would
// carry, nothing else of the media fetched.
func TestProbeMP4DescribesBareMediaPlaylist(t *testing.T) {
	segs, durs := genTS(t, testmedia.H264, 1280, 720, 3000, 12, 0)
	s := newServer(t)
	total := serveBare(s, "/b/", png1x1(), segs, durs)
	sel := Selection{ManifestURL: s.url("/b/index.m3u8"),
		Headers: http.Header{"Cookie": {"a=1"}, "Referer": {"https://page.example/watch"}}}

	info, err := ProbeMP4(testContext(t), testFactory(t), sel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Tracks) != 1 || info.DefaultVideo != "v0" {
		t.Fatalf("tracks = %+v", info.Tracks)
	}
	tr := info.Tracks[0]
	if tr.ID != "v0" || tr.Kind != media.KindMuxed || tr.Width != 1280 || tr.Height != 720 || tr.FrameRate != 30 ||
		tr.Codecs != "avc1.4D001F,mp4a.40.2" || tr.Name != "720p (HD)" || tr.Bandwidth != 0 || tr.Key != "" {
		t.Errorf("track = %+v", tr)
	}
	if !tr.SizeApprox || tr.Size < total*4/5 || tr.Size > total*6/5 {
		t.Errorf("size %d (approx %v), want about %d", tr.Size, tr.SizeApprox, total)
	}
	n := len(segs)
	want := map[int]string{0: "bytes=0-524287", n / 2: "bytes=0-0", n - 2: "bytes=0-0"}
	for i := range segs {
		seen := s.seen(fmt.Sprintf("/b/seg%d.ts", i))
		if w, ok := want[i]; !ok {
			if len(seen.ranges) != 0 {
				t.Errorf("segment %d fetched at probe time: %v", i, seen.ranges)
			}
			continue
		} else if !slices.Equal(seen.ranges, []string{w}) {
			t.Errorf("segment %d: ranges %q, want [%q]", i, seen.ranges, w)
		}
		if !slices.Equal(seen.cookies, []string{"a=1"}) || !slices.Equal(seen.referers, []string{"https://page.example/watch"}) {
			t.Errorf("segment %d: cookies %q, referers %q", i, seen.cookies, seen.referers)
		}
	}
	if n := s.hitCount("/b/index.m3u8"); n != 1 {
		t.Errorf("playlist fetched %d times", n)
	}
}

// Every segment a byte range: the size is the sum of the ranges, without an estimate,
// and only the start of the first is read, for the picture. It is still marked
// approximate: the file saved is an MP4 written anew, not the segments' bytes.
func TestProbeMP4BareByteRanges(t *testing.T) {
	segs, durs := genTS(t, testmedia.H264, 1920, 1080, 3003, 4, 0)
	s := newServer(t)
	var all []byte
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:2\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	first := 0
	for i, seg := range segs {
		data := append(gifPrefix(), seg...)
		if i == 0 {
			first = len(data)
		}
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n#EXT-X-BYTERANGE:%d@%d\nall.ts\n", durs[i], len(data), len(all))
		all = append(all, data...)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	s.add("/r/all.ts", all, "video/mp2t")
	s.add("/r/index.m3u8", []byte(b.String()), hlsType)

	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/r/index.m3u8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr := info.Tracks[0]
	if tr.Size != int64(len(all)) || !tr.SizeApprox || tr.Width != 1920 || tr.Height != 1080 || tr.FrameRate != 29.97 ||
		tr.Name != "1080p (Full HD)" {
		t.Errorf("track = %+v, want about %d bytes", tr, len(all))
	}
	if got := s.seen("/r/all.ts").ranges; !slices.Equal(got, []string{"bytes=0-" + strconv.Itoa(first-1)}) {
		t.Errorf("requests %q", got)
	}
}

// A master playlist's variant without BANDWIDTH is measured too, keeping what its
// attributes declare (its picture declared, only sizes are asked for); a variant with
// BANDWIDTH is not.
func TestProbeMP4DescribesVariantWithoutBandwidth(t *testing.T) {
	segs, durs := genTS(t, testmedia.H264, 1280, 720, 3000, 4, 0)
	s := newServer(t)
	s.add("/m/master.m3u8", []byte(`#EXTM3U
#EXT-X-STREAM-INF:RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2"
nobw/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2"
bw/index.m3u8
`), hlsType)
	serveBare(s, "/m/nobw/", nil, segs, durs)
	serveBare(s, "/m/bw/", nil, segs, durs)

	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/m/master.m3u8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v := info.Tracks[0]; v.ID != "v0" || v.Size == 0 || !v.SizeApprox || v.Codecs != "avc1.64001f,mp4a.40.2" ||
		v.Name != "720p (HD)" || v.Width != 1280 || v.FrameRate != 0 {
		t.Errorf("variant without BANDWIDTH: %+v", v)
	}
	// Its picture is declared: no segment is read beyond its first byte.
	if got := s.seen("/m/nobw/seg0.ts").ranges; !slices.Equal(got, []string{"bytes=0-0"}) {
		t.Errorf("first segment of the variant without BANDWIDTH: %q", got)
	}
	if v := info.Tracks[1]; v.Size != 0 || v.FrameRate != 0 {
		t.Errorf("variant with BANDWIDTH: %+v", v)
	}
	for i := range segs {
		if n := s.hitCount(fmt.Sprintf("/m/bw/seg%d.ts", i)); n != 0 {
			t.Errorf("segment %d of the variant with BANDWIDTH fetched %d times", i, n)
		}
	}
}

// A variant that declares its picture but not its codecs is measured from its first
// segment's start: the frame rate found there joins the standard name its picture gave
// ("720p (HD)" becomes "720p (HD) 60fps").
func TestProbeMP4DescribeRenames(t *testing.T) {
	segs, durs := genTS(t, testmedia.H264, 1280, 720, 1500, 4, 0)
	s := newServer(t)
	s.add("/n/master.m3u8", []byte("#EXTM3U\n#EXT-X-STREAM-INF:RESOLUTION=1280x720\nv/index.m3u8\n"), hlsType)
	serveBare(s, "/n/v/", nil, segs, durs)
	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/n/master.m3u8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v := info.Tracks[0]; v.FrameRate != 60 || v.Name != "720p (HD) 60fps" || v.Codecs == "" {
		t.Errorf("track = %+v", v)
	}
}

// What goes wrong while a track is measured only drops what could not be measured:
// segments that are not transport streams keep the size, missing ones drop it, a
// server without lengths is asked once more with HEAD (at most 4 requests per track),
// and slow segments give up within the probe's deadline.
func TestProbeMP4DescribeFailures(t *testing.T) {
	t.Run("not a transport stream", func(t *testing.T) {
		s := newServer(t)
		page := []byte(strings.Repeat("<html>not a video</html>", 40))
		serveBare(s, "/h/", page, make([][]byte, 6), []float64{4, 4, 4, 4, 4, 4})
		info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/h/index.m3u8")}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tr := info.Tracks[0]; tr.Size != int64(6*len(page)) || !tr.SizeApprox || tr.Width != 0 || tr.Codecs != "" || tr.Name != "" {
			t.Errorf("track = %+v", tr)
		}
	})
	t.Run("missing segments", func(t *testing.T) {
		s := newServer(t)
		s.add("/x/index.m3u8", []byte(mediaPlaylist("", []string{"a.ts", "b.ts", "c.ts", "d.ts", "e.ts"})), hlsType)
		info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/x/index.m3u8")}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tr := info.Tracks[0]; tr.Size != 0 || tr.SizeApprox || tr.Width != 0 || tr.Name != "" {
			t.Errorf("track = %+v", tr)
		}
		hits := 0
		for _, n := range []string{"a", "b", "c", "d", "e"} {
			hits += s.hitCount("/x/" + n + ".ts")
		}
		if hits > 4 {
			t.Errorf("%d requests for one track, want at most 4", hits)
		}
	})
	t.Run("no lengths", func(t *testing.T) {
		segs, durs := genTS(t, testmedia.H264, 640, 360, 3000, 8, 0)
		var mu sync.Mutex
		methods := map[string][]string{}
		list := make([]testmedia.Segment, len(segs))
		for i := range segs {
			list[i] = testmedia.Segment{URI: fmt.Sprintf("s%d.ts", i), Duration: durs[i]}
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/index.m3u8" {
				w.Header().Set("Content-Type", hlsType)
				_, _ = w.Write([]byte(testmedia.MediaPlaylist(list)))
				return
			}
			var i int
			if _, err := fmt.Sscanf(r.URL.Path, "/s%d.ts", &i); err != nil || i >= len(segs) {
				http.NotFound(w, r)
				return
			}
			mu.Lock()
			methods[r.URL.Path] = append(methods[r.URL.Path], r.Method)
			mu.Unlock()
			if r.Method == http.MethodHead {
				w.Header().Set("Content-Length", strconv.Itoa(len(segs[i])))
				return
			}
			// The whole segment, ranges ignored, of unknown length.
			w.(http.Flusher).Flush()
			_, _ = w.Write(segs[i])
		}))
		t.Cleanup(srv.Close)
		info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: srv.URL + "/index.m3u8"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var all int64
		for _, seg := range segs {
			all += int64(len(seg))
		}
		tr := info.Tracks[0]
		if tr.Width != 640 || tr.Height != 360 || !tr.SizeApprox || tr.Size < all*4/5 || tr.Size > all*6/5 {
			t.Errorf("track = %+v, want about %d bytes", tr, all)
		}
		mu.Lock()
		defer mu.Unlock()
		requests, heads := 0, 0
		for _, ms := range methods {
			requests += len(ms)
			for _, m := range ms {
				if m == http.MethodHead {
					heads++
				}
			}
		}
		if requests > 4 || heads != 1 {
			t.Errorf("requests %v: %d in all, %d HEAD; want at most 4 with one HEAD", methods, requests, heads)
		}
	})
	t.Run("slow segments", func(t *testing.T) {
		segs, durs := genTS(t, testmedia.H264, 640, 360, 3000, 4, 0)
		s := newServer(t)
		serveBare(s, "/s/", nil, segs, durs)
		s.setHook(func(r *http.Request) {
			if strings.HasSuffix(r.URL.Path, ".ts") {
				<-r.Context().Done()
			}
		})
		ctx, cancel := context.WithTimeout(testContext(t), 3*time.Second)
		defer cancel()
		start := time.Now()
		info, err := ProbeMP4(ctx, testFactory(t), Selection{ManifestURL: s.url("/s/index.m3u8")}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d > 2500*time.Millisecond {
			t.Errorf("the probe took %v of its 3 s", d)
		}
		if tr := info.Tracks[0]; tr.Size != 0 || tr.Width != 0 {
			t.Errorf("track = %+v", tr)
		}
	})
}

// A bare media playlist of fragmented MP4 gets a size from one byte of each sampled
// segment, plus its initialization section, which is read (up to 512 KiB) for the
// picture and codecs the playlist does not declare. This one has no video track (an
// 'mvhd' and a 'udta' only): nothing is described, and the first segment's 'moof' is not
// read either.
func TestProbeMP4DescribesBareFMP4(t *testing.T) {
	s := newServer(t)
	init, segs := addFMP4Playlist(s, "/f/", 6, 1)
	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/f/index.m3u8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	size := func(i int) int64 { return int64(32 + 500 + 97*i) } // moof + mdat of addFMP4Playlist's segment i
	if n := int64(len(segs)); n != size(0)+size(1)+size(2)+size(3)+size(4)+size(5) {
		t.Fatalf("segments of %d bytes", n)
	}
	// The first, middle and last but one sampled: 4 s each, so twice their bytes.
	if tr := info.Tracks[0]; tr.Size != int64(len(init))+2*(size(0)+size(3)+size(4)) || !tr.SizeApprox || tr.Width != 0 ||
		tr.Codecs != "" || tr.Name != "" {
		t.Errorf("track = %+v", tr)
	}
	for i := range 6 {
		want := []string(nil)
		if i == 0 || i == 3 || i == 4 {
			want = []string{"bytes=0-0"}
		}
		if got := s.seen(fmt.Sprintf("/f/s%d.m4s", i)).ranges; !slices.Equal(got, want) {
			t.Errorf("segment %d: %q, want %q", i, got, want)
		}
	}
	if got := s.seen("/f/init.mp4").ranges; !slices.Equal(got, []string{"bytes=0-524287"}) {
		t.Errorf("init requests %q", got)
	}
}

// scramble marks every packet of pid in seg as scrambled (transport_scrambling_control
// 10), as a stream encrypted without HLS's own encryption is.
func scramble(seg []byte, pid uint16) []byte {
	out := slices.Clone(seg)
	for i := 0; i+188 <= len(out); i += 188 {
		if uint16(out[i+1]&0x1f)<<8|uint16(out[i+2]) == pid {
			out[i+3] = out[i+3]&0x3f | 0x80
		}
	}
	return out
}

// A track is measured one request at a time, on one connection, so that a host
// allowed a single connection (a host rule) gets no more at probe time.
func TestProbeMP4DescribesOneRequestAtATime(t *testing.T) {
	segs, durs := genTS(t, testmedia.H264, 640, 360, 3000, 8, 0)
	s := newServer(t)
	serveBare(s, "/o/", nil, segs, durs)
	s.setDelay(30 * time.Millisecond)
	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/o/index.m3u8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tr := info.Tracks[0]; tr.Size == 0 || tr.Width != 640 {
		t.Errorf("track = %+v", tr)
	}
	s.mu.Lock()
	peak := s.maxActive
	s.mu.Unlock()
	if peak != 1 {
		t.Errorf("%d requests at once, want one at a time", peak)
	}
}

// Nothing more is asked of a stream once its start shows that Run would refuse it
// (audio the remuxer does not convert, scrambled packets): only its first segment is
// read, as Run's early check reads only that one before refusing.
func TestProbeMP4DescribeStopsAtARefusedStart(t *testing.T) {
	ac3, ac3Durs := genTS(t, testmedia.H264, 854, 480, 3000, 6, 0x81)
	plain, durs := genTS(t, testmedia.H264, 640, 360, 3000, 6, 0)
	scrambled := make([][]byte, len(plain))
	for i, seg := range plain {
		scrambled[i] = scramble(seg, bareVideoPID)
	}
	for name, c := range map[string]struct {
		segs [][]byte
		durs []float64
	}{"AC-3 audio": {ac3, ac3Durs}, "scrambled video": {scrambled, durs}} {
		s := newServer(t)
		serveBare(s, "/r/", png1x1(), c.segs, c.durs)
		info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/r/index.m3u8")}, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if tr := info.Tracks[0]; tr.Codecs != "" {
			t.Errorf("%s: track = %+v, want no codecs", name, tr)
		}
		for i := range c.segs {
			var want []string
			if i == 0 {
				want = []string{"bytes=0-524287"}
			}
			if got := s.seen(fmt.Sprintf("/r/seg%d.ts", i)).ranges; !slices.Equal(got, want) {
				t.Errorf("%s: segment %d: requests %q, want %q", name, i, got, want)
			}
		}
	}
}

// A segment request the server refuses is not followed by a HEAD (the other samples are
// still asked for), and a server that throttles the probe (429, 503) is asked nothing
// more: its Retry-After is not waited for at probe time.
func TestProbeMP4DescribeRefusedRequests(t *testing.T) {
	segs, durs := genTS(t, testmedia.H264, 640, 360, 3000, 8, 0)
	for _, c := range []struct {
		status     int
		retryAfter string
		gets       int
	}{
		{http.StatusNotFound, "", 3},
		{http.StatusForbidden, "", 3},
		{http.StatusTooManyRequests, "120", 1},
		{http.StatusServiceUnavailable, "120", 1},
	} {
		s := newServer(t)
		serveBare(s, "/q/", nil, segs, durs)
		for i := range segs {
			s.failNext(fmt.Sprintf("/q/seg%d.ts", i), 100, c.status, c.retryAfter)
		}
		var mu sync.Mutex
		var methods []string
		s.setHook(func(r *http.Request) {
			if strings.HasSuffix(r.URL.Path, ".ts") {
				mu.Lock()
				methods = append(methods, r.Method)
				mu.Unlock()
			}
		})
		info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/q/index.m3u8")}, nil)
		if err != nil {
			t.Fatalf("HTTP %d: %v", c.status, err)
		}
		if tr := info.Tracks[0]; tr.Size != 0 || tr.Width != 0 {
			t.Errorf("HTTP %d: track = %+v", c.status, tr)
		}
		mu.Lock()
		got := slices.Clone(methods)
		mu.Unlock()
		if want := slices.Repeat([]string{http.MethodGet}, c.gets); !slices.Equal(got, want) {
			t.Errorf("HTTP %d: requests %v, want %v", c.status, got, want)
		}
	}
}

// One probe measures at most describeMaxTracks tracks, the default video first, so that
// a master playlist of many variants without BANDWIDTH costs a bounded number of
// requests.
func TestProbeMP4DescribesBoundedTracks(t *testing.T) {
	segs, durs := genTS(t, testmedia.H264, 640, 360, 3000, 4, 0)
	s := newServer(t)
	const n = 10
	var m strings.Builder
	m.WriteString("#EXTM3U\n")
	for i := range n {
		fmt.Fprintf(&m, "#EXT-X-STREAM-INF:RESOLUTION=640x360,CODECS=\"avc1.4d401e,mp4a.40.2\"\nv%d/index.m3u8\n", i)
		serveBare(s, fmt.Sprintf("/many/v%d/", i), nil, segs, durs)
	}
	s.add("/many/master.m3u8", []byte(m.String()), hlsType)
	info, err := ProbeMP4(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/many/master.m3u8"), VideoTrack: "v7"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	measured := 0
	for _, tr := range info.Tracks {
		if tr.Size > 0 {
			measured++
		} else if tr.ID == "v7" {
			t.Errorf("the default video was not measured: %+v", tr)
		}
	}
	if len(info.Tracks) != n || measured != describeMaxTracks {
		t.Errorf("%d of %d tracks measured, want %d", measured, len(info.Tracks), describeMaxTracks)
	}
	requests := 0
	for i := range n {
		for j := range segs {
			requests += s.hitCount(fmt.Sprintf("/many/v%d/seg%d.ts", i, j))
		}
	}
	if requests > describeMaxTracks*describeRequests {
		t.Errorf("%d segment requests, want at most %d", requests, describeMaxTracks*describeRequests)
	}
}

// ProbeMP4Playlists, a look-ahead's probe, fetches the playlists only: nothing of the
// media is requested, and the tracks are listed as the playlists describe them.
func TestProbeMP4PlaylistsMeasuresNothing(t *testing.T) {
	segs, durs := genTS(t, testmedia.H264, 640, 360, 3000, 4, 0)
	s := newServer(t)
	serveBare(s, "/p/", nil, segs, durs)
	info, err := ProbeMP4Playlists(testContext(t), testFactory(t), Selection{ManifestURL: s.url("/p/index.m3u8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tr := info.Tracks[0]; len(info.Tracks) != 1 || tr.Size != 0 || tr.Width != 0 || tr.Name != "" {
		t.Errorf("tracks = %+v", info.Tracks)
	}
	for i := range segs {
		if n := s.hitCount(fmt.Sprintf("/p/seg%d.ts", i)); n != 0 {
			t.Errorf("segment %d fetched %d times", i, n)
		}
	}
}
