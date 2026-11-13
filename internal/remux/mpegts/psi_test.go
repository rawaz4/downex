package mpegts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"slices"
	"testing"

	"downex/internal/media"
	"downex/internal/testmedia"
)

func TestCRC32(t *testing.T) {
	if got := crc32([]byte("123456789")); got != 0x0376e6e7 {
		t.Fatalf("crc32 = %#x", got)
	}
	s := testmedia.PATSection(1, 0x100, 3)
	if crc32(s) != 0 {
		t.Fatal("section CRC does not check")
	}
	if got := crc32(s[:len(s)-4]); got != testmedia.CRC32(s[:len(s)-4]) {
		t.Fatal("table and bitwise CRC differ")
	}
}

func TestSectionAssembler(t *testing.T) {
	pat := testmedia.PATSection(1, 0x100, 0)
	pmt := (&testmedia.Muxer{Streams: []testmedia.Stream{{PID: 0x100, Type: testmedia.TypeH264}}}).PMTSection()
	var got [][]byte
	collect := func(s []byte) { got = append(got, bytes.Clone(s)) }
	var a sectionAssembler

	// Two sections in one packet, then stuffing.
	a.push(slices.Concat([]byte{0}, pat, pmt, []byte{0xff, 0xff}), true, collect)
	if len(got) != 2 || !bytes.Equal(got[0], pat) || !bytes.Equal(got[1], pmt) {
		t.Fatalf("got %d sections", len(got))
	}
	// A section split over three packets, the last of which starts another
	// section after a pointer_field.
	got = nil
	a.push(slices.Concat([]byte{0}, pmt[:5]), true, collect)
	a.push(pmt[5:9], false, collect)
	a.push(slices.Concat([]byte{byte(len(pmt) - 9)}, pmt[9:], pat), true, collect)
	if len(got) != 2 || !bytes.Equal(got[0], pmt) || !bytes.Equal(got[1], pat) {
		t.Fatalf("got %d sections", len(got))
	}
	// A section_length above 1021 is abandoned, as is a pointer past the payload.
	got = nil
	a.push([]byte{0, 0x02, 0xb3, 0xff, 1, 2, 3}, true, collect)
	a.push(bytes.Repeat([]byte{1}, 2000), false, collect)
	a.push([]byte{200, 1, 2}, true, collect)
	if len(got) != 0 {
		t.Fatalf("got %d sections", len(got))
	}
	// Continuation without an active section is ignored.
	a.push(pat, false, collect)
	if len(got) != 0 {
		t.Fatal("continuation without start produced a section")
	}
}

func TestParseSectionRejectsBadCRC(t *testing.T) {
	s := testmedia.PATSection(1, 0x100, 0)
	if _, ok := parseSection(s); !ok {
		t.Fatal("valid section refused")
	}
	bad := slices.Clone(s)
	bad[9] ^= 1
	if _, ok := parseSection(bad); ok {
		t.Fatal("section with a wrong CRC accepted")
	}
	if _, ok := parseSection(s[:8]); ok {
		t.Fatal("short section accepted")
	}
}

func TestPMTDescriptorsAndLimit(t *testing.T) {
	m := &testmedia.Muxer{Streams: []testmedia.Stream{
		{PID: 0x100, Type: testmedia.TypeH264},
		{PID: 0x101, Type: testmedia.TypeAAC, Language: "FRA"},
		{PID: 0x102, Type: testmedia.TypePrivate, Descriptors: testmedia.AC3Descriptor()},
		{PID: 0x103, Type: testmedia.TypePrivate, Descriptors: []byte{0x05, 4, 'E', 'A', 'C', '3'}},
		{PID: 0x104, Type: testmedia.TypePrivate, Descriptors: []byte{0x59, 0}},
		{PID: 0x105, Type: testmedia.TypeAAC, Descriptors: []byte{0x0a, 4, '1', '2', '3', 0}},
	}}
	p, err := Probe(m.PSI())
	if err != nil {
		t.Fatal(err)
	}
	want := []Stream{
		{PID: 0x100, Type: TypeH264},
		{PID: 0x101, Type: TypeAAC, Language: "fra"},
		{PID: 0x102, Type: TypePrivatePES, Dolby: true},
		{PID: 0x103, Type: TypePrivatePES, Dolby: true},
		{PID: 0x104, Type: TypePrivatePES},
		{PID: 0x105, Type: TypeAAC},
	}
	if !slices.Equal(p.Streams, want) {
		t.Fatalf("streams %+v", p.Streams)
	}
	// At most 64 streams are read from one PMT.
	var many []testmedia.Stream
	for i := range 70 {
		many = append(many, testmedia.Stream{PID: uint16(0x100 + i), Type: testmedia.TypeAAC})
	}
	m = &testmedia.Muxer{Streams: many}
	p, err = Probe(m.PSI())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Streams) != maxStreams {
		t.Fatalf("%d streams", len(p.Streams))
	}
}

func TestProbe(t *testing.T) {
	m := &testmedia.Muxer{ProgramNumber: 7, PMTPID: 0x42, Streams: []testmedia.Stream{{PID: 0x100, Type: testmedia.TypeHEVC}}}
	p, err := Probe(slices.Concat(testmedia.NullPackets(2), m.PSI(), testmedia.NullPackets(2)))
	if err != nil {
		t.Fatal(err)
	}
	if p.Number != 7 || p.PMTPID != 0x42 || len(p.Streams) != 1 || p.Streams[0].Type != TypeHEVC {
		t.Fatalf("program %+v", p)
	}
	if _, err := Probe(testmedia.Filler(1, 50)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("filler: %v", err)
	}
	if _, err := Probe(nil); !errors.Is(err, ErrMalformed) {
		t.Fatalf("empty: %v", err)
	}
	// Only a PAT: no program.
	if _, err := Probe(testmedia.Packet(m.PSI(), 0)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("PAT only: %v", err)
	}
	scr := slices.Clone(m.PSI())
	scr[3] |= 0x80
	if _, err := Probe(scr); !errors.Is(err, media.ErrEncrypted) {
		t.Fatalf("scrambled PAT: %v", err)
	}
}

// TestPSIFloodIsBounded: a stream of nothing but changing program map tables and
// broken sections is read with allocations proportional to its size.
func TestPSIFloodIsBounded(t *testing.T) {
	var stream []byte
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: 0x100, Type: testmedia.TypeH264}}}
	stream = append(stream, m.PSI()...)
	for i := range 20000 {
		m.PMTVersion = byte(i)
		m.Streams[0].PID = uint16(0x100 + i%50)
		sec := m.PMTSection()
		if i%3 == 0 {
			sec[len(sec)-1] ^= 0xff // a wrong CRC
		}
		stream = append(stream, m.SectionPackets(m.PMTPID, sec)...)
	}
	// Endless sections: a section header announcing 1021 bytes, continued forever.
	stream = append(stream, m.SectionPackets(m.PMTPID, []byte{0x02, 0xb3, 0xfd})...)
	for range 5000 {
		stream = append(stream, m.SectionPackets(m.PMTPID, bytes.Repeat([]byte{1}, 183))[0:PacketSize]...)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	programs := 0
	d := NewBytesDemuxer([][]byte{stream}, keepAll, WithProgramCheck(func(Program) error { programs++; return nil }))
	for {
		_, err := d.Next(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	if programs < 13000 {
		t.Fatalf("%d programs applied", programs)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 4*uint64(len(stream)) {
		t.Fatalf("allocated %d bytes for %d bytes of PSI", grew, len(stream))
	}
}
