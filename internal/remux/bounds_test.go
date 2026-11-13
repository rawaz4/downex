package remux

import (
	"context"
	"errors"
	"io"
	"runtime"
	"slices"
	"testing"
)

// TestSampleDataClaimedOnce: movie fragments that describe the same 'mdat' bytes again
// and again are refused once they claim more sample data than the file's 'mdat' boxes
// hold, before their sample tables grow beyond the file.
func TestSampleDataClaimedOnce(t *testing.T) {
	fx := &fxTrack{video: true, timescale: 90000}
	init := encodeBox(t, fx.initSegment(t))
	const n = 1 << 16 // payload bytes of the one 'mdat', and samples of each 'moof'
	data := rawBox("mdat", make([]byte, n))
	for k := range 32 {
		off := len(data)
		data = append(data, rawBox("moof", fullBox("mfhd", 0, 0, be32(uint32(k+1))),
			rawBox("traf",
				fullBox("tfhd", 0, tfhdDefaultBaseIsMoof|tfhdDefaultDuration|tfhdDefaultSize, be32(1), be32(3000), be32(1)),
				// data_offset points back at the payload of the 'mdat' at the file start.
				fullBox("trun", 0, trunDataOffset, be32(n), be32(uint32(int32(8-off))))))...)
	}
	dir := t.TempDir()
	in := Input{Kind: KindVideo, Container: ContainerFMP4, Init: writeFile(t, dir, "init.mp4", init),
		Fragments: []string{writeFile(t, dir, "a.m4s", data)}}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	out, _, err := assemble(t, []Input{in})
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
	t.Log(err)
	assertNoOutput(t, out)
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 16<<20 {
		t.Fatalf("allocated %d bytes for a %d-byte fragment", grew, len(data))
	}
}

// TestEmptySamplesBoundedPerFile: samples of the default size 0 occupy no data, so their
// number is bounded per file, however many movie fragments and track runs share it.
func TestEmptySamplesBoundedPerFile(t *testing.T) {
	fx := &fxTrack{video: true, timescale: 90000}
	init := encodeBox(t, fx.initSegment(t))
	var data []byte
	for k := range 3 {
		trafs := [][]byte{fullBox("tfhd", 0, tfhdDefaultBaseIsMoof|tfhdDefaultDuration|tfhdDefaultSize, be32(1), be32(3000), be32(0))}
		for range 8 {
			trafs = append(trafs, fullBox("trun", 0, 0, be32(50_000)))
		}
		data = append(data, rawBox("moof", fullBox("mfhd", 0, 0, be32(uint32(k+1))), rawBox("traf", trafs...))...)
	}
	data = append(data, rawBox("mdat")...)
	dir := t.TempDir()
	in := Input{Kind: KindVideo, Container: ContainerFMP4, Init: writeFile(t, dir, "init.mp4", init),
		Fragments: []string{writeFile(t, dir, "a.m4s", data)}}
	out, _, err := assemble(t, []Input{in})
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
	t.Log(err)
	assertNoOutput(t, out)
}

// TestScannerReadsOneMovieFragmentAtATime: a fragment file of many movie fragments is
// read one 'moof' at a time, so the scanner never holds the runs of the whole file, and
// it is assembled like separate fragment files.
func TestScannerReadsOneMovieFragmentAtATime(t *testing.T) {
	fx := &fxTrack{video: true, timescale: 90000, perFrag: 2, samples: videoSamples(120, 3000, 77)}
	dir := t.TempDir()
	init := writeFile(t, dir, "init.mp4", encodeBox(t, fx.initSegment(t)))
	all := writeFile(t, dir, "all.m4s", slices.Concat(fx.fragmentBytes(t)...))
	in := Input{Kind: KindVideo, Container: ContainerFMP4, Init: init, Fragments: []string{all}}

	m := &muxer{inits: make([]*initInfo, 1), fileIdx: map[string]int{}, progressive: true}
	s := newScanner(m, 0, in)
	samples := 0
	for {
		r, err := s.next(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(s.runs) > 1 {
			t.Fatalf("the scanner holds %d runs of one file", len(s.runs))
		}
		samples += len(r.samples)
	}
	if samples != 120 {
		t.Fatalf("%d samples, want 120", samples)
	}
	out := mustAssemble(t, []Input{in}, ContainerMP4)
	checkProgressive(t, out, []expTrack{expect(t, fx)})
}
