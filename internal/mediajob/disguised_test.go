package mediajob

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	pngcrc "hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/media"
	"downex/internal/remux"
	"downex/internal/remux/mpegts"
	"downex/internal/testmedia"
)

// Segments disguised as images (docs/VIDEO-DETECT-SPEC.md §4.2, §4.8): a CDN serves
// every HLS segment as image/png, a complete 1×1 PNG of 120 bytes followed by the
// transport stream, which players skip. Run identifies, checks and assembles such a
// track from each fragment's first packet (mpegts.FindStart); the fragment files keep
// what was received, and every reader finds the packets again.

// imagePNG is a 1×1 RGBA PNG of 120 bytes (IHDR, sRGB, gAMA, pHYs, IDAT, IEND).
func imagePNG() []byte {
	chunk := func(b []byte, typ string, data []byte) []byte {
		b = binary.BigEndian.AppendUint32(b, uint32(len(data)))
		b = append(append(b, typ...), data...)
		return binary.BigEndian.AppendUint32(b, pngcrc.ChecksumIEEE(append([]byte(typ), data...)))
	}
	b := []byte("\x89PNG\r\n\x1a\n")
	b = chunk(b, "IHDR", []byte{0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0})
	b = chunk(b, "sRGB", []byte{0})
	b = chunk(b, "gAMA", []byte{0, 0, 0xb1, 0x8f})
	b = chunk(b, "pHYs", []byte{0, 0, 0x0e, 0xc3, 0, 0, 0x0e, 0xc3, 1})
	b = chunk(b, "IDAT", []byte{0x78, 0x9c, 0x63, 0x60, 0x60, 0x60, 0x60, 0x00, 0x00, 0x00, 0x05, 0x00, 0x01})
	return chunk(b, "IEND", nil)
}

// imagePrefixes returns the PNG, a GIF (whose first byte is the sync byte) and a JPEG,
// each of one pixel.
func imagePrefixes(t testing.TB) [][]byte {
	t.Helper()
	px := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Transparent, color.White})
	var g, j bytes.Buffer
	if err := gif.Encode(&g, px, nil); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&j, px, nil); err != nil {
		t.Fatal(err)
	}
	return [][]byte{imagePNG(), g.Bytes(), j.Bytes()}
}

// serveDisguised serves segs under dir as seg<i><ext> of type ctype, each behind the
// image prefixes in turn, with a media playlist dir+"index.m3u8" (mapURI: its
// EXT-X-MAP), and returns the names.
func serveDisguised(t testing.TB, s *server, dir, ext, ctype, mapURI string, segs [][]byte) []string {
	t.Helper()
	ps := imagePrefixes(t)
	names := make([]string, len(segs))
	for i, seg := range segs {
		names[i] = fmt.Sprintf("seg%d%s", i, ext)
		s.add(dir+names[i], slices.Concat(ps[i%len(ps)], seg), ctype)
	}
	s.add(dir+"index.m3u8", []byte(mediaPlaylist(mapURI, names)), hlsType)
	return names
}

// An HLS track of PNG/GIF/JPEG-disguised segments is identified as a transport stream
// from its first segment, checked from that segment's first packet, and handed to the
// assembler; a job paused midway resumes without fetching its durable segments again
// and checks the same bytes.
func TestRunImagePrefixedSegments(t *testing.T) {
	if p := imagePNG(); len(p) != 120 {
		t.Fatalf("the PNG fixture has %d bytes", len(p))
	}
	s := newServer(t)
	var segs [][]byte
	for i := range 5 {
		segs = append(segs, tsSegment(uint64(60+i), 3+i%4))
	}
	serveDisguised(t, s, "/p/", ".png", "image/png", "", segs)
	asm := &fakeAssembler{}
	opts := testOptions(t, asm)
	var check tsChecker
	opts.CheckTS = check.check
	c, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/p/index.m3u8")}, nil, nil, nil)
	if err != nil || c != "mp4" {
		t.Fatalf("Run = %q, %v", c, err)
	}
	if prefixes, kinds := check.calls(); len(prefixes) != 1 || !bytes.Equal(prefixes[0], segs[0]) || kinds[0] != remux.KindMuxed {
		t.Errorf("CheckTS was given %d prefixes (the first of %d bytes), want the first segment from its first packet", len(prefixes), len(prefixes[0]))
	}
	ps := imagePrefixes(t)
	if in := asm.inputs[0]; in.Container != "ts" || in.Init != "" || len(in.Fragments) != 5 {
		t.Fatalf("input = %+v", in)
	} else if got := readFile(t, in.Fragments[1]); !bytes.Equal(got, slices.Concat(ps[1], segs[1])) {
		t.Errorf("fragment 1 holds %d bytes, not the %d received", len(got), len(ps[1])+len(segs[1]))
	}
	if h := segHits(s, "/p/", ".png", 5); slices.Max(h) != 1 || slices.Min(h) != 1 {
		t.Errorf("segment hits %v", h)
	}

	// Paused at segment 2, resumed.
	serveDisguised(t, s, "/r/", ".png", "image/png", "", segs)
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	var once sync.Once
	s.setHook(func(r *http.Request) {
		if r.URL.Path == "/r/seg2.png" {
			once.Do(func() {
				cancel()
				select {
				case <-r.Context().Done():
				case <-time.After(10 * time.Second):
				}
			})
		}
	})
	opts = testOptions(t, asm)
	opts.MaxConnections = 1
	check = tsChecker{}
	opts.CheckTS = check.check
	var rec recorder
	if _, err := Run(ctx, opts, Selection{ManifestURL: s.url("/r/index.m3u8")}, nil, nil, rec.onCheckpoint); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	s.setHook(nil)
	if c, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/r/index.m3u8")}, rec.last(), nil, nil); err != nil || c != "mp4" {
		t.Fatalf("resumed Run = %q, %v", c, err)
	}
	if prefixes, _ := check.calls(); len(prefixes) != 2 || !bytes.Equal(prefixes[0], segs[0]) || !bytes.Equal(prefixes[1], segs[0]) {
		t.Errorf("CheckTS calls: %d", len(prefixes))
	}
	if h := segHits(s, "/r/", ".png", 5); h[0] != 1 || h[1] != 1 {
		t.Errorf("segment hits %v: the resumed job fetched durable segments again", h)
	}
}

// A track that the playlist names a transport stream (a .ts map and .ts segments) is
// checked from the first packet of its map and of its first segment.
func TestRunImagePrefixedMapChecked(t *testing.T) {
	s := newServer(t)
	tables := tsSegment(70, 2)
	s.add("/m/map.ts", slices.Concat(imagePrefixes(t)[1], tables), "video/mp2t")
	segs := [][]byte{tsSegment(71, 4), tsSegment(72, 3), tsSegment(73, 5)}
	serveDisguised(t, s, "/m/", ".ts", "video/mp2t", "map.ts", segs)
	opts := testOptions(t, &fakeAssembler{})
	var check tsChecker
	opts.CheckTS = check.check
	if c, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/m/index.m3u8")}, nil, nil, nil); err != nil || c != "mp4" {
		t.Fatalf("Run = %q, %v", c, err)
	}
	if prefixes, _ := check.calls(); len(prefixes) != 1 || !bytes.Equal(prefixes[0], slices.Concat(tables, segs[0])) {
		t.Errorf("CheckTS was given %d prefixes, not the map and the first segment from their first packets", len(prefixes))
	}
}

// muxedSegments is a converted H.264 + AAC stream cut into segments of about 1 s.
func muxedSegments(seed int64) [][]byte {
	aus := testmedia.Video(testmedia.VideoConfig{Frames: 96, Seed: seed})
	frames := testmedia.AAC(testmedia.AudioConfig{Frames: 150, Seed: seed + 1})
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: 0x100, Type: testmedia.TypeH264}, {PID: 0x101, Type: testmedia.TypeAAC}}}
	units := testmedia.Interleave(testmedia.VideoPES(0x100, aus), testmedia.AudioPES(0x101, frames, 5))
	return m.Segments(units, testmedia.SyncCut(0x100, 1))
}

// With the real remuxer, disguised segments are saved as the MP4 of the plain ones,
// byte for byte, and a legacy job joins them into the plain segments' .ts.
func TestRunImagePrefixedConversion(t *testing.T) {
	s := newServer(t)
	segs := muxedSegments(75)
	if len(segs) < 3 {
		t.Fatalf("%d segments", len(segs))
	}
	var names []string
	for i, seg := range segs {
		names = append(names, fmt.Sprintf("seg%d.ts", i))
		s.add("/plain/"+names[i], seg, "video/mp2t")
	}
	s.add("/plain/index.m3u8", []byte(mediaPlaylist("", names)), hlsType)
	serveDisguised(t, s, "/img/", ".png", "image/png", "", segs)

	save := func(dir string) []byte {
		opts := testOptions(t, nil)
		opts.CheckTS = remux.CheckTS
		opts.MP4Only = true
		c, err := Run(testContext(t), opts, Selection{ManifestURL: s.url(dir + "index.m3u8")}, nil, nil, nil)
		if err != nil || c != "mp4" {
			t.Fatalf("%s: Run = %q, %v", dir, c, err)
		}
		return readFile(t, opts.OutPath)
	}
	if plain, disguised := save("/plain/"), save("/img/"); !bytes.Equal(plain, disguised) {
		t.Errorf("the MP4 of the disguised segments (%d bytes) differs from the plain one (%d bytes)", len(disguised), len(plain))
	}

	opts := testOptions(t, nil)
	opts.LegacyTS = true
	if c, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/img/index.m3u8")}, nil, nil, nil); err != nil || c != "ts" {
		t.Fatalf("legacy Run = %q, %v", c, err)
	}
	if got := readFile(t, opts.OutPath); !bytes.Equal(got, bytes.Join(segs, nil)) {
		t.Errorf("the legacy join has %d bytes, not the %d of the plain segments", len(got), len(bytes.Join(segs, nil)))
	}
}

// A first segment without a packet start in its first mpegts.MaxPrefix bytes, or
// fragmented MP4 behind an image, is refused as before: neither MPEG-TS nor fragmented
// MP4, after that segment only.
func TestRunImagePrefixRefusals(t *testing.T) {
	for name, first := range map[string][]byte{
		"prefix too long": slices.Concat(make([]byte, mpegts.MaxPrefix), tsSegment(80, 6)),
		"fmp4 behind png": slices.Concat(imagePNG(), mediaSegment(81, 900)),
	} {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)
			serveDisguised(t, s, "/x/", ".png", "image/png", "", [][]byte{nil, tsSegment(82, 3), tsSegment(83, 3)})
			s.add("/x/seg0.png", first, "image/png") // in place of the first one
			asm := &fakeAssembler{}
			opts := testOptions(t, asm)
			var check tsChecker
			opts.CheckTS = check.check
			_, err := Run(testContext(t), opts, Selection{ManifestURL: s.url("/x/index.m3u8")}, nil, nil, nil)
			if !errors.Is(err, media.ErrUnsupported) || !strings.Contains(err.Error(), "neither MPEG-TS nor fragmented MP4") {
				t.Fatalf("err = %v", err)
			}
			if h := segHits(s, "/x/", ".png", 3); !slices.Equal(h, []int{1, 0, 0}) {
				t.Errorf("segment hits %v, want only the first", h)
			}
			if p, _ := check.calls(); len(p) != 0 || asm.callCount() != 0 {
				t.Errorf("CheckTS called %d times, assembled %d times", len(p), asm.callCount())
			}
		})
	}
}
