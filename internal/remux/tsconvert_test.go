package remux

import (
	"bytes"
	"context"
	"errors"
	"math"
	"math/rand"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/media"
	"downex/internal/testmedia"
)

// payloadsOf returns the sample data of a progressive track.
func payloadsOf(t testing.TB, f *mp4.File, data []byte, trak *mp4.TrakBox) [][]byte {
	t.Helper()
	stbl := trak.Mdia.Minf.Stbl
	n := stbl.Stsz.GetNrSamples()
	all, err := f.ReadSampleData(trak, 1, n, bytes.NewReader(data), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := make([][]byte, n)
	for i := range out {
		sz := stbl.Stsz.GetSampleSize(i + 1)
		out[i], all = all[:sz], all[sz:]
	}
	return out
}

func sampleData(samples []fxSample) [][]byte {
	out := make([][]byte, len(samples))
	for i, s := range samples {
		out[i] = s.data
	}
	return out
}

// TestTSDiscontinuities: three discontinuity domains with timestamp jumps (one of
// them across the 33-bit wrap) give one continuous MP4: each domain starts where
// the previous one ended, and video and audio keep their offset within 1 ms.
func TestTSDiscontinuities(t *testing.T) {
	bases := []int64{10 * tsClock, tsWrap - 30000, 777}
	m := avMuxer(false, "")
	var segs [][]byte
	var breaks []int
	var wantV, wantA []fxSample
	for d, base := range bases {
		fx := newTSFixture(
			testmedia.VideoConfig{Frames: 24, BFrames: true, StartDTS: base, Seed: int64(d)},
			testmedia.AudioConfig{Frames: 38, StartPTS: base - 2000, Seed: int64(10 + d)})
		if d > 0 {
			breaks = append(breaks, len(segs))
		}
		segs = append(segs, segments(m, fx.units, 0.4)...)
		wantV, wantA = append(wantV, expVideo(fx.aus)...), append(wantA, expAudio(fx.frames)...)
	}
	out := mustAssemble(t, []Input{tsInputOf(t, KindMuxed, segs, breaks...)}, ContainerMP4)
	f, data := decodeMP4(t, out)
	vt, at := f.Moov.Traks[0], f.Moov.Traks[1]
	if !slices.EqualFunc(payloadsOf(t, f, data, vt), sampleData(wantV), bytes.Equal) {
		t.Fatal("video payloads differ")
	}
	if !slices.EqualFunc(payloadsOf(t, f, data, at), sampleData(wantA), bytes.Equal) {
		t.Fatal("audio payloads differ")
	}
	vts, ats := sampleTimes(t, f, vt), sampleTimes(t, f, at)
	// Per domain: video presents 5000 ticks after audio (its first picture one
	// frame after its decode time, which is 2000 ticks after the first audio
	// frame); a domain starts where the previous one's latest presentation ended
	// (the video's, 75000 ticks after its first decode time).
	const domainLen = 77000.0 / tsClock
	for d := range bases {
		v0 := slices.Min(vts[d*24 : d*24+24])
		a0 := ats[d*38]
		if math.Abs(v0-a0-5000.0/tsClock) > 0.001 {
			t.Errorf("domain %d: video starts %.4f s after audio", d, v0-a0)
		}
		if math.Abs(a0-float64(d)*domainLen) > 0.001 {
			t.Errorf("domain %d starts at %.4f s, want %.4f", d, a0, float64(d)*domainLen)
		}
	}
	if got, want := float64(f.Moov.Mvhd.Duration)/1000, 3*domainLen; math.Abs(got-want) > 0.002 {
		t.Errorf("movie duration %.3f s, want %.3f", got, want)
	}
	// Decode times increase and durations stay positive across the jumps.
	durs, _ := vt.Mdia.Minf.Stbl.Stts.SampleDurations(uint32(len(wantV)))
	for i, d := range durs {
		if d == 0 || d > 9000 {
			t.Errorf("video sample %d lasts %d ticks", i, d)
		}
	}
}

// TestTSUnlabelledJumpKeepsAVTogether: a muxed input whose timestamps jump inside
// one domain (no EXT-X-DISCONTINUITY: back by 2 s, back by 0.3 s, forward by 15 s)
// is stitched by one offset for both streams: B's video and audio keep their
// relative offset (0 in the source) within one frame, and no picture is lost; the
// audio loses at most the one PES that came before the video showed a small jump.
func TestTSUnlabelledJumpKeepsAVTogether(t *testing.T) {
	const base = 10 * tsClock
	for _, tc := range []struct {
		name     string
		bStart   int64 // B's first timestamp; A spans [base, base+2 s)
		maxAudio int   // audio frames that may be lost
	}{
		{"back 2 s", base, 0},
		{"back 0.3 s", base + 2*tsClock - 27000, 5},
		{"forward 15 s", base + 17*tsClock, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := avMuxer(false, "")
			a := newTSFixture(testmedia.VideoConfig{Frames: 60, StartDTS: base, Seed: 31}, testmedia.AudioConfig{Frames: 94, StartPTS: base, Seed: 32})
			b := newTSFixture(testmedia.VideoConfig{Frames: 180, StartDTS: tc.bStart, Seed: 33}, testmedia.AudioConfig{Frames: 282, StartPTS: tc.bStart, Seed: 34})
			segs := append(segments(m, a.units, 0.4), segments(m, b.units, 0.4)...)
			out := mustAssemble(t, []Input{tsInputOf(t, KindMuxed, segs)}, ContainerMP4)
			f, data := decodeMP4(t, out)
			vt, at := f.Moov.Traks[0], f.Moov.Traks[1]
			vts, ats := sampleTimes(t, f, vt), sampleTimes(t, f, at)
			if len(vts) != 240 {
				t.Fatalf("%d video samples, want 240", len(vts))
			}
			if lost := 94 + 282 - len(ats); lost < 0 || lost > tc.maxAudio {
				t.Fatalf("%d audio frames lost, at most %d allowed", lost, tc.maxAudio)
			}
			// B's audio origin: the output time of its first kept frame minus that
			// frame's place in B.
			ap := payloadsOf(t, f, data, at)
			origin := math.NaN()
			for i, p := range ap {
				if k := slices.IndexFunc(b.frames, func(fr testmedia.AudioFrame) bool { return bytes.Equal(fr.Raw, p) }); k >= 0 {
					origin = ats[i] - float64(k)*1024/48000
					break
				}
			}
			if v0 := slices.Min(vts[60:]); math.IsNaN(origin) || math.Abs(origin-v0) > 1.0/30 {
				t.Fatalf("B's audio starts at %.4f s, its video at %.4f s", origin, v0)
			}
			// The jump leaves no gap of more than a frame in the video.
			durs, _ := vt.Mdia.Minf.Stbl.Stts.SampleDurations(240)
			for i, d := range durs {
				if d == 0 || d > 6000 {
					t.Errorf("video sample %d lasts %d ticks", i, d)
				}
			}
		})
	}
}

// TestTSContinuityGapDropsThatUnit: a lost packet discards the access unit it
// belonged to; the sample before it lasts until the next one.
func TestTSContinuityGapDropsThatUnit(t *testing.T) {
	fx := newTSFixture(testmedia.VideoConfig{Frames: 36, BFrames: true, Seed: 4}, testmedia.AudioConfig{Frames: 56, Seed: 5})
	ts := avMuxer(false, "").Segment(fx.units)
	const lost = 20 // a P picture
	ts = testmedia.DropPacket(ts, vPID, videoPacketIndex(ts, lost)+1)
	out := mustAssemble(t, []Input{tsInputOf(t, KindMuxed, [][]byte{ts})}, ContainerMP4)
	kept := slices.Delete(slices.Clone(fx.aus), lost, lost+1)
	checkProgressive(t, out, []expTrack{
		{handler: "vide", timescale: 90000, samples: expVideo(kept), elst: []mp4.ElstEntry{
			{SegmentDuration: 33, MediaTime: -1, MediaRateInteger: 1},
			{SegmentDuration: 1200, MediaTime: 3000, MediaRateInteger: 1}}},
		{handler: "soun", timescale: 48000, samples: expAudio(fx.frames)},
	})
}

// videoPacketIndex returns the index, among the video PID's packets with payload,
// of the first packet of the n-th video unit.
func videoPacketIndex(ts []byte, n int) int {
	k, units := 0, 0
	for i := 0; i+188 <= len(ts); i += 188 {
		p := ts[i : i+188]
		if testmedia.PacketPID(p) != vPID || p[3]&0x10 == 0 {
			continue
		}
		if p[1]&0x40 != 0 {
			if units == n {
				return k
			}
			units++
		}
		k++
	}
	return -1
}

// TestTSLeadingUnitsDropped: access units before the first IDR picture are
// dropped; the track starts at the IDR.
func TestTSLeadingUnitsDropped(t *testing.T) {
	aus := testmedia.Video(testmedia.VideoConfig{Frames: 29, LeadingNonSync: 5, BFrames: true, Seed: 6})
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}
	out := mustAssemble(t, []Input{tsInputOf(t, KindVideo, segments(m, testmedia.VideoPES(vPID, aus), 0.3))}, ContainerMP4)
	checkProgressive(t, out, []expTrack{{handler: "vide", timescale: 90000, samples: expVideo(aus), elst: []mp4.ElstEntry{
		{SegmentDuration: 800, MediaTime: 3000, MediaRateInteger: 1}}}})
}

// TestTSParameterSetChange: new parameter sets start a second sample description;
// returning to the first ones uses it again.
func TestTSParameterSetChange(t *testing.T) {
	var aus []testmedia.AccessUnit
	for i, size := range [][2]int{{640, 360}, {1280, 720}, {640, 360}} {
		aus = append(aus, testmedia.Video(testmedia.VideoConfig{Width: size[0], Height: size[1], Frames: 24, StartDTS: int64(i) * 24 * 3000, Seed: int64(i)})...)
	}
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}
	out := mustAssemble(t, []Input{tsInputOf(t, KindVideo, segments(m, testmedia.VideoPES(vPID, aus), 0.4))}, ContainerMP4)
	f := checkProgressive(t, out, []expTrack{{handler: "vide", timescale: 90000, samples: expVideo(aus)}})
	trak := f.Moov.Traks[0]
	stsd := trak.Mdia.Minf.Stbl.Stsd
	if len(stsd.Children) != 2 {
		t.Fatalf("%d sample descriptions", len(stsd.Children))
	}
	for i, want := range [][2]uint16{{640, 360}, {1280, 720}} {
		e := stsd.Children[i].(*mp4.VisualSampleEntryBox)
		if e.Type() != "avc1" || e.Width != want[0] || e.Height != want[1] {
			t.Errorf("description %d: %s %dx%d", i+1, e.Type(), e.Width, e.Height)
		}
	}
	for i, sdi := range sampleDescriptions(t, trak) {
		if want := uint32(1 + i/24%2); sdi != want {
			t.Fatalf("sample %d uses description %d, want %d", i+1, sdi, want)
		}
	}
	if trak.Tkhd.Width != 640<<16 {
		t.Errorf("tkhd width %#x", trak.Tkhd.Width)
	}
}

// TestTSHEVC converts HEVC with VPS, SPS and PPS into hvc1 with complete hvcC
// arrays.
func TestTSHEVC(t *testing.T) {
	fx := newTSFixture(testmedia.VideoConfig{Codec: testmedia.HEVC, Width: 1280, Height: 720, Frames: 36, BFrames: true, SEI: true, Seed: 7}, testmedia.AudioConfig{Frames: 56, Seed: 8})
	out := mustAssemble(t, []Input{tsInputOf(t, KindMuxed, segments(avMuxer(true, ""), fx.units, 0.4))}, ContainerMP4)
	f := checkProgressive(t, out, []expTrack{
		{handler: "vide", timescale: 90000, samples: expVideo(fx.aus), elst: []mp4.ElstEntry{
			{SegmentDuration: 33, MediaTime: -1, MediaRateInteger: 1},
			{SegmentDuration: 1200, MediaTime: 3000, MediaRateInteger: 1}}},
		{handler: "soun", timescale: 48000, samples: expAudio(fx.frames)},
	})
	e := f.Moov.Traks[0].Mdia.Minf.Stbl.Stsd.HvcX
	if e == nil || e.Type() != "hvc1" || e.Width != 1280 || e.Height != 720 || e.HvcC == nil {
		t.Fatalf("sample entry %+v", f.Moov.Traks[0].Mdia.Minf.Stbl.Stsd.Children)
	}
	ps := fx.video.ParamSets()
	for i, typ := range []hevc.NaluType{hevc.NALU_VPS, hevc.NALU_SPS, hevc.NALU_PPS} {
		arr := e.HvcC.NaluArrays[i]
		if arr.NaluType() != typ || arr.Complete() != 1 || len(arr.Nalus) != 1 || !bytes.Equal(arr.Nalus[0], ps[i]) {
			t.Errorf("hvcC array %d: type %v complete %d", i, arr.NaluType(), arr.Complete())
		}
	}
}

// TestTSHEVCLeadingPictures: the RASL pictures of a domain's first CRA cannot be
// decoded and are dropped; those of a later CRA are kept.
func TestTSHEVCLeadingPictures(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	ps := testmedia.VideoConfig{Codec: testmedia.HEVC}.ParamSets()
	au := func(dts, pts int64, typ byte, withPS bool) testmedia.AccessUnit {
		nalus := [][]byte{testmedia.HEVCAUDUnit()}
		if withPS {
			nalus = append(nalus, ps...)
		}
		slice := testmedia.HEVCSlice(rng, typ, true, 300)
		nalus = append(nalus, slice)
		return testmedia.AccessUnit{DTS: dts, PTS: pts, Sync: typ >= 16 && typ <= 23,
			AnnexB: testmedia.AnnexB(nalus, nil), Sample: testmedia.LengthPrefixed([][]byte{slice})}
	}
	const d = 3000
	aus := []testmedia.AccessUnit{
		au(0, 3*d, testmedia.HEVCCRA, true),  // the domain's first CRA
		au(d, d, testmedia.HEVCRASLN, false), // its leading pictures: dropped
		au(2*d, 2*d, testmedia.HEVCRASLN, false),
		au(3*d, 4*d, testmedia.HEVCTrailR, false),
		au(4*d, 7*d, testmedia.HEVCCRA, false),   // a later CRA
		au(5*d, 5*d, testmedia.HEVCRASLR, false), // its leading pictures: kept
		au(6*d, 6*d, testmedia.HEVCRASLN, false),
		au(7*d, 8*d, testmedia.HEVCTrailR, false),
	}
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeHEVC}}}
	out := mustAssemble(t, []Input{tsInputOf(t, KindVideo, [][]byte{m.Segment(testmedia.VideoPES(vPID, aus))})}, ContainerMP4)
	f, data := decodeMP4(t, out)
	var want [][]byte
	for _, a := range slices.Concat(aus[:1], aus[3:]) {
		want = append(want, a.Sample)
	}
	if got := payloadsOf(t, f, data, f.Moov.Traks[0]); !slices.EqualFunc(got, want, bytes.Equal) {
		t.Fatalf("%d samples kept, want %d", len(got), len(want))
	}
	if stss := f.Moov.Traks[0].Mdia.Minf.Stbl.Stss; stss == nil || !slices.Equal(stss.SampleNumber, []uint32{1, 3}) {
		t.Fatalf("sync samples %+v", stss)
	}
}

// TestTSSeparateAudio: a TS video track and a separate TS audio rendition become
// one MP4, video first; an audio stream inside the video's TS is ignored.
func TestTSSeparateAudio(t *testing.T) {
	const start = 5 * tsClock
	aus := testmedia.Video(testmedia.VideoConfig{Frames: 48, StartDTS: start, Seed: 11})
	frames := testmedia.AAC(testmedia.AudioConfig{Frames: 70, StartPTS: start + tsClock/2, Seed: 12, SampleRate: 44100, Channels: 6})
	vm := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}, {PID: aPID, Type: testmedia.TypeAC3}}}
	ac3 := []testmedia.PES{{PID: aPID, PTS: start, DTS: -1, Data: bytes.Repeat([]byte{0x0b, 0x77, 1, 2}, 100), Bounded: true}}
	video := tsInputOf(t, KindVideo, segments(vm, testmedia.Interleave(testmedia.VideoPES(vPID, aus), ac3), 0.4))
	am := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: 0x200, Type: testmedia.TypeAAC}}}
	audio := tsInputOf(t, KindAudio, am.Segments(testmedia.AudioPES(0x200, frames, 4), func(i int, _ testmedia.PES) bool { return i%5 == 0 }))
	audio.Language = "fr-CA"
	out := mustAssemble(t, []Input{audio, video}, ContainerMP4)
	presDur := scaleTime(70*1024, 1000, 44100)
	f := checkProgressive(t, out, []expTrack{
		{handler: "vide", timescale: 90000, samples: expVideo(aus)},
		{handler: "soun", timescale: 44100, samples: expAudio(frames), elst: []mp4.ElstEntry{
			{SegmentDuration: 500, MediaTime: -1, MediaRateInteger: 1},
			{SegmentDuration: presDur, MediaTime: 0, MediaRateInteger: 1}}},
	})
	at := f.Moov.Traks[1]
	if at.Mdia.Minf.Stbl.Stsd.Mp4a.ChannelCount != 6 {
		t.Errorf("%d channels", at.Mdia.Minf.Stbl.Stsd.Mp4a.ChannelCount)
	}
	if at.Mdia.Mdhd.GetLanguage() != "und" || at.Mdia.Elng == nil || at.Mdia.Elng.Language != "fr-CA" {
		t.Errorf("audio language %q / %+v", at.Mdia.Mdhd.GetLanguage(), at.Mdia.Elng)
	}
}

// TestTSAudioOnly converts an audio-only transport stream (44.1 kHz mono, ADTS
// with CRC).
func TestTSAudioOnly(t *testing.T) {
	cfg := testmedia.AudioConfig{Frames: 50, SampleRate: 44100, Channels: 1, CRC: true, Seed: 13}
	frames := testmedia.AAC(cfg)
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: aPID, Type: testmedia.TypeAAC, Language: "spa"}}}
	out := mustAssemble(t, []Input{tsInputOf(t, KindAudio, segments(m, testmedia.AudioPES(aPID, frames, 3), 0))}, ContainerMP4)
	f := checkProgressive(t, out, []expTrack{{handler: "soun", timescale: 44100, samples: expAudio(frames)}})
	mp4a := f.Moov.Traks[0].Mdia.Minf.Stbl.Stsd.Mp4a
	if mp4a.ChannelCount != 1 || !bytes.Equal(mp4a.Esds.DecConfigDescriptor.DecSpecificInfo.DecConfig, testmedia.AudioSpecificConfig(cfg)) {
		t.Errorf("sample entry %+v", mp4a)
	}
	if f.Moov.Traks[0].Mdia.Mdhd.GetLanguage() != "spa" {
		t.Errorf("language %q", f.Moov.Traks[0].Mdia.Mdhd.GetLanguage())
	}
	if slices.Contains(f.Ftyp.CompatibleBrands(), "avc1") {
		t.Error("avc1 brand without video")
	}
}

// TestTSSeparateRenditionsAcrossWrap: a TS video input whose first decode time is
// 0.1 s before the 33-bit wrap and a TS audio rendition whose first time is 0.1 s
// after it are unwrapped separately; the audio still starts 0.2 s after the video.
func TestTSSeparateRenditionsAcrossWrap(t *testing.T) {
	vstart := int64(tsWrap - 9000)
	aus := testmedia.Video(testmedia.VideoConfig{Frames: 48, StartDTS: vstart, Seed: 11})
	frames := testmedia.AAC(testmedia.AudioConfig{Frames: 70, StartPTS: vstart + 18000, Seed: 12})
	vm := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}
	video := tsInputOf(t, KindVideo, segments(vm, testmedia.VideoPES(vPID, aus), 0.4))
	am := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: 0x200, Type: testmedia.TypeAAC}}}
	audio := tsInputOf(t, KindAudio, am.Segments(testmedia.AudioPES(0x200, frames, 4), func(i int, _ testmedia.PES) bool { return i%5 == 0 }))
	out := mustAssemble(t, []Input{video, audio}, ContainerMP4)
	f, _ := decodeMP4(t, out)
	vts, ats := sampleTimes(t, f, f.Moov.Traks[0]), sampleTimes(t, f, f.Moov.Traks[1])
	if d := ats[0] - slices.Min(vts); d < 0.199 || d > 0.201 {
		t.Fatalf("audio starts %.4f s after the video, want 0.2", d)
	}
	if f.Moov.Mvhd.Duration > 2000 {
		t.Fatalf("movie lasts %d ms", f.Moov.Mvhd.Duration)
	}
}

// TestTSNoSyncVideoRefused: a video whose units carry no IDR picture yields no
// video sample; the conversion is refused rather than saving the audio alone.
func TestTSNoSyncVideoRefused(t *testing.T) {
	fx := newTSFixture(testmedia.VideoConfig{Frames: 48, LeadingNonSync: 48, Seed: 21}, testmedia.AudioConfig{Frames: 70, Seed: 22})
	segs := segments(avMuxer(false, ""), fx.units, 0.4)
	for _, k := range []Kind{KindMuxed, KindVideo} {
		_, _, err := assemble(t, []Input{tsInputOf(t, k, segs)})
		if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "no decodable video") {
			t.Errorf("%s: %v, want ErrUnsupported (no decodable video)", k, err)
		}
	}
}

// TestTSAccessUnitAcrossPES: access units carried in two bounded PES packets each,
// the second without PTS and DTS (and not starting at a start code, or starting at
// a later slice of the same picture), are joined into one sample each.
func TestTSAccessUnitAcrossPES(t *testing.T) {
	for _, hevc := range []bool{false, true} {
		codec := testmedia.H264
		if hevc {
			codec = testmedia.HEVC
		}
		aus := testmedia.Video(testmedia.VideoConfig{Codec: codec, Frames: 24, Seed: 41, SliceBytes: 3000, BFrames: true})
		var units []testmedia.PES
		for i, au := range aus {
			h := len(au.AnnexB) / 2
			if i%3 == 2 {
				// Cut at the start code of the last NAL unit (a slice continuing the picture).
				h = bytes.LastIndex(au.AnnexB, []byte{0, 0, 1})
			}
			units = append(units,
				testmedia.PES{PID: vPID, PTS: au.PTS, DTS: au.DTS, Data: au.AnnexB[:h], RandomAccess: au.Sync, Bounded: true},
				testmedia.PES{PID: vPID, PTS: -1, DTS: -1, Data: au.AnnexB[h:], Bounded: true})
		}
		typ := byte(testmedia.TypeH264)
		if hevc {
			typ = testmedia.TypeHEVC
		}
		m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: typ}}}
		ts := m.Segment(units)
		if err := CheckTS(ts, KindVideo); err != nil {
			t.Fatalf("hevc %v: CheckTS: %v", hevc, err)
		}
		out := mustAssemble(t, []Input{tsInputOf(t, KindVideo, [][]byte{ts})}, ContainerMP4)
		f, data := decodeMP4(t, out)
		if !slices.EqualFunc(payloadsOf(t, f, data, f.Moov.Traks[0]), sampleData(expVideo(aus)), bytes.Equal) {
			t.Fatalf("hevc %v: the samples differ from the access units", hevc)
		}
	}
}

// TestTSAudioInLaterProgram: a muxed stream whose first program (a video-only
// slate) has no audio, followed after a discontinuity by a program with H.264 and
// AAC, keeps the programme's audio.
func TestTSAudioInLaterProgram(t *testing.T) {
	slate := testmedia.Video(testmedia.VideoConfig{Frames: 24, Seed: 61})
	vm := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}
	segs := segments(vm, testmedia.VideoPES(vPID, slate), 0.4)
	brk := len(segs)
	fx := newTSFixture(testmedia.VideoConfig{Frames: 60, StartDTS: 50 * tsClock, Seed: 62}, testmedia.AudioConfig{Frames: 94, StartPTS: 50 * tsClock, Seed: 63})
	am := avMuxer(false, "")
	am.PMTVersion = 1
	segs = append(segs, segments(am, fx.units, 0.4)...)
	out := mustAssemble(t, []Input{tsInputOf(t, KindMuxed, segs, brk)}, ContainerMP4)
	f, data := decodeMP4(t, out)
	if len(f.Moov.Traks) != 2 || f.Moov.Traks[1].Mdia.Hdlr.HandlerType != "soun" {
		t.Fatalf("%d tracks", len(f.Moov.Traks))
	}
	if !slices.EqualFunc(payloadsOf(t, f, data, f.Moov.Traks[1]), sampleData(expAudio(fx.frames)), bytes.Equal) {
		t.Fatal("audio payloads differ")
	}
	// A later program that brings an unsupported audio stream is refused.
	bad := segments(vm, testmedia.VideoPES(vPID, slate), 0.4)
	ac3 := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}, {PID: aPID, Type: testmedia.TypeAC3}}, PMTVersion: 1}
	bad = append(bad, segments(ac3, testmedia.VideoPES(vPID, fx.aus), 0.4)...)
	if _, _, err := assemble(t, []Input{tsInputOf(t, KindMuxed, bad, brk)}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("AC-3 in a later program: %v", err)
	}
}

// TestTSRefusals: unsupported, encrypted and malformed transport streams are
// refused with their kind, and no output is left.
func TestTSRefusals(t *testing.T) {
	fx := newTSFixture(testmedia.VideoConfig{Frames: 24, Seed: 14}, testmedia.AudioConfig{Frames: 38, Seed: 15})
	vpes := func() []testmedia.PES { return testmedia.VideoPES(vPID, fx.aus) }
	apes := func() []testmedia.PES { return testmedia.AudioPES(aPID, fx.frames, 5) }
	mux := func(vt, at byte, adesc []byte, units []testmedia.PES) [][]byte {
		m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: vt}, {PID: aPID, Type: at, Descriptors: adesc}}}
		return [][]byte{m.Segment(units)}
	}
	videoMux := func(units []testmedia.PES) [][]byte {
		return [][]byte{(&testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}).Segment(units)}
	}
	audioMux := func(c testmedia.AudioConfig) [][]byte {
		m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: aPID, Type: testmedia.TypeAAC}}}
		return [][]byte{m.Segment(testmedia.AudioPES(aPID, testmedia.AAC(c), 4))}
	}
	scrambledTS, scrambledPES := vpes(), vpes()
	scrambledTS[5].TSScrambled = true
	scrambledPES[5].PESScrambled = true
	twoAUDs := vpes()
	twoAUDs[3].Data = slices.Concat(fx.aus[3].AnnexB, fx.aus[4].AnnexB)
	twoAUDs = slices.Delete(twoAUDs, 4, 5)
	rateChange := slices.Concat(audioMux(testmedia.AudioConfig{Frames: 10}), audioMux(testmedia.AudioConfig{Frames: 10, SampleRate: 44100}))
	noIDR := testmedia.Video(testmedia.VideoConfig{Frames: 5, LeadingNonSync: 5})
	for name, tc := range map[string]struct {
		kind Kind
		segs [][]byte
		want error
	}{
		"AC-3 stream type":         {KindMuxed, mux(testmedia.TypeH264, testmedia.TypeAC3, nil, vpes()), ErrUnsupported},
		"AC-3 descriptor":          {KindMuxed, mux(testmedia.TypeH264, testmedia.TypePrivate, testmedia.AC3Descriptor(), vpes()), ErrUnsupported},
		"MPEG-2 video":             {KindVideo, mux(testmedia.TypeMPEG2Video, testmedia.TypeAAC, nil, apes()), ErrUnsupported},
		"SAMPLE-AES video":         {KindVideo, mux(testmedia.TypeSAESH264, testmedia.TypeAAC, nil, apes()), media.ErrEncrypted},
		"SAMPLE-AES audio":         {KindMuxed, mux(testmedia.TypeH264, testmedia.TypeSAESAAC, nil, vpes()), media.ErrEncrypted},
		"scrambled packets":        {KindMuxed, mux(testmedia.TypeH264, testmedia.TypeAAC, nil, scrambledTS), media.ErrEncrypted},
		"scrambled PES":            {KindMuxed, mux(testmedia.TypeH264, testmedia.TypeAAC, nil, scrambledPES), media.ErrEncrypted},
		"several AUDs in one PES":  {KindVideo, videoMux(twoAUDs), ErrUnsupported},
		"muxed without video":      {KindMuxed, audioMux(testmedia.AudioConfig{Frames: 10}), ErrUnsupported},
		"audio kind without audio": {KindAudio, videoMux(vpes()), ErrUnsupported},
		"96 kHz AAC":               {KindAudio, audioMux(testmedia.AudioConfig{Frames: 10, SampleRate: 96000}), ErrUnsupported},
		"program config element":   {KindAudio, audioMux(testmedia.AudioConfig{Frames: 10, Channels: 8}), ErrUnsupported},
		"several raw data blocks":  {KindAudio, audioMux(testmedia.AudioConfig{Frames: 10, RawBlocks: 1}), ErrUnsupported},
		"AAC configuration change": {KindAudio, rateChange, ErrUnsupported},
		"filler":                   {KindMuxed, [][]byte{testmedia.Filler(1, 40), testmedia.Filler(2, 40)}, ErrMalformed},
		"error page":               {KindMuxed, [][]byte{[]byte("<html><body>403 Forbidden</body></html>")}, ErrMalformed},
		"no sync picture":          {KindVideo, videoMux(testmedia.VideoPES(vPID, noIDR)), ErrUnsupported},
	} {
		t.Run(name, func(t *testing.T) {
			in := tsInputOf(t, tc.kind, tc.segs)
			out, _, err := assemble(t, []Input{in})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			t.Log(err)
			if strings.Contains(err.Error(), in.Fragments[0]) {
				t.Errorf("the message names a path: %v", err)
			}
			assertNoOutput(t, out)
		})
	}
}

// TestTSResync: bytes before the first packet are skipped; the output is the same
// as without them.
func TestTSResync(t *testing.T) {
	fx := newTSFixture(testmedia.VideoConfig{Frames: 24, Seed: 16}, testmedia.AudioConfig{Frames: 38, Seed: 17})
	segs := segments(avMuxer(false, ""), fx.units, 0.4)
	clean := mustAssemble(t, []Input{tsInputOf(t, KindMuxed, segs)}, ContainerMP4)
	junk := slices.Clone(segs)
	junk[0] = slices.Concat(bytes.Repeat([]byte("ID3 junk G\x00"), 50), segs[0])
	dirty := mustAssemble(t, []Input{tsInputOf(t, KindMuxed, junk)}, ContainerMP4)
	a, _ := os.ReadFile(clean)
	b, _ := os.ReadFile(dirty)
	if !bytes.Equal(a, b) {
		t.Fatal("leading junk changed the output")
	}
}

// TestTSInitSection: Input.Init is read as a leading file (an EXT-X-MAP section
// holding the PAT and PMT) and counts for no break.
func TestTSInitSection(t *testing.T) {
	fx := newTSFixture(testmedia.VideoConfig{Frames: 24, Seed: 18}, testmedia.AudioConfig{Frames: 38, Seed: 19})
	m := avMuxer(false, "")
	var segs [][]byte
	for _, s := range segments(m, fx.units, 0.4) {
		segs = append(segs, s[2*188:]) // without their PAT and PMT
	}
	in := tsInputOf(t, KindMuxed, segs, 1)
	in.Init = writeFile(t, t.TempDir(), "map.ts", (&testmedia.Muxer{Streams: m.Streams}).PSI())
	out := mustAssemble(t, []Input{in}, ContainerMP4)
	f, data := decodeMP4(t, out)
	if !slices.EqualFunc(payloadsOf(t, f, data, f.Moov.Traks[0]), sampleData(expVideo(fx.aus)), bytes.Equal) ||
		!slices.EqualFunc(payloadsOf(t, f, data, f.Moov.Traks[1]), sampleData(expAudio(fx.frames)), bytes.Equal) {
		t.Fatal("payloads differ")
	}
}

// TestTSFragmentedFallback: above maxProgressiveSamples the conversion writes a
// fragmented MP4 from in-memory runs, aligned like the progressive output.
func TestTSFragmentedFallback(t *testing.T) {
	old := maxProgressiveSamples
	maxProgressiveSamples = 50
	t.Cleanup(func() { maxProgressiveSamples = old })
	fx := newTSFixture(testmedia.VideoConfig{Frames: 60, BFrames: true, Seed: 20, StartDTS: tsWrap - 5000}, testmedia.AudioConfig{Frames: 94, Seed: 21, StartPTS: tsWrap - 5000})
	out := mustAssemble(t, []Input{tsInputOf(t, KindMuxed, segments(avMuxer(false, ""), fx.units, 0.4))}, ContainerMP4)
	f, _ := decodeMP4(t, out)
	if !f.IsFragmented() || f.Init == nil || len(f.Init.Moov.Mvex.Trexs) != 2 {
		t.Fatal("output is not a two-track fragmented MP4")
	}
	moov := f.Init.Moov
	if moov.Mvex.Mehd == nil || moov.Mvex.Mehd.FragmentDuration != 2033 {
		t.Errorf("mehd = %+v", moov.Mvex.Mehd)
	}
	if e := moov.Traks[0].Edts; e == nil || e.Elst[0].Entries[0].MediaTime != 3000 {
		t.Errorf("video edit list %+v", e)
	}
	trex := map[uint32]*mp4.TrexBox{}
	for _, tr := range moov.Mvex.Trexs {
		trex[tr.TrackID] = tr
	}
	got := map[uint32][]mp4.FullSample{}
	last := map[uint32]uint64{}
	for _, seg := range f.Segments {
		for _, frag := range seg.Fragments {
			id := frag.Moof.Traf.Tfhd.TrackID
			fs, err := frag.GetFullSamples(trex[id])
			if err != nil {
				t.Fatal(err)
			}
			if len(got[id]) > 0 && fs[0].DecodeTime != last[id] {
				t.Errorf("track %d: fragment at %d, previous ended at %d", id, fs[0].DecodeTime, last[id])
			}
			got[id] = append(got[id], fs...)
			l := fs[len(fs)-1]
			last[id] = l.DecodeTime + uint64(l.Dur)
		}
	}
	for id, want := range map[uint32][]fxSample{1: expVideo(fx.aus), 2: expAudio(fx.frames)} {
		g := got[id]
		if len(g) != len(want) {
			t.Fatalf("track %d: %d samples, want %d", id, len(g), len(want))
		}
		for i, s := range want {
			if !bytes.Equal(g[i].Data, s.data) || g[i].Dur != s.dur || g[i].CompositionTimeOffset != s.cto || mp4.IsSyncSampleFlags(g[i].Flags) != s.sync {
				t.Fatalf("track %d sample %d differs", id, i)
			}
		}
	}
	// The tracks are aligned through their decode times: the video starts 33 ms
	// later.
	if got[1][0].DecodeTime != 2970 || got[2][0].DecodeTime != 0 {
		t.Errorf("first decode times %d / %d", got[1][0].DecodeTime, got[2][0].DecodeTime)
	}
}

func TestTSLargeOffsetsUseCo64(t *testing.T) {
	old := largeThreshold
	largeThreshold = 4096
	t.Cleanup(func() { largeThreshold = old })
	fx := newTSFixture(testmedia.VideoConfig{Frames: 24, Seed: 22}, testmedia.AudioConfig{Frames: 38, Seed: 23})
	out := mustAssemble(t, []Input{tsInputOf(t, KindMuxed, segments(avMuxer(false, ""), fx.units, 0.4))}, ContainerMP4)
	f := checkProgressive(t, out, []expTrack{
		{handler: "vide", timescale: 90000, samples: expVideo(fx.aus)},
		{handler: "soun", timescale: 48000, samples: expAudio(fx.frames)},
	})
	for i, trak := range f.Moov.Traks {
		if trak.Mdia.Minf.Stbl.Co64 == nil || trak.Mdia.Minf.Stbl.Stco != nil {
			t.Errorf("track %d does not use co64", i)
		}
	}
	if f.Mdat.HeaderSize() != 16 {
		t.Errorf("mdat header %d bytes", f.Mdat.HeaderSize())
	}
}

func TestTSCancellationRemovesOutput(t *testing.T) {
	fx := newTSFixture(testmedia.VideoConfig{Frames: 48, Seed: 24}, testmedia.AudioConfig{Frames: 75, Seed: 25})
	inputs := []Input{tsInputOf(t, KindMuxed, segments(avMuxer(false, ""), fx.units, 0.4))}
	out := tempOut(t)
	cancelled, completed := 0, 0
	for n := int64(0); n < 400 && completed == 0; n++ {
		ctx := &countdownCtx{Context: context.Background()}
		ctx.left.Store(n)
		_, err := Assemble(ctx, inputs, out)
		switch {
		case err == nil:
			completed++
		case errors.Is(err, context.Canceled):
			cancelled++
			assertNoOutput(t, out)
		default:
			t.Fatalf("after %d checks: %v", n, err)
		}
	}
	if cancelled < 5 || completed != 1 {
		t.Fatalf("cancelled %d times, completed %d times", cancelled, completed)
	}
}

// TestTSFragmentsChangedBetweenPasses: a fragment that changes after pass 1 fails
// the conversion, whether its size changed or only its content (with the same size
// and modification time, found by the sample sizes).
func TestTSFragmentsChangedBetweenPasses(t *testing.T) {
	fx := newTSFixture(testmedia.VideoConfig{Frames: 24, Seed: 26}, testmedia.AudioConfig{Frames: 38, Seed: 27})
	segs := segments(avMuxer(false, ""), fx.units, 0.4)
	for name, change := range map[string]func(path string){
		"size": func(p string) {
			b, _ := os.ReadFile(p)
			_ = os.WriteFile(p, append(b, testmedia.NullPackets(1)...), 0o644)
		},
		"content": func(p string) {
			st, _ := os.Stat(p)
			b, _ := os.ReadFile(p)
			// One video packet lost, a null packet at the end: the same size, but a
			// unit fewer.
			b = append(testmedia.DropPacket(b, vPID, videoPacketIndex(b, 3)+1), testmedia.NullPackets(1)...)
			_ = os.WriteFile(p, b, 0o644)
			_ = os.Chtimes(p, st.ModTime(), st.ModTime())
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := tsInputOf(t, KindMuxed, segs)
			afterTSPass1 = func() { change(in.Fragments[0]) }
			t.Cleanup(func() { afterTSPass1 = func() {} })
			out, _, err := assemble(t, []Input{in})
			if !errors.Is(err, ErrMalformed) || !strings.Contains(err.Error(), "changed between passes") {
				t.Fatalf("err = %v", err)
			}
			assertNoOutput(t, out)
		})
	}
}

func TestMDHDLanguage(t *testing.T) {
	for _, tc := range []struct {
		pmt, input, want string
	}{
		{"eng", "de", "eng"},
		{"", "deu", "deu"},
		{"", "FRA", "fra"},
		{"", "pt-BR", "pt-BR"},
		{"", "en", "en"},
		{"", "", "und"},
		{"", "1x", "und"},
		{"", "a b", "und"},
	} {
		if got := mdhdLanguage(tc.pmt, tc.input); got != tc.want {
			t.Errorf("mdhdLanguage(%q, %q) = %q, want %q", tc.pmt, tc.input, got, tc.want)
		}
	}
}
