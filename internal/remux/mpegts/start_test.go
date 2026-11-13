package mpegts

import (
	"bytes"
	"encoding/binary"
	pngcrc "hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"slices"
	"testing"
)

// Segments disguised as images (docs/VIDEO-DETECT-SPEC.md §4.2): some CDNs serve each
// HLS segment as an image, a complete picture followed by the transport stream, which
// players skip. These are generated, real-shaped prefixes.

// pngChunk appends a PNG chunk (length, type, data, CRC-32) to b.
func pngChunk(b []byte, typ string, data []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(data)))
	b = append(b, typ...)
	b = append(b, data...)
	return binary.BigEndian.AppendUint32(b, pngcrc.ChecksumIEEE(append([]byte(typ), data...)))
}

// pngPixel is a 1×1 RGBA PNG of 120 bytes (IHDR, sRGB, gAMA, pHYs, IDAT, IEND), as a
// CDN put before every segment of a real stream; text adds a tEXt chunk after IHDR.
func pngPixel(text []byte) []byte {
	b := []byte("\x89PNG\r\n\x1a\n")
	b = pngChunk(b, "IHDR", []byte{0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0})
	if text != nil {
		b = pngChunk(b, "tEXt", append([]byte("Comment\x00"), text...))
	}
	b = pngChunk(b, "sRGB", []byte{0})
	b = pngChunk(b, "gAMA", []byte{0, 0, 0xb1, 0x8f})
	b = pngChunk(b, "pHYs", []byte{0, 0, 0x0e, 0xc3, 0, 0, 0x0e, 0xc3, 1})
	b = pngChunk(b, "IDAT", []byte{0x78, 0x9c, 0x63, 0x60, 0x60, 0x60, 0x60, 0x00, 0x00, 0x00, 0x05, 0x00, 0x01})
	return pngChunk(b, "IEND", nil)
}

// pngTrap is a PNG whose 'G' at offset 3 ("\x89PNG") is followed by sync bytes at +188
// and +376 inside its text: three packets apart, but not five.
func pngTrap() []byte {
	const textAt = 8 + 25 + 8 + len("Comment\x00") // signature, IHDR, tEXt header
	text := bytes.Repeat([]byte("a"), 700)
	text[3+PacketSize-textAt] = SyncByte
	text[3+2*PacketSize-textAt] = SyncByte
	return pngPixel(text)
}

func onePixel() image.Image {
	img := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Transparent, color.White})
	return img
}

// gifPixel is a 1×1 GIF: it starts with 'G', the sync byte.
func gifPixel(t testing.TB) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := gif.Encode(&b, onePixel(), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// jpegPixel is a 1×1 baseline JPEG.
func jpegPixel(t testing.TB) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, onePixel(), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// packets returns n packets of filler, each with the sync byte and no other 0x47.
func packets(n int) []byte {
	b := bytes.Repeat([]byte{0x1f}, n*PacketSize)
	for i := 0; i < len(b); i += PacketSize {
		b[i] = SyncByte
	}
	return b
}

func TestFindStart(t *testing.T) {
	png := pngPixel(nil)
	if len(png) != 120 || png[3] != SyncByte {
		t.Fatalf("the PNG fixture has %d bytes, byte 3 is 0x%02x", len(png), png[3])
	}
	gifp, jpg, trap := gifPixel(t), jpegPixel(t), pngTrap()
	damaged := packets(10)
	damaged[2*PacketSize] = 0x00
	short := slices.Delete(packets(10), 3*PacketSize-1, 3*PacketSize) // a byte missing from the third packet
	// A GIF with sync bytes at 0 ('G') and 188, then the packets.
	gifTrap := append([]byte("GIF89a"), make([]byte, 2*PacketSize)...)
	gifTrap[PacketSize] = SyncByte
	for _, c := range []struct {
		name string
		b    []byte
		want int // -1 = none
	}{
		{"empty", nil, -1},
		{"one packet", packets(1), 0},
		{"less than a packet", packets(1)[:PacketSize-1], -1},
		{"plain", packets(10), 0},
		{"png", slices.Concat(png, packets(10)), len(png)},
		{"gif", slices.Concat(gifp, packets(10)), len(gifp)},
		{"jpeg", slices.Concat(jpg, packets(10)), len(jpg)},
		{"three packets apart are not enough", slices.Concat(trap, packets(10)), len(trap)},
		{"short fragment: the packets it has", slices.Concat(png, packets(2)), len(png)},
		{"one packet and a partial one", slices.Concat(png, packets(2)[:PacketSize+50]), len(png)},
		{"only part of a packet after the prefix", slices.Concat(png, packets(1)[:PacketSize-1]), -1},
		{"prefix of the largest size", slices.Concat(make([]byte, MaxPrefix-1), packets(10)), MaxPrefix - 1},
		{"prefix too large", slices.Concat(make([]byte, MaxPrefix), packets(10)), -1},
		// A file that starts as a transport stream does (sync bytes at 0 and 188, as
		// media.Sniff has it) is read from 0: a damaged packet among its first five is
		// passed by the demuxer's resynchronisation, not taken for the end of a prefix.
		{"damaged third packet", damaged, 0},
		{"a byte missing from the third packet", short, 0},
		{"a GIF is a prefix even with a sync byte at 188", slices.Concat(gifTrap, packets(10)), len(gifTrap)},
		{"error page", bytes.Repeat([]byte("<html>Gone G</html>"), 200), -1},
	} {
		k, ok := FindStart(c.b)
		if c.want < 0 && ok || c.want >= 0 && (!ok || k != c.want) {
			t.Errorf("%s: FindStart = %d, %v; want %d", c.name, k, ok, c.want)
		}
	}
	// StartBytes decides every offset below MaxPrefix: more bytes change nothing.
	b := slices.Concat(make([]byte, MaxPrefix-1), packets(400))
	if k, ok := FindStart(b[:StartBytes]); !ok || k != MaxPrefix-1 {
		t.Errorf("StartBytes: FindStart = %d, %v", k, ok)
	}
}

// The demuxer starts every file at the packet FindStart finds: the units of segments
// disguised as images are those of the plain segments, and the prefixes count as
// skipped bytes. A GIF (whose first byte is the sync byte) and a PNG with sync bytes
// three packets apart are not taken for packets.
func TestDemuxerSkipsImagePrefix(t *testing.T) {
	fx := newFixture(t, 0)
	want, err := readAll(t, NewBytesDemuxer(fx.segs, keepAll))
	if err != nil {
		t.Fatal(err)
	}
	prefixes := [][]byte{gifPixel(t), pngPixel(nil), jpegPixel(t), pngTrap(), nil}
	var files [][]byte
	var skipped int64
	for i, seg := range fx.segs {
		p := prefixes[i%len(prefixes)]
		files = append(files, slices.Concat(p, seg))
		skipped += int64(len(p))
	}
	for name, d := range map[string]*Demuxer{
		"bytes": NewBytesDemuxer(files, keepAll),
		"files": NewDemuxer(writeFiles(t, files), keepAll),
	} {
		got, err := readAll(t, d)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.EqualFunc(got, want, func(a, b Unit) bool {
			return a.PID == b.PID && a.PTS == b.PTS && a.DTS == b.DTS && a.File == b.File && a.AfterLoss == b.AfterLoss && bytes.Equal(a.Data, b.Data)
		}) {
			t.Errorf("%s: %d units differ from the %d of the plain segments", name, len(got), len(want))
		}
		if s := d.Stats(); s.Skipped != skipped || s.Gaps != 0 || s.Dropped != 0 {
			t.Errorf("%s: stats %+v, want %d bytes skipped", name, s, skipped)
		}
		d.Close()
	}
	// Probe finds the program behind a prefix too.
	for _, p := range prefixes {
		if prog, err := Probe(slices.Concat(p, fx.segs[0])); err != nil || len(prog.Streams) != 2 {
			t.Errorf("Probe behind %d bytes: %+v, %v", len(p), prog, err)
		}
	}
}
