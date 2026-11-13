package media

import (
	"slices"
	"testing"
)

// qualityNameCases is the table of docs/SITE-MEDIA-SPEC.md (amendment 2026-10-23) that
// the extension's and the desktop's helpers repeat case for case: the class comes from
// the picture's longer side scaled to 16:9, the nearest ladder step (ties up).
var qualityNameCases = []struct {
	width, height int
	fps           float64
	hdr           bool
	class         int
	name          string
}{
	// The user's 2.35:1 film, every rung of its ladder.
	{3840, 1634, 24, false, 2160, "4K (2160p)"},
	{2560, 1090, 24, false, 1440, "1440p (2K)"},
	{1920, 818, 24, false, 1080, "1080p (Full HD)"},
	{1280, 544, 24, false, 720, "720p (HD)"},
	{854, 364, 24, false, 480, "480p"},
	{640, 272, 24, false, 360, "360p"},
	{426, 182, 24, false, 240, "240p"},
	{256, 128, 24, false, 144, "144p"},
	// 16:9 pictures are their height.
	{7680, 4320, 30, false, 4320, "8K (4320p)"},
	{3840, 2160, 30, false, 2160, "4K (2160p)"},
	{2560, 1440, 30, false, 1440, "1440p (2K)"},
	{1920, 1080, 30, false, 1080, "1080p (Full HD)"},
	{1280, 720, 30, false, 720, "720p (HD)"},
	{854, 480, 30, false, 480, "480p"},
	{640, 360, 30, false, 360, "360p"},
	{426, 240, 30, false, 240, "240p"},
	{256, 144, 30, false, 144, "144p"},
	// The frame rate when it rounds to more than 30, and HDR.
	{1920, 1080, 60, false, 1080, "1080p (Full HD) 60fps"},
	{1920, 1080, 59.94, false, 1080, "1080p (Full HD) 60fps"},
	{1920, 1080, 29.97, false, 1080, "1080p (Full HD)"},
	{1920, 1080, 30.4, false, 1080, "1080p (Full HD)"}, // rounds to 30
	{1920, 1080, 30.5, false, 1080, "1080p (Full HD) 31fps"},
	{1280, 720, 50, false, 720, "720p (HD) 50fps"},
	{3840, 2160, 60, true, 2160, "4K (2160p) 60fps HDR"},
	{3840, 1600, 24, true, 2160, "4K (2160p) HDR"},
	{2560, 1440, 0, true, 1440, "1440p (2K) HDR"},
	// Other wide shapes.
	{7680, 3264, 24, false, 4320, "8K (4320p)"},
	{1920, 800, 24, false, 1080, "1080p (Full HD)"},
	// Portrait: the longer side scaled to 16:9.
	{1080, 1920, 30, false, 1080, "1080p (Full HD)"},
	{720, 1280, 60, false, 720, "720p (HD) 60fps"},
	{2160, 3840, 30, false, 2160, "4K (2160p)"},
	{1080, 1350, 30, false, 1080, "1080p (Full HD)"},
	// Odd sizes: 4:3 by its height, square, nearest step with ties up.
	{1440, 1080, 25, false, 1080, "1080p (Full HD)"},
	{960, 720, 30, false, 720, "720p (HD)"},
	{640, 480, 25, false, 480, "480p"},
	{1080, 1080, 30, false, 1080, "1080p (Full HD)"},
	{720, 576, 25, false, 480, "480p"},               // 576: 96 from 480, 144 from 720
	{1024, 576, 30, false, 480, "480p"},              // the same 576
	{1066, 600, 30, false, 720, "720p (HD)"},         // 600: as far from 480 as from 720
	{1600, 900, 30, false, 1080, "1080p (Full HD)"},  // 900: as far from 720 as from 1080
	{2048, 1080, 24, false, 1080, "1080p (Full HD)"}, // DCI 2K: round(2048 × 9/16) = 1152
	{4096, 2160, 24, false, 2160, "4K (2160p)"},
	// Off the ladder.
	{15360, 8640, 30, false, 4320, "8K (4320p)"},
	{16384, 16384, 0, false, 4320, "8K (4320p)"},
	{320, 180, 30, false, 144, "144p"},
	{176, 144, 15, false, 144, "144p"},
	{160, 90, 30, false, 144, "144p"},
	{100, 50, 0, false, 144, "144p"},
	// Half a picture: the side that is known.
	{0, 720, 30, false, 720, "720p (HD)"},
	{1920, 0, 30, false, 1080, "1080p (Full HD)"},
	// Unknown.
	{0, 0, 60, true, 0, ""},
	{-1, -1, 0, false, 0, ""},
}

func TestQualityName(t *testing.T) {
	for _, c := range qualityNameCases {
		if got := QualityClass(c.width, c.height); got != c.class {
			t.Errorf("QualityClass(%d, %d) = %d, want %d", c.width, c.height, got, c.class)
		}
		if got := QualityName(c.width, c.height, c.fps, c.hdr); got != c.name {
			t.Errorf("QualityName(%d, %d, %v, %v) = %q, want %q", c.width, c.height, c.fps, c.hdr, got, c.name)
		}
	}
}

// Class names of every ladder step; a height that is not one names nothing.
func TestClassName(t *testing.T) {
	for _, c := range []struct {
		class int
		fps   float64
		hdr   bool
		want  string
	}{
		{4320, 60, true, "8K (4320p) 60fps HDR"},
		{2160, 0, false, "4K (2160p)"},
		{1440, 60, false, "1440p (2K) 60fps"},
		{1080, 48, false, "1080p (Full HD) 48fps"},
		{720, 30, false, "720p (HD)"},
		{480, 0, false, "480p"},
		{360, 0, true, "360p HDR"},
		{240, 0, false, "240p"},
		{144, 0, false, "144p"},
		{1634, 24, false, ""},
		{0, 0, false, ""},
	} {
		if got := ClassName(c.class, c.fps, c.hdr); got != c.want {
			t.Errorf("ClassName(%d, %v, %v) = %q, want %q", c.class, c.fps, c.hdr, got, c.want)
		}
	}
}

// A site's own label names the class when it starts with a ladder step ("2160p60 HDR",
// "1080p Premium"); any other text names none.
func TestLabelClass(t *testing.T) {
	for label, want := range map[string]int{
		"2160p": 2160, "2160p60": 2160, "2160p60 HDR": 2160, "1440p HDR": 1440, "1080p Premium": 1080,
		"720p50": 720, "480p": 480, "360p, mp4": 360, "240p": 240, "144p": 144, "4320p60": 4320, "1080P": 1080,
		" 720p": 720,
		"1634p": 0, "1090p": 0, "128p": 0, "p60": 0, "1080": 0, "1080i": 0, "1080p60fps": 0, "1080p 60": 1080,
		"medium": 0, "DASH video": 0, "storyboard": 0, "": 0, "12345678901234567890p": 0,
	} {
		if got := LabelClass(label); got != want {
			t.Errorf("LabelClass(%q) = %d, want %d", label, got, want)
		}
	}
}

// Manifest tracks are named by their picture: an HLS variant with a RESOLUTION, a DASH
// video Representation with a width and height (its Label then gives way); a track
// without a picture keeps no name or its Label, and audio keeps its own name.
func TestManifestTrackNames(t *testing.T) {
	pl, err := ParseHLS(mustURL(t, "https://example.com/vod/master.m3u8"), []byte(`#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="a/en.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=9000000,RESOLUTION=3840x1634,FRAME-RATE=23.976,CODECS="avc1.640033,mp4a.40.2",AUDIO="aud"
uhd/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=4000000,RESOLUTION=1920x1080,FRAME-RATE=59.940,CODECS="avc1.640028,mp4a.40.2",AUDIO="aud"
hd/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=800000,CODECS="avc1.4d401e,mp4a.40.2"
bare/index.m3u8
`))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tr := range pl.Options() {
		names = append(names, tr.Name)
	}
	if want := []string{"4K (2160p)", "1080p (Full HD) 60fps", "", "English"}; !slices.Equal(names, want) {
		t.Errorf("HLS names %q, want %q", names, want)
	}

	m, err := ParseDASH(mustURL(t, mpdBase), mpd("PT4S", `<Period>
		<AdaptationSet contentType="video" mimeType="video/mp4"><Label>Main</Label>
			<SegmentTemplate media="$RepresentationID$/$Number$.m4s" initialization="$RepresentationID$/i.mp4" duration="2"/>
			<Representation id="wide" bandwidth="5000000" width="2560" height="1090" codecs="av01.0.12M.08" frameRate="24"/>
			<Representation id="nopic" bandwidth="100000" codecs="avc1.4d401e"/>
		</AdaptationSet>
		<AdaptationSet contentType="audio" mimeType="audio/mp4" lang="en"><Label>English</Label>
			<SegmentTemplate media="$RepresentationID$/$Number$.m4s" initialization="$RepresentationID$/i.mp4" duration="2"/>
			<Representation id="aac" bandwidth="128000" codecs="mp4a.40.2"/>
		</AdaptationSet></Period>`))
	if err != nil {
		t.Fatal(err)
	}
	names = names[:0]
	for _, tr := range m.Options() {
		names = append(names, tr.Name)
	}
	if want := []string{"1440p (2K)", "Main", "English"}; !slices.Equal(names, want) {
		t.Errorf("DASH names %q, want %q", names, want)
	}
}

// A label of YouTube's names the class unless it only restates the picture's height
// while the picture is of another class ("480p" for 1138x480 is 720p by the rule);
// without a ladder label the picture decides.
func TestLabeledClass(t *testing.T) {
	for _, c := range []struct {
		label         string
		width, height int
		want          int
	}{
		{"2160p", 3840, 1634, 2160},
		{"2160p60 HDR", 3840, 2160, 2160},
		{"1080p", 1080, 1920, 1080},  // a short: labelled by its width
		{"720p", 1920, 1080, 720},    // the label wins where it is not the raw height
		{"1080p", 1440, 1080, 1080},  // the raw height, and the picture agrees
		{"480p", 1138, 480, 720},     // the raw height of a wider picture: the picture decides
		{"480p HDR", 1138, 480, 720}, //
		{"360p", 0, 360, 360},        // no width: the height is the picture's class
		{"1440p", 0, 0, 1440},        // no picture: the label
		{"1634p", 3840, 1634, 2160},  // not a ladder label
		{"medium", 640, 360, 360},    //
		{"", 0, 0, 0},                //
	} {
		if got := LabeledClass(c.label, c.width, c.height); got != c.want {
			t.Errorf("LabeledClass(%q, %d, %d) = %d, want %d", c.label, c.width, c.height, got, c.want)
		}
	}
}

// ParseName reads back exactly the names ClassName makes, and nothing else: an older
// daemon's raw heights ("1634p", "1080p60", "2160p60 HDR"), a manifest's "1920x1080".
func TestParseName(t *testing.T) {
	type parsed struct {
		class, fps int
		hdr, ok    bool
	}
	for name, want := range map[string]parsed{
		"4K (2160p) 60fps HDR": {2160, 60, true, true},
		"1080p (Full HD)":      {1080, 0, false, true},
		" 480p ":               {480, 0, false, true},
		"8K (4320p) HDR":       {4320, 0, true, true},
		"720p (HD) 50fps":      {720, 50, false, true},
		"144p 120fps":          {144, 120, false, true},
		"1634p":                {},
		"1080p60":              {},
		"1080p HDR":            {},
		"2160p60 HDR":          {},
		"1920x1080":            {},
		"4K":                   {},
		"HD":                   {},
		"English original":     {},
		"":                     {},
		"720p (HD) 5fps":       {},
		"720p (HD) 1000fps":    {},
		"720p (HD) 060fps":     {},
		"720p (HD) HDR 60fps":  {},
		"720p (HD)  60fps":     {},
		"720p (hd)":            {},
	} {
		var got parsed
		got.class, got.fps, got.hdr, got.ok = ParseName(name)
		if got != want {
			t.Errorf("ParseName(%q) = %+v, want %+v", name, got, want)
		}
	}
	for _, c := range qualityNameCases {
		if c.name == "" {
			continue
		}
		class, fps, hdr, ok := ParseName(c.name)
		wantFPS := 0
		if r := int(c.fps + 0.5); r > 30 {
			wantFPS = r
		}
		if !ok || class != c.class || fps != wantFPS || hdr != c.hdr {
			t.Errorf("ParseName(%q) = %d, %d, %v, %v", c.name, class, fps, hdr, ok)
		}
	}
}

// trackNameCases is what every UI shows for a video track (TrackName here, the
// extension's and the desktop's trackQuality): the daemon's standard name as it is, an
// older daemon's name (a raw height, "1080p60", a manifest's "1920x1080") or none named
// again from the picture, any other name kept where the picture is unknown. The same
// cases are in ext/src/lib/quality.test.ts and ui/frontend/src/lib/quality.test.ts.
var trackNameCases = []struct {
	name          string
	width, height int
	fps           float64
	want          string
}{
	{"4K (2160p) 60fps HDR", 3840, 1634, 60, "4K (2160p) 60fps HDR"},
	{"1080p (Full HD)", 1440, 1080, 30, "1080p (Full HD)"},
	{"480p", 854, 364, 30, "480p"},             // a standard name that is not the raw height
	{"720p (HD)", 1920, 1080, 30, "720p (HD)"}, // YouTube's own label, kept
	{"1634p", 3840, 1634, 24, "4K (2160p)"},
	{"1090p", 2560, 1090, 24, "1440p (2K)"},
	{"2160p60 HDR", 3840, 2160, 60, "4K (2160p) 60fps HDR"},
	{"1080p60", 1920, 1080, 60, "1080p (Full HD) 60fps"},
	{"1920x1080", 1920, 1080, 30, "1080p (Full HD)"},
	{"1920x1080", 1920, 1080, 30.2, "1080p (Full HD)"},
	{"HD", 1280, 720, 30, "720p (HD)"},
	{"", 1080, 1920, 30, "1080p (Full HD)"},
	{"", 1920, 0, 30, "1080p (Full HD)"},
	// An older daemon's raw height that looks like a standard name.
	{"480p", 1138, 480, 24, "720p (HD)"},
	{"360p", 852, 360, 24, "480p"},
	{"240p", 568, 240, 24, "360p"},
	{"480p HDR", 1138, 480, 24, "720p (HD) HDR"},
	{"480p", 0, 480, 24, "480p"},
	{"480p", 854, 480, 24, "480p"},
	// The picture unknown: the name as it is.
	{"", 0, 0, 30, ""},
	{"Main", 0, 0, 30, "Main"},
	{"1634p", 0, 0, 24, "1634p"},
}

func TestTrackName(t *testing.T) {
	for _, c := range trackNameCases {
		if got := TrackName(c.name, c.width, c.height, c.fps); got != c.want {
			t.Errorf("TrackName(%q, %d, %d, %v) = %q, want %q", c.name, c.width, c.height, c.fps, got, c.want)
		}
	}
}
