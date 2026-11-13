package remux

import (
	"bytes"
	"math"
	"os"
	"slices"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/testmedia"
)

// TestTSMuxedH264AAC converts a muxed H.264 (with B-frames) and AAC transport
// stream whose timestamps wrap past 2^33 into a progressive MP4 and checks it
// with mp4ff: every table, the sample descriptions and every payload.
func TestTSMuxedH264AAC(t *testing.T) {
	const start = tsWrap - 90000 // the timestamps wrap one second in
	fx := newTSFixture(
		testmedia.VideoConfig{Frames: 60, BFrames: true, SEI: true, StartDTS: start, Seed: 1},
		testmedia.AudioConfig{Frames: 94, StartPTS: start, Seed: 2})
	segs := segments(avMuxer(false, "eng"), fx.units, 0.4)
	if len(segs) < 4 {
		t.Fatalf("%d segments", len(segs))
	}
	in := tsInputOf(t, KindMuxed, segs)
	out := mustAssemble(t, []Input{in}, ContainerMP4)

	ev := expTrack{handler: "vide", timescale: 90000, samples: expVideo(fx.aus)}
	// The audio presents first (at the first PTS); the video's first picture is
	// shown one frame after its decode time.
	ev.elst = []mp4.ElstEntry{
		{SegmentDuration: 33, MediaTime: -1, MediaRateInteger: 1},
		{SegmentDuration: 2000, MediaTime: 3000, MediaRateInteger: 1},
	}
	ea := expTrack{handler: "soun", timescale: 48000, samples: expAudio(fx.frames)}
	f := checkProgressive(t, out, []expTrack{ev, ea})

	vt, at := f.Moov.Traks[0], f.Moov.Traks[1]
	avc := vt.Mdia.Minf.Stbl.Stsd.AvcX
	if avc == nil || len(vt.Mdia.Minf.Stbl.Stsd.Children) != 1 || avc.Width != 640 || avc.Height != 360 {
		t.Fatalf("video sample entry %+v", vt.Mdia.Minf.Stbl.Stsd.Children)
	}
	ps := fx.video.ParamSets()
	if !slices.EqualFunc(avc.AvcC.SPSnalus, ps[:1], bytes.Equal) || !slices.EqualFunc(avc.AvcC.PPSnalus, ps[1:], bytes.Equal) {
		t.Error("avcC does not hold the stream's parameter sets")
	}
	if vt.Tkhd.Width != 640<<16 || vt.Tkhd.Height != 360<<16 {
		t.Errorf("tkhd size %#x×%#x", vt.Tkhd.Width, vt.Tkhd.Height)
	}
	mp4a := at.Mdia.Minf.Stbl.Stsd.Mp4a
	if mp4a == nil || mp4a.Esds == nil || mp4a.ChannelCount != 2 || mp4a.SampleRate != 48000 {
		t.Fatalf("audio sample entry %+v", at.Mdia.Minf.Stbl.Stsd.Children)
	}
	if asc := mp4a.Esds.DecConfigDescriptor.DecSpecificInfo.DecConfig; !bytes.Equal(asc, testmedia.AudioSpecificConfig(fx.audio)) {
		t.Errorf("AudioSpecificConfig %x", asc)
	}
	if got := at.Mdia.Mdhd.GetLanguage(); got != "eng" {
		t.Errorf("audio language %q", got)
	}
	if got := vt.Mdia.Mdhd.GetLanguage(); got != "und" {
		t.Errorf("video language %q", got)
	}
	if !slices.Contains(f.Ftyp.CompatibleBrands(), "avc1") {
		t.Error("no avc1 brand")
	}
	// Every sample uses the one description; the tracks are interleaved.
	for _, sdi := range sampleDescriptions(t, vt) {
		if sdi != 1 {
			t.Fatal("a sample refers to another description")
		}
	}
	// Chunks of at most a second, interleaved by time: audio (from 0 s), video (from
	// 0.033 s), audio, video, audio.
	vo, ao := vt.Mdia.Minf.Stbl.Stco.ChunkOffset, at.Mdia.Minf.Stbl.Stco.ChunkOffset
	if len(vo) != 2 || len(ao) != 3 || !(ao[0] < vo[0] && vo[0] < ao[1] && ao[1] < vo[1] && vo[1] < ao[2]) {
		t.Errorf("chunk offsets video %v, audio %v", vo, ao)
	}
	if f.Moov.Mvhd.Duration != 2033 {
		t.Errorf("movie duration %d ms", f.Moov.Mvhd.Duration)
	}
	// Video and audio start together on the movie timeline (the video picture one
	// frame after the first audio sample, as in the stream).
	vts, ats := sampleTimes(t, f, vt), sampleTimes(t, f, at)
	if d := slices.Min(vts) - ats[0]; math.Abs(d-1.0/30) > 0.001 {
		t.Errorf("video starts %.4f s after the audio", d)
	}
	// The conversion is deterministic.
	again := mustAssemble(t, []Input{in}, ContainerMP4)
	a, _ := os.ReadFile(out)
	b, _ := os.ReadFile(again)
	if !bytes.Equal(a, b) {
		t.Error("two conversions differ")
	}
}
