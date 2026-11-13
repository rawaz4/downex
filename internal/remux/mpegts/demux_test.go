package mpegts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"downex/internal/media"
	"downex/internal/testmedia"
)

const (
	videoPID = 0x100
	audioPID = 0x101
)

func keepAll(Program, Stream) bool { return true }

// fixture is a generated muxed H.264 + AAC stream cut into segments.
type fixture struct {
	aus    []testmedia.AccessUnit
	frames []testmedia.AudioFrame
	units  []testmedia.PES
	segs   [][]byte
}

func newFixture(t testing.TB, start int64) *fixture {
	t.Helper()
	fx := &fixture{
		aus:    testmedia.Video(testmedia.VideoConfig{Frames: 60, BFrames: true, StartDTS: start, Seed: 1}),
		frames: testmedia.AAC(testmedia.AudioConfig{Frames: 94, StartPTS: start, Seed: 2}),
	}
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: videoPID, Type: testmedia.TypeH264}, {PID: audioPID, Type: testmedia.TypeAAC, Language: "ENG"}}}
	fx.units = testmedia.Interleave(testmedia.VideoPES(videoPID, fx.aus), testmedia.AudioPES(audioPID, fx.frames, 5))
	fx.segs = m.Segments(fx.units, testmedia.SyncCut(videoPID, 0.3))
	return fx
}

// readAll demuxes files and returns every unit with a copy of its data.
func readAll(t testing.TB, d *Demuxer) ([]Unit, error) {
	t.Helper()
	var out []Unit
	for {
		u, err := d.Next(context.Background())
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		u.Data = bytes.Clone(u.Data)
		out = append(out, u)
	}
}

func writeFiles(t testing.TB, segs [][]byte) []string {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for i, s := range segs {
		p := filepath.Join(dir, string(rune('a'+i))+".ts")
		if err := os.WriteFile(p, s, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

func TestDemuxMuxedStream(t *testing.T) {
	fx := newFixture(t, 1<<33-90000) // the timestamps wrap one second in
	if len(fx.segs) < 3 {
		t.Fatalf("%d segments", len(fx.segs))
	}
	d := NewDemuxer(writeFiles(t, fx.segs), keepAll)
	defer d.Close()
	units, err := readAll(t, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != len(fx.units) {
		t.Fatalf("%d units, want %d", len(units), len(fx.units))
	}
	// Units complete when the next one of their PID starts, so the order per PID
	// is kept.
	var gotV, gotA []Unit
	for _, u := range units {
		if u.PID == videoPID {
			gotV = append(gotV, u)
		} else {
			gotA = append(gotA, u)
		}
	}
	for i, au := range fx.aus {
		u := gotV[i]
		if u.Type != TypeH264 || !bytes.Equal(u.Data, au.AnnexB) || u.RandomAccess != au.Sync || u.AfterLoss {
			t.Fatalf("video unit %d differs", i)
		}
		wantDTS := au.DTS & (1<<33 - 1)
		if au.DTS == au.PTS {
			wantDTS = -1
		}
		if u.PTS != au.PTS&(1<<33-1) || u.DTS != wantDTS {
			t.Fatalf("video unit %d: PTS %d DTS %d, want %d %d", i, u.PTS, u.DTS, au.PTS&(1<<33-1), wantDTS)
		}
	}
	for i, u := range gotA {
		var want []byte
		for _, f := range fx.frames[i*5 : min(i*5+5, len(fx.frames))] {
			want = append(want, f.ADTS...)
		}
		if !bytes.Equal(u.Data, want) || u.PTS != fx.frames[i*5].PTS&(1<<33-1) || u.DTS != -1 {
			t.Fatalf("audio unit %d differs", i)
		}
	}
	p, ok := d.Program()
	if !ok || p.Number != 1 || p.PMTPID != 0x1000 || p.PCRPID != videoPID || len(p.Streams) != 2 || p.Streams[1].Language != "eng" {
		t.Fatalf("program %+v", p)
	}
	if s := d.Stats(); s != (Stats{}) {
		t.Fatalf("stats %+v", s)
	}
	// Each unit reports the file it started in.
	if gotV[0].File != 0 || gotV[len(gotV)-1].File != len(fx.segs)-1 {
		t.Fatalf("files %d, %d", gotV[0].File, gotV[len(gotV)-1].File)
	}
}

func TestUnitContinuesAcrossFiles(t *testing.T) {
	fx := newFixture(t, 0)
	all := slices.Concat(fx.segs...)
	// Cut in the middle of a packet run of the tenth video unit: the second file
	// starts at a packet boundary inside that unit.
	cut := 40 * PacketSize
	d := NewBytesDemuxer([][]byte{all[:cut], all[cut:]}, keepAll)
	units, err := readAll(t, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != len(fx.units) {
		t.Fatalf("%d units, want %d", len(units), len(fx.units))
	}
	crossing := 0
	for _, u := range units {
		if u.File == 0 {
			crossing++
		}
	}
	if crossing == 0 {
		t.Fatal("no unit started in the first file")
	}
	if s := d.Stats(); s.Gaps != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestResync(t *testing.T) {
	fx := newFixture(t, 0)
	all := slices.Concat(fx.segs...)
	want, err := readAll(t, NewBytesDemuxer([][]byte{all}, keepAll))
	if err != nil {
		t.Fatal(err)
	}
	junk := bytes.Repeat([]byte("<html>not a stream G</html>"), 300) // contains 'G' = 0x47
	t.Run("leading junk", func(t *testing.T) {
		d := NewBytesDemuxer([][]byte{slices.Concat(junk, all)}, keepAll)
		got, err := readAll(t, d)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) || d.Stats().Skipped != int64(len(junk)) {
			t.Fatalf("%d units (want %d), skipped %d", len(got), len(want), d.Stats().Skipped)
		}
	})
	t.Run("trailing partial packet", func(t *testing.T) {
		got, err := readAll(t, NewBytesDemuxer([][]byte{all[:len(all)-100]}, keepAll))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("%d units, want %d", len(got), len(want))
		}
	})
	t.Run("junk beyond the window", func(t *testing.T) {
		big := bytes.Repeat([]byte{0x00, 0x47, 0x11}, 30000)
		_, err := readAll(t, NewBytesDemuxer([][]byte{all[:20*PacketSize], slices.Concat(big, all)}, keepAll))
		var e *Error
		if !errors.Is(err, ErrMalformed) || !errors.As(err, &e) || e.File != 1 {
			t.Fatalf("err = %v, want ErrMalformed in file 1", err)
		}
	})
	t.Run("error page", func(t *testing.T) {
		_, err := readAll(t, NewBytesDemuxer([][]byte{bytes.Repeat([]byte("<html>403</html>"), 20)}, keepAll))
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestContinuity(t *testing.T) {
	fx := newFixture(t, 0)
	all := slices.Concat(fx.segs...)
	t.Run("duplicate", func(t *testing.T) {
		d := NewBytesDemuxer([][]byte{testmedia.DuplicatePacket(all, videoPID, 7)}, keepAll)
		got, err := readAll(t, d)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(fx.units) || d.Stats().Duplicates != 1 {
			t.Fatalf("%d units, stats %+v", len(got), d.Stats())
		}
	})
	t.Run("gap", func(t *testing.T) {
		// Drop a packet in the middle of the first video unit (an IDR of many
		// packets): that unit is discarded, the next one reports the loss.
		d := NewBytesDemuxer([][]byte{testmedia.DropPacket(all, videoPID, 3)}, keepAll)
		got, err := readAll(t, d)
		if err != nil {
			t.Fatal(err)
		}
		var v []Unit
		for _, u := range got {
			if u.PID == videoPID {
				v = append(v, u)
			}
		}
		if len(v) != len(fx.aus)-1 || !bytes.Equal(v[0].Data, fx.aus[1].AnnexB) || !v[0].AfterLoss || v[1].AfterLoss {
			t.Fatalf("%d video units; first after loss %v", len(v), v[0].AfterLoss)
		}
		if s := d.Stats(); s.Gaps != 1 || s.Dropped != 1 {
			t.Fatalf("stats %+v", s)
		}
	})
	t.Run("reset at file start", func(t *testing.T) {
		// Each file is muxed by its own muxer: the counters restart at 0.
		m1 := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: videoPID, Type: testmedia.TypeH264}}}
		m2 := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: videoPID, Type: testmedia.TypeH264}}}
		vp := testmedia.VideoPES(videoPID, fx.aus)
		d := NewBytesDemuxer([][]byte{m1.Segment(vp[:31]), m2.Segment(vp[31:])}, keepAll)
		got, err := readAll(t, d)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(fx.aus) || d.Stats().Gaps != 0 {
			t.Fatalf("%d units, stats %+v", len(got), d.Stats())
		}
	})
}

func TestSkippedPackets(t *testing.T) {
	fx := newFixture(t, 0)
	all := slices.Concat(fx.segs...)
	tei := slices.Clone(testmedia.Packet(all, 5))
	tei[1] |= 0x80
	tei[3] = tei[3]&0xf0 | (tei[3]+9)&0x0f // a counter that would be a gap
	stream := slices.Concat(all[:10*PacketSize], testmedia.NullPackets(3), tei, all[10*PacketSize:])
	got, err := readAll(t, NewBytesDemuxer([][]byte{stream}, keepAll))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(fx.units) {
		t.Fatalf("%d units, want %d", len(got), len(fx.units))
	}
}

func TestScrambling(t *testing.T) {
	aus := testmedia.Video(testmedia.VideoConfig{Frames: 12})
	frames := testmedia.AAC(testmedia.AudioConfig{Frames: 20})
	mk := func(vScr, aScr, pes bool) []byte {
		m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: videoPID, Type: testmedia.TypeH264}, {PID: audioPID, Type: testmedia.TypeAAC}}}
		v := testmedia.VideoPES(videoPID, aus)
		a := testmedia.AudioPES(audioPID, frames, 4)
		v[2].TSScrambled = vScr
		a[1].TSScrambled = aScr
		v[3].PESScrambled = pes
		return m.Segment(testmedia.Interleave(v, a))
	}
	videoOnly := func(_ Program, s Stream) bool { return s.Type == TypeH264 }
	for name, tc := range map[string]struct {
		data []byte
		keep func(Program, Stream) bool
		want error
	}{
		"scrambled packets":                 {mk(true, false, false), keepAll, media.ErrEncrypted},
		"scrambled PES":                     {mk(false, false, true), keepAll, media.ErrEncrypted},
		"scrambled stream that is not kept": {mk(false, true, false), videoOnly, nil},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readAll(t, NewBytesDemuxer([][]byte{tc.data}, tc.keep))
			if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if err != nil {
				var e *Error
				if !errors.As(err, &e) || e.PID < 0 {
					t.Fatalf("error %v does not name the PID", err)
				}
			}
		})
	}
}

func TestBoundedAndUnboundedUnits(t *testing.T) {
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: audioPID, Type: testmedia.TypeAAC}}}
	frames := testmedia.AAC(testmedia.AudioConfig{Frames: 10})
	a := testmedia.AudioPES(audioPID, frames, 2)
	seg := m.Segment(a)
	d := NewBytesDemuxer([][]byte{seg}, keepAll)
	ctx := context.Background()
	// Bounded units complete as soon as their length is reached: the first one is
	// returned before the stream ends.
	u, err := d.Next(ctx)
	if err != nil || !bytes.Equal(u.Data, slices.Concat(frames[0].ADTS, frames[1].ADTS)) {
		t.Fatalf("first unit: %v", err)
	}
	// Bytes after a bounded unit's end up to the next start are not part of it.
	pes := m.PESBytes(testmedia.PES{PID: audioPID, PTS: 0, DTS: -1, Data: []byte{0xff, 0xf1, 1, 2}, Bounded: true})
	pes = append(pes, 9, 9, 9)
	pkt := append([]byte{SyncByte, 0x40 | byte(audioPID>>8), byte(audioPID & 0xff), 0x30, byte(183 - len(pes))}, 0)
	for len(pkt) < PacketSize-len(pes) {
		pkt = append(pkt, 0xff)
	}
	pkt = append(pkt, pes...)
	got, err := readAll(t, NewBytesDemuxer([][]byte{slices.Concat(m.PSI(), pkt)}, keepAll))
	if err != nil || len(got) != 1 || !bytes.Equal(got[0].Data, []byte{0xff, 0xf1, 1, 2}) {
		t.Fatalf("units %+v, %v", got, err)
	}
}

func TestTimestampsOnly(t *testing.T) {
	fx := newFixture(t, 5000)
	files := writeFiles(t, fx.segs)
	full, err := readAll(t, NewDemuxer(files, keepAll))
	if err != nil {
		t.Fatal(err)
	}
	isVideo := func(_ Program, s Stream) bool { return s.Type == TypeH264 }
	partial, err := readAll(t, NewDemuxer(files, isVideo, WithTimestampsOnly(keepAll)))
	if err != nil {
		t.Fatal(err)
	}
	if len(partial) != len(full) {
		t.Fatalf("%d units, want %d", len(partial), len(full))
	}
	for i := range full {
		f, p := full[i], partial[i]
		if f.PID != p.PID || f.PTS != p.PTS || f.DTS != p.DTS || f.File != p.File {
			t.Fatalf("unit %d differs", i)
		}
		if p.PID == audioPID && p.Data != nil {
			t.Fatalf("unit %d of a timestamps-only stream has data", i)
		}
		if p.PID == videoPID && !bytes.Equal(p.Data, f.Data) {
			t.Fatalf("unit %d: data differs", i)
		}
	}
}

func TestProgramChanges(t *testing.T) {
	aus := testmedia.Video(testmedia.VideoConfig{Frames: 24})
	vp := testmedia.VideoPES(videoPID, aus)
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: videoPID, Type: testmedia.TypeH264}}}
	first := m.Segment(vp[:12])
	// A new PMT version moves the video to another PID.
	m2 := &testmedia.Muxer{PMTVersion: 1, Streams: []testmedia.Stream{{PID: 0x200, Type: testmedia.TypeH264}}}
	for i := range vp[12:] {
		vp[12+i].PID = 0x200
	}
	second := m2.Segment(vp[12:])
	var programs []Program
	d := NewBytesDemuxer([][]byte{first, second}, func(_ Program, s Stream) bool { return s.Type == TypeH264 },
		WithProgramCheck(func(p Program) error { programs = append(programs, p); return nil }))
	got, err := readAll(t, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 24 || got[11].PID != videoPID || got[12].PID != 0x200 || len(programs) != 2 {
		t.Fatalf("%d units, %d programs", len(got), len(programs))
	}
	// The repeated PMT of every segment is not reported again.
	refuse := errors.New("refused")
	_, err = readAll(t, NewBytesDemuxer([][]byte{first, second}, keepAll, WithProgramCheck(func(p Program) error {
		if p.Streams[0].PID == 0x200 {
			return refuse
		}
		return nil
	})))
	if err != refuse {
		t.Fatalf("err = %v, want the check's error", err)
	}
}

func TestEndlessPESIsBounded(t *testing.T) {
	m := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: videoPID, Type: testmedia.TypeH264}}}
	// One unbounded video unit of more than MaxPESBytes, followed by many packets
	// that never start a new unit.
	data := bytes.Repeat([]byte{0, 0, 3, 0x80}, (MaxPESBytes+PacketSize*1000)/4)
	stream := slices.Concat(m.PSI(), m.Packets(testmedia.PES{PID: videoPID, PTS: 0, DTS: -1, Data: data}))
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := readAll(t, NewBytesDemuxer([][]byte{stream}, keepAll))
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 3*MaxPESBytes {
		t.Fatalf("allocated %d bytes", grew)
	}
}

func TestCancellation(t *testing.T) {
	fx := newFixture(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := NewBytesDemuxer(fx.segs, keepAll)
	if _, err := d.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := d.Next(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("the error is not sticky: %v", err)
	}
}

func TestMissingFile(t *testing.T) {
	d := NewDemuxer([]string{filepath.Join(t.TempDir(), "nope.ts")}, keepAll)
	if _, err := d.Next(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}
