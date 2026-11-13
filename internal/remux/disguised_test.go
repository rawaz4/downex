package remux

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	pngcrc "hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"downex/internal/remux/mpegts"
	"downex/internal/testmedia"
)

// Segments disguised as images (docs/VIDEO-DETECT-SPEC.md §4.2): a CDN serves every
// HLS segment as an image, a complete picture followed by the transport stream. The
// conversion, the early check and the legacy join use the stream from its first packet
// (mpegts.FindStart) and never the picture.

// imagePNG is a 1×1 RGBA PNG of 120 bytes (IHDR, sRGB, gAMA, pHYs, IDAT, IEND), the
// prefix of a real stream's segments.
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

// imagePrefixes returns a GIF (whose first byte is the sync byte), the PNG and a JPEG,
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
	return [][]byte{g.Bytes(), imagePNG(), j.Bytes()}
}

// disguise puts the image prefixes before segs in turn.
func disguise(t testing.TB, segs [][]byte) [][]byte {
	t.Helper()
	ps := imagePrefixes(t)
	out := make([][]byte, len(segs))
	for i, s := range segs {
		out[i] = slices.Concat(ps[i%len(ps)], s)
	}
	return out
}

// Converted segments disguised as images give the MP4 of the plain segments, byte for
// byte: one muxed input, and a video input with a separate audio input.
func TestAssembleImagePrefixedTS(t *testing.T) {
	if p := imagePNG(); len(p) != 120 {
		t.Fatalf("the PNG fixture has %d bytes", len(p))
	}
	fx := newTSFixture(testmedia.VideoConfig{Frames: 60, BFrames: true, Seed: 31}, testmedia.AudioConfig{Frames: 94, Seed: 32})
	segs := segments(avMuxer(false, "eng"), fx.units, 0.5)
	if len(segs) < 3 {
		t.Fatalf("%d segments", len(segs))
	}
	plain := mustAssemble(t, []Input{tsInputOf(t, KindMuxed, segs)}, ContainerMP4)
	disguised := mustAssemble(t, []Input{tsInputOf(t, KindMuxed, disguise(t, segs))}, ContainerMP4)
	if a, b := readOut(t, plain), readOut(t, disguised); !bytes.Equal(a, b) {
		t.Errorf("muxed: the MP4 of the disguised segments (%d bytes) differs from the plain one (%d bytes)", len(b), len(a))
	}

	vm := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}
	am := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: aPID, Type: testmedia.TypeAAC}}}
	vsegs := segments(vm, testmedia.VideoPES(vPID, fx.aus), 0.5)
	asegs := segments(am, testmedia.AudioPES(aPID, fx.frames, 5), 0)
	plain = mustAssemble(t, []Input{tsInputOf(t, KindVideo, vsegs), tsInputOf(t, KindAudio, asegs)}, ContainerMP4)
	disguised = mustAssemble(t, []Input{tsInputOf(t, KindVideo, disguise(t, vsegs)), tsInputOf(t, KindAudio, disguise(t, asegs))}, ContainerMP4)
	if a, b := readOut(t, plain), readOut(t, disguised); !bytes.Equal(a, b) {
		t.Errorf("video + audio: the MP4 of the disguised segments (%d bytes) differs from the plain one (%d bytes)", len(b), len(a))
	}
}

func readOut(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// CheckTS reads a disguised first segment from its first packet: a GIF before the PAT
// is not taken for a packet.
func TestCheckTSImagePrefix(t *testing.T) {
	fx := newTSFixture(testmedia.VideoConfig{Frames: 30, Seed: 33}, testmedia.AudioConfig{Frames: 47, Seed: 34})
	first := segments(avMuxer(false, ""), fx.units, 0.5)[0]
	for _, p := range imagePrefixes(t) {
		if err := CheckTS(slices.Concat(p, first), KindMuxed); err != nil {
			t.Errorf("CheckTS behind %d bytes: %v", len(p), err)
		}
	}
}

// The legacy join copies every file from its first packet: the output is the plain
// segments byte for byte. A file without a packet start in its first
// mpegts.MaxPrefix bytes fails as before.
func TestConcatTSImagePrefix(t *testing.T) {
	rng := rand.New(rand.NewSource(35))
	dir := t.TempDir()
	init := tsPackets(rng, 2)
	ps := imagePrefixes(t)
	in := Input{Kind: KindMuxed, Container: ContainerTS, Init: writeFile(t, dir, "init.ts", slices.Concat(ps[0], init))}
	want := slices.Clone(init)
	for i := 0; i < 5; i++ {
		seg := tsPackets(rng, 40+i*9)
		if i == 4 {
			seg = seg[:len(seg)-11] // a segment need not end on a packet boundary
		}
		want = append(want, seg...)
		in.Fragments = append(in.Fragments, writeFile(t, dir, itoa(i)+".png", slices.Concat(ps[i%len(ps)], seg)))
	}
	// A prefix of the largest size.
	last := tsPackets(rng, 30)
	want = append(want, last...)
	in.Fragments = append(in.Fragments, writeFile(t, dir, "last.png", slices.Concat(make([]byte, mpegts.MaxPrefix-1), last)))

	out := filepath.Join(t.TempDir(), "out.dxpart")
	if err := ConcatTS(context.Background(), in, out); err != nil {
		t.Fatal(err)
	}
	if got := readOut(t, out); !bytes.Equal(got, want) {
		t.Fatalf("concatenation: %d bytes, want the %d of the plain segments", len(got), len(want))
	}

	bad := in
	bad.Fragments = append(slices.Clone(in.Fragments), writeFile(t, dir, "bad.png", slices.Concat(make([]byte, mpegts.MaxPrefix), tsPackets(rng, 30))))
	err := ConcatTS(context.Background(), bad, out)
	if !errors.Is(err, ErrMalformed) || !strings.Contains(err.Error(), "fragment 6") || !strings.Contains(err.Error(), "not an MPEG transport stream") {
		t.Fatalf("err = %v, want ErrMalformed in fragment 6", err)
	}
	assertNoOutput(t, out)
}

// A segment that starts as a transport stream is read from its offset 0 even when one of
// its first five packets is damaged (a lost sync byte, a missing byte), as before images
// were skipped: the early check keeps its PAT and PMT, and the legacy join copies it
// whole.
func TestDamagedEarlyPacketIsNotAPrefix(t *testing.T) {
	fx := newTSFixture(testmedia.VideoConfig{Frames: 30, Seed: 36}, testmedia.AudioConfig{Frames: 47, Seed: 37})
	first := segments(avMuxer(false, ""), fx.units, 0.5)[0]
	if len(first) < 10*mpegts.PacketSize {
		t.Fatalf("a first segment of %d bytes", len(first))
	}
	zeroed := slices.Clone(first)
	zeroed[2*mpegts.PacketSize] = 0
	short := slices.Delete(slices.Clone(first), 3*mpegts.PacketSize-1, 3*mpegts.PacketSize)
	dir := t.TempDir()
	for name, seg := range map[string][]byte{"a lost sync byte in packet 3": zeroed, "a byte missing from packet 3": short} {
		if err := CheckTS(seg, KindMuxed); err != nil {
			t.Errorf("%s: CheckTS = %v", name, err)
		}
		in := Input{Kind: KindMuxed, Container: ContainerTS, Fragments: []string{writeFile(t, dir, "seg.ts", seg)}}
		out := filepath.Join(t.TempDir(), "out.dxpart")
		if err := ConcatTS(context.Background(), in, out); err != nil {
			t.Fatalf("%s: ConcatTS = %v", name, err)
		}
		if got := readOut(t, out); !bytes.Equal(got, seg) {
			t.Errorf("%s: the join has %d bytes, want the segment's %d", name, len(got), len(seg))
		}
	}
}
