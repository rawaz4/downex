package engine

import (
	"testing"
	"time"
)

// TestPlanSplitConformance pins every constant of docs/ENGINE-SPEC.md §4 and the
// forced-split rule of §5.
func TestPlanSplitConformance(t *testing.T) {
	const big = 100_000_000
	seg := func(start, end, pos int64) segView { return segView{Start: start, End: end, Pos: pos} }
	slow := func(s segView, idle time.Duration) segView { s.Idle = idle; return s }
	behind := func(s segView, eta time.Duration) segView { s.ETA = eta; return s }

	tests := []struct {
		name       string
		segs       []segView
		speed      float64
		first      bool
		tail       bool
		several    bool
		force      bool
		wantOK     bool
		wantVictim int
		wantStart  int64
	}{
		// Floor and 2·minChunk+3 with minChunk = 11,680: 2·11,680+3 = 23,363.
		{name: "exactly 2*minChunk+3 splits in half", segs: []segView{seg(0, 23_362, 0)},
			wantOK: true, wantStart: 23_362 - 23_363/2},
		{name: "one byte below 23,363 refused", segs: []segView{seg(0, 23_361, 0)}},
		{name: "received bytes count against remaining", segs: []segView{seg(0, 23_362, 1)}},
		{name: "remaining measured from position", segs: []segView{seg(1_000, 100_999, 50_000)},
			wantOK: true, wantStart: 100_999 - 51_000/2},
		// minChunk = 116,800 above 116,800 B/s: threshold 2·116,800+3 = 233,603.
		{name: "fast download needs 233,603", segs: []segView{seg(0, 233_601, 0)}, speed: 116_801},
		{name: "fast download splits at 233,603", segs: []segView{seg(0, 233_602, 0)}, speed: 116_801,
			wantOK: true, wantStart: 233_602 - 233_603/2},
		{name: "speed of exactly 116,800 is not fast", segs: []segView{seg(0, 23_362, 0)}, speed: 116_800,
			wantOK: true, wantStart: 23_362 - 23_363/2},
		// Victim selection.
		{name: "largest remaining is the victim",
			segs:   []segView{seg(0, 99_999, 0), seg(100_000, 499_999, 100_000), seg(500_000, 599_999, 500_000)},
			wantOK: true, wantVictim: 1, wantStart: 499_999 - 400_000/2},
		{name: "ties pick the first", segs: []segView{seg(0, 99_999, 0), seg(100_000, 199_999, 100_000)},
			wantOK: true, wantVictim: 0, wantStart: 99_999 - 50_000},
		{name: "open-ended segments are never split", segs: []segView{seg(0, -1, 0)}},
		{name: "finished segments are skipped", segs: []segView{seg(0, 99, 100), seg(100, 50_099, 100)},
			wantOK: true, wantVictim: 1, wantStart: 50_099 - 25_000},
		// Tail-first first split: end−23,360, or end−11,680 when remaining < 46,721.
		{name: "tail-first at 46,721 takes 23,360", segs: []segView{seg(0, 46_720, 0)}, first: true, tail: true,
			wantOK: true, wantStart: 46_720 - 23_360},
		{name: "tail-first below 46,721 takes 11,680", segs: []segView{seg(0, 46_719, 0)}, first: true, tail: true,
			wantOK: true, wantStart: 46_719 - 11_680},
		{name: "tail-first on a big file", segs: []segView{seg(0, big-1, 0)}, first: true, tail: true,
			wantOK: true, wantStart: big - 1 - 23_360},
		{name: "tail-first at the floor", segs: []segView{seg(0, 23_362, 0)}, first: true, tail: true,
			wantOK: true, wantStart: 23_362 - 11_680},
		{name: "tail-first only applies to the first split", segs: []segView{seg(0, big-1, 0)}, tail: true,
			wantOK: true, wantStart: big - 1 - big/2},
		// Early first split: min(mid, start + 10·(received + 11,680)).
		{name: "early first split nothing received", segs: []segView{seg(0, big-1, 0)}, first: true,
			wantOK: true, wantStart: 116_800},
		{name: "early first split with received bytes", segs: []segView{seg(0, big-1, 5_000)}, first: true,
			wantOK: true, wantStart: 10 * (5_000 + 11_680)},
		{name: "early first split relative to start", segs: []segView{seg(1_000, big-1, 1_000)}, first: true,
			wantOK: true, wantStart: 1_000 + 116_800},
		{name: "early first split capped at mid", segs: []segView{seg(0, 99_999, 0)}, first: true,
			wantOK: true, wantStart: 99_999 - 50_000},
		// A ramp-up step opening several connections splits the first segment in half
		// (the probe keeps an even share); tail-first still applies.
		{name: "first split of several at once halves", segs: []segView{seg(0, big-1, 5_000)}, first: true, several: true,
			wantOK: true, wantStart: big - 1 - (big-5_000)/2},
		{name: "tail-first first split of several at once", segs: []segView{seg(0, big-1, 0)}, first: true, tail: true, several: true,
			wantOK: true, wantStart: big - 1 - 23_360},
		// Forced splits: slowest segment, floor ignored, half split.
		{name: "forced split ignores the floor", segs: []segView{slow(seg(0, 99, 0), 3*time.Second)}, force: true,
			wantOK: true, wantStart: 99 - 50},
		{name: "unforced split of the same segment refused", segs: []segView{slow(seg(0, 99, 0), 3*time.Second)}},
		{name: "forced split picks the slowest, not the largest",
			segs:  []segView{slow(seg(0, 999_999, 0), 2*time.Second), slow(seg(1_000_000, 1_000_999, 1_000_000), 5*time.Second)},
			force: true, wantOK: true, wantVictim: 1, wantStart: 1_000_999 - 500},
		{name: "forced split needs a slow segment", segs: []segView{seg(0, 999_999, 0)}, force: true},
		{name: "forced split keeps a byte for the victim", segs: []segView{slow(seg(0, 1, 0), time.Minute)}, force: true},
		{name: "forced first split uses half split", segs: []segView{slow(seg(0, big-1, 0), time.Minute)},
			first: true, tail: true, force: true, wantOK: true, wantStart: big - 1 - big/2},
		// End-game stealing from slow but still-receiving owners (ETA, §5).
		{name: "forced split prefers an idle owner over a slow one",
			segs:  []segView{behind(seg(0, 999_999, 0), time.Hour), slow(seg(1_000_000, 1_000_099, 1_000_000), 3*time.Second)},
			force: true, wantOK: true, wantVictim: 1, wantStart: 1_000_099 - 50},
		{name: "forced split takes the largest ETA",
			segs:  []segView{behind(seg(0, 99_999, 0), 2*time.Second), behind(seg(100_000, 149_999, 100_000), 5*time.Second)},
			force: true, wantOK: true, wantVictim: 1, wantStart: 149_999 - 25_000},
		{name: "a slow owner at 46,720 remaining is split in half",
			segs: []segView{behind(seg(0, 46_719, 0), time.Minute)}, force: true, wantOK: true, wantStart: 46_719 - 23_360},
		{name: "a slow owner below 46,720 remaining is refused",
			segs: []segView{behind(seg(0, 46_718, 0), time.Minute)}, force: true},
		{name: "remaining of a slow owner counts from its position",
			segs: []segView{behind(seg(0, 99_999, 53_281), time.Minute)}, force: true},
		{name: "a slow owner too small to steal from does not hide another",
			segs:  []segView{behind(seg(0, 46_718, 0), time.Hour), behind(seg(46_719, 146_718, 46_719), 2*time.Second)},
			force: true, wantOK: true, wantVictim: 1, wantStart: 146_718 - 50_000},
		{name: "an idle owner below 46,720 is still split", segs: []segView{slow(seg(0, 99, 0), 3*time.Second), behind(seg(100, 999_999, 100), time.Hour)},
			force: true, wantOK: true, wantVictim: 0, wantStart: 99 - 50},
		{name: "forced first split of a slow owner uses half split", segs: []segView{behind(seg(0, big-1, 0), time.Minute)},
			first: true, tail: true, force: true, wantOK: true, wantStart: big - 1 - big/2},
		{name: "unforced split ignores slow owners",
			segs:   []segView{behind(seg(0, 99_999, 0), time.Hour), seg(100_000, 299_999, 100_000)},
			wantOK: true, wantVictim: 1, wantStart: 299_999 - 100_000},
		{name: "unforced split does not lift the floor for a slow owner",
			segs: []segView{behind(seg(0, 23_361, 0), time.Hour)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vi, ns, ok := planSplit(tc.segs, tc.speed, tc.first, tc.tail, tc.several, tc.force)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (victim %d, newStart %d)", ok, tc.wantOK, vi, ns)
			}
			if !ok {
				return
			}
			if vi != tc.wantVictim || ns != tc.wantStart {
				t.Fatalf("got victim %d newStart %d, want victim %d newStart %d", vi, ns, tc.wantVictim, tc.wantStart)
			}
			v := tc.segs[vi]
			if ns <= v.Pos || ns > v.End {
				t.Fatalf("newStart %d outside (%d, %d]", ns, v.Pos, v.End)
			}
		})
	}
}

func TestSplitConstants(t *testing.T) {
	if 2*minChunkBase+3 != splitFloor {
		t.Fatalf("2·minChunk+3 = %d, floor = %d", 2*minChunkBase+3, splitFloor)
	}
	for _, c := range []struct {
		got, want int64
	}{
		{minChunkBase, 11_680}, {minChunkFast, 116_800}, {fastThresholdBps, 116_800}, {splitFloor, 23_363},
		{tailFirstChunk, 23_360}, {tailFirstSmall, 11_680}, {tailFirstLimit, 46_721}, {earlySplitPad, 11_680},
		{earlySplitFactor, 10}, {bytesPerConnection, 10_240}, {stealMinRemaining, 46_720}, {slowSpeedRatio, 4},
	} {
		if c.got != c.want {
			t.Errorf("constant = %d, want %d", c.got, c.want)
		}
	}
	if minChunk(116_800) != 11_680 || minChunk(116_801) != 116_800 {
		t.Fatal("minChunk threshold")
	}
	if slowMinETA != time.Second || slowMinAge != time.Second {
		t.Fatalf("slowMinETA %v, slowMinAge %v, want 1 s each", slowMinETA, slowMinAge)
	}
}

func TestIsTailFirst(t *testing.T) {
	for _, tc := range []struct {
		name, ct string
		want     bool
	}{
		{"a.zip", "", true}, {"A.ZIP", "", true}, {"a.7z", "", true}, {"a.rar", "", true}, {"a.apk", "", true},
		{"a.jar", "", true}, {"a.mp4", "", true}, {"a.mov", "", true}, {"a.m4a", "", true}, {"a.mkv", "", true},
		{"a.iso", "", false}, {"download", "application/zip", true}, {"x", "video/mp4; codecs=avc1", true},
		{"x", "application/vnd.android.package-archive", true}, {"x", "video/x-matroska", true},
		{"x", "application/octet-stream", false}, {"x", "not a / type;;", false}, {"a.tar.gz", "", false},
	} {
		if got := isTailFirst(tc.name, tc.ct); got != tc.want {
			t.Errorf("isTailFirst(%q, %q) = %v, want %v", tc.name, tc.ct, got, tc.want)
		}
	}
}
