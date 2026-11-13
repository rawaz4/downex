package remux

import (
	"bytes"
	"errors"
	"math/rand"
	"runtime"
	"slices"
	"testing"
	"time"

	"downex/internal/remux/mpegts"
	"downex/internal/testmedia"
)

// allocated runs fn and returns the bytes it allocated.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestTSEndlessPESIsBounded: a video unit that never ends is refused once it
// passes mpegts.MaxPESBytes, without growing memory further.
func TestTSEndlessPESIsBounded(t *testing.T) {
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}
	data := bytes.Repeat([]byte{0, 0, 3, 0x41}, (mpegts.MaxPESBytes+1<<20)/4)
	ts := slices.Concat(m.PSI(), m.Packets(testmedia.PES{PID: vPID, PTS: 0, DTS: -1, Data: data}))
	in := tsInputOf(t, KindVideo, [][]byte{ts[:len(ts)/2], ts[len(ts)/2:]})
	var err error
	var out string
	grew := allocated(func() { out, _, err = assemble(t, []Input{in}) })
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
	t.Log(err)
	assertNoOutput(t, out)
	if grew > 2*mpegts.MaxPESBytes+16<<20 {
		t.Fatalf("allocated %d bytes", grew)
	}
}

// TestTSTinyADTSFramesBounded: a stream of the smallest ADTS frames (one byte of
// raw data each) converts with allocations in proportion to its size.
func TestTSTinyADTSFramesBounded(t *testing.T) {
	cfg := testmedia.AudioConfig{}
	frame := testmedia.ADTSFrame(cfg, []byte{0x5a})
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: aPID, Type: testmedia.TypeAAC}}}
	var ts []byte
	ts = append(ts, m.PSI()...)
	const perPES, units = 4000, 60 // 240,000 frames: 85 minutes at 48 kHz
	payload := bytes.Repeat(frame, perPES)
	for i := range units {
		ts = append(ts, m.Packets(testmedia.PES{PID: aPID, PTS: int64(i) * perPES * 1920, DTS: -1, Data: payload, Bounded: false})...)
	}
	in := tsInputOf(t, KindAudio, [][]byte{ts})
	var err error
	var out string
	grew := allocated(func() { out, _, err = assemble(t, []Input{in}) })
	if err != nil {
		t.Fatal(err)
	}
	f, _ := decodeMP4(t, out)
	if n := f.Moov.Traks[0].Mdia.Minf.Stbl.Stsz.SampleNumber; n != perPES*units {
		t.Fatalf("%d samples", n)
	}
	if grew > 24*uint64(len(ts))+16<<20 {
		t.Fatalf("allocated %d bytes for %d bytes of input", grew, len(ts))
	}
	t.Logf("allocated %d bytes for %d bytes of input", grew, len(ts))
}

// TestTSPSIFloodIsBounded: program tables repeated before every unit, a new PMT
// version moving the video to another PID before each, and sections with a wrong
// CRC: the conversion follows the video and allocates in proportion to the input.
func TestTSPSIFloodIsBounded(t *testing.T) {
	aus := testmedia.Video(testmedia.VideoConfig{Frames: 240, SliceBytes: 40, Seed: 40})
	units := testmedia.VideoPES(vPID, aus)
	var ts []byte
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}
	for i, u := range units {
		pid := uint16(vPID + i%2)
		m.PMTVersion = byte(i)
		m.Streams = []testmedia.Stream{{PID: pid, Type: testmedia.TypeH264}}
		for range 20 {
			ts = append(ts, m.PSI()...)
			bad := m.PMTSection()
			bad[len(bad)-1] ^= 0x55
			ts = append(ts, m.SectionPackets(m.PMTPID, bad)...)
		}
		u.PID = pid
		ts = append(ts, m.Packets(u)...)
	}
	in := tsInputOf(t, KindVideo, [][]byte{ts})
	var out string
	var err error
	grew := allocated(func() { out, _, err = assemble(t, []Input{in}) })
	if err != nil {
		t.Fatal(err)
	}
	checkProgressive(t, out, []expTrack{{handler: "vide", timescale: 90000, samples: expVideo(aus)}})
	if grew > 8*uint64(len(ts))+16<<20 {
		t.Fatalf("allocated %d bytes for %d bytes of input", grew, len(ts))
	}
}

// TestTSContinuationUnitsAreLinear: video PES units without a PTS that continue an
// access unit without a slice yet (each a start code and an SEI, as a muxer that
// splits an access unit over packets could send) are joined at a cost in proportion
// to their size: a check of the whole joined unit for every piece would take
// minutes for these 8 MiB.
func TestTSContinuationUnitsAreLinear(t *testing.T) {
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}
	sei := append([]byte{0, 0, 0, 1, 6, 5}, bytes.Repeat([]byte{0x11}, 150)...)
	ts := slices.Concat(m.PSI(), m.Packets(testmedia.PES{PID: vPID, PTS: 0, DTS: -1, Data: sei, Bounded: true}))
	for len(ts) < 8<<20 {
		ts = append(ts, m.Packets(testmedia.PES{PID: vPID, PTS: -1, DTS: -1, Data: sei, Bounded: true})...)
	}
	start := time.Now()
	_, _, err := assemble(t, []Input{tsInputOf(t, KindVideo, [][]byte{ts})})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported (no picture)", err)
	}
	checked := time.Now()
	if err := CheckTS(ts, KindVideo); err != nil {
		t.Fatalf("CheckTS: %v", err)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("%d bytes took %v (Assemble %v)", len(ts), d, checked.Sub(start))
	}
	t.Logf("%d bytes: Assemble %v, CheckTS %v", len(ts), checked.Sub(start), time.Since(checked))
}

// churnStream is one large parameter-set state (four PPS of about 63 KB) and then
// n-1 one-packet access units that each carry a 10-byte PPS (id 5) whose bytes
// alternate, so that every unit changes the parameter sets and switches between
// two sample entries (after the first).
func churnStream(n int) []byte {
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}
	rng := rand.New(rand.NewSource(1))
	long := func(int) bool { return true }
	first := [][]byte{testmedia.H264SPS(0, 64, 64), testmedia.H264PPS(0, 0)}
	for id := 1; id <= 4; id++ {
		first = append(first, append(testmedia.H264PPS(id, 0), bytes.Repeat([]byte{0xaa}, 63000)...))
	}
	first = append(first, testmedia.H264SliceUnit(rng, true, true, true, 20))
	units := []testmedia.PES{{PID: vPID, PTS: 0, DTS: 0, Data: testmedia.AnnexB(first, long), RandomAccess: true}}
	for i := 1; i < n; i++ {
		p := append(testmedia.H264PPS(5, 0), byte(0xbb+i%2))
		data := testmedia.AnnexB([][]byte{p, testmedia.H264SliceUnit(rng, false, true, true, 20)}, long)
		units = append(units, testmedia.PES{PID: vPID, PTS: int64(i) * 3000, DTS: int64(i) * 3000, Data: data})
	}
	return m.Segment(units)
}

// TestTSParameterSetChurnIsBounded: parameter sets that change with every unit
// cost work in proportion to the change, not to the whole parameter-set state.
func TestTSParameterSetChurnIsBounded(t *testing.T) {
	ts := churnStream(4000)
	in := tsInputOf(t, KindVideo, [][]byte{ts})
	var out string
	var err error
	start := time.Now()
	grew := allocated(func() { out, _, err = assemble(t, []Input{in}) })
	if err != nil {
		t.Fatal(err)
	}
	f, _ := decodeMP4(t, out)
	if n := len(f.Moov.Traks[0].Mdia.Minf.Stbl.Stsd.Children); n != 3 {
		t.Fatalf("%d sample entries, want 3", n)
	}
	if grew > 24*uint64(len(ts))+16<<20 {
		t.Fatalf("allocated %d bytes for %d bytes of input", grew, len(ts))
	}
	prefix := ts[:min(len(ts), MaxCheckTSBytes)]
	if grew := allocated(func() { err = CheckTS(prefix, KindVideo) }); err != nil || grew > 24*uint64(len(prefix))+16<<20 {
		t.Fatalf("CheckTS: %v, allocated %d bytes for %d bytes", err, grew, len(prefix))
	}
	t.Logf("%d bytes of input: %v, %d bytes allocated", len(ts), time.Since(start), grew)
}
