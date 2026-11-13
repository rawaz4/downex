package testmedia

import (
	"bytes"
	"testing"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

func TestEBSP(t *testing.T) {
	for _, tc := range []struct{ in, out []byte }{
		{[]byte{0, 0, 0}, []byte{0, 0, 3, 0, 3}},
		{[]byte{0, 0, 1, 0, 0, 2}, []byte{0, 0, 3, 1, 0, 0, 3, 2}},
		{[]byte{1, 0, 0, 4}, []byte{1, 0, 0, 4}},
	} {
		if got := EBSP(tc.in); !bytes.Equal(got, tc.out) {
			t.Errorf("EBSP(%x) = %x, want %x", tc.in, got, tc.out)
		}
		if got := Unescape(EBSP(tc.in)); !bytes.Equal(got, append(tc.in, bytes.Repeat([]byte{}, 0)...)) && !bytes.HasPrefix(got, tc.in) {
			t.Errorf("Unescape(EBSP(%x)) = %x", tc.in, got)
		}
	}
}

func TestH264ParameterSetsParse(t *testing.T) {
	for _, sz := range [][2]int{{640, 360}, {1920, 1080}, {1280, 720}, {320, 240}} {
		sps, err := avc.ParseSPSNALUnit(H264SPS(3, sz[0], sz[1]), true)
		if err != nil {
			t.Fatalf("%v: %v", sz, err)
		}
		if int(sps.Width) != sz[0] || int(sps.Height) != sz[1] || sps.ParameterID != 3 {
			t.Errorf("SPS %v parses as %dx%d id %d", sz, sps.Width, sps.Height, sps.ParameterID)
		}
		spsMap := map[uint32]*avc.SPS{3: sps}
		pps, err := avc.ParsePPSNALUnit(H264PPS(7, 3), spsMap)
		if err != nil || pps.PicParameterSetID != 7 {
			t.Fatalf("PPS: %+v, %v", pps, err)
		}
	}
	if _, err := mp4.CreateAvcC([][]byte{H264SPS(0, 640, 360)}, [][]byte{H264PPS(0, 0)}, true); err != nil {
		t.Fatal(err)
	}
}

func TestHEVCParameterSetsParse(t *testing.T) {
	for _, sz := range [][2]int{{640, 360}, {1920, 1080}, {1280, 720}} {
		sps, err := hevc.ParseSPSNALUnit(HEVCSPS(2, sz[0], sz[1]))
		if err != nil {
			t.Fatalf("%v: %v", sz, err)
		}
		w, h := sps.ImageSize()
		if int(w) != sz[0] || int(h) != sz[1] || sps.SpsID != 2 {
			t.Errorf("SPS %v parses as %dx%d id %d", sz, w, h, sps.SpsID)
		}
	}
	if _, err := hevc.ParseVPSNALUnit(HEVCVPS()); err != nil {
		t.Fatalf("VPS: %v", err)
	}
	sps, _ := hevc.ParseSPSNALUnit(HEVCSPS(0, 640, 360))
	if _, err := hevc.ParsePPSNALUnit(HEVCPPS(5, 0), map[uint32]*hevc.SPS{0: sps}); err != nil {
		t.Fatalf("PPS: %v", err)
	}
	if _, err := mp4.CreateHvcC([][]byte{HEVCVPS()}, [][]byte{HEVCSPS(0, 640, 360)}, [][]byte{HEVCPPS(0, 0)}, true, true, true, true); err != nil {
		t.Fatal(err)
	}
}

func TestVideoTiming(t *testing.T) {
	aus := Video(VideoConfig{Frames: 30, BFrames: true, GOP: 12, StartDTS: 1000})
	if len(aus) != 30 {
		t.Fatalf("%d units", len(aus))
	}
	seen := map[int64]bool{}
	for i, au := range aus {
		if au.DTS != 1000+int64(i)*3000 {
			t.Errorf("unit %d: DTS %d", i, au.DTS)
		}
		if au.PTS < au.DTS {
			t.Errorf("unit %d: PTS %d before DTS %d", i, au.PTS, au.DTS)
		}
		seen[au.PTS] = true
		if au.Sync != (i%12 == 0) {
			t.Errorf("unit %d: sync %v", i, au.Sync)
		}
		// The sample is the slice and SEI units of the byte stream.
		nalus := avc.ExtractNalusFromByteStream(au.AnnexB)
		var kept [][]byte
		for _, n := range nalus {
			switch n[0] & 0x1f {
			case H264AUD, H264SPSType, H264PPSType:
			default:
				kept = append(kept, n)
			}
		}
		if !bytes.Equal(LengthPrefixed(kept), au.Sample) {
			t.Errorf("unit %d: sample differs from its byte stream", i)
		}
	}
	for k := range 30 {
		if !seen[1000+int64(k+1)*3000] {
			t.Errorf("no picture presented at frame %d", k)
		}
	}
}

func TestADTSHeaderDecodes(t *testing.T) {
	for _, c := range []AudioConfig{{}, {SampleRate: 44100, Channels: 1}, {CRC: true, ObjectType: 1, Channels: 6}} {
		frames := AAC(AudioConfig{SampleRate: c.SampleRate, Channels: c.Channels, CRC: c.CRC, ObjectType: c.ObjectType, Frames: 3})
		for _, f := range frames {
			h, off, err := aac.DecodeADTSHeader(bytes.NewReader(f.ADTS))
			if err != nil {
				t.Fatal(err)
			}
			if off != 0 || int(h.HeaderLength)+len(f.Raw) != len(f.ADTS) || !bytes.Equal(f.ADTS[h.HeaderLength:], f.Raw) {
				t.Fatalf("%+v: header %+v", c, h)
			}
		}
	}
}

func TestCRC32(t *testing.T) {
	if got := CRC32([]byte("123456789")); got != 0x0376e6e7 {
		t.Fatalf("CRC32 = %#x", got)
	}
	s := PATSection(1, 0x100, 0)
	if CRC32(s) != 0 {
		t.Fatal("a section with its CRC does not check to 0")
	}
}

func TestMuxerPackets(t *testing.T) {
	m := &Muxer{Streams: []Stream{{PID: 0x100, Type: TypeH264}, {PID: 0x101, Type: TypeAAC, Language: "deu"}}}
	aus := Video(VideoConfig{Frames: 24, BFrames: true, StartDTS: 1<<33 - 6000})
	frames := AAC(AudioConfig{Frames: 40, StartPTS: 1<<33 - 6000})
	units := Interleave(VideoPES(0x100, aus), AudioPES(0x101, frames, 4))
	segs := m.Segments(units, SyncCut(0x100, 0.3))
	if len(segs) != 2 {
		t.Fatalf("%d segments", len(segs))
	}
	for _, s := range segs {
		if len(s)%PacketSize != 0 {
			t.Fatal("segment is not whole packets")
		}
		for i := 0; i < len(s); i += PacketSize {
			if s[i] != SyncByte {
				t.Fatalf("no sync byte at %d", i)
			}
		}
		if PacketPID(Packet(s, 0)) != 0 || PacketPID(Packet(s, 1)) != 0x1000 {
			t.Fatal("segment does not start with PAT and PMT")
		}
	}
	all := append(append([]byte(nil), segs[0]...), segs[1]...)
	n := len(all) / PacketSize
	if got := len(DropPacket(all, 0x100, 3)) / PacketSize; got != n-1 {
		t.Fatalf("DropPacket left %d of %d packets", got, n)
	}
	if got := len(DuplicatePacket(all, 0x100, 3)) / PacketSize; got != n+1 {
		t.Fatalf("DuplicatePacket left %d of %d packets", got, n)
	}
	// The PES timestamps are written modulo 2^33.
	p := m.PESBytes(PES{PID: 0x100, PTS: 1<<33 + 5, DTS: 1<<33 + 2})
	pts := int64(p[9]>>1&7)<<30 | int64(p[10])<<22 | int64(p[11]>>1)<<15 | int64(p[12])<<7 | int64(p[13]>>1)
	if pts != 5 {
		t.Fatalf("PTS written as %d", pts)
	}
}

func TestProgressiveMP4Decodes(t *testing.T) {
	v := VideoConfig{Frames: 48, BFrames: true}
	a := AudioConfig{Frames: 75}
	data, err := ProgressiveMP4(v, Video(v), a, AAC(a))
	if err != nil {
		t.Fatal(err)
	}
	f, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Moov.Traks) != 2 || f.Moov.Traks[0].Mdia.Minf.Stbl.Stsz.SampleNumber != 48 || f.Moov.Traks[1].Mdia.Minf.Stbl.Stsz.SampleNumber != 75 {
		t.Fatal("unexpected tracks")
	}
	aus := Video(v)
	trak := f.Moov.Traks[0]
	got, err := f.ReadSampleData(trak, 1, 48, bytes.NewReader(data), nil)
	if err != nil {
		t.Fatal(err)
	}
	var want []byte
	for _, au := range aus {
		want = append(want, au.Sample...)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("video sample data differs")
	}
	hv := VideoConfig{Codec: HEVC, Frames: 12}
	if _, err := ProgressiveMP4(hv, Video(hv), a, nil); err != nil {
		t.Fatal(err)
	}
}
