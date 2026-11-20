package remux

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/testmedia"
)

// windowOut assembles inputs cut to w.
func windowOut(t *testing.T, inputs []Input, w Window) (string, Result, error) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out.dxpart")
	res, err := AssembleWindow(context.Background(), inputs, out, w)
	return out, res, err
}

// trackOf returns the decoded track with handler h.
func trackOf(t *testing.T, f *mp4.File, h string) *mp4.TrakBox {
	t.Helper()
	for _, trak := range f.Moov.Traks {
		if trak.Mdia.Hdlr.HandlerType == h {
			return trak
		}
	}
	t.Fatalf("no %s track", h)
	return nil
}

// visible returns the presentation times (seconds, movie timeline) of the samples of
// trak that its edit list shows, and the edit's span.
func editOf(t *testing.T, f *mp4.File, trak *mp4.TrakBox) (empty, mediaTime, dur float64) {
	t.Helper()
	if trak.Edts == nil || len(trak.Edts.Elst) == 0 {
		t.Fatal("no edit list")
	}
	ts := float64(trak.Mdia.Mdhd.Timescale)
	mts := float64(f.Moov.Mvhd.Timescale)
	for _, e := range trak.Edts.Elst[0].Entries {
		if e.MediaTime == -1 {
			empty += float64(e.SegmentDuration) / mts
			continue
		}
		return empty, float64(e.MediaTime) / ts, float64(e.SegmentDuration) / mts
	}
	t.Fatal("no media edit")
	return 0, 0, 0
}

func isSyncSample(trak *mp4.TrakBox, nr uint32) bool {
	stss := trak.Mdia.Minf.Stbl.Stss
	return stss == nil || stss.IsSyncSample(nr)
}

// A window of a fragmented MP4 video + audio pair (docs/MEDIA-SPEC.md §8 C6–C8): the
// video starts at the keyframe at or before Start, audio at that keyframe, the edit
// lists show exactly [Start, End), no sample at or after End is kept, and the sample
// data is the source's.
func TestWindowFMP4(t *testing.T) {
	video := &fxTrack{video: true, timescale: 90000, perFrag: 24, samples: videoSamples(90, 3000, 41)}
	audio := &fxTrack{timescale: 48000, perFrag: 47, samples: audioSamples(141, 42)}
	dir := t.TempDir()
	inputs := []Input{video.writeInput(t, dir, "v"), audio.writeInput(t, dir, "a")}
	out, res, err := windowOut(t, inputs, Window{Start: time.Second, End: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	// Sync samples every 12 frames (0.4 s), presented one frame after their decode
	// time: the one at or before 1.0 s is sample 24, presented at 0.8333 s.
	wantK := time.Duration(24*3000+3000) * time.Second / 90000
	if res.Keyframe != wantK || !res.Exact || res.Container != ContainerMP4 {
		t.Errorf("result %+v, want keyframe %v", res, wantK)
	}
	f, data := decodeMP4(t, out)
	vt, at := trackOf(t, f, "vide"), trackOf(t, f, "soun")
	n := vt.Mdia.Minf.Stbl.Stsz.GetNrSamples()
	if n != 36 { // decode times 0.8 s .. < 2.0 s: samples 24..59
		t.Errorf("%d video samples, want 36", n)
	}
	if !isSyncSample(vt, 1) {
		t.Error("the first video sample is not a sync sample")
	}
	got := payloadsOf(t, f, data, vt)
	for i := range got {
		if !bytes.Equal(got[i], video.samples[24+i].data) {
			t.Fatalf("video sample %d differs from the source", i)
		}
	}
	empty, mt, dur := editOf(t, f, vt)
	if empty != 0 || math.Abs(mt-(1.0-0.8)) > 1e-9 || dur != 1.0 {
		t.Errorf("video edit: empty %v media %v duration %v; want 0, 0.2, 1.0", empty, mt, dur)
	}
	// Audio: from the frame that holds the keyframe time; its edit starts at Start too.
	frame := 1024.0 / 48000
	k := wantK.Seconds()
	first := int(k / frame)
	ga := payloadsOf(t, f, data, at)
	if !bytes.Equal(ga[0], audio.samples[first].data) {
		t.Errorf("audio does not start with frame %d", first)
	}
	if last := first + len(ga) - 1; float64(last)*frame >= 2.0 || float64(last+1)*frame < 2.0 {
		t.Errorf("audio ends with frame %d", last)
	}
	empty, mt, dur = editOf(t, f, at)
	if empty != 0 || math.Abs(mt-(1.0-float64(first)*frame)) > 1.0/48000 || math.Abs(dur-1.0) > 0.002 {
		t.Errorf("audio edit: empty %v media %v duration %v", empty, mt, dur)
	}
	if d := f.Moov.Mvhd.Duration; d != 1000 {
		t.Errorf("movie duration %d ms, want 1000", d)
	}
}

// An open end keeps everything after the keyframe; OriginMedia counts from the media
// timeline of the inputs, here fragments that begin at 10 s.
func TestWindowOpenEndAndMediaOrigin(t *testing.T) {
	video := &fxTrack{video: true, timescale: 90000, base: 900000, perFrag: 24, samples: videoSamples(72, 3000, 43)}
	in := video.writeInput(t, t.TempDir(), "v")
	out, res, err := windowOut(t, []Input{in}, Window{Start: 10500 * time.Millisecond, Origin: OriginMedia})
	if err != nil {
		t.Fatal(err)
	}
	if want := 10*time.Second + time.Duration(12*3000+3000)*time.Second/90000; res.Keyframe != want {
		t.Errorf("keyframe %v, want %v", res.Keyframe, want)
	}
	f, _ := decodeMP4(t, out)
	vt := trackOf(t, f, "vide")
	if n := vt.Mdia.Minf.Stbl.Stsz.GetNrSamples(); n != 60 {
		t.Errorf("%d samples, want 60", n)
	}
	_, mt, dur := editOf(t, f, vt)
	if math.Abs(mt-(0.5-0.4)) > 1e-9 || math.Abs(dur-(2.4-0.5)) > 0.002 {
		t.Errorf("edit media %v duration %v", mt, dur)
	}
}

// Without a sync sample at or before Start the window is ErrNoKeyframe, so the caller
// can fetch earlier media (C5); Late starts at the next keyframe instead (C6).
func TestWindowNoKeyframe(t *testing.T) {
	samples := videoSamples(48, 3000, 44)
	for i := range 12 {
		samples[i].sync = false
	}
	video := &fxTrack{video: true, timescale: 90000, perFrag: 24, samples: samples}
	in := video.writeInput(t, t.TempDir(), "v")
	out, _, err := windowOut(t, []Input{in}, Window{Start: 200 * time.Millisecond})
	if !errors.Is(err, ErrNoKeyframe) {
		t.Fatalf("%v, want ErrNoKeyframe", err)
	}
	assertNoOutput(t, out)
	out, res, err := windowOut(t, []Input{in}, Window{Start: 200 * time.Millisecond, Late: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Duration(12*3000+3000) * time.Second / 90000; res.Exact || res.Keyframe != want {
		t.Errorf("result %+v, want a late start at %v", res, want)
	}
	f, _ := decodeMP4(t, out)
	vt := trackOf(t, f, "vide")
	if !isSyncSample(vt, 1) {
		t.Error("the first sample is not a sync sample")
	}
	empty, mt, _ := editOf(t, f, vt)
	if empty != 0 || mt != 3000.0/90000 { // the keyframe's composition time
		t.Errorf("a late start shows the keyframe first: empty %v media %v", empty, mt)
	}
}

// The zero window writes exactly what Assemble writes.
func TestZeroWindowIsAssemble(t *testing.T) {
	video := &fxTrack{video: true, timescale: 90000, perFrag: 24, samples: videoSamples(48, 3000, 45)}
	audio := &fxTrack{timescale: 48000, perFrag: 47, samples: audioSamples(75, 46)}
	dir := t.TempDir()
	inputs := []Input{video.writeInput(t, dir, "v"), audio.writeInput(t, dir, "a")}
	a := mustAssemble(t, inputs, ContainerMP4)
	b, res, err := windowOut(t, inputs, Window{})
	if err != nil || !res.Exact {
		t.Fatal(err, res)
	}
	da, _ := os.ReadFile(a)
	db, _ := os.ReadFile(b)
	if !bytes.Equal(da, db) {
		t.Error("the zero window changed the output")
	}
}

// A window of a transport stream (H.264 with B frames + AAC): the converted samples
// before the keyframe are skipped in the second pass.
func TestWindowTS(t *testing.T) {
	fx := newTSFixture(testmedia.VideoConfig{Frames: 90, BFrames: true, Seed: 47}, testmedia.AudioConfig{Frames: 141, Seed: 48})
	m := avMuxer(false, "")
	in := tsInputOf(t, KindMuxed, segments(m, fx.units, 1))
	out, res, err := windowOut(t, []Input{in}, Window{Start: 1500 * time.Millisecond, End: 2500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	f, data := decodeMP4(t, out)
	vt := trackOf(t, f, "vide")
	if !isSyncSample(vt, 1) {
		t.Error("the first video sample is not a sync sample")
	}
	exp := expVideo(fx.aus)
	got := payloadsOf(t, f, data, vt)
	start := -1
	for i := range exp {
		if bytes.Equal(exp[i].data, got[0]) {
			start = i
			break
		}
	}
	if start < 0 || !exp[start].sync {
		t.Fatalf("the first video sample is not one of the source's sync samples")
	}
	for i := range got {
		if !bytes.Equal(got[i], exp[start+i].data) {
			t.Fatalf("video sample %d differs", i)
		}
	}
	if res.Keyframe > 1500*time.Millisecond || res.Keyframe < 1100*time.Millisecond || !res.Exact {
		t.Errorf("keyframe %v", res.Keyframe)
	}
	_, _, dur := editOf(t, f, vt)
	if math.Abs(dur-1.0) > 0.002 {
		t.Errorf("video edit duration %v, want 1.0", dur)
	}
	_, _, adur := editOf(t, f, trackOf(t, f, "soun"))
	if math.Abs(adur-1.0) > 0.03 {
		t.Errorf("audio edit duration %v, want 1.0", adur)
	}
}

// TrimMP4 cuts a progressive file, from the whole file or from the span SampleSpan
// gives, with the same result.
func TestTrimMP4(t *testing.T) {
	vc := testmedia.VideoConfig{Frames: 90, BFrames: true, Seed: 49}
	ac := testmedia.AudioConfig{Frames: 141, Seed: 50}
	aus, frames := testmedia.Video(vc), testmedia.AAC(ac)
	file, err := testmedia.ProgressiveMP4(vc, aus, ac, frames)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := writeFile(t, dir, "src.mp4", file)
	src, _ := decodeMP4(t, path)
	var moov bytes.Buffer
	if err := src.Moov.Encode(&moov); err != nil {
		t.Fatal(err)
	}
	w := Window{Start: 1500 * time.Millisecond, End: 2500 * time.Millisecond, Origin: OriginMedia}
	whole := filepath.Join(dir, "whole.mp4")
	res, err := TrimMP4(context.Background(), ProgressiveSource{Moov: moov.Bytes(), Data: path}, whole, w)
	if err != nil {
		t.Fatal(err)
	}
	if res.Keyframe > w.Start || res.Keyframe < 1100*time.Millisecond {
		t.Errorf("keyframe %v", res.Keyframe)
	}
	start, end, err := SampleSpan(moov.Bytes(), w)
	if err != nil || start <= 0 || end <= start || end > int64(len(file)) {
		t.Fatalf("span %d-%d %v", start, end, err)
	}
	spanPath := writeFile(t, dir, "span", file[start:end])
	part := filepath.Join(dir, "span.mp4")
	if _, err := TrimMP4(context.Background(), ProgressiveSource{Moov: moov.Bytes(), Data: spanPath, DataBase: start}, part, w); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(whole)
	b, _ := os.ReadFile(part)
	if !bytes.Equal(a, b) {
		t.Error("the part cut from the span differs from the one cut from the whole file")
	}
	f, data := decodeMP4(t, whole)
	vt := trackOf(t, f, "vide")
	if !isSyncSample(vt, 1) {
		t.Error("the first video sample is not a sync sample")
	}
	got := payloadsOf(t, f, data, vt)
	k := -1
	for i, au := range aus {
		if bytes.Equal(au.Sample, got[0]) {
			k = i
		}
	}
	if k < 0 || !aus[k].Sync {
		t.Fatal("the first sample is not a source keyframe")
	}
	for i := range got {
		if !bytes.Equal(got[i], aus[k+i].Sample) {
			t.Fatalf("sample %d differs", i)
		}
	}
	_, _, dur := editOf(t, f, vt)
	if math.Abs(dur-1.0) > 0.002 {
		t.Errorf("edit duration %v", dur)
	}
	// A span that does not hold the part's samples is refused.
	if _, err := TrimMP4(context.Background(), ProgressiveSource{Moov: moov.Bytes(), Data: spanPath, DataBase: start + 100}, filepath.Join(dir, "bad.mp4"), w); !errors.Is(err, ErrMalformed) {
		t.Errorf("a short span: %v", err)
	}
}

// A part longer than the in-memory tables allow is refused (C15).
func TestWindowTooLong(t *testing.T) {
	old := maxProgressiveSamples
	maxProgressiveSamples = 20
	defer func() { maxProgressiveSamples = old }()
	video := &fxTrack{video: true, timescale: 90000, perFrag: 24, samples: videoSamples(48, 3000, 51)}
	in := video.writeInput(t, t.TempDir(), "v")
	if _, _, err := windowOut(t, []Input{in}, Window{Start: 500 * time.Millisecond}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("%v, want ErrUnsupported", err)
	}
}

// A transport stream window without any keyframe (fragments inside a long picture group) is
// ErrNoKeyframe, so that the caller fetches earlier media (docs/MEDIA-SPEC.md §8 C5); with
// a Late start, as without a window, it is refused as video without a decodable picture.
func TestWindowTSWithoutKeyframe(t *testing.T) {
	noIDR := testmedia.Video(testmedia.VideoConfig{Frames: 5, LeadingNonSync: 5})
	seg := (&testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}).Segment(testmedia.VideoPES(vPID, noIDR))
	in := tsInputOf(t, KindVideo, [][]byte{seg})
	out, _, err := windowOut(t, []Input{in}, Window{Start: 100 * time.Millisecond})
	if !errors.Is(err, ErrNoKeyframe) {
		t.Fatalf("%v, want ErrNoKeyframe", err)
	}
	assertNoOutput(t, out)
	if _, _, err := windowOut(t, []Input{in}, Window{Start: 100 * time.Millisecond, Late: true}); errors.Is(err, ErrNoKeyframe) || !errors.Is(err, ErrUnsupported) {
		t.Errorf("late: %v, want ErrUnsupported", err)
	}
}

// A late start whose first keyframe comes at or after the part's end has nothing to show:
// it is refused, never written as an empty part.
func TestWindowLateKeyframeAfterEnd(t *testing.T) {
	samples := videoSamples(48, 3000, 49)
	for i := range 36 {
		samples[i].sync = i == 0
	}
	video := &fxTrack{video: true, timescale: 90000, perFrag: 24, samples: samples}
	in := video.writeInput(t, t.TempDir(), "v")
	// Keyframes: the first sample and sample 36 (1.2 s); the part 0.4–1.1 s starts at the
	// first. Without it, the first keyframe comes after the part.
	_, res, err := windowOut(t, []Input{in}, Window{Start: 400 * time.Millisecond, End: 1100 * time.Millisecond, Late: true})
	if err != nil || res.Keyframe != 3000*time.Second/90000 {
		t.Fatalf("a keyframe before the start: %+v, %v", res, err)
	}
	samples[0].sync = false
	video = &fxTrack{video: true, timescale: 90000, perFrag: 24, samples: samples}
	in = video.writeInput(t, t.TempDir(), "v")
	out, _, err := windowOut(t, []Input{in}, Window{Start: 400 * time.Millisecond, End: 1100 * time.Millisecond, Late: true})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("the first keyframe after the end: %v, want ErrUnsupported", err)
	}
	assertNoOutput(t, out)
}

// OriginFirst counts from the first input's start (docs/MEDIA-SPEC.md §8 C3: a stream window's
// first video fragment), not from an audio input that starts earlier: a separate audio track's
// window begins with the fragment that holds the video's start, often most of a fragment before.
func TestWindowOriginFirstIsTheFirstInput(t *testing.T) {
	video := &fxTrack{video: true, timescale: 90000, base: 2 * 90000, perFrag: 24, samples: videoSamples(72, 3000, 50)}
	audio := &fxTrack{timescale: 48000, base: 48000, perFrag: 47, samples: audioSamples(160, 51)}
	dir := t.TempDir()
	inputs := []Input{video.writeInput(t, dir, "v"), audio.writeInput(t, dir, "a")}
	// The part is 3–4 s of media time: 1–2 s after the video input's start at 2 s.
	out, res, err := windowOut(t, inputs, Window{Start: time.Second, End: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	// The keyframe at or before 3 s: decoded at 2.8 s, presented one frame later.
	if want := time.Duration(24*3000+3000) * time.Second / 90000; res.Keyframe != want || !res.Exact {
		t.Errorf("result %+v, want keyframe %v after the video's start", res, want)
	}
	f, _ := decodeMP4(t, out)
	empty, mt, dur := editOf(t, f, trackOf(t, f, "vide"))
	if empty != 0 || math.Abs(mt-(1.0-0.8)) > 1e-9 || dur != 1.0 {
		t.Errorf("video edit: empty %v media %v duration %v; want 0, 0.2, 1.0", empty, mt, dur)
	}
	// Audio from the frame that holds the keyframe's time (2.8333 s of media time).
	frame := 1024.0 / 48000
	_, amt, adur := editOf(t, f, trackOf(t, f, "soun"))
	first := int((2.8333 - 1.0) / frame)
	if math.Abs(amt-(3.0-1.0-float64(first)*frame)) > 1.0/48000 || math.Abs(adur-1.0) > 0.002 {
		t.Errorf("audio edit: media %v duration %v", amt, adur)
	}
}
