package remux

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/media"
	"downex/internal/remux/mpegts"
)

func assemble(t *testing.T, inputs []Input) (string, string, error) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out.dxpart")
	c, err := Assemble(context.Background(), inputs, out)
	return out, c, err
}

func mustAssemble(t *testing.T, inputs []Input, wantContainer string) string {
	t.Helper()
	out, c, err := assemble(t, inputs)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if c != wantContainer {
		t.Fatalf("container %q, want %q", c, wantContainer)
	}
	return out
}

func assertNoOutput(t *testing.T, out string) {
	t.Helper()
	if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("output left behind after failure (stat: %v)", err)
	}
}

func TestSingleVideoTrack(t *testing.T) {
	fx := &fxTrack{video: true, timescale: 90000, perFrag: 24, samples: videoSamples(72, 3000, 1)}
	dir := t.TempDir()
	in := fx.writeInput(t, dir, "v")
	out := filepath.Join(dir, "out.dxpart")
	// A stale part file from an earlier attempt is replaced.
	writeFile(t, dir, "out.dxpart", bytes.Repeat([]byte("stale"), 100000))
	c, err := Assemble(context.Background(), []Input{in}, out)
	if err != nil || c != ContainerMP4 {
		t.Fatalf("Assemble = %q, %v", c, err)
	}
	f := checkProgressive(t, out, []expTrack{expect(t, fx)})
	if got := f.Moov.Traks[0].Tkhd.Duration; got != 2400 { // 72 frames at 30 fps
		t.Errorf("track duration %d ms, want 2400", got)
	}
	if !slices.Contains(f.Ftyp.CompatibleBrands(), "avc1") {
		t.Errorf("compatible brands %v lack avc1", f.Ftyp.CompatibleBrands())
	}
}

func TestVideoAndAudioTracks(t *testing.T) {
	video := &fxTrack{video: true, timescale: 90000, base: 900000, perFrag: 30, samples: videoSamples(90, 3000, 2)}
	audio := &fxTrack{timescale: 48000, base: 480000, perFrag: 47, optimize: true, samples: audioSamples(141, 3)}
	dir := t.TempDir()
	out := mustAssemble(t, []Input{video.writeInput(t, dir, "v"), audio.writeInput(t, dir, "a")}, ContainerMP4)
	f := checkProgressive(t, out, []expTrack{expect(t, video), expect(t, audio)})
	if got := f.Moov.Traks[0].Tkhd.Duration; got != 3000 {
		t.Errorf("video duration %d ms, want 3000", got)
	}
	if got := f.Moov.Traks[1].Tkhd.Duration; got != 3008 { // 141*1024/48 ms
		t.Errorf("audio duration %d ms, want 3008", got)
	}
	if f.Moov.Traks[1].Tkhd.Volume != 0x0100 {
		t.Errorf("audio volume %#x", f.Moov.Traks[1].Tkhd.Volume)
	}
	// The chunks of both tracks are interleaved in the mdat.
	type co struct {
		off   uint32
		track int
	}
	var cos []co
	for i, trak := range f.Moov.Traks {
		for _, off := range trak.Mdia.Minf.Stbl.Stco.ChunkOffset {
			cos = append(cos, co{off, i})
		}
	}
	sort.Slice(cos, func(i, j int) bool { return cos[i].off < cos[j].off })
	switches := 0
	for i := 1; i < len(cos); i++ {
		if cos[i].track != cos[i-1].track {
			switches++
		}
	}
	if switches < 4 {
		t.Errorf("tracks are not interleaved: %d switches between %d chunks", switches, len(cos))
	}
}

func TestStartOffsetsAndInitEditList(t *testing.T) {
	video := &fxTrack{video: true, timescale: 90000, perFrag: 24, elst: 3000, samples: videoSamples(72, 3000, 4)}
	audio := &fxTrack{timescale: 48000, base: 24000, perFrag: 40, samples: audioSamples(100, 5)} // starts 0.5 s later
	dir := t.TempDir()
	out := mustAssemble(t, []Input{video.writeInput(t, dir, "v"), audio.writeInput(t, dir, "a")}, ContainerMP4)
	ev, ea := expect(t, video), expect(t, audio)
	ev.elst = []mp4.ElstEntry{{SegmentDuration: (72*3000 - 3000 + 45) / 90, MediaTime: 3000, MediaRateInteger: 1}}
	ea.elst = []mp4.ElstEntry{
		{SegmentDuration: 500, MediaTime: -1, MediaRateInteger: 1},
		{SegmentDuration: (100*1024 + 24) / 48, MediaTime: 0, MediaRateInteger: 1},
	}
	f := checkProgressive(t, out, []expTrack{ev, ea})
	if got, want := f.Moov.Traks[1].Tkhd.Duration, uint64(500+(100*1024+24)/48); got != want {
		t.Errorf("audio track duration %d, want %d", got, want)
	}
}

func TestNegativeCompositionOffsets(t *testing.T) {
	samples := videoSamples(48, 3000, 6)
	for i := range samples {
		samples[i].cto -= 3000
	}
	fx := &fxTrack{video: true, timescale: 90000, perFrag: 12, samples: samples}
	out := mustAssemble(t, []Input{fx.writeInput(t, t.TempDir(), "v")}, ContainerMP4)
	e := expect(t, fx)
	e.shift = 3000
	e.elst = []mp4.ElstEntry{{SegmentDuration: 48 * 3000 / 90, MediaTime: 3000, MediaRateInteger: 1}}
	checkProgressive(t, out, []expTrack{e})
}

func TestTimelineGapsOverlapsAndDiscontinuities(t *testing.T) {
	const d = 3000
	fx := &fxTrack{video: true, timescale: 90000, perFrag: 12, samples: videoSamples(60, d, 7)}
	fx.times = []uint64{
		0,
		12*d + 2*d,                    // a gap of two frames
		0,                             // timestamps restart: a discontinuity
		12*d - 1000,                   // overlaps the previous run by 1000 ticks
		12*d - 1000 + 12*d + 20*90000, // a 20 s jump: a discontinuity
	}
	out := mustAssemble(t, []Input{fx.writeInput(t, t.TempDir(), "v")}, ContainerMP4)
	e := expect(t, fx)
	for range fx.samples {
		e.durs = append(e.durs, d)
	}
	e.durs[11] += 2 * d
	e.durs[35] -= 1000
	checkProgressive(t, out, []expTrack{e})
}

// trexFixture returns a video init whose trex gives default sample values, and
// two hand-made fragment files exercising every base data offset rule of
// ISO/IEC 14496-12 §8.8.7.1 and §8.8.8.1.
func trexFixture(t testing.TB) (init []byte, fragA, fragB []byte, samples []fxSample) {
	fx := &fxTrack{video: true, timescale: 90000}
	is := fx.initSegment(t)
	is.Moov.Mvex.Trex.DefaultSampleDuration = 3000
	is.Moov.Mvex.Trex.DefaultSampleSize = 100
	is.Moov.Mvex.Trex.DefaultSampleFlags = mp4.NonSyncSampleFlags | mp4.SampleDependsOn1
	init = encodeBox(t, is)
	rng := rand.New(rand.NewSource(9))
	mk := func(size int, dur uint32, sync bool) fxSample {
		s := fxSample{dur: dur, sync: sync, data: make([]byte, size)}
		rng.Read(s.data)
		return s
	}
	samples = []fxSample{mk(100, 3000, true), mk(100, 3000, false), mk(100, 3000, false), mk(100, 3000, false), mk(100, 3000, false)}
	// File A: mdat first; tfhd base_data_offset points at its payload, and the
	// truns have no data_offset (the second continues after the first).
	var dataA []byte
	for _, s := range samples {
		dataA = append(dataA, s.data...)
	}
	fragA = slices.Concat(
		rawBox("mdat", dataA),
		rawBox("moof",
			fullBox("mfhd", 0, 0, be32(1)),
			rawBox("traf",
				fullBox("tfhd", 0, tfhdBaseDataOffset, be32(1), be64(8)),
				fullBox("tfdt", 0, 0, be32(0)),
				fullBox("trun", 0, trunFirstSampleFlags, be32(2), be32(mp4.SyncSampleFlags)),
				fullBox("trun", 0, 0, be32(3)))))
	// File B: no base offset flags at all. The first traf is based at the moof
	// (data_offset given), the second continues where the first traf's data ends.
	b1, b2, b3 := mk(50, 3000, false), mk(70, 3000, false), mk(100, 1500, false)
	samples = append(samples, b1, b2, b3)
	moof := func(dataOffset uint32) []byte {
		return rawBox("moof",
			fullBox("mfhd", 0, 0, be32(2)),
			rawBox("traf",
				fullBox("tfhd", 0, 0, be32(1)),
				fullBox("tfdt", 1, 0, be64(15000)),
				fullBox("trun", 0, trunDataOffset|trunSize, be32(2), be32(dataOffset), be32(50), be32(70))),
			rawBox("traf",
				fullBox("tfhd", 0, tfhdDefaultDuration, be32(1), be32(1500)),
				fullBox("trun", 0, 0, be32(1))))
	}
	m := moof(0)
	fragB = slices.Concat(moof(uint32(len(m)+8)), rawBox("mdat", b1.data, b2.data, b3.data))
	return init, fragA, fragB, samples
}

func TestTrexDefaultsAndBaseDataOffsets(t *testing.T) {
	init, fragA, fragB, samples := trexFixture(t)
	dir := t.TempDir()
	in := Input{Kind: KindVideo, Container: ContainerFMP4, Init: writeFile(t, dir, "init.mp4", init),
		Fragments: []string{writeFile(t, dir, "a.m4s", fragA), writeFile(t, dir, "b.m4s", fragB)}}
	out := mustAssemble(t, []Input{in}, ContainerMP4)
	fx := &fxTrack{video: true, timescale: 90000}
	e := expect(t, fx)
	e.samples = samples
	checkProgressive(t, out, []expTrack{e})
}

func TestMuxedInput(t *testing.T) {
	video := &fxTrack{video: true, timescale: 90000, samples: videoSamples(90, 3000, 10)}
	audio := &fxTrack{timescale: 48000, trackID: 2, samples: audioSamples(141, 11)}
	init := video.initSegment(t)
	at := init.AddEmptyTrack(48000, "audio", "und")
	if err := at.SetAACDescriptor(2, 48000); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in := Input{Kind: KindMuxed, Container: ContainerFMP4, Init: writeFile(t, dir, "init.mp4", encodeBox(t, init))}
	var vt, atime uint64
	for f := 0; f < 3; f++ {
		frag, err := mp4.CreateMultiTrackFragment(uint32(f+1), []uint32{1, 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range video.samples[f*30 : f*30+30] {
			fs := mp4.FullSample{Sample: mp4.Sample{Flags: sampleFlags(s), Dur: s.dur, Size: uint32(len(s.data)), CompositionTimeOffset: s.cto}, DecodeTime: vt, Data: s.data}
			if err := frag.AddFullSampleToTrack(fs, 1); err != nil {
				t.Fatal(err)
			}
			vt += uint64(s.dur)
		}
		for _, s := range audio.samples[f*47 : f*47+47] {
			fs := mp4.FullSample{Sample: mp4.Sample{Flags: sampleFlags(s), Dur: s.dur, Size: uint32(len(s.data))}, DecodeTime: atime, Data: s.data}
			if err := frag.AddFullSampleToTrack(fs, 2); err != nil {
				t.Fatal(err)
			}
			atime += uint64(s.dur)
		}
		in.Fragments = append(in.Fragments, writeFile(t, dir, itoa(f)+".m4s", encodeBox(t, frag)))
	}
	out := mustAssemble(t, []Input{in}, ContainerMP4)
	ev := expect(t, video)
	ea := expTrack{handler: "soun", timescale: 48000, samples: audio.samples, stsd: encodeBox(t, init.Moov.Traks[1].Mdia.Minf.Stbl.Stsd)}
	checkProgressive(t, out, []expTrack{ev, ea})
}

func TestInitInFragmentFilesAndRepeats(t *testing.T) {
	fx := &fxTrack{video: true, timescale: 90000, perFrag: 12, samples: videoSamples(36, 3000, 12)}
	init := encodeBox(t, fx.initSegment(t))
	frags := fx.fragmentBytes(t)
	dir := t.TempDir()
	in := Input{Kind: KindVideo, Container: ContainerFMP4, Fragments: []string{
		writeFile(t, dir, "0.m4s", slices.Concat(init, frags[0])),
		writeFile(t, dir, "1.m4s", frags[1]),
		writeFile(t, dir, "2.m4s", slices.Concat(init, frags[2])), // the same init again
	}}
	out := mustAssemble(t, []Input{in}, ContainerMP4)
	checkProgressive(t, out, []expTrack{expect(t, fx)})

	// A different initialization segment mid-stream is not handled.
	other := &fxTrack{video: true, timescale: 30000}
	in.Fragments[2] = writeFile(t, dir, "2b.m4s", slices.Concat(encodeBox(t, other.initSegment(t)), frags[2]))
	out, _, err := assemble(t, []Input{in})
	if !errors.Is(err, ErrUnsupported) || !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("changed init: %v, want ErrUnsupported", err)
	}
	assertNoOutput(t, out)
}

func TestFragmentedFallback(t *testing.T) {
	old := maxProgressiveSamples
	maxProgressiveSamples = 50
	t.Cleanup(func() { maxProgressiveSamples = old })

	video := &fxTrack{video: true, timescale: 90000, perFrag: 24, elst: 3000, samples: videoSamples(72, 3000, 13)}
	audio := &fxTrack{timescale: 48000, base: 24000, perFrag: 40, optimize: true, samples: audioSamples(100, 14)}
	dir := t.TempDir()
	out := mustAssemble(t, []Input{video.writeInput(t, dir, "v"), audio.writeInput(t, dir, "a")}, ContainerMP4)

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	f, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !f.IsFragmented() || f.Init == nil || f.Init.Moov.Mvex == nil || len(f.Init.Moov.Mvex.Trexs) != 2 {
		t.Fatal("output is not a two-track fragmented MP4")
	}
	moov := f.Init.Moov
	if moov.Mvex.Mehd == nil || moov.Mvex.Mehd.FragmentDuration != 2633 { // audio: 500 ms + 100*1024/48 ms
		t.Errorf("mehd = %+v", moov.Mvex.Mehd)
	}
	if e := moov.Traks[0].Edts; e == nil || e.Elst[0].Entries[0].MediaTime != 3000 {
		t.Errorf("video edit list not kept: %+v", e)
	}
	trex := map[uint32]*mp4.TrexBox{}
	for _, tr := range moov.Mvex.Trexs {
		trex[tr.TrackID] = tr
	}
	got := map[uint32][]mp4.FullSample{}
	var lastStart float64
	nFrags := 0
	for _, seg := range f.Segments {
		for _, frag := range seg.Fragments {
			nFrags++
			id := frag.Moof.Traf.Tfhd.TrackID
			fs, err := frag.GetFullSamples(trex[id])
			if err != nil {
				t.Fatal(err)
			}
			ts := 90000.0
			if id == 2 {
				ts = 48000
			}
			start := float64(fs[0].DecodeTime) / ts
			if start < lastStart {
				t.Errorf("fragments not in decode-time order: %.3f after %.3f", start, lastStart)
			}
			lastStart = start
			got[id] = append(got[id], fs...)
		}
	}
	if nFrags != 3+3 {
		t.Errorf("%d fragments, want 6", nFrags)
	}
	for id, want := range map[uint32]*fxTrack{1: video, 2: audio} {
		g := got[id]
		if len(g) != len(want.samples) {
			t.Fatalf("track %d: %d samples, want %d", id, len(g), len(want.samples))
		}
		dt := uint64(0)
		if id == 2 {
			dt = 24000
		}
		for i, s := range want.samples {
			if !bytes.Equal(g[i].Data, s.data) || g[i].Dur != s.dur || g[i].CompositionTimeOffset != s.cto || g[i].DecodeTime != dt {
				t.Fatalf("track %d sample %d differs: dt %d want %d", id, i, g[i].DecodeTime, dt)
			}
			if mp4.IsSyncSampleFlags(g[i].Flags) != s.sync && id == 1 {
				t.Fatalf("track %d sample %d: sync flag differs", id, i)
			}
			dt += uint64(s.dur)
		}
	}
}

func TestLargeOffsetsUseCo64AndLargesize(t *testing.T) {
	old := largeThreshold
	largeThreshold = 4096
	t.Cleanup(func() { largeThreshold = old })
	video := &fxTrack{video: true, timescale: 90000, perFrag: 24, samples: videoSamples(48, 3000, 15)}
	audio := &fxTrack{timescale: 48000, perFrag: 40, samples: audioSamples(80, 16)}
	dir := t.TempDir()
	out := mustAssemble(t, []Input{video.writeInput(t, dir, "v"), audio.writeInput(t, dir, "a")}, ContainerMP4)
	f := checkProgressive(t, out, []expTrack{expect(t, video), expect(t, audio)})
	for i, trak := range f.Moov.Traks {
		if trak.Mdia.Minf.Stbl.Co64 == nil || trak.Mdia.Minf.Stbl.Stco != nil {
			t.Errorf("track %d does not use co64", i)
		}
	}
	if f.Mdat.HeaderSize() != 16 {
		t.Errorf("mdat header %d bytes, want a 16-byte largesize header", f.Mdat.HeaderSize())
	}
}

func tsPackets(rng *rand.Rand, n int) []byte {
	out := make([]byte, 0, n*188)
	for i := 0; i < n; i++ {
		p := make([]byte, 188)
		rng.Read(p)
		p[0] = mpegts.SyncByte
		out = append(out, p...)
	}
	return out
}

// TestConcatTS: the legacy byte-for-byte join of one transport stream track, kept
// for downloads stored as .ts.
func TestConcatTS(t *testing.T) {
	rng := rand.New(rand.NewSource(20))
	dir := t.TempDir()
	init := tsPackets(rng, 2)
	in := Input{Kind: KindMuxed, Container: ContainerTS, Init: writeFile(t, dir, "init.ts", init)}
	want := slices.Clone(init)
	for i := 0; i < 4; i++ {
		seg := tsPackets(rng, 50+i*7)
		if i == 3 {
			seg = seg[:len(seg)-11] // a segment need not end on a packet boundary
		}
		want = append(want, seg...)
		in.Fragments = append(in.Fragments, writeFile(t, dir, itoa(i)+".ts", seg))
	}
	out := filepath.Join(t.TempDir(), "out.dxpart")
	if err := ConcatTS(context.Background(), in, out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("concatenation differs: %d bytes, want %d", len(got), len(want))
	}

	in.Init = ""
	if err := ConcatTS(context.Background(), in, out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, want[len(init):]) {
		t.Fatal("concatenation without init differs")
	}

	// A fragment that is not a transport stream fails, and the output is removed.
	bad := in
	bad.Fragments = append(slices.Clone(in.Fragments), writeFile(t, dir, "bad.ts", []byte("<html>")))
	if err := ConcatTS(context.Background(), bad, out); !errors.Is(err, ErrMalformed) || !strings.Contains(err.Error(), "fragment 4") {
		t.Fatalf("err = %v, want ErrMalformed in fragment 4", err)
	}
	assertNoOutput(t, out)
	if err := ConcatTS(context.Background(), Input{Kind: KindVideo, Container: ContainerFMP4, Fragments: in.Fragments}, out); !errors.Is(err, ErrInvalid) {
		t.Fatalf("fMP4 input: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ConcatTS(ctx, in, out); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	assertNoOutput(t, out)
}

func TestUnsupportedCombinations(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	dir := t.TempDir()
	ts := Input{Kind: KindVideo, Container: ContainerTS, Fragments: []string{writeFile(t, dir, "v.ts", tsPackets(rng, 3))}}
	tsAudio := Input{Kind: KindAudio, Container: ContainerTS, Fragments: []string{writeFile(t, dir, "a.ts", tsPackets(rng, 3))}}
	fx := &fxTrack{video: true, timescale: 90000, perFrag: 12, samples: videoSamples(12, 3000, 22)}
	fm := fx.writeInput(t, dir, "v")
	webm := Input{Kind: KindVideo, Container: "webm", Fragments: []string{writeFile(t, dir, "v.webm", []byte{0x1a, 0x45, 0xdf, 0xa3})}}
	tsMuxed := ts
	tsMuxed.Kind = KindMuxed
	for name, inputs := range map[string][]Input{
		"ts muxed + ts audio": {tsMuxed, tsAudio},
		"ts video + ts video": {ts, ts},
		"three ts tracks":     {ts, tsAudio, tsAudio},
		"ts + fmp4":           {ts, fm},
		"webm":                {webm},
		"fmp4 + webm":         {fm, webm},
		"unknown container":   {{Kind: KindVideo, Container: "unknown", Fragments: fm.Fragments}},
	} {
		t.Run(name, func(t *testing.T) {
			out, _, err := assemble(t, inputs)
			if !errors.Is(err, ErrUnsupported) || !errors.Is(err, errors.ErrUnsupported) {
				t.Fatalf("err = %v, want ErrUnsupported", err)
			}
			assertNoOutput(t, out)
		})
	}
}

func TestInvalidArguments(t *testing.T) {
	dir := t.TempDir()
	frag := writeFile(t, dir, "a.ts", []byte{mpegts.SyncByte})
	for name, tc := range map[string]struct {
		inputs []Input
		out    string
	}{
		"no inputs":      {nil, filepath.Join(dir, "o")},
		"no output":      {[]Input{{Kind: KindVideo, Container: ContainerTS, Fragments: []string{frag}}}, ""},
		"unknown kind":   {[]Input{{Kind: "subtitles", Container: ContainerTS, Fragments: []string{frag}}}, filepath.Join(dir, "o")},
		"no fragments":   {[]Input{{Kind: KindVideo, Container: ContainerTS}}, filepath.Join(dir, "o")},
		"empty path":     {[]Input{{Kind: KindVideo, Container: ContainerTS, Fragments: []string{""}}}, filepath.Join(dir, "o")},
		"output = input": {[]Input{{Kind: KindVideo, Container: ContainerTS, Fragments: []string{frag}}}, frag},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Assemble(context.Background(), tc.inputs, tc.out); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	if got, _ := os.ReadFile(frag); !bytes.Equal(got, []byte{mpegts.SyncByte}) {
		t.Fatal("an input was modified")
	}
}

func TestMalformedInputs(t *testing.T) {
	fx := &fxTrack{video: true, timescale: 90000, perFrag: 12, samples: videoSamples(24, 3000, 23)}
	init := encodeBox(t, fx.initSegment(t))
	frags := fx.fragmentBytes(t)
	encInit := bytes.Replace(init, []byte("avc1"), []byte("encv"), 1)
	sencFrag := slices.Concat(rawBox("moof", fullBox("mfhd", 0, 0, be32(1)),
		rawBox("traf", fullBox("tfhd", 0, tfhdDefaultBaseIsMoof, be32(1)), fullBox("senc", 0, 0, be32(0)))), rawBox("mdat"))
	// moofOnly builds a moof with one traf, followed by an mdat of 64 bytes; at
	// marks the payload start of that mdat (relative to the moof) in trun fields.
	moofOnly := func(traf ...[]byte) []byte {
		build := func(at uint32) []byte {
			parts := make([][]byte, len(traf))
			for i, b := range traf {
				parts[i] = bytes.ReplaceAll(b, be32(0xa7a7a7a7), be32(at))
			}
			return rawBox("moof", fullBox("mfhd", 0, 0, be32(1)), rawBox("traf", parts...))
		}
		m := build(0)
		return slices.Concat(build(uint32(len(m)+8)), rawBox("mdat", make([]byte, 64)))
	}
	at := be32(0xa7a7a7a7)
	cases := map[string]struct {
		init  []byte
		frags [][]byte
		want  error
	}{
		"truncated fragment":    {init, [][]byte{frags[0][:len(frags[0])-10]}, ErrMalformed},
		"garbage fragment":      {init, [][]byte{bytes.Repeat([]byte{0xff, 0x13, 0x00, 0x07}, 100)}, ErrMalformed},
		"error page":            {init, [][]byte{[]byte("<html><body>403 Forbidden</body></html>")}, ErrMalformed},
		"empty fragment":        {init, [][]byte{{}}, ErrMalformed},
		"init without moov":     {rawBox("ftyp", []byte("isom"), be32(0)), [][]byte{frags[0]}, ErrMalformed},
		"fragment before moov":  {nil, [][]byte{frags[0]}, ErrMalformed},
		"encrypted init":        {encInit, [][]byte{frags[0]}, media.ErrEncrypted},
		"sample encryption":     {init, [][]byte{sencFrag}, media.ErrEncrypted},
		"pssh in fragment":      {init, [][]byte{slices.Concat(rawBox("moof", fullBox("pssh", 0, 0, make([]byte, 20))), rawBox("mdat"))}, media.ErrEncrypted},
		"unknown track":         {init, [][]byte{moofOnly(fullBox("tfhd", 0, tfhdDefaultBaseIsMoof, be32(7)), fullBox("trun", 0, trunDataOffset|trunSize, be32(1), at, be32(10)))}, ErrMalformed},
		"sample past the mdat":  {init, [][]byte{moofOnly(fullBox("tfhd", 0, tfhdDefaultBaseIsMoof, be32(1)), fullBox("trun", 0, trunDataOffset|trunSize, be32(2), at, be32(60), be32(10)))}, ErrMalformed},
		"data outside mdat":     {init, [][]byte{moofOnly(fullBox("tfhd", 0, tfhdDefaultBaseIsMoof, be32(1)), fullBox("trun", 0, trunDataOffset|trunSize, be32(1), be32(0), be32(10)))}, ErrMalformed},
		"data past the file":    {init, [][]byte{moofOnly(fullBox("tfhd", 0, tfhdDefaultBaseIsMoof, be32(1)), fullBox("trun", 0, trunDataOffset|trunSize, be32(1), be32(1000), be32(10)))}, ErrMalformed},
		"bad description index": {init, [][]byte{moofOnly(fullBox("tfhd", 0, tfhdDefaultBaseIsMoof|tfhdSampleDescIndex, be32(1), be32(2)), fullBox("trun", 0, trunDataOffset|trunSize, be32(1), at, be32(10)))}, ErrMalformed},
		"trun too short":        {init, [][]byte{moofOnly(fullBox("tfhd", 0, tfhdDefaultBaseIsMoof, be32(1)), fullBox("trun", 0, trunSize, be32(1000)))}, ErrMalformed},
		"trun before tfhd":      {init, [][]byte{moofOnly(fullBox("trun", 0, trunSize, be32(1), be32(1)))}, ErrMalformed},
		"huge default samples":  {init, [][]byte{moofOnly(fullBox("tfhd", 0, tfhdDefaultBaseIsMoof|tfhdDefaultSize, be32(1), be32(1000)), fullBox("trun", 0, 0, be32(1<<30)))}, ErrMalformed},
		"no samples":            {init, [][]byte{rawBox("styp", []byte("msdh"), be32(0))}, ErrMalformed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			in := Input{Kind: KindVideo, Container: ContainerFMP4}
			if tc.init != nil {
				in.Init = writeFile(t, dir, "init.mp4", tc.init)
			}
			for i, f := range tc.frags {
				in.Fragments = append(in.Fragments, writeFile(t, dir, itoa(i)+".m4s", f))
			}
			out, _, err := assemble(t, []Input{in})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			t.Log(err)
			assertNoOutput(t, out)
		})
	}

	t.Run("missing file", func(t *testing.T) {
		in := Input{Kind: KindVideo, Container: ContainerFMP4, Fragments: []string{filepath.Join(t.TempDir(), "nope.m4s")}}
		out, _, err := assemble(t, []Input{in})
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err = %v, want ErrNotExist", err)
		}
		assertNoOutput(t, out)
	})
	t.Run("transport stream error page", func(t *testing.T) {
		dir := t.TempDir()
		in := Input{Kind: KindMuxed, Container: ContainerTS, Fragments: []string{writeFile(t, dir, "a.ts", []byte("<html>"))}}
		out, _, err := assemble(t, []Input{in})
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("err = %v, want ErrMalformed", err)
		}
		assertNoOutput(t, out)
	})
}

// countdownCtx is cancelled after a given number of Err calls.
type countdownCtx struct {
	context.Context
	left atomic.Int64
}

func (c *countdownCtx) Err() error {
	if c.left.Add(-1) < 0 {
		return context.Canceled
	}
	return nil
}

func (c *countdownCtx) Done() <-chan struct{} { return nil }
func (c *countdownCtx) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func TestCancellationRemovesOutput(t *testing.T) {
	video := &fxTrack{video: true, timescale: 90000, perFrag: 24, samples: videoSamples(72, 3000, 24)}
	audio := &fxTrack{timescale: 48000, perFrag: 47, samples: audioSamples(141, 25)}
	dir := t.TempDir()
	inputs := []Input{video.writeInput(t, dir, "v"), audio.writeInput(t, dir, "a")}
	cancelled, completed := 0, 0
	for n := int64(0); n < 200 && completed == 0; n++ {
		ctx := &countdownCtx{Context: context.Background()}
		ctx.left.Store(n)
		out := filepath.Join(dir, "out.dxpart")
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

func TestReadTopBoxesLargesizeAndToEnd(t *testing.T) {
	data := slices.Concat(
		be32(1), []byte("free"), be64(16+4), []byte{1, 2, 3, 4}, // largesize
		be32(0), []byte("mdat"), []byte{9, 9, 9}, // to the end of the file
	)
	boxes, err := readTopBoxes(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 2 || boxes[0].hdr != 16 || boxes[0].size != 20 || boxes[1].typ != "mdat" || boxes[1].size != 11 {
		t.Fatalf("boxes = %+v", boxes)
	}
	for _, bad := range [][]byte{
		{0, 0, 0, 4, 'f', 'r', 'e', 'e'},             // smaller than its header
		{0, 0, 0, 9, 'f', 'r', 'e', 'e'},             // past the end
		{0, 0, 0, 1, 'f', 'r', 'e', 'e', 0, 0},       // truncated largesize
		{0, 0, 0, 8, 'f', 'r', 'e', 'e', 0, 0, 0, 0}, // trailing bytes
	} {
		if _, err := readTopBoxes(bytes.NewReader(bad), int64(len(bad))); !errors.Is(err, ErrMalformed) {
			t.Errorf("%x: err = %v", bad, err)
		}
	}
	var _ io.ReaderAt = bytes.NewReader(nil)
}

// TestAgreesWithMP4FFDefragmenter compares the sample tables of a remuxed
// single track with those mp4ff's own defragmenter derives from the same
// fragmented stream (init + fragments concatenated), an independent reading of
// the track fragment boxes.
func TestAgreesWithMP4FFDefragmenter(t *testing.T) {
	fx := &fxTrack{video: true, timescale: 90000, base: 123456, perFrag: 20, optimize: true, samples: videoSamples(100, 3000, 40)}
	dir := t.TempDir()
	in := fx.writeInput(t, dir, "v")
	out := mustAssemble(t, []Input{in}, ContainerMP4)

	var whole []byte
	for _, p := range append([]string{in.Init}, in.Fragments...) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		whole = append(whole, b...)
	}
	rs := bytes.NewReader(whole)
	src, err := mp4.DecodeFile(rs, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		t.Fatal(err)
	}
	var ref bytes.Buffer
	if err := mp4.Defragment(src, rs, &ref); err != nil {
		t.Fatal(err)
	}
	samplesOf := func(data []byte) ([]mp4.Sample, []byte) {
		r := bytes.NewReader(data)
		f, err := mp4.DecodeFile(r, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
		if err != nil {
			t.Fatal(err)
		}
		trak := f.Moov.Traks[0]
		n := trak.GetNrSamples()
		ss, err := trak.GetSampleData(1, n)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := f.ReadSampleData(trak, 1, n, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := range ss {
			ss[i].Flags = boolToFlag(mp4.DecodeSampleFlags(ss[i].Flags).SampleIsNonSync)
		}
		return ss, payload
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	gs, gp := samplesOf(got)
	rsamp, rp := samplesOf(ref.Bytes())
	if !slices.Equal(gs, rsamp) {
		t.Fatalf("sample tables differ from mp4ff's defragmenter:\n got %v\nwant %v", gs[:5], rsamp[:5])
	}
	if !bytes.Equal(gp, rp) {
		t.Fatal("sample data differs from mp4ff's defragmenter")
	}
}

func boolToFlag(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// AV1 and VP9 video in fragmented MP4 (what some social sites' players fetch,
// docs/SITE-MEDIA-SPEC.md §2.9) joins with AAC audio: the sample entries and their
// configuration boxes are carried as they are. This is the gate that lets a played save
// offer those files.
func TestJoinAV1AndVP9(t *testing.T) {
	for _, codec := range []string{"av01", "vp09"} {
		video := &fxTrack{video: true, codec: codec, timescale: 90000, perFrag: 30, samples: videoSamples(90, 3000, 4)}
		audio := &fxTrack{timescale: 48000, perFrag: 47, samples: audioSamples(141, 5)}
		dir := t.TempDir()
		out := mustAssemble(t, []Input{video.writeInput(t, dir, "v"), audio.writeInput(t, dir, "a")}, ContainerMP4)
		f := checkProgressive(t, out, []expTrack{expect(t, video), expect(t, audio)})
		stsd := f.Moov.Traks[0].Mdia.Minf.Stbl.Stsd
		if len(stsd.Children) != 1 || stsd.Children[0].Type() != codec {
			t.Fatalf("%s: sample entry %v", codec, stsd.Children)
		}
	}
}
