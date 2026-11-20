package media

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

// clipPlan: an init, then six 4 s fragments, a map change before fragment 4 (index 5)
// and a discontinuity before fragment 5.
func clipPlan(t *testing.T) *Plan {
	t.Helper()
	pl := &HLSPlaylist{EndList: true}
	for i := range 6 {
		s := Segment{URI: string(rune('a' + i)), Duration: 4 * time.Second, Map: &InitSection{URI: "init1"}}
		if i >= 4 {
			s.Map = &InitSection{URI: "init2"}
		}
		if i == 5 {
			s.Discontinuity = true
		}
		pl.Segments = append(pl.Segments, s)
	}
	p, err := PlanHLS(pl)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFragmentStart(t *testing.T) {
	p := clipPlan(t)
	want := []time.Duration{0, 0, 4, 8, 12, 16, 16, 20}
	if len(p.Fragments) != len(want) {
		t.Fatalf("%d fragments", len(p.Fragments))
	}
	for i, f := range p.Fragments {
		if f.Start != want[i]*time.Second || f.FullIndex != i {
			t.Errorf("fragment %d: start %v full %d", i, f.Start, f.FullIndex)
		}
	}
}

func urls(p *Plan) string {
	s := ""
	for _, f := range p.Fragments {
		s += f.URL + ","
	}
	return s
}

func TestPlanWindow(t *testing.T) {
	p := clipPlan(t)
	for _, c := range []struct {
		clip   Clip
		leadIn int
		urls   string
		w      PlanWindow
	}{
		// [5 s, 9 s): fragments b (4-8) and c (8-12), with the first map.
		{Clip{Start: 5 * time.Second, End: 9 * time.Second}, 0, "init1,b,c,", PlanWindow{First: 0, From: 4 * time.Second, Exact: true}},
		// An edge: a fragment that ends at Start is not kept, one that starts at End neither.
		{Clip{Start: 4 * time.Second, End: 8 * time.Second}, 0, "init1,b,", PlanWindow{First: 0, From: 4 * time.Second, Exact: true}},
		// Lead-in: one more fragment back.
		{Clip{Start: 5 * time.Second, End: 9 * time.Second}, 1, "init1,a,b,c,", PlanWindow{First: 0, From: 0, Exact: true, AtStart: true, LeadIn: 1}},
		// Lead-in never before the first fragment.
		{Clip{Start: time.Second, End: 2 * time.Second}, 3, "init1,a,", PlanWindow{First: 0, From: 0, Exact: true, AtStart: true}},
		// Into the second map: the map change is kept.
		{Clip{Start: 13 * time.Second, End: 17 * time.Second}, 0, "init1,d,init2,e,", PlanWindow{First: 0, From: 12 * time.Second, Exact: true}},
		// Starting after the map change: only the second map.
		{Clip{Start: 17 * time.Second}, 0, "init2,e,f,", PlanWindow{First: 5, From: 16 * time.Second, Exact: true}},
		// After the discontinuity: not exact.
		{Clip{Start: 21 * time.Second}, 0, "init2,f,", PlanWindow{First: 5, From: 20 * time.Second, Exact: false}},
		// The whole plan.
		{Clip{}, 0, "init1,a,b,c,d,init2,e,f,", PlanWindow{First: 0, From: 0, Exact: true, AtStart: true}},
	} {
		sub, w, err := p.Window(c.clip, c.leadIn)
		if err != nil {
			t.Errorf("%+v: %v", c.clip, err)
			continue
		}
		if got := urls(sub); got != c.urls || w != c.w {
			t.Errorf("%+v lead-in %d: %s %+v, want %s %+v", c.clip, c.leadIn, got, w, c.urls, c.w)
		}
		for i, f := range sub.Fragments {
			if f.Index != i || p.Fragments[f.FullIndex].URL != f.URL {
				t.Errorf("%+v: fragment %d index %d full %d", c.clip, i, f.Index, f.FullIndex)
			}
		}
	}
	// A clip past the end.
	if _, _, err := p.Window(Clip{Start: 30 * time.Second}, 0); !errors.Is(err, ErrClip) {
		t.Errorf("past the end: %v", err)
	}
	// The audio of a clip: from the video's first kept fragment.
	sub, _, err := p.WindowFrom(4*time.Second, 9*time.Second)
	if err != nil || urls(sub) != "init1,b,c," {
		t.Errorf("WindowFrom: %s %v", urls(sub), err)
	}
	// A fragment without a duration cannot be windowed.
	p.Fragments[3].Duration = 0
	if _, _, err := p.Window(Clip{Start: 5 * time.Second}, 0); !errors.Is(err, ErrUnsupported) {
		t.Errorf("zero duration: %v", err)
	}
}

// DASH SegmentTimeline plans give exact segment times.
func TestPlanWindowDASH(t *testing.T) {
	m := &MPD{Type: "static", Periods: []Period{{Duration: 10 * time.Second, AdaptationSets: []AdaptationSet{{
		Representations: []Representation{{ID: "v", BaseURL: "https://h/", Template: &SegmentTemplate{
			Media: "s$Time$.m4s", Initialization: "init.mp4", Timescale: 1000,
			Timeline: []TimelineEntry{{T: 0, D: 2000, R: 1}, {D: 3000, R: 1}},
		}}},
	}}}}}
	p, err := PlanDASH(m, "v")
	if err != nil {
		t.Fatal(err)
	}
	sub, w, err := p.Window(Clip{Start: 4500 * time.Millisecond, End: 6 * time.Second}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Fragments) != 2 || sub.Fragments[1].URL != "https://h/s4000.m4s" || w.From != 4*time.Second || !w.Exact {
		t.Errorf("window %s %+v", urls(sub), w)
	}
}

// sidxBox builds a version-v 'sidx' box.
func sidxBox(v byte, ts uint32, ept, first uint64, refs [][3]uint32) []byte {
	var p []byte
	p = append(p, v, 0, 0, 0)
	p = binary.BigEndian.AppendUint32(p, 1)
	p = binary.BigEndian.AppendUint32(p, ts)
	if v == 0 {
		p = binary.BigEndian.AppendUint32(p, uint32(ept))
		p = binary.BigEndian.AppendUint32(p, uint32(first))
	} else {
		p = binary.BigEndian.AppendUint64(p, ept)
		p = binary.BigEndian.AppendUint64(p, first)
	}
	p = binary.BigEndian.AppendUint16(p, 0)
	p = binary.BigEndian.AppendUint16(p, uint16(len(refs)))
	for _, r := range refs {
		p = binary.BigEndian.AppendUint32(p, r[0])
		p = binary.BigEndian.AppendUint32(p, r[1])
		p = binary.BigEndian.AppendUint32(p, r[2])
	}
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(p)))
	b = append(b, "sidx"...)
	return append(b, p...)
}

func TestParseSidx(t *testing.T) {
	refs := [][3]uint32{{1000, 2000, 1 << 31}, {1500, 2000, 1 << 31}, {500, 1000, 0}}
	for _, v := range []byte{0, 1} {
		box := sidxBox(v, 1000, 500, 100, refs)
		ix, err := ParseSidx(box, 1000)
		if err != nil {
			t.Fatalf("v%d: %v", v, err)
		}
		want := []SidxRef{
			{Offset: 1100, Size: 1000, Start: 500 * time.Millisecond, Duration: 2 * time.Second, StartsWithSAP: true},
			{Offset: 2100, Size: 1500, Start: 2500 * time.Millisecond, Duration: 2 * time.Second, StartsWithSAP: true},
			{Offset: 3600, Size: 500, Start: 4500 * time.Millisecond, Duration: time.Second},
		}
		if len(ix.Refs) != len(want) {
			t.Fatalf("v%d: %d refs", v, len(ix.Refs))
		}
		for i := range want {
			if ix.Refs[i] != want[i] {
				t.Errorf("v%d ref %d: %+v, want %+v", v, i, ix.Refs[i], want[i])
			}
		}
		if f, l, ok := ix.Span(3*time.Second, 4600*time.Millisecond, 0); !ok || f != 1 || l != 2 {
			t.Errorf("span %d %d %v", f, l, ok)
		}
		if f, l, ok := ix.Span(3*time.Second, 0, 4); !ok || f != 0 || l != 2 {
			t.Errorf("span with lead-in %d %d %v", f, l, ok)
		}
		if _, _, ok := ix.Span(6*time.Second, 0, 0); ok {
			t.Error("span past the end")
		}
	}
	for name, c := range map[string]struct {
		box  []byte
		want error
	}{
		"hierarchical": {sidxBox(0, 1000, 0, 0, [][3]uint32{{1<<31 | 10, 1, 0}}), ErrUnsupported},
		"zero size":    {sidxBox(0, 1000, 0, 0, [][3]uint32{{0, 1, 0}}), ErrMalformed},
		"timescale 0":  {sidxBox(0, 0, 0, 0, nil), ErrMalformed},
		"version 2":    {sidxBox(2, 1000, 0, 0, nil), ErrUnsupported},
		"truncated":    {sidxBox(0, 1000, 0, 0, refs)[:40], ErrMalformed},
		"not sidx":     {[]byte("\x00\x00\x00\x08moov"), ErrMalformed},
	} {
		if _, err := ParseSidx(c.box, 0); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

func FuzzParseSidx(f *testing.F) {
	f.Add(sidxBox(0, 1000, 0, 0, [][3]uint32{{1000, 2000, 1 << 31}}), int64(100))
	f.Add(sidxBox(1, 90000, 1<<40, 1<<20, [][3]uint32{{1, 1, 0}, {2, 2, 0}}), int64(0))
	f.Fuzz(func(t *testing.T, box []byte, end int64) {
		ix, err := ParseSidx(box, end)
		if err != nil {
			checkClassified(t, "ParseSidx", err)
			return
		}
		if len(ix.Refs) > MaxSidxRefs {
			t.Fatalf("%d refs", len(ix.Refs))
		}
		for i, r := range ix.Refs {
			if r.Size <= 0 || r.Offset < end || r.Duration < 0 || i > 0 && r.Offset != ix.Refs[i-1].End() {
				t.Fatalf("ref %d: %+v", i, r)
			}
		}
	})
}
