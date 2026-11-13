package remux

import (
	"math"
	"testing"
)

func TestUnwrapper(t *testing.T) {
	var u unwrapper
	seq := []struct{ in, want int64 }{
		{tsWrap - 6000, tsWrap - 6000},
		{tsWrap - 3000, tsWrap - 3000},
		{0, tsWrap},                                         // wraps forward
		{3000, tsWrap + 3000},                               //
		{tsWrap - 1000, tsWrap - 1000},                      // a step back across the wrap
		{4000, tsWrap + 4000},                               // forward again
		{4000 + tsWrap/2 - 1, tsWrap + 4000 + tsWrap/2 - 1}, // just under half a period ahead
	}
	for i, s := range seq {
		if got := u.unwrap(s.in); got != s.want {
			t.Fatalf("step %d: unwrap(%d) = %d, want %d", i, s.in, got, s.want)
		}
	}
	// PTS and DTS of a unit: DTS first; a missing DTS is the PTS; a missing PTS
	// gives the last value.
	u = unwrapper{}
	if p, d, has := u.unit(tsWrap-100, tsWrap-3100); !has || p != tsWrap-100 || d != tsWrap-3100 {
		t.Fatalf("unit = %d %d %v", p, d, has)
	}
	if p, d, has := u.unit(500, -1); !has || p != tsWrap+500 || d != p {
		t.Fatalf("unit = %d %d %v", p, d, has)
	}
	if p, d, has := u.unit(-1, -1); has || p != tsWrap+500 || d != p {
		t.Fatalf("unit without PTS = %d %d %v", p, d, has)
	}
	// Values beyond the bound start afresh instead of growing.
	u = unwrapper{last: maxTSValue + 1, ok: true}
	if got := u.unwrap(7); got != 7 {
		t.Fatalf("unwrap after the bound = %d", got)
	}
}

func TestMulDivRound(t *testing.T) {
	for _, tc := range []struct {
		v        int64
		mul, div uint64
		want     int64
	}{
		{90000, 48000, 90000, 48000},
		{1920, 48000, 90000, 1024},
		{2090, 44100, 90000, 1024}, // 1024.1
		{-1920, 48000, 90000, -1024},
		{3, 1, 2, 2}, // 1.5 rounds away from zero
		{-3, 1, 2, -2},
		{math.MaxInt64, 2, 1, math.MaxInt64},
		{math.MinInt64, 2, 1, math.MinInt64},
		{math.MinInt64, 1, 1, math.MinInt64},
		{1 << 60, 96000, 90000, 1229782938247303441},
	} {
		if got := mulDivRound(tc.v, tc.mul, tc.div); got != tc.want {
			t.Errorf("mulDivRound(%d, %d, %d) = %d, want %d", tc.v, tc.mul, tc.div, got, tc.want)
		}
	}
}

func TestVideoTimer(t *testing.T) {
	type step struct {
		pts, dts int64
		has      bool
		d        int64
		cto      int32
		prevDur  int64
	}
	for name, steps := range map[string][]step{
		"B-frames": {
			{3000, 0, true, 0, 3000, -1},
			{12000, 3000, true, 3000, 9000, 3000},
			{6000, 6000, true, 6000, 0, 3000},
		},
		"repeat within 1 ms is clamped": {
			{0, 0, true, 0, 0, -1},
			{3000, 3000, true, 3000, 0, 3000},
			{2950, 2950, true, 3001, -51, 1},
		},
		"step back beyond 1 ms is stitched": {
			{0, 0, true, 0, 0, -1},
			{3000, 3000, true, 3000, 0, 3000},
			{100, 100, true, 6000, 0, 3000},
			{3100, 3100, true, 9000, 0, 3000}, // follows the stitched timeline
		},
		"jump of more than 10 s is stitched": {
			{0, 0, true, 0, 0, -1},
			{1500, 1500, true, 1500, 0, 1500},
			{1500 + 11*tsClock, 1500 + 11*tsClock, true, 3000, 0, 1500},
		},
		"gap within 10 s is kept": {
			{0, 0, true, 0, 0, -1},
			{9 * tsClock, 9 * tsClock, true, 9 * tsClock, 0, 9 * tsClock},
		},
		"missing PTS continues": {
			{0, 0, true, 0, 0, -1},
			{0, 0, false, 3000, 0, 3000},
		},
		"bogus composition offset": {
			{20 * tsClock, 0, true, 0, 0, -1},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var vt videoTimer
			for i, s := range steps {
				d, cto, prev := vt.place(s.pts, s.dts, s.has)
				if d != s.d || cto != s.cto || prev != s.prevDur {
					t.Fatalf("step %d: place = %d %d %d, want %d %d %d", i, d, cto, prev, s.d, s.cto, s.prevDur)
				}
			}
		})
	}
	var vt videoTimer
	if vt.dur() != defaultFrameTicks {
		t.Fatal("default duration")
	}
	vt.place(0, 0, true)
	vt.place(1800, 1800, true)
	vt.newDomain()
	if vt.dur() != 1800 {
		t.Fatal("the last duration is not kept across domains")
	}
	if d, _, prev := vt.place(5, 5, true); d != 5 || prev != -1 {
		t.Fatalf("new domain: %d %d", d, prev)
	}
}

func TestAudioTimer(t *testing.T) {
	type step struct {
		f    int64
		has  bool
		at   int64
		drop bool
		gap  int64
	}
	const rate = 48000
	for name, steps := range map[string][]step{
		"continuous with jitter": {
			{1000, true, 1000, false, 0},
			{2025, true, 2024, false, 0},
			{3047, true, 3048, false, 0},
			{0, false, 4072, false, 0},
		},
		"gap lengthens": {
			{0, true, 0, false, 0},
			{1024 + 600, true, 1624, false, 600},
			{2648, true, 2648, false, 0},
		},
		"overlap drops until time catches up": {
			{0, true, 0, false, 0},
			{1024, true, 1024, false, 0},
			{1024, true, 0, true, 0},
			{2048, true, 2048, false, 0},
		},
		"jump beyond 10 s is stitched": {
			{0, true, 0, false, 0},
			{1024 + 11*rate, true, 1024, false, 0},
			{1024 + 11*rate + 1024, true, 2048, false, 0},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var at audioTimer
			for i, s := range steps {
				got, drop, gap := at.place(s.f, s.has, rate)
				if drop != s.drop || (!drop && (got != s.at || gap != s.gap)) {
					t.Fatalf("step %d: place = %d %v %d, want %d %v %d", i, got, drop, gap, s.at, s.drop, s.gap)
				}
			}
		})
	}
}

func TestInputJumps(t *testing.T) {
	type unit struct {
		ro       role
		pts, dts int64
		has      bool
		domain   int
		wantPTS  int64
		wantDTS  int64
	}
	v := func(dts, want int64) unit { return unit{roleVideo, dts, dts, true, 0, want, want} }
	a := func(pts, want int64) unit { return unit{roleAudio, pts, pts, true, 0, want, want} }
	for name, units := range map[string][]unit{
		"steady streams are unchanged": {
			v(1_000_000, 1_000_000), a(1_000_000, 1_000_000), v(1_003_000, 1_003_000), a(1_009_600, 1_009_600),
		},
		// The audio shows the reset first: both streams move by what continues the audio.
		"audio shows a step back first": {
			v(1_000_000, 1_000_000), a(1_000_000, 1_000_000), v(1_003_000, 1_003_000), a(1_009_600, 1_009_600),
			a(0, 1_019_200), v(0, 1_019_200), v(3000, 1_022_200), a(9600, 1_028_800),
		},
		// The video shows it first: the audio follows by the same amount.
		"video shows a step back first": {
			v(1_000_000, 1_000_000), a(1_000_000, 1_000_000), v(1_003_000, 1_003_000), a(1_009_600, 1_009_600),
			v(500_000, 1_006_000), a(500_000, 1_006_000), v(503_000, 1_009_000),
		},
		// An audio unit up to 1 s before the previous one is an overlap the audio timer
		// drops, not a jump of the input.
		"audio overlap is not a jump": {
			a(1_000_000, 1_000_000), a(1_009_600, 1_009_600), a(1_004_800, 1_004_800), a(940_000, 940_000),
		},
		"video repeat within 1 ms is not a jump": {
			v(0, 0), v(3000, 3000), v(2950, 2950), v(6000, 6000),
		},
		"forward jump beyond 10 s": {
			v(0, 0), v(1500, 1500), a(0, 0), v(1500+11*tsClock, 3000), a(11*tsClock, 1500), v(4500+11*tsClock, 6000),
		},
		"a unit without timestamps moves with the input": {
			v(1_000_000, 1_000_000), v(1_003_000, 1_003_000), v(0, 1_006_000),
			{roleVideo, 5, 5, false, 0, 1_006_005, 1_006_005},
		},
		"the first step of a stream defaults to one frame": {
			v(1_000_000, 1_000_000), v(0, 1_003_000),
		},
		// Each discontinuity domain has its own offset; a late unit of an earlier
		// domain (one that completes after the next file started) keeps that domain's.
		"domains keep their own offsets": {
			v(1_000_000, 1_000_000), v(1_003_000, 1_003_000), v(0, 1_006_000),
			{roleVideo, 77, 77, true, 1, 77, 77},
			{roleAudio, 100, 100, true, 0, 1_006_100, 1_006_100},
			{roleVideo, 3077, 3077, true, 1, 3077, 3077},
		},
		"B-frames: presentation times are not checked": {
			{roleVideo, 6000, 0, true, 0, 6000, 0},
			{roleVideo, 3000, 3000, true, 0, 3000, 3000},
			{roleVideo, 12000, 6000, true, 0, 12000, 6000},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var j inputJumps
			for i, u := range units {
				p, d := j.adjust(u.ro, u.pts, u.dts, u.has, u.domain)
				if p != u.wantPTS || d != u.wantDTS {
					t.Fatalf("unit %d: adjust = %d %d, want %d %d", i, p, d, u.wantPTS, u.wantDTS)
				}
			}
		})
	}
}

// dom is a trackDomain of n samples of duration dur starting at dts, with
// composition offset cto for all of them.
func dom(n int, dts int64, dur uint32, cto int32) trackDomain {
	last := dts + int64(n-1)*int64(dur)
	return trackDomain{n: n, srcDTS: dts, lastDTS: last, lastDur: dur, minPTS: dts + int64(cto), presEnd: last + int64(dur) + int64(cto), minCTO: cto}
}

func TestPlaceDomains(t *testing.T) {
	t.Run("A/V offsets kept, no gaps", func(t *testing.T) {
		// Domain 0: video 0..3 s (PTS one frame later), audio from 0.5 s. Domain 1
		// starts over at other timestamps, audio 0.2 s before video.
		v := &timedTrack{input: 0, timescale: tsClock, domains: []trackDomain{
			dom(90, 1_000_000, 3000, 3000), dom(30, 50_000, 3000, 3000)}}
		a := &timedTrack{input: 0, timescale: 48000, domains: []trackDomain{
			dom(100, 1_000_000*48000/tsClock+24000, 1024, 0), dom(40, (53_000-18_000)*48000/tsClock, 1024, 0)}}
		if !placeDomains([]*timedTrack{v, a}) {
			t.Fatal("overflow")
		}
		v0, v1, a0, a1 := v.domains[0], v.domains[1], a.domains[0], a.domains[1]
		// The video presents first: it starts at 0.
		if v0.presStart != 0 || v0.outDTS != -3000 {
			t.Fatalf("video domain 0 at %d (decode %d)", v0.presStart, v0.outDTS)
		}
		if toClock(a0.presStart, 48000) != 45000-3000 {
			t.Fatalf("audio domain 0 at %d", toClock(a0.presStart, 48000))
		}
		// Domain 1 starts where domain 0 ended (the later of the two ends).
		e0 := max(toClock(v0.presStop, tsClock), toClock(a0.presStop, 48000))
		p1 := min(toClock(v1.presStart, tsClock), toClock(a1.presStart, 48000))
		if p1 != e0 {
			t.Fatalf("domain 1 starts at %d, domain 0 ended at %d", p1, e0)
		}
		// Audio still precedes video by 0.2 s (±1 ms).
		if d := toClock(v1.presStart, tsClock) - toClock(a1.presStart, 48000); d < 18000-90 || d > 18000+90 {
			t.Fatalf("A/V offset in domain 1: %d ticks", d)
		}
		// The last sample of a domain lasts until the next domain's first.
		if got := v0.outDTS + (v0.lastDTS - v0.srcDTS) + int64(v0.finalLastDur); got != v1.outDTS {
			t.Fatalf("video: domain 0 ends at %d, domain 1 starts at %d", got, v1.outDTS)
		}
		if got := a0.outDTS + (a0.lastDTS - a0.srcDTS) + int64(a0.finalLastDur); got != a1.outDTS {
			t.Fatalf("audio: domain 0 ends at %d, domain 1 starts at %d", got, a1.outDTS)
		}
	})
	t.Run("decode times increase strictly", func(t *testing.T) {
		// The second domain's first sample decodes three frames before it is
		// presented, earlier than the first domain's last sample.
		v := &timedTrack{timescale: tsClock, domains: []trackDomain{dom(10, 0, 3000, 0), dom(10, 900000, 3000, 9000)}}
		if !placeDomains([]*timedTrack{v}) {
			t.Fatal("overflow")
		}
		d0, d1 := v.domains[0], v.domains[1]
		lastDTS := d0.outDTS + d0.lastDTS - d0.srcDTS
		if d1.outDTS != lastDTS+1 || d1.delta != 6001 || d0.finalLastDur != 1 {
			t.Fatalf("domain 1 decodes at %d (last %d), delta %d, last duration %d", d1.outDTS, lastDTS, d1.delta, d0.finalLastDur)
		}
		if d1.presStart != 30000 {
			t.Fatalf("domain 1 presented at %d, want 30000", d1.presStart)
		}
	})
	t.Run("an input with more domains continues at its own end", func(t *testing.T) {
		v := &timedTrack{input: 0, timescale: tsClock, domains: []trackDomain{dom(10, 0, 3000, 0), dom(10, 500, 3000, 0), dom(5, 77, 3000, 0)}}
		a := &timedTrack{input: 1, timescale: tsClock, domains: []trackDomain{dom(20, 0, 3000, 0), dom(5, 900, 3000, 0)}}
		if !placeDomains([]*timedTrack{v, a}) {
			t.Fatal("overflow")
		}
		// Domain 1 starts at the end of the longer track of domain 0; the audio keeps
		// its offset of 400 ticks.
		if v.domains[1].presStart != 60000 || a.domains[1].presStart != 60400 {
			t.Fatalf("domain 1 at %d / %d", v.domains[1].presStart, a.domains[1].presStart)
		}
		// Domain 2 exists in input 0 only: it follows input 0's own end.
		if v.domains[2].presStart != 90000 {
			t.Fatalf("domain 2 at %d, want 90000", v.domains[2].presStart)
		}
	})
	t.Run("empty domains and late tracks", func(t *testing.T) {
		v := &timedTrack{input: 0, timescale: tsClock, domains: []trackDomain{{}, dom(10, 1000, 3000, 0)}}
		a := &timedTrack{input: 0, timescale: tsClock, domains: []trackDomain{dom(10, 0, 3000, 0)}}
		if !placeDomains([]*timedTrack{v, a}) {
			t.Fatal("overflow")
		}
		if a.domains[0].presStart != 0 || v.domains[1].presStart != 30000 {
			t.Fatalf("starts %d %d", a.domains[0].presStart, v.domains[1].presStart)
		}
	})
	t.Run("gap beyond 32 bits is stitched", func(t *testing.T) {
		v := &timedTrack{input: 0, timescale: tsClock, domains: []trackDomain{dom(2, 0, 3000, 0), {}, dom(2, 0, 3000, 0)}}
		a := &timedTrack{input: 0, timescale: tsClock, domains: []trackDomain{dom(2, 0, 3000, 0), dom(2, 0, math.MaxUint32, 0), dom(2, 0, 3000, 0)}}
		if !placeDomains([]*timedTrack{v, a}) {
			t.Fatal("overflow")
		}
		if v.domains[0].finalLastDur != 3000 || v.domains[2].outDTS != 6000 {
			t.Fatalf("video continues at %d with a last duration of %d", v.domains[2].outDTS, v.domains[0].finalLastDur)
		}
	})
	t.Run("separate inputs straddling the wrap", func(t *testing.T) {
		// Input 0 (video) unwrapped from just below 2^33, input 1 (audio) from just
		// after the wrap: the audio really starts 0.2 s after the video, in both
		// domains, also when the second domain of the video jumps by about 2^32.
		v := &timedTrack{input: 0, timescale: tsClock, domains: []trackDomain{dom(10, tsWrap-9000, 3000, 0), dom(10, tsWrap+tsWrap/2+100, 3000, 0)}}
		a := &timedTrack{input: 1, timescale: 48000, domains: []trackDomain{dom(10, 9000*48000/tsClock, 1024, 0), dom(10, (tsWrap/2-tsWrap+100+18000)*48000/tsClock, 1024, 0)}}
		if !placeDomains([]*timedTrack{v, a}) {
			t.Fatal("overflow")
		}
		for d := range 2 {
			off := toClock(a.domains[d].presStart, 48000) - toClock(v.domains[d].presStart, tsClock)
			if off < 18000-90 || off > 18000+90 {
				t.Fatalf("domain %d: audio starts %d ticks after the video, want 18000", d, off)
			}
		}
		if v.domains[0].presStart != 0 {
			t.Fatalf("video starts at %d", v.domains[0].presStart)
		}
	})
	t.Run("overflow is reported", func(t *testing.T) {
		v := &timedTrack{timescale: tsClock, domains: []trackDomain{dom(2, math.MaxInt64-10, 3000, 0), dom(2, math.MinInt64+10, 3000, 0)}}
		if placeDomains([]*timedTrack{v}) {
			t.Fatal("no overflow reported")
		}
	})
}
