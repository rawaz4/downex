package mpegts

import (
	"context"
	"errors"
	"io"
	"runtime"
	"slices"
	"testing"

	"downex/internal/media"
	"downex/internal/testmedia"
)

// fuzzSeeds are generated transport streams: muxed H.264 and AAC, HEVC, junk
// before the packets, scrambling, a PMT change and a continuity gap.
func fuzzSeeds() [][]byte {
	aus := testmedia.Video(testmedia.VideoConfig{Frames: 6, BFrames: true, SliceBytes: 60, StartDTS: 1<<33 - 3000})
	frames := testmedia.AAC(testmedia.AudioConfig{Frames: 8, FrameBytes: 40})
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: videoPID, Type: testmedia.TypeH264}, {PID: audioPID, Type: testmedia.TypeAAC, Language: "eng"}}}
	muxed := m.Segment(testmedia.Interleave(testmedia.VideoPES(videoPID, aus), testmedia.AudioPES(audioPID, frames, 3)))
	h := testmedia.Video(testmedia.VideoConfig{Codec: testmedia.HEVC, Frames: 3, SliceBytes: 40})
	hm := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: videoPID, Type: testmedia.TypeHEVC}}}
	scr := testmedia.VideoPES(videoPID, aus)
	scr[1].TSScrambled = true
	m2 := &testmedia.Muxer{PMTVersion: 4, Streams: []testmedia.Stream{{PID: 0x200, Type: testmedia.TypeH264}, {PID: 0x201, Type: testmedia.TypeAC3}}}
	return [][]byte{
		muxed,
		hm.Segment(testmedia.VideoPES(videoPID, h)),
		slices.Concat([]byte("junk before the first packet G"), muxed),
		m.Segment(scr),
		slices.Concat(muxed, m2.PSI()),
		testmedia.DropPacket(muxed, videoPID, 2),
		muxed[:len(muxed)-50],
	}
}

// FuzzDemux feeds arbitrary bytes, cut into one or two files, to the demuxer with
// every stream kept. It must not panic, must stop with io.EOF or one of the
// declared errors, must keep every unit within MaxPESBytes, and must allocate in
// proportion to the input.
func FuzzDemux(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add(s, uint16(len(s)/2))
		f.Add(s, uint16(0))
	}
	f.Fuzz(func(t *testing.T, data []byte, split uint16) {
		files := [][]byte{data}
		if s := int(split); s > 0 && s < len(data) {
			files = [][]byte{data[:s], data[s:]}
		}
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		d := NewBytesDemuxer(files, keepAll, WithProgramCheck(func(p Program) error {
			if len(p.Streams) > maxStreams {
				t.Fatalf("%d streams", len(p.Streams))
			}
			return nil
		}))
		total := 0
		for {
			u, err := d.Next(context.Background())
			if err == io.EOF {
				break
			}
			if err != nil {
				if !errors.Is(err, ErrMalformed) && !errors.Is(err, media.ErrEncrypted) {
					t.Fatalf("undeclared error %v", err)
				}
				break
			}
			if len(u.Data) > MaxPESBytes || u.File < 0 || u.File >= len(files) {
				t.Fatalf("unit of %d bytes from file %d", len(u.Data), u.File)
			}
			if u.PTS < -1 || u.PTS >= 1<<33 || u.DTS < -1 || u.DTS >= 1<<33 {
				t.Fatalf("timestamps %d %d", u.PTS, u.DTS)
			}
			total += len(u.Data)
		}
		if total > len(data) {
			t.Fatalf("units hold %d bytes of a %d-byte input", total, len(data))
		}
		runtime.ReadMemStats(&after)
		if grew := after.TotalAlloc - before.TotalAlloc; grew > 8<<20+16*uint64(len(data)) {
			t.Fatalf("allocated %d bytes for %d bytes of input", grew, len(data))
		}
	})
}

// FuzzProbe: Probe never panics and returns a program or one of its declared
// errors.
func FuzzProbe(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := Probe(data)
		if err != nil {
			if !errors.Is(err, ErrMalformed) && !errors.Is(err, media.ErrEncrypted) {
				t.Fatalf("undeclared error %v", err)
			}
			return
		}
		if len(p.Streams) > maxStreams {
			t.Fatalf("%d streams", len(p.Streams))
		}
	})
}
