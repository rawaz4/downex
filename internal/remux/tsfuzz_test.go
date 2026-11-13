package remux

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"

	"downex/internal/media"
	"downex/internal/remux/mpegts"
	"downex/internal/testmedia"
)

// tsFuzzSeeds are small generated transport streams: muxed H.264 with B-frames and
// AAC across the timestamp wrap, HEVC, audio only, and damaged variants.
func tsFuzzSeeds() [][]byte {
	fx := newTSFixture(testmedia.VideoConfig{Frames: 14, BFrames: true, SEI: true, SliceBytes: 30, StartDTS: tsWrap - 6000, Seed: 50},
		testmedia.AudioConfig{Frames: 20, FrameBytes: 20, StartPTS: tsWrap - 7000, Seed: 51})
	muxed := avMuxer(false, "eng").Segment(fx.units)
	h := newTSFixture(testmedia.VideoConfig{Codec: testmedia.HEVC, Frames: 4, SliceBytes: 30, Seed: 52}, testmedia.AudioConfig{Frames: 6, FrameBytes: 20, Seed: 53})
	audio := (&testmedia.Muxer{Streams: []testmedia.Stream{{PID: aPID, Type: testmedia.TypeAAC}}}).Segment(testmedia.AudioPES(aPID, fx.frames, 3))
	// Access units split over two PES packets, the second without PTS.
	var split []testmedia.PES
	for _, au := range testmedia.Video(testmedia.VideoConfig{Frames: 4, SliceBytes: 60, Seed: 54}) {
		h := len(au.AnnexB) / 2
		split = append(split,
			testmedia.PES{PID: vPID, PTS: au.PTS, DTS: au.DTS, Data: au.AnnexB[:h], RandomAccess: au.Sync, Bounded: true},
			testmedia.PES{PID: vPID, PTS: -1, DTS: -1, Data: au.AnnexB[h:], Bounded: true})
	}
	vm := &testmedia.Muxer{Streams: []testmedia.Stream{{PID: vPID, Type: testmedia.TypeH264}}}
	return [][]byte{
		muxed,
		avMuxer(true, "").Segment(h.units),
		audio,
		testmedia.DropPacket(muxed, vPID, 4),
		slices.Concat([]byte("junk G"), muxed[:len(muxed)-300]),
		vm.Segment(split),
	}
}

// allocBound is what a conversion of n bytes of input may allocate: work in
// proportion to the input (the bounds tests use the same pattern).
func allocBound(n int) uint64 { return 16<<20 + 64*uint64(n) }

func declared(err error) bool {
	return errors.Is(err, ErrMalformed) || errors.Is(err, ErrUnsupported) || errors.Is(err, media.ErrEncrypted)
}

// FuzzCheckTS: CheckTS never panics and returns nil or one of its declared
// errors, for every kind.
func FuzzCheckTS(f *testing.F) {
	for _, s := range tsFuzzSeeds() {
		f.Add(s, uint8(0))
	}
	f.Fuzz(func(t *testing.T, data []byte, k uint8) {
		kind := []Kind{KindMuxed, KindVideo, KindAudio}[k%3]
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		if err := CheckTS(data, kind); err != nil && !declared(err) {
			t.Fatalf("undeclared error %v", err)
		}
		runtime.ReadMemStats(&after)
		if grew := after.TotalAlloc - before.TotalAlloc; grew > allocBound(min(len(data), MaxCheckTSBytes)) {
			t.Fatalf("allocated %d bytes for %d bytes of input", grew, len(data))
		}
	})
}

// FuzzAnnexB: the converted access unit is the length-prefixed sequence of the
// kept NAL units, its size the computed size, with no start code left inside a
// NAL unit.
func FuzzAnnexB(f *testing.F) {
	for _, au := range testmedia.Video(testmedia.VideoConfig{Frames: 3, SEI: true, SliceBytes: 40}) {
		f.Add(au.AnnexB, false)
	}
	for _, au := range testmedia.Video(testmedia.VideoConfig{Codec: testmedia.HEVC, Frames: 2, SliceBytes: 40}) {
		f.Add(au.AnnexB, true)
	}
	f.Add([]byte{0, 0, 0, 1, 0x65, 0, 0, 0, 0, 1, 0x41, 0, 0, 3, 1, 0}, false)
	f.Fuzz(func(t *testing.T, data []byte, isHEVC bool) {
		typ := mpegts.TypeH264
		if isHEVC {
			typ = mpegts.TypeHEVC
		}
		c := newVideoConv(typ)
		_, _, _, _, err := c.accessUnit(data)
		if err != nil {
			if !declared(err) {
				t.Fatalf("undeclared error %v", err)
			}
			return
		}
		// The NAL units a fresh converter keeps, one by one.
		ref := newVideoConv(typ)
		var want [][]byte
		auds := 0
		_ = splitAnnexB(data, func(nal []byte) error {
			if bytes.Contains(nal, []byte{0, 0, 1}) || len(nal) == 0 || nal[len(nal)-1] == 0 {
				t.Fatalf("split unit %x", nal)
			}
			var keep bool
			if isHEVC {
				keep, _ = ref.hevcNAL(nal, &auds)
			} else {
				keep, _ = ref.avcNAL(nal, &auds)
			}
			if keep {
				want = append(want, nal)
			}
			return nil
		})
		size := 0
		for _, n := range want {
			size += 4 + len(n)
		}
		if size != len(c.buf) {
			t.Fatalf("converted %d bytes, computed %d", len(c.buf), size)
		}
		var got [][]byte
		for b := c.buf; len(b) > 0; {
			l := int(binary.BigEndian.Uint32(b))
			if l == 0 || 4+l > len(b) {
				t.Fatalf("bad NAL length %d", l)
			}
			if bytes.Contains(b[4:4+l], []byte{0, 0, 1}) {
				t.Fatal("a start code is left in the sample")
			}
			got, b = append(got, b[4:4+l]), b[4+l:]
		}
		if !slices.EqualFunc(got, want, bytes.Equal) {
			t.Fatal("the sample does not hold the kept units")
		}
	})
}

// FuzzADTS: splitting a stream fed in three pieces gives the frames of splitting
// it whole.
func FuzzADTS(f *testing.F) {
	for _, c := range []testmedia.AudioConfig{{Frames: 4, FrameBytes: 30}, {Frames: 3, CRC: true, FrameBytes: 10}} {
		var s []byte
		for _, fr := range testmedia.AAC(c) {
			s = append(s, fr.ADTS...)
		}
		f.Add(s, uint16(10), uint16(40))
		f.Add(slices.Concat([]byte{0xff, 0xf1, 0x50}, s), uint16(1), uint16(9))
	}
	type frame struct {
		h   adtsHeader
		raw []byte
	}
	split := func(pieces ...[]byte) ([]frame, error) {
		var s adtsSplitter
		var out []frame
		for _, p := range pieces {
			if err := s.feed(p, func(h adtsHeader, raw []byte, _ bool) error {
				out = append(out, frame{h, slices.Clone(raw)})
				return nil
			}); err != nil {
				return out, err
			}
		}
		return out, nil
	}
	f.Fuzz(func(t *testing.T, data []byte, a, b uint16) {
		i, j := min(int(a), len(data)), min(int(b), len(data))
		if i > j {
			i, j = j, i
		}
		whole, err1 := split(data)
		cut, err2 := split(data[:i], data[i:j], data[j:])
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("errors differ: %v / %v", err1, err2)
		}
		if !slices.EqualFunc(whole, cut, func(x, y frame) bool { return x.h == y.h && bytes.Equal(x.raw, y.raw) }) {
			t.Fatalf("%d frames whole, %d cut at %d and %d", len(whole), len(cut), i, j)
		}
	})
}

// FuzzTSTwoPass converts arbitrary bytes as a transport stream track (one or two
// fragments, any kind, progressive or fragmented). Whatever passes pass 1 must
// pass pass 2 with the same samples, and an output, when produced, decodes with
// mp4ff and holds exactly its samples' data.
func FuzzTSTwoPass(f *testing.F) {
	for _, s := range tsFuzzSeeds() {
		f.Add(s, uint16(len(s)/2/188*188), uint8(0))
		f.Add(s, uint16(0), uint8(4))
	}
	// One folder for all iterations: they run one after the other in a worker.
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte, split uint16, mode uint8) {
		in := Input{Kind: []Kind{KindMuxed, KindVideo, KindAudio}[mode%3], Container: ContainerTS}
		parts := [][]byte{data}
		if s := int(split); s > 0 && s < len(data) {
			parts = [][]byte{data[:s], data[s:]}
			if mode&8 != 0 {
				in.Breaks = []int{1}
			}
		}
		for i, p := range parts {
			in.Fragments = append(in.Fragments, writeFile(t, dir, itoa(i)+".ts", p))
		}
		if mode&4 != 0 {
			old := maxProgressiveSamples
			maxProgressiveSamples = 8
			defer func() { maxProgressiveSamples = old }()
		}
		// assembleTS writes into memory: the file wrapper of Assemble (create, sync,
		// remove on failure) is tested elsewhere and would slow the fuzzer down.
		var buf bytes.Buffer
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		err := assembleTS(context.Background(), []Input{in}, &buf)
		runtime.ReadMemStats(&after)
		if grew := after.TotalAlloc - before.TotalAlloc; grew > allocBound(len(data)) {
			t.Fatalf("allocated %d bytes for %d bytes of input", grew, len(data))
		}
		if err != nil {
			if strings.Contains(err.Error(), "changed between passes") || !declared(err) {
				t.Fatalf("pass 2: %v", err)
			}
			return
		}
		mf, err := mp4.DecodeFile(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("output does not decode: %v", err)
		}
		if mf.IsFragmented() {
			return
		}
		var total uint64
		for _, trak := range mf.Moov.Traks {
			stsz := trak.Mdia.Minf.Stbl.Stsz
			for i := range stsz.GetNrSamples() {
				total += uint64(stsz.GetSampleSize(int(i) + 1))
			}
		}
		if total != mf.Mdat.DataLength() {
			t.Fatalf("samples hold %d bytes, mdat %d", total, mf.Mdat.DataLength())
		}
	})
}

// FuzzTSTime: unwrapping follows every step of less than half a period exactly,
// and domain placement either reports an overflow or gives strictly increasing
// decode times, positive durations and domains in order.
func FuzzTSTime(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0, 1, 255, 255, 255, 255, 1, 0, 0, 0, 0, 3, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7})
	f.Add(bytes.Repeat([]byte{0x80, 1, 2, 3, 4, 5, 6, 7, 8}, 12))
	f.Fuzz(func(t *testing.T, data []byte) {
		r := fuzzReader{b: data}
		var u unwrapper
		var prevRaw, prevOut int64
		for i := range 8 {
			raw := int64(r.u64() & (tsWrap - 1))
			got := u.unwrap(raw)
			if (got-raw)%tsWrap != 0 {
				t.Fatalf("unwrap(%d) = %d is not the same residue", raw, got)
			}
			if i > 0 {
				step := (raw - prevRaw) & (tsWrap - 1)
				if step >= tsWrap/2 {
					step -= tsWrap
				}
				if prevOut <= maxTSValue && got-prevOut != step {
					t.Fatalf("unwrap step %d, want %d", got-prevOut, step)
				}
			}
			prevRaw, prevOut = raw, got
		}

		var tracks []*timedTrack
		for range 1 + int(r.u8()%3) {
			tr := &timedTrack{input: int(r.u8() % 2), timescale: []uint32{tsClock, 48000, 44100}[r.u8()%3]}
			for range 1 + int(r.u8()%4) {
				n := int(r.u8() % 4)
				if n == 0 {
					tr.domains = append(tr.domains, trackDomain{})
					continue
				}
				src := int64(r.u64()) >> uint(r.u8()%64)
				span := int64(r.u32() % (1 << 24))
				dur := 1 + r.u32()%(1<<20)
				c1 := int64(int32(r.u32())) >> 8
				c2 := int64(r.u32() % (1 << 20))
				minPTS, ok1 := addOK(src, c1)
				last, ok2 := addOK(src, span)
				end, ok3 := addOK(last, int64(dur)+c2)
				if !ok1 || !ok2 || !ok3 || end < minPTS {
					return
				}
				tr.domains = append(tr.domains, trackDomain{n: n, srcDTS: src, lastDTS: last, lastDur: dur, minPTS: minPTS, presEnd: end, minCTO: int32(min(max(c1, -1<<30), 1<<30))})
			}
			tracks = append(tracks, tr)
		}
		if !placeDomains(tracks) {
			return // an overflow, reported
		}
		// Per input, the domains start in order, except tracks continued at their own
		// end because the gap to the domain would not fit 32 bits.
		for _, input := range []int{0, 1} {
			var prevStart int64
			started := false
			for d := range 4 {
				first, found := int64(0), false
				for _, tr := range tracks {
					if tr.input == input && d < len(tr.domains) && tr.domains[d].n > 0 && tr.domains[d].shift == 0 {
						s := toClock(tr.domains[d].presStart, tr.timescale)
						if !found || s < first {
							first, found = s, true
						}
					}
				}
				if found {
					if started && first < prevStart-1 {
						t.Fatalf("input %d: domain %d starts at %d, before the previous one at %d", input, d, first, prevStart)
					}
					prevStart, started = first, true
				}
			}
		}
		for _, tr := range tracks {
			var prev *trackDomain
			for i := range tr.domains {
				dom := &tr.domains[i]
				if dom.n == 0 {
					continue
				}
				if dom.presStop < dom.presStart {
					t.Fatal("a domain ends before it starts")
				}
				if prev != nil {
					last := prev.outDTS + prev.lastDTS - prev.srcDTS
					if dom.outDTS <= last || prev.finalLastDur == 0 || int64(prev.finalLastDur) != dom.outDTS-last {
						t.Fatalf("decode times: domain starts at %d after a last sample at %d, which lasts %d", dom.outDTS, last, prev.finalLastDur)
					}
				}
				prev = dom
			}
		}
	})
}

// fuzzReader reads numbers from fuzz bytes, zeros past the end.
type fuzzReader struct{ b []byte }

func (r *fuzzReader) u8() uint8 {
	if len(r.b) == 0 {
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *fuzzReader) u32() uint32 {
	return uint32(r.u8())<<24 | uint32(r.u8())<<16 | uint32(r.u8())<<8 | uint32(r.u8())
}

func (r *fuzzReader) u64() uint64 { return uint64(r.u32())<<32 | uint64(r.u32()) }
