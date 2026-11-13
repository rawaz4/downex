package media

import (
	"errors"
	"testing"
	"time"
)

// Plans also check playlists and MPDs built by hand rather than parsed.
func TestPlansCheckHandBuiltInput(t *testing.T) {
	vod := func(segs ...Segment) *HLSPlaylist { return &HLSPlaylist{EndList: true, Segments: segs} }
	for name, c := range map[string]struct {
		p    *HLSPlaylist
		want error
	}{
		"empty URI":      {vod(Segment{Duration: time.Second}), ErrMalformed},
		"reversed range": {vod(Segment{URI: "a", ByteRange: &[2]int64{5, 4}}), ErrMalformed},
		"negative range": {vod(Segment{URI: "a", ByteRange: &[2]int64{-1, 4}}), ErrMalformed},
		"negative dur":   {vod(Segment{URI: "a", Duration: -time.Second}), ErrMalformed},
		"bad map":        {vod(Segment{URI: "a", Map: &InitSection{}}), ErrMalformed},
		"too many":       {vod(make([]Segment, MaxSegments+1)...), ErrTooLarge},
		"no segments":    {vod(), ErrNotFound},
	} {
		if _, err := PlanHLS(c.p); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	// A repeated initialization section is fetched once, even when equal by value only.
	p, err := PlanHLS(vod(
		Segment{URI: "a", Map: &InitSection{URI: "i", ByteRange: &[2]int64{0, 9}}},
		Segment{URI: "b", Map: &InitSection{URI: "i", ByteRange: &[2]int64{0, 9}}},
		Segment{URI: "c", Map: &InitSection{URI: "i"}},
	))
	if err != nil {
		t.Fatal(err)
	}
	comparePlan(t, p, []wantFragment{
		{url: "i", rng: rng(0, 9), init: true}, {url: "a"}, {url: "b"}, {url: "i", init: true}, {url: "c"},
	})

	rep := func(r Representation) *MPD {
		r.ID = "r"
		return &MPD{Type: "static", Periods: []Period{{Duration: 4 * time.Second,
			AdaptationSets: []AdaptationSet{{Representations: []Representation{r}}}}}}
	}
	for name, m := range map[string]*MPD{
		"zero template timescale": rep(Representation{BaseURL: "https://h/", Template: &SegmentTemplate{Media: "$Number$", Duration: 1}}),
		"zero list timescale":     rep(Representation{BaseURL: "https://h/", List: &SegmentList{Segments: []SegmentURL{{Media: "a"}}}}),
		"zero S duration": rep(Representation{BaseURL: "https://h/", Template: &SegmentTemplate{Media: "$Number$", Timescale: 1,
			Timeline: []TimelineEntry{{D: 0}}}}),
		"bad list range": rep(Representation{BaseURL: "https://h/", List: &SegmentList{Timescale: 1,
			Segments: []SegmentURL{{Media: "a", MediaRange: &[2]int64{3, 1}}}}}),
		"bad base": rep(Representation{BaseURL: "http://[::1", Base: &SegmentBase{Timescale: 1}}),
	} {
		if _, err := PlanDASH(m, "r"); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v, want ErrMalformed", name, err)
		}
	}
	// endNumber numbers segments even when the Period length is unknown, and caps
	// them otherwise.
	open := rep(Representation{BaseURL: "https://h/", Template: &SegmentTemplate{Media: "$Number$.m4s", Timescale: 1, Duration: 2,
		StartNumber: 3, EndNumber: 5}})
	open.Periods[0].Duration = 0
	p, err = PlanDASH(open, "r")
	if err != nil {
		t.Fatal(err)
	}
	comparePlan(t, p, []wantFragment{{url: "https://h/3.m4s", durMS: 2000}, {url: "https://h/4.m4s", durMS: 2000}, {url: "https://h/5.m4s", durMS: 2000}})
	capped := rep(Representation{BaseURL: "https://h/", Template: &SegmentTemplate{Media: "$Time$.m4s", Timescale: 1,
		StartNumber: 1, EndNumber: 2, Timeline: []TimelineEntry{{D: 1, R: -1}}}})
	capped.Periods[0].Duration = time.Hour * 1000
	p, err = PlanDASH(capped, "r")
	if err != nil {
		t.Fatal(err)
	}
	comparePlan(t, p, []wantFragment{{url: "https://h/0.m4s", durMS: 1000}, {url: "https://h/1.m4s", durMS: 1000}})
}
