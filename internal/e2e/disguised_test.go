package e2e

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"downex/internal/testmedia"
)

// Segments disguised as images (docs/VIDEO-DETECT-SPEC.md §4.2): a CDN serves every
// segment of an HLS VOD as image/png, a complete 1×1 PNG of 120 bytes followed by the
// MPEG-TS segment, which players skip. downexd saves such a stream as the MP4 of the
// plain segments, also across a crash.

// pixelPNG is a 1×1 RGBA PNG of 120 bytes (IHDR, sRGB, gAMA, pHYs, IDAT, IEND), as the
// CDN put it before each segment.
func pixelPNG() []byte {
	chunk := func(b []byte, typ string, data []byte) []byte {
		b = binary.BigEndian.AppendUint32(b, uint32(len(data)))
		b = append(append(b, typ...), data...)
		return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	}
	b := []byte("\x89PNG\r\n\x1a\n")
	b = chunk(b, "IHDR", []byte{0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0})
	b = chunk(b, "sRGB", []byte{0})
	b = chunk(b, "gAMA", []byte{0, 0, 0xb1, 0x8f})
	b = chunk(b, "pHYs", []byte{0, 0, 0x0e, 0xc3, 0, 0, 0x0e, 0xc3, 1})
	b = chunk(b, "IDAT", []byte{0x78, 0x9c, 0x63, 0x60, 0x60, 0x60, 0x60, 0x00, 0x00, 0x00, 0x05, 0x00, 0x01})
	return chunk(b, "IEND", nil)
}

// serveImageTS publishes g below dir as a master playlist dir+"master.m3u8" of one
// variant (with its bandwidth, resolution and codecs) whose media playlist
// dir+"video/index.m3u8" lists the segments dir+"video/seg-<n>.<ext>" (n from 1), each
// served as ctype after prefix. It returns the segment paths.
func serveImageTS(s *streamServer, dir string, g *genTS, ext, ctype string, prefix []byte) []string {
	var list []testmedia.Segment
	var paths []string
	for i, seg := range g.segs {
		uri := fmt.Sprintf("seg-%d.%s", i+1, ext)
		list = append(list, testmedia.Segment{URI: uri, Duration: g.durs[i]})
		paths = append(paths, dir+"video/"+uri)
		s.add(dir+"video/"+uri, slices.Concat(prefix, seg), ctype)
	}
	s.add(dir+"video/index.m3u8", []byte(testmedia.MediaPlaylist(list)), hlsMIME)
	s.add(dir+"master.m3u8", []byte(testmedia.MasterPlaylist([]testmedia.Variant{
		{URI: "video/index.m3u8", Bandwidth: 400000, Codecs: "avc1.4d401f,mp4a.40.2", Resolution: fmt.Sprintf("%dx%d", g.width, g.height)},
	}, nil)), hlsMIME)
	return paths
}

// A stream of PNG-disguised segments is saved as an MP4 with every picture and AAC
// frame: killed while running after three durable segments, the restarted daemon
// fetches only the others, and the MP4 is byte for byte the one of the same stream
// served as plain .ts segments.
func TestMediaImageDisguisedSegments(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := newStreamServer(t)
	png := pixelPNG()
	if len(png) != 120 {
		t.Fatalf("the PNG fixture has %d bytes", len(png))
	}
	g := muxedTS(t, 320, 180, 480, 0, 141) // 16 s: 8 segments
	paths := serveImageTS(s, "/cdn/", g, "png", "image/png", png)
	serveImageTS(s, "/plain/", g, "ts", "video/mp2t", nil)
	upTo := func(n int) func(string) bool {
		return func(p string) bool { return strings.HasPrefix(p, "/cdn/video/") && segmentNumber(p) > n }
	}

	s.setGate(upTo(3))
	d := h.startDaemon()
	h.waitReady(d)
	id := h.cli("media", s.url("/cdn/master.m3u8"), "-o", h.out, "--name", "disguised").ok(t).id(t)
	h.waitFragments(id, 3, true)
	d.kill(t)
	s.waitClosed(t)

	s.setGate(nil)
	d = h.startDaemon()
	h.waitReady(d)
	dl := h.waitCompleted(id)
	path := filepath.Join(h.out, "disguised.mp4")
	if dl.GetFilename() != "disguised.mp4" || dl.GetMedia().GetContainer() != "mp4" {
		t.Fatalf("completed as %v", dl)
	}
	checkMP4(t, path, tsTracks(g)...)
	noMediaLeftovers(t, h.out)
	for i, p := range paths {
		if n := s.servedCount(p); n != 1 {
			t.Errorf("%s was downloaded %d times", p, n)
		}
		if n := s.requests(p); i < 3 && n != 1 {
			t.Errorf("%s, durable when downexd was killed, was requested %d times", p, n)
		}
	}

	out2 := filepath.Join(h.root, "plain")
	h.cli("media", s.url("/plain/master.m3u8"), "-o", out2, "--name", "disguised", "--wait").ok(t)
	a, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out2, "disguised.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("the MP4 of the disguised segments (%d bytes) differs from the one of the plain segments (%d bytes)", len(a), len(b))
	}
	h.stop(d)
}
