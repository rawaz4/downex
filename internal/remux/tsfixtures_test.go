package remux

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/testmedia"
)

// Transport stream fixtures, generated with internal/testmedia.

const (
	vPID = 0x100
	aPID = 0x101
)

// avMuxer returns a muxer of an H.264 (or HEVC) video stream and an AAC stream.
func avMuxer(hevc bool, lang string) *testmedia.Muxer {
	vt := byte(testmedia.TypeH264)
	if hevc {
		vt = testmedia.TypeHEVC
	}
	return &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: vt}, {PID: aPID, Type: testmedia.TypeAAC, Language: lang}}}
}

// segments cuts units into segments at video sync units, about every seconds,
// or into segments of n units when there is no video.
func segments(m *testmedia.Muxer, units []testmedia.PES, seconds float64) [][]byte {
	for _, u := range units {
		if u.PID == vPID {
			return m.Segments(units, testmedia.SyncCut(vPID, seconds))
		}
	}
	return m.Segments(units, func(i int, _ testmedia.PES) bool { return i%10 == 0 })
}

// writeSegments writes segs into dir and returns their paths.
func writeSegments(t testing.TB, dir, name string, segs [][]byte) []string {
	t.Helper()
	var paths []string
	for i, s := range segs {
		paths = append(paths, writeFile(t, dir, name+"-"+itoa(i)+".ts", s))
	}
	return paths
}

// tsInputOf writes segs and returns them as an input of kind k.
func tsInputOf(t testing.TB, k Kind, segs [][]byte, breaks ...int) Input {
	t.Helper()
	return Input{Kind: k, Container: ContainerTS, Fragments: writeSegments(t, t.TempDir(), string(k), segs), Breaks: breaks}
}

// expVideo returns the samples a converter keeps of aus: those from the first sync
// unit on, with durations from the decode time differences (the last one
// repeating the one before) and composition offsets PTS − DTS.
func expVideo(aus []testmedia.AccessUnit) []fxSample {
	start := slices.IndexFunc(aus, func(a testmedia.AccessUnit) bool { return a.Sync })
	aus = aus[start:]
	out := make([]fxSample, len(aus))
	for i, au := range aus {
		d := uint32(3000)
		switch {
		case i+1 < len(aus):
			d = uint32(aus[i+1].DTS - au.DTS)
		case i > 0:
			d = out[i-1].dur
		}
		out[i] = fxSample{dur: d, cto: int32(au.PTS - au.DTS), sync: au.Sync, data: au.Sample}
	}
	return out
}

// expAudio returns the raw AAC samples of frames, 1024 ticks each.
func expAudio(frames []testmedia.AudioFrame) []fxSample {
	out := make([]fxSample, len(frames))
	for i, f := range frames {
		out[i] = fxSample{dur: 1024, sync: true, data: f.Raw}
	}
	return out
}

// decodeMP4 decodes an output file with mp4ff.
func decodeMP4(t testing.TB, path string) (*mp4.File, []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	return f, data
}

// sampleTimes returns the presentation time of every sample of a progressive
// track on the movie timeline, in seconds: its decode time plus composition
// offset, moved by the edit list (an empty edit, then one media edit).
func sampleTimes(t testing.TB, f *mp4.File, trak *mp4.TrakBox) []float64 {
	t.Helper()
	stbl := trak.Mdia.Minf.Stbl
	n := stbl.Stsz.GetNrSamples()
	durs, err := stbl.Stts.SampleDurations(n)
	if err != nil {
		t.Fatal(err)
	}
	var ctos []int32
	if stbl.Ctts != nil {
		if ctos, err = stbl.Ctts.CompositionTimeOffsets(n); err != nil {
			t.Fatal(err)
		}
	}
	ts := float64(trak.Mdia.Mdhd.Timescale)
	var empty, mediaTime float64
	if trak.Edts != nil {
		for _, e := range trak.Edts.Elst[0].Entries {
			if e.MediaTime == -1 {
				empty += float64(e.SegmentDuration) / float64(f.Moov.Mvhd.Timescale)
			} else {
				mediaTime = float64(e.MediaTime) / ts
				break
			}
		}
	}
	out := make([]float64, n)
	var dts uint64
	for i := range out {
		c := 0.0
		if ctos != nil {
			c = float64(ctos[i])
		}
		out[i] = empty + (float64(dts)+c)/ts - mediaTime
		dts += uint64(durs[i])
	}
	return out
}

// sampleDescriptions returns the 1-based sample description index of every
// sample of a progressive track, from its stsc.
func sampleDescriptions(t testing.TB, trak *mp4.TrakBox) []uint32 {
	t.Helper()
	stbl := trak.Mdia.Minf.Stbl
	n := stbl.Stsz.GetNrSamples()
	out := make([]uint32, n)
	for i := range out {
		c, _, err := stbl.Stsc.ChunkNrFromSampleNr(i + 1)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = stbl.Stsc.GetSampleDescriptionID(c)
	}
	return out
}

// tsFixture is a muxed H.264 + AAC stream.
type tsFixture struct {
	video  testmedia.VideoConfig
	audio  testmedia.AudioConfig
	aus    []testmedia.AccessUnit
	frames []testmedia.AudioFrame
	units  []testmedia.PES
}

func newTSFixture(v testmedia.VideoConfig, a testmedia.AudioConfig) *tsFixture {
	fx := &tsFixture{video: v, audio: a, aus: testmedia.Video(v), frames: testmedia.AAC(a)}
	fx.units = testmedia.Interleave(testmedia.VideoPES(vPID, fx.aus), testmedia.AudioPES(aPID, fx.frames, 5))
	return fx
}

func tempOut(t testing.TB) string { return filepath.Join(t.TempDir(), "out.dxpart") }
