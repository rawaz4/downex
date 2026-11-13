package remux

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"downex/internal/remux/mpegts"
	"downex/internal/testmedia"
)

func TestSplitAnnexB(t *testing.T) {
	for _, tc := range []struct {
		in   []byte
		want [][]byte
	}{
		{nil, nil},
		{[]byte{9, 9, 9}, nil}, // no start code
		{[]byte{0, 0, 1, 0x65, 1, 2, 0, 0, 0, 1, 0x41, 3}, [][]byte{{0x65, 1, 2}, {0x41, 3}}},
		{[]byte{7, 7, 0, 0, 1, 0x09, 0xf0, 0, 0, 1, 0, 0, 1, 0x06, 5}, [][]byte{{0x09, 0xf0}, {0x06, 5}}},    // junk, empty unit
		{[]byte{0, 0, 0, 1, 0x67, 0, 0, 3, 1, 0, 0, 0, 0, 0, 1, 0x68}, [][]byte{{0x67, 0, 0, 3, 1}, {0x68}}}, // trailing zeros
		{[]byte{0, 0, 1, 0x41, 0, 0}, [][]byte{{0x41}}},
	} {
		var got [][]byte
		if err := splitAnnexB(tc.in, func(n []byte) error { got = append(got, slices.Clone(n)); return nil }); err != nil {
			t.Fatal(err)
		}
		if !slices.EqualFunc(got, tc.want, bytes.Equal) {
			t.Errorf("splitAnnexB(%x) = %x, want %x", tc.in, got, tc.want)
		}
	}
}

func TestVideoAccessUnit(t *testing.T) {
	aus := testmedia.Video(testmedia.VideoConfig{Frames: 2, SEI: true})
	c := newVideoConv(mpegts.TypeH264)
	vcl, sync, _, _, err := c.accessUnit(aus[0].AnnexB)
	if err != nil || !vcl || !sync || !bytes.Equal(c.buf, aus[0].Sample) {
		t.Fatalf("IDR unit: vcl %v sync %v err %v", vcl, sync, err)
	}
	if len(c.ps.list(kindSPS)) != 1 || len(c.ps.list(kindPPS)) != 1 {
		t.Fatal("parameter sets not taken")
	}
	vcl, sync, _, _, err = c.accessUnit(aus[1].AnnexB)
	if err != nil || !vcl || sync || !bytes.Equal(c.buf, aus[1].Sample) {
		t.Fatalf("P unit: vcl %v sync %v err %v", vcl, sync, err)
	}
	// Filler, end of sequence and SPS extension units are removed; a unit with only
	// them has no slice.
	vcl, _, _, _, err = c.accessUnit(testmedia.AnnexB([][]byte{{0x0c, 0xff, 0xff}, {0x0a}, {0x0d, 1}}, nil))
	if err != nil || vcl || len(c.buf) != 0 {
		t.Fatalf("filler unit: vcl %v, %d bytes", vcl, len(c.buf))
	}
	_, _, _, _, err = c.accessUnit(slices.Concat(aus[0].AnnexB, aus[1].AnnexB))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("two access units: %v", err)
	}
	// A parameter set over 64 KiB is malformed.
	big := append([]byte{0x68}, bytes.Repeat([]byte{0x88}, maxParamSetBytes)...)
	if _, _, _, _, err = c.accessUnit(testmedia.AnnexB([][]byte{big}, nil)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("big PPS: %v", err)
	}
}

func TestParamStateIDs(t *testing.T) {
	c := newVideoConv(mpegts.TypeHEVC)
	cfg := testmedia.VideoConfig{Codec: testmedia.HEVC, SPSID: 3, PPSID: 9}
	for _, p := range cfg.ParamSets() {
		var auds int
		if _, err := c.hevcNAL(p, &auds); err != nil {
			t.Fatal(err)
		}
	}
	if s := c.ps.sets[kindSPS]; len(s) != 4 || s[3] == nil {
		t.Fatalf("SPS ids %d", len(s))
	}
	if s := c.ps.sets[kindPPS]; len(s) != 10 || s[9] == nil {
		t.Fatalf("PPS ids %d", len(s))
	}
	gen := c.ps.gen
	var auds int
	c.hevcNAL(cfg.ParamSets()[1], &auds) // the same SPS again
	if c.ps.gen != gen {
		t.Fatal("a repeated parameter set counts as a change")
	}
}

func TestADTSSplitter(t *testing.T) {
	cfg := testmedia.AudioConfig{Frames: 6, FrameBytes: 50, Seed: 41}
	frames := testmedia.AAC(cfg)
	crc := testmedia.AAC(testmedia.AudioConfig{Frames: 2, CRC: true, Seed: 42})
	var stream []byte
	var starts []int
	for i, f := range frames {
		starts = append(starts, len(stream))
		stream = append(stream, f.ADTS...)
		if i == 2 {
			stream = append(stream, "junk\xff\xf1"...) // bytes without a valid header
		}
	}
	starts = append(starts, len(stream))
	stream = append(stream, crc[0].ADTS...)
	want := append(sampleData(expAudio(frames)), crc[0].Raw)
	// Cut everywhere: every split gives the same frames.
	for cut := 0; cut <= len(stream); cut += 7 {
		var s adtsSplitter
		var got [][]byte
		var here []bool
		fn := func(h adtsHeader, raw []byte, startedHere bool) error {
			got, here = append(got, slices.Clone(raw)), append(here, startedHere)
			return nil
		}
		if err := s.feed(stream[:cut], fn); err != nil {
			t.Fatal(err)
		}
		n := len(got)
		if err := s.feed(stream[cut:], fn); err != nil {
			t.Fatal(err)
		}
		if !slices.EqualFunc(got, want, bytes.Equal) {
			t.Fatalf("cut at %d: %d frames", cut, len(got))
		}
		// The first frame of the second piece started there unless it was carried.
		if n < len(got) && here[n] != (starts[n] >= cut) {
			t.Fatalf("cut at %d: frame %d started here = %v", cut, n, here[n])
		}
	}
}

// TestTSADTSAcrossPES: frames split across PES units are carried over; a frame
// without raw data gives no sample and lengthens the one before.
func TestTSADTSAcrossPES(t *testing.T) {
	cfg := testmedia.AudioConfig{Frames: 40, Seed: 43}
	frames := testmedia.AAC(cfg)
	frames[17].ADTS, frames[17].Raw = testmedia.ADTSFrame(cfg, nil), nil
	var stream []byte
	var starts []int
	for _, f := range frames {
		starts = append(starts, len(stream))
		stream = append(stream, f.ADTS...)
	}
	// PES units of 1000 bytes, each timed by the first frame that starts in it.
	var units []testmedia.PES
	for off := 0; off < len(stream); off += 1000 {
		end := min(off+1000, len(stream))
		pts := int64(-1)
		for i, s := range starts {
			if s >= off && s < end {
				pts = frames[i].PTS
				break
			}
		}
		units = append(units, testmedia.PES{PID: aPID, PTS: pts, DTS: -1, Data: stream[off:end], Bounded: true})
	}
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: aPID, Type: testmedia.TypeAAC}}}
	out := mustAssemble(t, []Input{tsInputOf(t, KindAudio, [][]byte{m.Segment(units)})}, ContainerMP4)
	want := expAudio(slices.Delete(slices.Clone(frames), 17, 18))
	want[16].dur = 2048
	checkProgressive(t, out, []expTrack{{handler: "soun", timescale: 48000, samples: want}})
}
