package remux

import (
	"bytes"
	"io"
	"slices"
	"testing"
)

// FuzzFragmentReader feeds arbitrary bytes to the fragment reader as one media
// fragment file of a video track (it may also carry a 'moov'), then builds the
// progressive sample tables and movie box from whatever it accepted. Nothing may
// panic, and accepted runs must describe data inside the file.
func FuzzFragmentReader(f *testing.F) {
	fx := &fxTrack{video: true, timescale: 90000, perFrag: 6, samples: videoSamples(12, 3000, 30)}
	initFile := encodeBox(f, fx.initSegment(f))
	boxes, err := readTopBoxes(bytes.NewReader(initFile), int64(len(initFile)))
	if err != nil {
		f.Fatal(err)
	}
	var moov []byte
	for _, b := range boxes {
		if b.typ == "moov" {
			moov = initFile[b.off:b.end()]
		}
	}
	frags := fx.fragmentBytes(f)
	for _, b := range frags {
		f.Add(b)
	}
	f.Add(slices.Concat(initFile, frags[0], frags[1]))
	_, fragA, fragB, _ := trexFixture(f)
	f.Add(fragA)
	f.Add(fragB)
	f.Add(slices.Concat(rawBox("moof", fullBox("mfhd", 0, 0, be32(1)),
		rawBox("traf", fullBox("tfhd", 0, tfhdDefaultBaseIsMoof, be32(1)), fullBox("senc", 0, 0, be32(0)))), rawBox("mdat")))
	f.Add([]byte{})
	f.Add(rawBox("moof"))

	f.Fuzz(func(t *testing.T, data []byte) {
		m := &muxer{inits: make([]*initInfo, 1), fileIdx: map[string]int{}, progressive: true}
		s := newScanner(m, 0, Input{Kind: KindVideo, Container: ContainerFMP4, Fragments: []string{"fuzz"}})
		info, err := parseInit(moov)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.setInit(info); err != nil {
			t.Fatal(err)
		}
		if err := s.scanFile(bytes.NewReader(data), int64(len(data)), m.fileID("fuzz"), "fuzz"); err != nil {
			return
		}
		var claimed int64
		for len(s.moofs) > 0 {
			s.runs = s.runs[:0]
			if err := s.nextMoof(); err != nil {
				return
			}
			for i := range s.runs {
				r := &s.runs[i]
				// Runs of empty samples copy nothing, wherever they point.
				if r.size > 0 && (r.offset < 0 || r.offset+r.size > int64(len(data))) {
					t.Fatalf("run data [%d, +%d) outside a %d-byte file", r.offset, r.size, len(data))
				}
				if claimed += r.size; claimed > int64(len(data)) {
					t.Fatalf("runs claim %d bytes of a %d-byte file", claimed, len(data))
				}
				if err := m.collect(r); err != nil {
					return
				}
			}
		}
		if err := m.finish(); err != nil {
			return
		}
		moov, err := m.buildMoov(true, false)
		if err != nil {
			return
		}
		_ = m.chunkOrder()
		if err := moov.Encode(io.Discard); err != nil {
			t.Fatalf("encode: %v", err)
		}
	})
}
