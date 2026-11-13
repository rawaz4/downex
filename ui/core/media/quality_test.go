package media

import (
	"slices"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
)

// The cases of ui/frontend/src/lib/quality.test.ts that this package ports (a track's quality,
// one quality per class). Its NAMES and parseStandardName tables are internal/media's
// qualityNameCases and ParseName tests, held equal by quality_sync_test.

// trackNames is quality.test.ts TRACK_NAMES (internal/media trackNameCases): a track's name,
// width, height, frame rate → the name every UI shows.
var trackNames = []struct {
	name   string
	w, h   int32
	fps    float64
	result string
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
	// An older downexd's raw height that looks like a standard name.
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

func TestTrackQuality(t *testing.T) {
	for _, c := range trackNames {
		got := QualityOf(&downexv1.MediaTrack{Name: c.name, Width: c.w, Height: c.h, FrameRate: c.fps}).Name
		if got != c.result {
			t.Errorf("%q of %d×%d at %v fps = %q, want %q", c.name, c.w, c.h, c.fps, got, c.result)
		}
	}
	q := func(name string, w, h int32, fps float64) TrackQuality {
		return QualityOf(&downexv1.MediaTrack{Name: name, Width: w, Height: h, FrameRate: fps})
	}
	for _, c := range []struct {
		got, want TrackQuality
	}{
		{q("4K (2160p) 60fps HDR", 3840, 1634, 60), TrackQuality{2160, "4K (2160p) 60fps HDR", true}},
		{q("720p (HD)", 1920, 1080, 30), TrackQuality{720, "720p (HD)", false}},
		{q("2160p60 HDR", 3840, 2160, 60), TrackQuality{2160, "4K (2160p) 60fps HDR", true}},
		{q("480p HDR", 1138, 480, 24), TrackQuality{720, "720p (HD) HDR", true}},
		{q(" Main ", 0, 0, 30), TrackQuality{0, "Main", false}},
	} {
		if c.got != c.want {
			t.Errorf("quality = %+v, want %+v", c.got, c.want)
		}
	}
	if got := PictureText(&downexv1.MediaTrack{Width: 3840, Height: 1634}); got != "3840×1634" {
		t.Errorf("picture = %q", got)
	}
	if got := PictureText(&downexv1.MediaTrack{Height: 720}); got != "" {
		t.Errorf("half a picture = %q", got)
	}
}

// v is quality.test.ts's track: a 24 fps H.264 video at 1 Mbit/s.
func v(id string, edit ...func(*downexv1.MediaTrack)) *downexv1.MediaTrack {
	t := &downexv1.MediaTrack{Id: id, Kind: "video", FrameRate: 24, Codecs: "avc1.640028", Bandwidth: 1_000_000}
	for _, e := range edit {
		e(t)
	}
	return t
}

func film(w, h int32) func(*downexv1.MediaTrack) {
	return func(t *downexv1.MediaTrack) { t.Width, t.Height = w, h }
}

func codecs(c string, bw int64) func(*downexv1.MediaTrack) {
	return func(t *downexv1.MediaTrack) { t.Codecs, t.Bandwidth = c, bw }
}

func kind(k string) func(*downexv1.MediaTrack) { return func(t *downexv1.MediaTrack) { t.Kind = k } }
func fps(f float64) func(*downexv1.MediaTrack) {
	return func(t *downexv1.MediaTrack) { t.FrameRate = f }
}
func size(n int64) func(*downexv1.MediaTrack) { return func(t *downexv1.MediaTrack) { t.Size = n } }
func bitrate(b int64) func(*downexv1.MediaTrack) {
	return func(t *downexv1.MediaTrack) { t.Bandwidth = b }
}

// userFilm is the user's 3840×1634 film as an older downexd listed it (15 rows): H.264 and AV1
// at each class, HDR copies, a muxed 18, and the audios.
func userFilm() []*downexv1.MediaTrack {
	return []*downexv1.MediaTrack{
		v("401", film(3840, 1634), codecs("av01.0.12M.08", 14e6), named("1634p")),
		v("701", film(3840, 1634), codecs("av01.0.13M.10.0.110.09.16.09.0", 18e6), named("1634p HDR")),
		v("400", film(2560, 1090), codecs("av01.0.12M.08", 7e6), named("1090p")),
		v("700", film(2560, 1090), codecs("av01.0.12M.10.0.110.09.16.09.0", 9e6), named("1090p HDR")),
		v("137", film(1920, 818), codecs("avc1.640028", 4e6), named("818p")),
		v("399", film(1920, 818), codecs("av01.0.08M.08", 2e6), named("818p")),
		v("136", film(1280, 544), codecs("avc1.4d401f", 2e6), named("544p")),
		v("398", film(1280, 544), codecs("av01.0.05M.08", 1e6), named("544p")),
		v("135", film(854, 364), codecs("avc1.4d401e", 1e6), named("364p")),
		v("397", film(854, 364), codecs("av01.0.04M.08", 6e5), named("364p")),
		v("134", film(640, 272), codecs("avc1.4d4015", 6e5), named("272p")),
		v("18", film(640, 272), kind("muxed"), codecs("avc1.42001E, mp4a.40.2", 7e5), named("272p")),
		v("396", film(640, 272), codecs("av01.0.01M.08", 4e5), named("272p")),
		v("395", film(426, 182), codecs("av01.0.00M.08", 2e5), named("182p")),
		v("394", film(256, 108), codecs("av01.0.00M.08", 1e5), named("108p")),
		v("140", kind("audio"), codecs("mp4a.40.2", 129_000), named("English original")),
	}
}

func first(ts []*downexv1.MediaTrack) string {
	if len(ts) == 0 {
		return ""
	}
	return ts[0].GetId()
}

func TestOnePerClass(t *testing.T) {
	t.Run("lists the user's film once per class with the codec that plays everywhere", func(t *testing.T) {
		var got [][2]string
		for _, tr := range OnePerClass(userFilm(), "") {
			got = append(got, [2]string{tr.Id, QualityOf(tr).Name})
		}
		want := [][2]string{
			{"401", "4K (2160p)"}, // AV1 is the only SDR 4K: the HDR copy is dropped
			{"400", "1440p (2K)"},
			{"137", "1080p (Full HD)"}, // H.264 before AV1
			{"136", "720p (HD)"},
			{"135", "480p"},
			{"134", "360p"}, // the video-only H.264, not the muxed 18
			{"395", "240p"},
			{"394", "144p"},
		}
		if !slices.Equal(got, want) {
			t.Errorf("rows = %q", got)
		}
	})
	t.Run("keeps downexd's own pick of its class", func(t *testing.T) {
		if got := ids(OnePerClass(userFilm(), "399")); !slices.Equal(got, []string{"401", "400", "399", "136", "135", "134", "395", "394"}) {
			t.Errorf("keep 399 = %v", got)
		}
		if got := ids(OnePerClass(userFilm(), "18")); !slices.Contains(got, "18") {
			t.Errorf("keep 18 = %v", got)
		}
		if got, want := ids(OnePerClass(userFilm(), "gone")), ids(OnePerClass(userFilm(), "")); !slices.Equal(got, want) {
			t.Errorf("keep gone = %v", got)
		}
	})
	t.Run("chooses within a class as downexd does", func(t *testing.T) {
		hd := film(1920, 1080)
		muxed := v("m", hd, kind("muxed"), codecs("avc1.640028,mp4a.40.2", 9e6))
		av1 := v("a", hd, codecs("av01.0.08M.08", 1e6))
		hevc := v("h", hd, codecs("hvc1.1.6.L120.90", 1e6))
		avc := v("v", hd)
		avcFast := v("f", hd, bitrate(2e6))
		av1Sixty := v("s", hd, codecs("av01.0.09M.08", 1e6), fps(60))
		hdr := v("x", hd, fps(60), named("1080p (Full HD) 60fps HDR"))
		for _, c := range []struct {
			tracks []*downexv1.MediaTrack
			want   string
		}{
			{[]*downexv1.MediaTrack{muxed, av1}, "a"},
			{[]*downexv1.MediaTrack{muxed}, "m"}, // the only one of its class
			{[]*downexv1.MediaTrack{hdr, av1}, "a"},
			{[]*downexv1.MediaTrack{hdr}, "x"}, // only HDR exists
			{[]*downexv1.MediaTrack{av1, av1Sixty, avc}, "s"},
			{[]*downexv1.MediaTrack{av1, hevc, avc}, "v"},
			{[]*downexv1.MediaTrack{av1, hevc}, "h"},
			{[]*downexv1.MediaTrack{avc, avcFast}, "f"},
			// The frame rate is compared rounded: H.264 at 59.94 before AV1 at 60.
			{[]*downexv1.MediaTrack{v("o", hd, codecs("av01.0.09M.08", 3e6), fps(60)), v("n", hd, codecs("avc1.64002a", 4e6), fps(59.94))}, "n"},
			// Equal otherwise: the larger picture, then the larger size, then the order given.
			{[]*downexv1.MediaTrack{v("small", film(1440, 1080)), avc}, "v"},
			{[]*downexv1.MediaTrack{v("s1", hd, size(100)), v("s2", hd, size(200))}, "s2"},
			{[]*downexv1.MediaTrack{v("first", hd), v("second", hd)}, "first"},
		} {
			if got := first(OnePerClass(c.tracks, "")); got != c.want {
				t.Errorf("%v: %q, want %q", ids(c.tracks), got, c.want)
			}
		}
		if c := CompareInClass(avc, v("w", hd)); c != 0 {
			t.Errorf("equal tracks compare %d", c)
		}
	})
	t.Run("orders classes best first and keeps tracks of unknown picture after them", func(t *testing.T) {
		tracks := []*downexv1.MediaTrack{v("u1", named("Main")), v("sd", film(640, 360)), v("u2", kind("muxed")), v("hd", film(1280, 720)), v("fhd", film(1080, 1920))}
		if got := ids(OnePerClass(tracks, "")); !slices.Equal(got, []string{"fhd", "hd", "sd", "u1", "u2"}) {
			t.Errorf("order = %v", got)
		}
	})
	t.Run("names downexd's one-per-class list as it comes", func(t *testing.T) {
		listed := []*downexv1.MediaTrack{
			v("401", film(3840, 1634), codecs("av01.0.12M.08", 1e6), named("4K (2160p)")),
			v("137", film(1920, 818), named("1080p (Full HD)")),
			v("18", film(640, 272), kind("muxed"), codecs("avc1.42001E, mp4a.40.2", 1e6), named("360p")),
		}
		got := OnePerClass(listed, "401")
		if len(got) != 3 || got[0] != listed[0] || got[1] != listed[1] || got[2] != listed[2] {
			t.Errorf("listed = %v", ids(got))
		}
	})
}
