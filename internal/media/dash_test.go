package media

import (
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"
)

const mpdBase = "https://example.com/vod/manifest.mpd"

func parseFixtureMPD(t *testing.T, name string) *MPD {
	t.Helper()
	m, err := ParseDASH(mustURL(t, mpdBase), fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func planDASH(t *testing.T, m *MPD, id string) *Plan {
	t.Helper()
	p, err := PlanDASH(m, id)
	if err != nil {
		t.Fatalf("PlanDASH(%q): %v", id, err)
	}
	checkPlan(t, p)
	return p
}

// mpd wraps Period content in a static MPD of the given duration.
func mpd(duration, periods string) []byte {
	return []byte(`<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="` +
		duration + `">` + periods + `</MPD>`)
}

func TestDASHTemplateNumber(t *testing.T) {
	m := parseFixtureMPD(t, "dash/template_number.mpd")
	if m.Type != "static" || m.MediaPresentationDuration != 60500*time.Millisecond || len(m.Periods) != 1 {
		t.Fatalf("MPD = %+v", m)
	}
	per := m.Periods[0]
	if per.ID != "p0" || per.Duration != 60500*time.Millisecond || len(per.AdaptationSets) != 2 {
		t.Fatalf("period = %+v", per)
	}
	v := per.AdaptationSets[0].Representations[0]
	if v.ID != "v720" || v.MimeType != "video/mp4" || v.Width != 1280 || v.Height != 720 || v.Bandwidth != 3000000 ||
		v.BaseURL != "https://cdn.example.org/content/video/" || v.Template == nil || v.Template.Timescale != 90000 ||
		v.Template.Duration != 360000 || v.Template.StartNumber != 1 || v.FrameRate != "30000/1001" {
		t.Fatalf("representation = %+v", v)
	}
	if a := per.AdaptationSets[1].Representations[0]; a.Lang != "en" || a.Label != "English" || a.AudioSamplingRate != "48000" {
		t.Fatalf("audio representation = %+v", a)
	}

	// 60.5 s of 4 s segments: 15 full segments and a last one of 0.5 s.
	p := planDASH(t, m, "v720")
	if p.Container != ContainerFMP4 || len(p.Fragments) != 17 || p.Duration != 60500*time.Millisecond {
		t.Fatalf("plan: container %q, %d fragments, %v", p.Container, len(p.Fragments), p.Duration)
	}
	b := "https://cdn.example.org/content/video/v720/"
	checkFragment(t, p, 0, wantFragment{url: b + "init.mp4", init: true})
	checkFragment(t, p, 1, wantFragment{url: b + "seg-00001.m4s", durMS: 4000})
	checkFragment(t, p, 15, wantFragment{url: b + "seg-00015.m4s", durMS: 4000})
	checkFragment(t, p, 16, wantFragment{url: b + "seg-00016.m4s", durMS: 500})

	a := planDASH(t, m, "a128")
	ab := "https://cdn.example.org/content/audio/a128/"
	checkFragment(t, a, 0, wantFragment{url: ab + "init.mp4", init: true})
	checkFragment(t, a, 1, wantFragment{url: ab + "128000/0.m4s", durMS: 4000})
	checkFragment(t, a, 16, wantFragment{url: ab + "128000/15.m4s", durMS: 500})

	if _, err := PlanDASH(m, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown representation: %v", err)
	}
	if _, err := PlanDASH(nil, "v720"); !errors.Is(err, ErrNotFound) {
		t.Errorf("nil MPD: %v", err)
	}
}

func TestDASHTimelineAcrossPeriods(t *testing.T) {
	m := parseFixtureMPD(t, "dash/timeline.mpd")
	if len(m.Periods) != 2 {
		t.Fatalf("%d periods", len(m.Periods))
	}
	// The second Period starts where the first ends and lasts to the MPD's end.
	if p2 := m.Periods[1]; p2.Start != 20*time.Second || p2.Duration != 10*time.Second {
		t.Fatalf("second period start %v duration %v", p2.Start, p2.Duration)
	}
	p := planDASH(t, m, "low")
	b1, b2 := "https://example.com/vod/p1/", "https://example.com/vod/p2/"
	comparePlan(t, p, []wantFragment{
		{url: b1 + "low-init.m4s", init: true},
		// S t=1000 d=4000 r=2
		{url: b1 + "low-1000.m4s", durMS: 4000},
		{url: b1 + "low-5000.m4s", durMS: 4000},
		{url: b1 + "low-9000.m4s", durMS: 4000},
		// S d=3000 continues at 13000
		{url: b1 + "low-13000.m4s", durMS: 3000},
		// S d=2000 r=-1 repeats until presentationTimeOffset + 20 s = 21000
		{url: b1 + "low-16000.m4s", durMS: 2000},
		{url: b1 + "low-18000.m4s", durMS: 2000},
		{url: b1 + "low-20000.m4s", durMS: 2000},
		// Second Period: timescale 10, 10 s, d=25 r=-1, numbers from 10.
		{url: b2 + "low/10.m4s", durMS: 2500, disc: true},
		{url: b2 + "low/11.m4s", durMS: 2500},
		{url: b2 + "low/12.m4s", durMS: 2500},
		{url: b2 + "low/13.m4s", durMS: 2500},
	})
}

func TestTimelineRuns(t *testing.T) {
	type run struct{ index, start, d, n uint64 }
	e := func(t uint64, hasT bool, d uint64, r int64) TimelineEntry {
		return TimelineEntry{T: t, HasT: hasT, D: d, R: r}
	}
	open := mediaWindow{}
	upTo := func(end uint64) mediaWindow { return mediaWindow{end: end, hasEnd: true} }
	cases := []struct {
		name       string
		tl         []TimelineEntry
		w          mediaWindow
		want       []run
		total, end uint64
		wantErr    error
	}{
		{"repeat", []TimelineEntry{e(0, true, 10, 2)}, open, []run{{0, 0, 10, 3}}, 3, 30, nil},
		{"implicit first t", []TimelineEntry{e(0, false, 10, 0), e(0, false, 5, 1)}, open, []run{{0, 0, 10, 1}, {1, 10, 5, 2}}, 3, 20, nil},
		{"explicit gap", []TimelineEntry{e(0, true, 10, 0), e(100, true, 10, 0)}, open, []run{{0, 0, 10, 1}, {1, 100, 10, 1}}, 2, 110, nil},
		{"negative r to next t", []TimelineEntry{e(0, true, 10, -1), e(35, true, 5, 0)}, open, []run{{0, 0, 10, 4}, {4, 35, 5, 1}}, 5, 40, nil},
		{"negative r to exact end", []TimelineEntry{e(0, true, 10, -1)}, upTo(30), []run{{0, 0, 10, 3}}, 3, 30, nil},
		{"negative r to partial end", []TimelineEntry{e(0, true, 10, -1)}, upTo(25), []run{{0, 0, 10, 3}}, 3, 30, nil},
		{"other negative r", []TimelineEntry{e(5, true, 10, -7)}, upTo(26), []run{{0, 5, 10, 3}}, 3, 35, nil},
		{"negative r past end", []TimelineEntry{e(40, true, 10, -1)}, upTo(30), nil, 0, 40, nil},
		{"repeat clipped at the end", []TimelineEntry{e(0, true, 10, 9)}, upTo(25), []run{{0, 0, 10, 3}}, 10, 100, nil},
		{"run after the end", []TimelineEntry{e(0, true, 10, 1), e(0, false, 5, 3)}, upTo(20), []run{{0, 0, 10, 2}}, 6, 40, nil},
		{"clipped at the start", []TimelineEntry{e(0, true, 10, 9)}, mediaWindow{start: 30}, []run{{3, 30, 10, 7}}, 10, 100, nil},
		{"straddling the start", []TimelineEntry{e(0, true, 10, 9)}, mediaWindow{start: 31, end: 45, hasEnd: true}, []run{{3, 30, 10, 2}}, 10, 100, nil},
		{"before the start", []TimelineEntry{e(0, true, 10, 2), e(0, false, 5, 1)}, mediaWindow{start: 35}, []run{{4, 35, 5, 1}}, 5, 40, nil},
		{"huge repeat clipped", []TimelineEntry{e(0, true, 1, 1<<62)}, mediaWindow{start: 1 << 61, end: 1<<61 + 2, hasEnd: true}, []run{{1 << 61, 1 << 61, 1, 2}}, 1<<62 + 1, 1<<62 + 1, nil},
		{"negative r unknown end", []TimelineEntry{e(0, true, 10, -1)}, open, nil, 0, 0, ErrMalformed},
		{"negative r before untimed S", []TimelineEntry{e(0, true, 10, -1), e(0, false, 5, 0)}, upTo(100), nil, 0, 0, ErrMalformed},
		{"zero d", []TimelineEntry{e(0, true, 0, 0)}, open, nil, 0, 0, ErrMalformed},
		{"time overflow", []TimelineEntry{e(1<<63, true, 1<<62, 2)}, open, nil, 0, 0, ErrMalformed},
		{"count overflow", []TimelineEntry{e(0, true, 1<<40, 1<<40)}, open, nil, 0, 0, ErrMalformed},
		{"number overflow", []TimelineEntry{e(0, true, 1, 1<<63-1), e(0, true, 1, 1<<63-1), e(0, true, 1, 1)}, open, nil, 0, 0, ErrMalformed},
	}
	for _, c := range cases {
		var got []run
		total, end, err := timelineRuns(c.tl, c.w, func(index, start, d, n uint64) error {
			got = append(got, run{index, start, d, n})
			return nil
		})
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) {
				t.Errorf("%s: error = %v, want %v", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || fmt.Sprint(got) != fmt.Sprint(c.want) || total != c.total || end != c.end {
			t.Errorf("%s: runs = %v, total %d, end %d, %v; want %v, %d, %d", c.name, got, total, end, err, c.want, c.total, c.end)
		}
	}
}

func TestPeriodEndUnits(t *testing.T) {
	cases := []struct {
		d        time.Duration
		ts, pto  uint64
		end      uint64
		hasEnd   bool
		overflow bool
	}{
		{20 * time.Second, 1000, 1000, 21000, true, false},
		{1500 * time.Millisecond, 3, 0, 5, true, false}, // 4.5 units rounds up
		{time.Second / 3, 90000, 0, 30000, true, false},
		{0, 1000, 0, 0, false, false},
		{maxDuration, 1 << 63, 0, 0, false, true},
		{time.Second, 1, 1<<64 - 1, 0, false, true},
	}
	for _, c := range cases {
		end, ok, err := periodEndUnits(c.d, c.ts, c.pto)
		if (err != nil) != c.overflow || end != c.end || ok != c.hasEnd {
			t.Errorf("periodEndUnits(%v, %d, %d) = %d, %v, %v", c.d, c.ts, c.pto, end, ok, err)
		}
	}
	if d := unitsToDuration(1001, 30000); d != 33366666 {
		t.Errorf("unitsToDuration(1001, 30000) = %d", d)
	}
	if d := unitsToDuration(1<<64-1, 1); d != maxDuration {
		t.Errorf("unitsToDuration saturates: %d", d)
	}
}

func TestTemplateExpansion(t *testing.T) {
	vars := templateVars{id: "v1", bandwidth: 128000, number: 42, time: 90000}
	cases := []struct{ tmpl, want string }{
		{"$Number%05d$.m4s", "00042.m4s"},
		{"$Number%01d$", "42"},
		{"$Number%d$-$Number%0d$", "42-42"},
		{"$$Number$$", "$Number$"},
		{"a$$b$$", "a$b$"},
		{"$RepresentationID$_$Bandwidth%08d$_$Time$.mp4", "v1_00128000_90000.mp4"},
		{"$Time%012d$", "000000090000"},
		{"plain.mp4", "plain.mp4"},
		{"", ""},
	}
	for _, c := range cases {
		parts, _, err := compileTemplate(c.tmpl)
		if err != nil {
			t.Errorf("compile %q: %v", c.tmpl, err)
			continue
		}
		got, err := expandTemplate(parts, vars)
		if err != nil || got != c.want {
			t.Errorf("expand %q = %q, %v; want %q", c.tmpl, got, err, c.want)
		}
	}
	big := templateVars{number: 1234567}
	parts, _, _ := compileTemplate("$Number%05d$")
	if got, _ := expandTemplate(parts, big); got != "1234567" {
		t.Errorf("narrow width = %q", got)
	}
	for tmpl, want := range map[string]error{
		"$Number":                 ErrMalformed,
		"seg-$Number$-$":          ErrMalformed,
		"$Foo$":                   ErrMalformed,
		"$RepresentationID%05d$":  ErrMalformed,
		"$Number%5d$":             ErrMalformed,
		"$Number%05x$":            ErrMalformed,
		"$Number%0a5d$":           ErrMalformed,
		"$Number%0100d$":          ErrMalformed,
		"$SubNumber$":             ErrUnsupported,
		"$number$":                ErrMalformed,
		"$Number%05d$$Bandwidth%": ErrMalformed,
	} {
		if _, _, err := compileTemplate(tmpl); !errors.Is(err, want) {
			t.Errorf("compile %q: %v, want %v", tmpl, err, want)
		}
	}
	// Expansion longer than MaxURIBytes is refused before it is built.
	parts, _, _ = compileTemplate(strings.Repeat("x", MaxURIBytes-3) + "$Number%064d$")
	if _, err := expandTemplate(parts, vars); !errors.Is(err, ErrTooLarge) {
		t.Errorf("long expansion: %v", err)
	}
}

func TestDASHSegmentList(t *testing.T) {
	m := parseFixtureMPD(t, "dash/segment_list.mpd")
	p := planDASH(t, m, "ts1")
	if p.Container != ContainerTS {
		t.Errorf("container = %q", p.Container)
	}
	b := "http://list.example.com/media/"
	comparePlan(t, p, []wantFragment{
		{url: b + "stream.ts", rng: rng(0, 187), init: true},
		{url: b + "stream.ts", rng: rng(188, 50000), durMS: 4000},
		{url: b + "stream.ts", rng: rng(50001, 99999), durMS: 4000},
		{url: b + "extra.ts", durMS: 4000},
	})
	tracks := m.Options()
	if len(tracks) != 1 || tracks[0].Kind != KindMuxed {
		t.Fatalf("tracks = %+v", tracks)
	}
}

func TestDASHSegmentBaseNeedsIndex(t *testing.T) {
	m := parseFixtureMPD(t, "dash/segment_base.mpd")
	opus := &m.Periods[0].AdaptationSets[0].Representations[0]
	if !opus.NeedsIndex() || *opus.Base.IndexRange != [2]int64{842, 1201} || opus.Base.Timescale != 48000 {
		t.Fatalf("opus = %+v base %+v", opus, opus.Base)
	}
	p := planDASH(t, m, "opus")
	comparePlan(t, p, []wantFragment{{url: "https://example.com/vod/audio_fr.mp4", durMS: 120000}})
	if r := p.Fragments[0].IndexRange; r == nil || *r != [2]int64{842, 1201} {
		t.Errorf("IndexRange = %v", r)
	}
	if r := p.Fragments[0].InitRange; r == nil || *r != [2]int64{0, 841} {
		t.Errorf("InitRange = %v", r)
	}
	if p.Container != ContainerFMP4 {
		t.Errorf("container = %q", p.Container)
	}

	w := planDASH(t, m, "vp9")
	comparePlan(t, w, []wantFragment{
		{url: "https://example.com/vod/video-init.webm", init: true},
		{url: "https://example.com/vod/video.webm", durMS: 120000},
	})
	if w.Fragments[1].InitRange != nil || w.Fragments[1].IndexRange == nil {
		t.Errorf("vp9 ranges: init %v index %v", w.Fragments[1].InitRange, w.Fragments[1].IndexRange)
	}
	if w.Container != ContainerWebM {
		t.Errorf("container = %q", w.Container)
	}

	for _, tr := range m.Options() {
		if tr.ID == "thumbs" {
			t.Errorf("thumbnail track offered: %+v", tr)
		}
	}
}

func TestDASHBaseURLInheritance(t *testing.T) {
	m := parseFixtureMPD(t, "dash/baseurl.mpd")
	if m.BaseURL != "https://origin.example.com/root/" || m.Periods[0].BaseURL != "https://origin.example.com/root/period/" {
		t.Fatalf("MPD base %q, period base %q", m.BaseURL, m.Periods[0].BaseURL)
	}
	for id, want := range map[string]string{
		"inherit":  "https://origin.example.com/root/period/set/inherit_1.m4s",
		"relative": "https://origin.example.com/root/period/set/rep/relative_1.m4s",
		"rooted":   "https://origin.example.com/abs/rooted_1.m4s",
		"absolute": "https://other.example.net/x/absolute_1.m4s",
		"parent":   "https://origin.example.com/root/period/up/parent_1.m4s",
	} {
		p := planDASH(t, m, id)
		if len(p.Fragments) != 2 || p.Fragments[0].URL != want {
			t.Errorf("%s: first fragment %+v, want %s", id, p.Fragments, want)
		}
	}
	// Without a document URL or BaseURL, URLs stay relative.
	rel, err := ParseDASH(nil, mpd("PT4S", `<Period><AdaptationSet><SegmentTemplate media="s$Number$.m4s" duration="2"/>`+
		`<Representation id="r" bandwidth="1"/></AdaptationSet></Period>`))
	if err != nil {
		t.Fatal(err)
	}
	if p := planDASH(t, rel, "r"); p.Fragments[1].URL != "s2.m4s" {
		t.Errorf("relative plan = %+v", p.Fragments)
	}
	// A BaseURL may not switch to another scheme.
	_, err = ParseDASH(mustURL(t, mpdBase), mpd("PT4S", `<BaseURL>file:///etc/</BaseURL><Period/>`))
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("file BaseURL: %v", err)
	}
}

func TestDASHSegmentInheritance(t *testing.T) {
	// Attributes merge across levels; the lowest level wins. Timelines given once are
	// shared, not copied, by the Representations that inherit them.
	m, err := ParseDASH(mustURL(t, mpdBase), mpd("PT10S", `<Period>
		<SegmentTemplate timescale="1000" startNumber="5"/>
		<AdaptationSet mimeType="audio/mp4" codecs="mp4a.40.2">
			<SegmentTemplate media="$RepresentationID$/$Number$.m4s" initialization="$RepresentationID$/i.mp4">
				<SegmentTimeline><S t="0" d="5000" r="1"/></SegmentTimeline>
			</SegmentTemplate>
			<Representation id="a" bandwidth="1"/>
			<Representation id="b" bandwidth="2"><SegmentTemplate media="other/$Number%03d$.m4s"/></Representation>
		</AdaptationSet></Period>`))
	if err != nil {
		t.Fatal(err)
	}
	reps := m.Periods[0].AdaptationSets[0].Representations
	ta, tb := reps[0].Template, reps[1].Template
	if ta.Timescale != 1000 || ta.StartNumber != 5 || tb.Timescale != 1000 || tb.Initialization != "$RepresentationID$/i.mp4" {
		t.Fatalf("merged templates %+v %+v", ta, tb)
	}
	if &ta.Timeline[0] != &tb.Timeline[0] {
		t.Error("inherited SegmentTimeline is copied per Representation")
	}
	p := planDASH(t, m, "b")
	comparePlan(t, p, []wantFragment{
		{url: "https://example.com/vod/b/i.mp4", init: true},
		{url: "https://example.com/vod/other/005.m4s", durMS: 5000},
		{url: "https://example.com/vod/other/006.m4s", durMS: 5000},
	})
}

func TestDASHEncryptionRefused(t *testing.T) {
	if _, err := ParseDASH(mustURL(t, mpdBase), fixture(t, "dash/drm.mpd")); !errors.Is(err, ErrEncrypted) {
		t.Errorf("drm.mpd: %v", err)
	}
	cp := `<ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>`
	levels := map[string]string{
		"MPD":            `<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT2S">` + cp + `<Period><AdaptationSet><Representation id="r"/></AdaptationSet></Period></MPD>`,
		"Period":         string(mpd("PT2S", `<Period>`+cp+`<AdaptationSet><Representation id="r"/></AdaptationSet></Period>`)),
		"AdaptationSet":  string(mpd("PT2S", `<Period><AdaptationSet>`+cp+`<Representation id="r"/></AdaptationSet></Period>`)),
		"Representation": string(mpd("PT2S", `<Period><AdaptationSet><Representation id="r">`+cp+`</Representation></AdaptationSet></Period>`)),
		"prefixed":       `<d:MPD xmlns:d="urn:mpeg:dash:schema:mpd:2011"><d:Period><d:AdaptationSet><d:ContentProtection/></d:AdaptationSet></d:Period></d:MPD>`,
		"dynamic too":    `<MPD type="dynamic"><Period><AdaptationSet>` + cp + `</AdaptationSet></Period></MPD>`,
	}
	for level, doc := range levels {
		if _, err := ParseDASH(nil, []byte(doc)); !errors.Is(err, ErrEncrypted) {
			t.Errorf("ContentProtection in %s: %v, want ErrEncrypted", level, err)
		}
	}
}

func TestDASHLive(t *testing.T) {
	if _, err := ParseDASH(mustURL(t, mpdBase), fixture(t, "dash/live.mpd")); !errors.Is(err, ErrLive) {
		t.Errorf("live.mpd: %v, want ErrLive", err)
	}
	if _, err := PlanDASH(&MPD{Type: "dynamic"}, "x"); !errors.Is(err, ErrLive) {
		t.Errorf("PlanDASH(dynamic): %v", err)
	}
}

func TestDASHRemoteElements(t *testing.T) {
	zero := mpd("PT4S", `<Period xmlns:xlink="http://www.w3.org/1999/xlink" xlink:href="urn:mpeg:dash:resolve-to-zero:2013"/>`+
		`<Period><AdaptationSet><SegmentTemplate media="$Number$.m4s" duration="2"/><Representation id="r"/></AdaptationSet></Period>`)
	m, err := ParseDASH(mustURL(t, mpdBase), zero)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Periods) != 1 {
		t.Fatalf("resolve-to-zero Period kept: %d periods", len(m.Periods))
	}
	for _, doc := range []string{
		`<Period xmlns:xlink="http://www.w3.org/1999/xlink" xlink:href="https://ads.example/period.xml"/>`,
		`<Period><AdaptationSet xmlns:xlink="http://www.w3.org/1999/xlink" xlink:href="remote.xml"/></Period>`,
		`<Period><AdaptationSet><Representation id="r"><SegmentList xmlns:xlink="http://www.w3.org/1999/xlink" xlink:href="l.xml"/></Representation></AdaptationSet></Period>`,
	} {
		if _, err := ParseDASH(nil, mpd("PT4S", doc)); !errors.Is(err, ErrUnsupported) {
			t.Errorf("remote element %s: %v, want ErrUnsupported", doc, err)
		}
	}
}

func TestDASHMalformed(t *testing.T) {
	period := func(inner string) []byte {
		return mpd("PT10S", `<Period><AdaptationSet mimeType="video/mp4">`+inner+`</AdaptationSet></Period>`)
	}
	for name, doc := range map[string][]byte{
		"not xml":             []byte("#EXTM3U\n"),
		"empty":               nil,
		"wrong root":          []byte(`<Playlist/>`),
		"two roots":           []byte(`<MPD><Period/></MPD><MPD/>`),
		"unclosed":            []byte(`<MPD><Period>`),
		"mismatched":          []byte(`<MPD><Period></AdaptationSet></MPD>`),
		"no period":           mpd("PT1S", ``),
		"bad type":            []byte(`<MPD type="sometimes"><Period/></MPD>`),
		"bad duration":        mpd("1 hour", `<Period/>`),
		"bad period start":    mpd("PT1S", `<Period start="soon"/>`),
		"years":               mpd("P1Y", `<Period/>`),
		"no id":               period(`<Representation bandwidth="1"/>`),
		"bad bandwidth":       period(`<Representation id="r" bandwidth="-1"/>`),
		"bad width":           period(`<Representation id="r" width="wide"/>`),
		"zero timescale":      period(`<SegmentTemplate media="$Number$" duration="1" timescale="0"/><Representation id="r"/>`),
		"no media":            period(`<SegmentTemplate duration="1"/><Representation id="r"/>`),
		"bad start number":    period(`<SegmentTemplate media="$Number$" duration="1" startNumber="x"/><Representation id="r"/>`),
		"S without d":         period(`<SegmentTemplate media="$Time$"><SegmentTimeline><S t="0"/></SegmentTimeline></SegmentTemplate><Representation id="r"/>`),
		"S bad t":             period(`<SegmentTemplate media="$Time$"><SegmentTimeline><S t="-1" d="1"/></SegmentTimeline></SegmentTemplate><Representation id="r"/>`),
		"reversed range":      period(`<Representation id="r"><SegmentBase indexRange="10-5"/></Representation>`),
		"open range":          period(`<Representation id="r"><SegmentBase indexRange="10-"/></Representation>`),
		"bad init range":      period(`<Representation id="r"><SegmentList><Initialization range="a-b"/></SegmentList></Representation>`),
		"bad media range":     period(`<Representation id="r"><SegmentList><SegmentURL mediaRange="1"/></SegmentList></Representation>`),
		"bad base url":        mpd("PT1S", `<BaseURL>http://[::1</BaseURL><Period/>`),
		"undeclared entity":   []byte(`<MPD><Period id="&nope;"/></MPD>`),
		"unsupported charset": []byte(`<?xml version="1.0" encoding="EBCDIC"?><MPD><Period/></MPD>`),
	} {
		_, err := ParseDASH(mustURL(t, mpdBase), doc)
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: error = %v, want ErrMalformed", name, err)
		}
	}

	// Problems found while planning.
	for name, inner := range map[string]string{
		"no duration or timeline": `<SegmentTemplate media="$Number$.m4s"/><Representation id="r"/>`,
		"init uses number":        `<SegmentTemplate media="$Number$.m4s" initialization="$Number$.mp4" duration="1"/><Representation id="r"/>`,
		"unknown identifier":      `<SegmentTemplate media="$Segment$.m4s" duration="1"/><Representation id="r"/>`,
		"no number or time":       `<SegmentTemplate media="same.m4s" duration="1"/><Representation id="r"/>`,
		"number overflow":         `<SegmentTemplate media="$Number$.m4s" duration="1" startNumber="18446744073709551615"/><Representation id="r"/>`,
	} {
		m, err := ParseDASH(mustURL(t, mpdBase), period(inner))
		if err != nil {
			t.Errorf("%s: parse: %v", name, err)
			continue
		}
		if _, err := PlanDASH(m, "r"); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: plan error = %v, want ErrMalformed", name, err)
		}
	}
	// Durations that need an unknown Period end.
	unknownEnd := []byte(`<MPD><Period><AdaptationSet>` +
		`<SegmentTemplate media="$Number$.m4s" duration="1"/><Representation id="d"/>` +
		`</AdaptationSet><AdaptationSet><SegmentTemplate media="$Time$.m4s"><SegmentTimeline><S d="1" r="-1"/></SegmentTimeline></SegmentTemplate>` +
		`<Representation id="t"/></AdaptationSet></Period></MPD>`)
	m, err := ParseDASH(nil, unknownEnd)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"d", "t"} {
		if _, err := PlanDASH(m, id); !errors.Is(err, ErrMalformed) {
			t.Errorf("unknown Period end for %s: %v", id, err)
		}
	}
	// Representations with nothing to fetch: without a BaseURL element, the base is
	// the MPD itself, which is not media.
	for _, rep := range []string{
		`<Representation id="r"/>`,
		`<Representation id="r"><SegmentBase indexRange="0-9"/></Representation>`,
		`<Representation id="r"><SegmentList><SegmentURL/></SegmentList></Representation>`,
		`<Representation id="r"><SegmentList><Initialization range="0-9"/><SegmentURL media="s.m4s"/></SegmentList></Representation>`,
	} {
		for _, base := range []string{"", mpdBase} {
			var b *url.URL
			if base != "" {
				b = mustURL(t, base)
			}
			m, err = ParseDASH(b, mpd("PT1S", `<Period><AdaptationSet>`+rep+`</AdaptationSet></Period>`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := PlanDASH(m, "r"); !errors.Is(err, ErrMalformed) {
				t.Errorf("no media URL in %s (base %q): %v", rep, base, err)
			}
		}
	}
}

func TestDASHLimits(t *testing.T) {
	if _, err := ParseDASH(nil, make([]byte, MaxManifestBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized MPD: %v", err)
	}
	deep := "<MPD>" + strings.Repeat("<X>", MaxXMLDepth) + strings.Repeat("</X>", MaxXMLDepth) + "</MPD>"
	if _, err := ParseDASH(nil, []byte(deep)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("deep nesting: %v", err)
	}
	reps := strings.Repeat(`<Representation id="r"/>`, MaxVariants+1)
	if _, err := ParseDASH(nil, mpd("PT1S", `<Period><AdaptationSet>`+reps+`</AdaptationSet></Period>`)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too many representations: %v", err)
	}
	periods := strings.Repeat(`<Period/>`, MaxVariants+1)
	if _, err := ParseDASH(nil, mpd("PT1S", periods)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too many periods: %v", err)
	}
	ss := `<SegmentTimeline>` + strings.Repeat(`<S d="1"/>`, MaxSegments+1) + `</SegmentTimeline>`
	if _, err := ParseDASH(nil, mpd("PT1S", `<Period><AdaptationSet><SegmentTemplate media="$Time$">`+ss+
		`</SegmentTemplate><Representation id="r"/></AdaptationSet></Period>`)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too many S elements: %v", err)
	}
	urls := strings.Repeat(`<SegmentURL/>`, MaxSegments+1)
	if _, err := ParseDASH(nil, mpd("PT1S", `<Period><AdaptationSet><Representation id="r"><SegmentList>`+urls+
		`</SegmentList></Representation></AdaptationSet></Period>`)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too many SegmentURL elements: %v", err)
	}

	// Expansion beyond MaxSegments is refused before the plan is allocated. The Period
	// is long enough that clipping the timelines to it leaves them over the limit.
	for name, tmpl := range map[string]string{
		"repeat count":   `<SegmentTemplate media="$Number$"><SegmentTimeline><S d="1" r="100000"/></SegmentTimeline></SegmentTemplate>`,
		"huge repeat":    `<SegmentTemplate media="$Number$"><SegmentTimeline><S d="1" r="9223372036854775806"/></SegmentTimeline></SegmentTemplate>`,
		"repeat to end":  `<SegmentTemplate media="$Number$" timescale="1000000"><SegmentTimeline><S d="1" r="-1"/></SegmentTimeline></SegmentTemplate>`,
		"short duration": `<SegmentTemplate media="$Number$" timescale="1000000" duration="1"/>`,
		"many runs":      `<SegmentTemplate media="$Number$"><SegmentTimeline>` + strings.Repeat(`<S d="1" r="999"/>`, 101) + `</SegmentTimeline></SegmentTemplate>`,
	} {
		m, err := ParseDASH(nil, mpd("PT1000H", `<Period><AdaptationSet>`+tmpl+`<Representation id="r"/></AdaptationSet></Period>`))
		if err != nil {
			t.Errorf("%s: parse: %v", name, err)
			continue
		}
		if _, err := PlanDASH(m, "r"); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: plan error = %v, want ErrTooLarge", name, err)
		}
	}
	// Exactly MaxSegments is allowed.
	m, err := ParseDASH(nil, mpd("PT1000H", `<Period><AdaptationSet><SegmentTemplate media="$Number$"><SegmentTimeline>`+
		fmt.Sprintf(`<S d="1" r="%d"/>`, MaxSegments-1)+`</SegmentTimeline></SegmentTemplate><Representation id="r"/></AdaptationSet></Period>`))
	if err != nil {
		t.Fatal(err)
	}
	if p := planDASH(t, m, "r"); p.MediaFragments() != MaxSegments {
		t.Errorf("%d media fragments", p.MediaFragments())
	}
	// The aggregate URL budget bounds MaxSegments long URLs.
	long := strings.Repeat("p", MaxURIBytes-100)
	m, err = ParseDASH(mustURL(t, "https://h.example/"+long+"/m.mpd"), mpd("PT1H",
		`<Period><AdaptationSet><SegmentTemplate media="$Number$" duration="1"/><Representation id="r"/></AdaptationSet></Period>`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PlanDASH(m, "r"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("aggregate URL budget: %v", err)
	}
}

func TestISODuration(t *testing.T) {
	ok := map[string]time.Duration{
		"PT0S":                  0,
		"PT1H2M3.5S":            time.Hour + 2*time.Minute + 3500*time.Millisecond,
		"P1DT1S":                24*time.Hour + time.Second,
		"P2D":                   48 * time.Hour,
		"PT0.000001S":           time.Microsecond,
		"P0Y0M0DT0H3M30.000S":   210 * time.Second,
		"PT1.123456789123S":     1123456789,
		"PT36H":                 36 * time.Hour,
		"PT90M":                 90 * time.Minute,
		" PT5S ":                5 * time.Second,
		"PT634.566S":            634566 * time.Millisecond,
		"PT9223372036.854775S":  9223372036854775000,
		"PT2562047H47M16.854S":  9223372036854000000,
		"P106751DT23H47M16.85S": 9223372036850000000,
	}
	for in, want := range ok {
		got, err := parseISODuration(in)
		if err != nil || got != want {
			t.Errorf("parseISODuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "P", "PT", "1S", "P1Y", "P1M", "P1W", "PT1.5M", "P1.5D", "-PT1S", "PT1S2M", "P1H", "PTS", "PT1.2.3S",
		"PT99999999999999999S", "P99999999999999D", "P1DT", "PT1H1H", "PT1M1H", "PT5", "pt5s", "PT9223372037S",
	} {
		if got, err := parseISODuration(in); err == nil {
			t.Errorf("parseISODuration(%q) = %v, want error", in, got)
		}
	}
}

func TestDASHSegmentListDurations(t *testing.T) {
	m, err := ParseDASH(mustURL(t, mpdBase), mpd("PT9S", `<Period><AdaptationSet mimeType="audio/mp4">
		<SegmentList timescale="10">
			<SegmentTimeline><S d="20" r="1"/><S d="50"/></SegmentTimeline>
		</SegmentList>
		<Representation id="tl"><SegmentList><SegmentURL media="1.m4s"/><SegmentURL media="2.m4s"/>
			<SegmentURL media="3.m4s"/><SegmentURL media="4.m4s"/></SegmentList></Representation>
	</AdaptationSet><AdaptationSet mimeType="audio/mp4">
		<Representation id="one"><BaseURL>whole.mp4</BaseURL><SegmentList><SegmentURL/></SegmentList></Representation>
	</AdaptationSet></Period>`))
	if err != nil {
		t.Fatal(err)
	}
	// Durations come from the inherited timeline, which fills the Period: a fourth
	// segment beyond it would start at the Period end, so it is left out.
	comparePlan(t, planDASH(t, m, "tl"), []wantFragment{
		{url: "https://example.com/vod/1.m4s", durMS: 2000},
		{url: "https://example.com/vod/2.m4s", durMS: 2000},
		{url: "https://example.com/vod/3.m4s", durMS: 5000},
	})
	// A single segment without durations lasts the Period.
	comparePlan(t, planDASH(t, m, "one"), []wantFragment{{url: "https://example.com/vod/whole.mp4", durMS: 9000}})
}

// A SegmentTimeline is clipped to the Period's media window [presentationTimeOffset,
// presentationTimeOffset + duration × timescale): segments that end at or before its
// start, or start at or after its end, belong to other Periods. $Number$ still counts
// timeline positions, clipped ones included.
func TestDASHTimelineClippedToPeriod(t *testing.T) {
	plan := func(period, template string) (*Plan, error) {
		m, err := ParseDASH(nil, mpd("PT1000H", period+`<AdaptationSet mimeType="video/mp4">`+template+
			`<Representation id="r"/></AdaptationSet></Period>`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return PlanDASH(m, "r")
	}
	cases := []struct {
		name, period, template string
		want                   []wantFragment
	}{
		{"repeats past the end", `<Period duration="PT4S">`,
			`<SegmentTemplate media="$Time$"><SegmentTimeline><S d="2" r="9"/></SegmentTimeline></SegmentTemplate>`,
			[]wantFragment{{url: "0", durMS: 2000}, {url: "2", durMS: 2000}}},
		{"untimed start before the offset", `<Period duration="PT10S">`,
			`<SegmentTemplate media="$Number$-$Time$" presentationTimeOffset="100"><SegmentTimeline><S d="2" r="-1"/></SegmentTimeline></SegmentTemplate>`,
			[]wantFragment{{url: "51-100", durMS: 2000}, {url: "52-102", durMS: 2000}, {url: "53-104", durMS: 2000},
				{url: "54-106", durMS: 2000}, {url: "55-108", durMS: 2000}}},
		{"segments straddle both edges", `<Period duration="PT4S">`,
			`<SegmentTemplate media="$Number$-$Time$" presentationTimeOffset="101"><SegmentTimeline><S t="0" d="2" r="99"/></SegmentTimeline></SegmentTemplate>`,
			[]wantFragment{{url: "51-100", durMS: 2000}, {url: "52-102", durMS: 2000}, {url: "53-104", durMS: 2000}}},
		{"endNumber counts clipped positions", `<Period duration="PT10S">`,
			`<SegmentTemplate media="$Number$" presentationTimeOffset="100" endNumber="52"><SegmentTimeline><S d="2" r="-1"/></SegmentTimeline></SegmentTemplate>`,
			[]wantFragment{{url: "51", durMS: 2000}, {url: "52", durMS: 2000}}},
		{"later runs past the end", `<Period duration="PT4S">`,
			`<SegmentTemplate media="$Time$"><SegmentTimeline><S d="3"/><S d="1" r="5"/><S t="50" d="1"/></SegmentTimeline></SegmentTemplate>`,
			[]wantFragment{{url: "0", durMS: 3000}, {url: "3", durMS: 1000}}},
		{"offset clips the start", `<Period>`,
			`<SegmentTemplate media="$Time$" presentationTimeOffset="4"><SegmentTimeline><S d="2" r="3"/></SegmentTimeline></SegmentTemplate>`,
			[]wantFragment{{url: "4", durMS: 2000}, {url: "6", durMS: 2000}}},
	}
	for _, c := range cases {
		p, err := plan(c.period, c.template)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		t.Run(c.name, func(t *testing.T) { comparePlan(t, p, c.want) })
	}
	// A timeline entirely outside the Period has no segments there.
	if _, err := plan(`<Period duration="PT4S">`, `<SegmentTemplate media="$Time$" presentationTimeOffset="10"><SegmentTimeline><S d="2" r="4"/></SegmentTimeline></SegmentTemplate>`); !errors.Is(err, ErrNotFound) {
		t.Errorf("timeline before the Period: %v, want ErrNotFound", err)
	}
	// A negative @r repeats up to the next S's @t; without one its end is unknown.
	if _, err := plan(`<Period duration="PT4S">`, `<SegmentTemplate media="$Time$"><SegmentTimeline><S d="2" r="-1"/><S d="3"/></SegmentTimeline></SegmentTemplate>`); !errors.Is(err, ErrMalformed) {
		t.Errorf("negative @r before an S without @t: %v, want ErrMalformed", err)
	}

	// Two Periods reuse one timeline with a different presentationTimeOffset: each
	// takes only its own media.
	m, err := ParseDASH(nil, mpd("PT8S", `
		<Period duration="PT4S"><AdaptationSet mimeType="video/mp4">
			<SegmentTemplate media="$Time$" presentationTimeOffset="0"><SegmentTimeline><S t="0" d="2" r="3"/></SegmentTimeline></SegmentTemplate>
			<Representation id="r"/></AdaptationSet></Period>
		<Period duration="PT4S"><AdaptationSet mimeType="video/mp4">
			<SegmentTemplate media="$Time$" presentationTimeOffset="4"><SegmentTimeline><S t="0" d="2" r="3"/></SegmentTimeline></SegmentTemplate>
			<Representation id="r"/></AdaptationSet></Period>`))
	if err != nil {
		t.Fatal(err)
	}
	p := planDASH(t, m, "r")
	comparePlan(t, p, []wantFragment{
		{url: "0", durMS: 2000}, {url: "2", durMS: 2000},
		{url: "4", durMS: 2000, disc: true}, {url: "6", durMS: 2000},
	})
	if p.Duration != 8*time.Second {
		t.Errorf("plan duration %v, want 8s", p.Duration)
	}
}

// SegmentList entries take their timing from the SegmentTimeline; entries it places
// outside the Period are left out.
func TestDASHSegmentListClippedToPeriod(t *testing.T) {
	list := func(duration, timeline string) []byte {
		var urls strings.Builder
		for i := 0; i < 6; i++ {
			fmt.Fprintf(&urls, `<SegmentURL media="%d.m4s"/>`, i)
		}
		return mpd("PT1000H", `<Period duration="`+duration+`"><AdaptationSet mimeType="audio/mp4"><Representation id="r">`+
			`<SegmentList presentationTimeOffset="2">`+timeline+urls.String()+`</SegmentList></Representation></AdaptationSet></Period>`)
	}
	// Positions 0 (ends at the offset), 3 and 4 (start at or after the end) are outside;
	// the sixth entry is beyond the timeline, which has already passed the Period end.
	m, err := ParseDASH(nil, list("PT4S", `<SegmentTimeline><S t="0" d="2" r="4"/></SegmentTimeline>`))
	if err != nil {
		t.Fatal(err)
	}
	comparePlan(t, planDASH(t, m, "r"), []wantFragment{{url: "1.m4s", durMS: 2000}, {url: "2.m4s", durMS: 2000}})
	// A timeline that ends inside the Period leaves the entries after it, untimed.
	m, err = ParseDASH(nil, list("PT20S", `<SegmentTimeline><S t="0" d="2" r="2"/></SegmentTimeline>`))
	if err != nil {
		t.Fatal(err)
	}
	comparePlan(t, planDASH(t, m, "r"), []wantFragment{
		{url: "1.m4s", durMS: 2000}, {url: "2.m4s", durMS: 2000}, {url: "3.m4s"}, {url: "4.m4s"}, {url: "5.m4s"},
	})
}

// Planning many Periods grows the fragment list geometrically, not by each Period's
// count.
func TestDASHManyPeriodsPlanLinearly(t *testing.T) {
	var b strings.Builder
	for i := 0; i < MaxVariants; i++ {
		b.WriteString(`<Period duration="PT100S"><AdaptationSet><SegmentTemplate media="$Number$" duration="1"/><Representation id="r"/></AdaptationSet></Period>`)
	}
	m, err := ParseDASH(nil, mpd("PT100000S", b.String()))
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	p, err := PlanDASH(m, "r")
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if p.MediaFragments() != MaxSegments {
		t.Fatalf("%d media fragments, want %d", p.MediaFragments(), MaxSegments)
	}
	// About 100,000 fragments of 64 bytes plus their URLs; growing by each Period's
	// count instead copied gigabytes.
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 256<<20 {
		t.Errorf("PlanDASH allocated %d MiB", alloc>>20)
	}
}

// Without a base URL, nested BaseURL elements and segment references compose into
// relative references (RFC 3986 §5.2) to resolve later against the MPD's location.
func TestDASHRelativeBaseURLs(t *testing.T) {
	cases := []struct {
		name, mpdBase, periodBase, setBase string
		repBase, init, seg                 string
	}{
		{"period", "", "video/", "", "video/", "video/init.mp4", "video/seg-1.m4s"},
		{"climbing", "../cdn/", "", "a/", "../cdn/a/", "../cdn/a/init.mp4", "../cdn/a/seg-1.m4s"},
		{"dot segments", "a/b/", "../../../x/./", "y/../z/", "../x/z/", "../x/z/init.mp4", "../x/z/seg-1.m4s"},
		{"back to the start", "a/", "..", "", "./", "init.mp4", "seg-1.m4s"},
		{"rooted", "/root/", "p/", "../q/", "/root/q/", "/root/q/init.mp4", "/root/q/seg-1.m4s"},
		{"network path", "//cdn.example/v/", "w/", "", "//cdn.example/v/w/", "//cdn.example/v/w/init.mp4", "//cdn.example/v/w/seg-1.m4s"},
		{"colon in first segment", "./a:b/", "c/", "", "./a:b/c/", "./a:b/c/init.mp4", "./a:b/c/seg-1.m4s"},
		{"query", "v/?x=1", "", "", "v/?x=1", "v/init.mp4", "v/seg-1.m4s"},
		{"absolute later", "rel/", "https://h.example/abs/", "s/", "https://h.example/abs/s/", "https://h.example/abs/s/init.mp4", "https://h.example/abs/s/seg-1.m4s"},
	}
	base := func(s string) string {
		if s == "" {
			return ""
		}
		return "<BaseURL>" + s + "</BaseURL>"
	}
	for _, c := range cases {
		doc := `<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011" mediaPresentationDuration="PT2S">` + base(c.mpdBase) +
			`<Period>` + base(c.periodBase) + `<AdaptationSet mimeType="video/mp4">` + base(c.setBase) +
			`<SegmentTemplate media="seg-$Number$.m4s" initialization="init.mp4" duration="2"/><Representation id="r"/></AdaptationSet></Period></MPD>`
		m, err := ParseDASH(nil, []byte(doc))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := m.Periods[0].AdaptationSets[0].Representations[0].BaseURL; got != c.repBase {
			t.Errorf("%s: Representation.BaseURL = %q, want %q", c.name, got, c.repBase)
		}
		p, err := PlanDASH(m, "r")
		if err != nil {
			t.Errorf("%s: plan: %v", c.name, err)
			continue
		}
		if len(p.Fragments) != 2 || p.Fragments[0].URL != c.init || p.Fragments[1].URL != c.seg {
			t.Errorf("%s: fragments %+v, want %q and %q", c.name, p.Fragments, c.init, c.seg)
		}
	}
}

func TestResolveAgainstRelativeBase(t *testing.T) {
	for _, c := range []struct{ base, ref, want string }{
		{"a/b/", "c", "a/b/c"},
		{"a/b", "c", "a/c"},
		{"a/b/", "../../../c", "../c"},
		{"../", "../c/", "../../c/"},
		{"a/", "..//b", ".//b"},
		{"a//", "..", "a/"},
		{"a/", "./", "a/"},
		{"a%20b/", "c%3Fd", "a%20b/c%3Fd"},
		{"x/", "?q=1", "x/?q=1"},
		{"x/?a=1", "#f", "x/?a=1#f"},
		{"x/", "//h.example/p", "//h.example/p"},
		{"x/", "/r", "/r"},
		{"x/", "https://h.example/p", "https://h.example/p"},
		{"./a:b/", "c", "./a:b/c"},
		{"?q", "c", "c"},
	} {
		u, err := resolveRef(mustURL(t, c.base), c.ref)
		if err != nil || u.String() != c.want {
			t.Errorf("%q + %q = %v, %v; want %q", c.base, c.ref, u, err, c.want)
		}
	}
	if _, err := resolveRef(mustURL(t, "x/"), "file:///etc/passwd"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("file reference against a relative base: %v", err)
	}
}
