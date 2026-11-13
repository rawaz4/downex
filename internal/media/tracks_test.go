package media

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestHLSOptionsAndDefaultPair(t *testing.T) {
	p, err := ParseHLS(mustURL(t, "https://media.example.com/show/master.m3u8"), fixture(t, "hls/master.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	tracks := p.Options()
	want := []struct {
		id    string
		kind  TrackKind
		group string
		url   string
	}{
		{"v0", KindVideo, "", "https://media.example.com/show/video/360p.m3u8"},
		{"v1", KindVideo, "", "https://media.example.com/show/video/1080p.m3u8"},
		{"v2", KindVideo, "", "https://cdn.example.net/video/720p.m3u8"},
		{"r0", KindAudio, "aud-lo", "https://media.example.com/show/audio/en/lo.m3u8"},
		{"r1", KindAudio, "aud-lo", "https://media.example.com/show/audio/de/lo.m3u8"},
		{"r2", KindAudio, "aud-hi", "https://media.example.com/show/audio/en/hi.m3u8"},
		{"r3", KindSubtitles, "subs", "https://media.example.com/show/subs/en.m3u8"},
	}
	if len(tracks) != len(want) {
		t.Fatalf("tracks = %+v", tracks)
	}
	for i, w := range want {
		tr := tracks[i]
		if tr.ID != w.id || tr.Kind != w.kind || tr.Group != w.group || tr.URL != w.url {
			t.Errorf("track %d = %+v, want %+v", i, tr, w)
		}
	}
	if v := tracks[1]; v.Width != 1920 || v.Height != 1080 || v.Bandwidth != 5000000 || v.FrameRate != 60 || v.AudioGroup != "aud-hi" || v.Name != "1080p (Full HD) 60fps" {
		t.Errorf("1080p track = %+v", v)
	}
	if a := tracks[5]; a.Codecs != "ec-3" || a.Language != "en" || a.Name != "English, 5.1" || !a.Default {
		t.Errorf("5.1 audio track = %+v", a)
	}
	if a := tracks[3]; a.Codecs != "mp4a.40.2" {
		t.Errorf("stereo audio codecs = %q", a.Codecs)
	}

	video, audio := DefaultPair(tracks)
	if video == nil || video.ID != "v1" {
		t.Fatalf("default video = %+v", video)
	}
	// The best video names the "aud-hi" group, so its audio comes from there.
	if audio == nil || audio.ID != "r2" {
		t.Fatalf("default audio = %+v", audio)
	}
	if video != &tracks[1] {
		t.Error("DefaultPair does not point into its argument")
	}
}

func TestHLSOptionsMuxedAndMedia(t *testing.T) {
	p, err := ParseHLS(mustURL(t, "https://m.example/hls/master.m3u8"), fixture(t, "hls/muxed_master.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	tracks := p.Options()
	if len(tracks) != 3 || tracks[0].Kind != KindMuxed || tracks[1].Kind != KindMuxed || tracks[2].Kind != KindAudio {
		t.Fatalf("tracks = %+v", tracks)
	}
	video, audio := DefaultPair(tracks)
	if video == nil || video.ID != "v1" || audio != nil {
		t.Fatalf("pair = %+v, %+v; want muxed v1 alone", video, audio)
	}

	media, err := ParseHLS(mustURL(t, hlsBase), fixture(t, "hls/media_vod.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if tr := media.Options(); len(tr) != 1 || tr[0].URL != hlsBase || tr[0].Kind != KindMuxed {
		t.Fatalf("media playlist options = %+v", tr)
	}
}

func TestHLSVariantKinds(t *testing.T) {
	master := "#EXTM3U\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"in\",NAME=\"main\",DEFAULT=YES\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"in\",NAME=\"alt\",URI=\"alt.m3u8\"\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"out\",NAME=\"main\",URI=\"main.m3u8\"\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=1,CODECS=\"avc1.42e01e,mp4a.40.2\",AUDIO=\"in\"\nv-in.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=2,CODECS=\"mp4a.40.2\",AUDIO=\"out\"\naudio-variant.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=3,AUDIO=\"out\"\nunknown.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=4,CODECS=\"hvc1.1.6.L93.B0\"\nvideo-only.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=5,CODECS=\"wvtt\"\ntext.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=6,CODECS=\"xyz1\"\nodd.m3u8\n"
	p, err := ParseHLS(nil, []byte(master))
	if err != nil {
		t.Fatal(err)
	}
	want := []TrackKind{
		KindMuxed,     // part of its audio group is carried in the variant
		KindAudio,     // audio codecs only, alternatives elsewhere
		KindMuxed,     // nothing says what it carries
		KindVideo,     // video codecs only
		KindSubtitles, // text only
		KindMuxed,     // unknown codec
	}
	tracks := p.Options()
	for i, w := range want {
		if tracks[i].Kind != w {
			t.Errorf("variant %d kind = %q, want %q", i, tracks[i].Kind, w)
		}
	}
}

func TestDASHOptionsAndDefaultPair(t *testing.T) {
	m := parseFixtureMPD(t, "dash/template_number.mpd")
	tracks := m.Options()
	if len(tracks) != 4 {
		t.Fatalf("tracks = %+v", tracks)
	}
	v := tracks[1]
	if v.ID != "v1080" || v.Kind != KindVideo || v.Width != 1920 || v.Height != 1080 || v.Codecs != "avc1.640028" || v.URL != "" ||
		v.Name != "1080p (Full HD)" || tracks[0].Name != "720p (HD)" {
		t.Errorf("video track = %+v", v)
	}
	if f := tracks[0].FrameRate; f < 29.97 || f > 29.98 {
		t.Errorf("frame rate = %v", f)
	}
	if a := tracks[2]; a.Kind != KindAudio || a.Language != "en" || a.Name != "English" || a.Bandwidth != 128000 {
		t.Errorf("audio track = %+v", a)
	}
	video, audio := DefaultPair(tracks)
	if video == nil || video.ID != "v1080" || audio == nil || audio.ID != "a128" {
		t.Fatalf("pair = %+v, %+v", video, audio)
	}

	// A Representation that recurs in later Periods is listed once.
	tl := parseFixtureMPD(t, "dash/timeline.mpd")
	if tr := tl.Options(); len(tr) != 1 || tr[0].ID != "low" || tr[0].Width != 640 {
		t.Fatalf("multi-period tracks = %+v", tr)
	}
}

func TestDefaultPairEdgeCases(t *testing.T) {
	if v, a := DefaultPair(nil); v != nil || a != nil {
		t.Error("empty track list")
	}
	audioOnly := []Track{
		{ID: "a", Kind: KindAudio, Bandwidth: 64000},
		{ID: "b", Kind: KindAudio, Bandwidth: 128000},
		{ID: "c", Kind: KindAudio, Bandwidth: 128000, Default: true},
		{ID: "s", Kind: KindSubtitles},
	}
	if v, a := DefaultPair(audioOnly); v != nil || a == nil || a.ID != "c" {
		t.Errorf("audio only = %+v, %+v", v, a)
	}
	// Resolution beats bandwidth; bandwidth then frame rate break ties.
	videos := []Track{
		{ID: "hd-fast", Kind: KindVideo, Width: 1280, Height: 720, Bandwidth: 9000000},
		{ID: "fhd30", Kind: KindVideo, Width: 1920, Height: 1080, Bandwidth: 5000000, FrameRate: 30},
		{ID: "fhd60", Kind: KindVideo, Width: 1920, Height: 1080, Bandwidth: 5000000, FrameRate: 60},
		{ID: "x", Kind: KindAudio, Group: "missing"},
	}
	v, a := DefaultPair(videos)
	if v == nil || v.ID != "fhd60" || a == nil || a.ID != "x" {
		t.Errorf("videos = %+v, %+v", v, a)
	}
	// An audio group without members falls back to any audio.
	grouped := []Track{{ID: "v", Kind: KindVideo, AudioGroup: "g"}, {ID: "a", Kind: KindAudio, Group: "other"}}
	if _, a := DefaultPair(grouped); a == nil || a.ID != "a" {
		t.Errorf("group fallback = %+v", a)
	}
}

func TestCodecKinds(t *testing.T) {
	for codec, want := range map[string]TrackKind{
		"avc1.640028": KindVideo, "hev1.1.6.L93.B0": KindVideo, "av01.0.08M.08": KindVideo, "vp09.00.10.08": KindVideo,
		"dvh1.05.06": KindVideo, "mp4a.40.2": KindAudio, "ec-3": KindAudio, "Opus": KindAudio, "fLaC": KindAudio,
		"wvtt": KindSubtitles, "stpp.ttml.im1t": KindSubtitles, "": "", "unknown": "",
	} {
		if got := codecKind(codec); got != want {
			t.Errorf("codecKind(%q) = %q, want %q", codec, got, want)
		}
	}
	for in, want := range map[string]float64{"25": 25, "30000/1001": 30000.0 / 1001, "60/2": 30, "": 0, "1/0": 0, "x": 0} {
		if got := parseFrameRate(in); got != want {
			t.Errorf("parseFrameRate(%q) = %v, want %v", in, got, want)
		}
	}
}

// Classifying tracks reads codecs, MIME types, content types and frame rates once per
// track, and many tracks can share one value (HLS renditions take their codecs from a
// variant; DASH Representations inherit from their AdaptationSet). Those values are
// bounded at parse time, so Options stays linear in the manifest.
func TestTrackAttributesBounded(t *testing.T) {
	// HLS: a thousand audio renditions name the group of a variant whose CODECS is
	// megabytes long.
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for i := 0; i < MaxVariants; i++ {
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"%d\",URI=\"a%d.m3u8\"\n", i, i)
	}
	hostile := b.String() + "#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO=\"a\",CODECS=\"" + strings.Repeat(",", 4<<20) + "\"\nv.m3u8\n"
	if _, err := ParseHLS(nil, []byte(hostile)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("HLS CODECS of 4 MiB: %v, want ErrTooLarge", err)
	}
	variant := func(codecs string) string {
		return "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,CODECS=\"" + codecs + "\"\nv.m3u8\n"
	}
	if _, err := ParseHLS(nil, []byte(variant(strings.Repeat("a", maxTokenBytes+1)))); !errors.Is(err, ErrTooLarge) {
		t.Errorf("HLS CODECS over the bound: %v, want ErrTooLarge", err)
	}
	if _, err := ParseHLS(nil, []byte(variant(strings.Repeat("a", maxTokenBytes)))); err != nil {
		t.Errorf("HLS CODECS at the bound: %v", err)
	}

	// DASH: a thousand Representations inherit the AdaptationSet's attribute, or each
	// gives its own.
	reps := strings.Repeat(`<Representation id="r"/>`, MaxVariants)
	for _, attr := range []string{"codecs", "mimeType", "frameRate", "contentType"} {
		set := func(value string) []byte {
			return mpd("PT1S", `<Period><AdaptationSet `+attr+`="`+value+`">`+reps+`</AdaptationSet></Period>`)
		}
		if _, err := ParseDASH(nil, set(strings.Repeat(",", 4<<20))); !errors.Is(err, ErrTooLarge) {
			t.Errorf("AdaptationSet@%s of 4 MiB: %v, want ErrTooLarge", attr, err)
		}
		if _, err := ParseDASH(nil, set(strings.Repeat("a", maxTokenBytes+1))); !errors.Is(err, ErrTooLarge) {
			t.Errorf("AdaptationSet@%s over the bound: %v, want ErrTooLarge", attr, err)
		}
		if _, err := ParseDASH(nil, set(" "+strings.Repeat("a", maxTokenBytes)+" ")); err != nil {
			t.Errorf("AdaptationSet@%s at the bound: %v", attr, err)
		}
		if attr == "contentType" {
			continue // an AdaptationSet attribute only
		}
		own := mpd("PT1S", `<Period><AdaptationSet><Representation id="r" `+attr+`="`+strings.Repeat("a", maxTokenBytes+1)+`"/></AdaptationSet></Period>`)
		if _, err := ParseDASH(nil, own); !errors.Is(err, ErrTooLarge) {
			t.Errorf("Representation@%s over the bound: %v, want ErrTooLarge", attr, err)
		}
	}
}

// Inherited attributes are normalized once per AdaptationSet: trimmed, and parsed where
// they are numbers.
func TestDASHInheritedAttributes(t *testing.T) {
	pad := strings.Repeat(" ", 256<<10)
	reps := strings.Repeat(`<Representation id="r"/>`, MaxVariants-1)
	m, err := ParseDASH(mustURL(t, mpdBase), mpd("PT4S", `<Period><AdaptationSet contentType="`+pad+`video`+pad+`" mimeType="`+pad+`video/mp4`+pad+
		`" codecs="`+pad+`avc1.4d401e`+pad+`" frameRate="`+pad+`25`+pad+`" width="`+pad+`640`+pad+`" height="360" lang="`+pad+`en`+pad+`">`+
		`<Label>`+pad+`Main`+pad+`</Label>`+
		`<SegmentTemplate media="`+pad+`$RepresentationID$/$Number$.m4s`+pad+`" startNumber="`+pad+`7`+pad+`" duration="`+pad+`2`+pad+`"/>`+
		`<Representation id="own" codecs="hvc1.1.6.L93.B0" width="1920" height="1080" frameRate="30000/1001" lang="de"><Label>Own</Label></Representation>`+
		reps+`</AdaptationSet></Period>`))
	if err != nil {
		t.Fatal(err)
	}
	as := m.Periods[0].AdaptationSets[0]
	if as.ContentType != "video" || as.MimeType != "video/mp4" || as.Codecs != "avc1.4d401e" || as.Lang != "en" || as.Label != "Main" {
		t.Errorf("AdaptationSet = %+v", as)
	}
	own, r := as.Representations[0], as.Representations[1]
	if own.Codecs != "hvc1.1.6.L93.B0" || own.Width != 1920 || own.Height != 1080 || own.FrameRate != "30000/1001" || own.Lang != "de" ||
		own.Label != "Own" || own.MimeType != "video/mp4" || own.ContentType != "video" {
		t.Errorf("own Representation = %+v", own)
	}
	if r.Codecs != "avc1.4d401e" || r.Width != 640 || r.Height != 360 || r.FrameRate != "25" || r.Lang != "en" || r.Label != "Main" ||
		r.MimeType != "video/mp4" || r.ContentType != "video" || r.Template == nil || r.Template.StartNumber != 7 || r.Template.Duration != 2 {
		t.Errorf("inheriting Representation = %+v", r)
	}
	tracks := m.Options()
	if len(tracks) != 2 || tracks[0].Kind != KindVideo || tracks[1].Kind != KindVideo || tracks[1].FrameRate != 25 {
		t.Errorf("tracks = %+v", tracks)
	}
	comparePlan(t, planDASH(t, m, "r"), []wantFragment{
		{url: "https://example.com/vod/r/7.m4s", durMS: 2000},
		{url: "https://example.com/vod/r/8.m4s", durMS: 2000},
	})

	// Numbers given at a higher level are bounded too: each inheriting Representation
	// parses them.
	for _, attr := range []string{"startNumber", "timescale", "duration", "presentationTimeOffset", "endNumber"} {
		long := strings.Repeat("0", maxTokenBytes) + "1"
		doc := mpd("PT4S", `<Period><SegmentTemplate media="$Number$" `+attr+`="`+long+`"/><AdaptationSet>`+reps+`</AdaptationSet></Period>`)
		if _, err := ParseDASH(nil, doc); !errors.Is(err, ErrTooLarge) {
			t.Errorf("Period SegmentTemplate@%s over the bound: %v, want ErrTooLarge", attr, err)
		}
	}
	// An AdaptationSet attribute is checked even when every Representation overrides it.
	if _, err := ParseDASH(nil, mpd("PT4S", `<Period><AdaptationSet width="wide"><Representation id="r" width="1"/></AdaptationSet></Period>`)); !errors.Is(err, ErrMalformed) {
		t.Errorf("bad AdaptationSet@width: %v, want ErrMalformed", err)
	}
}

// Renditions of one audio group share the codecs of the first variant that names it.
func TestHLSGroupCodecs(t *testing.T) {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for g := 0; g < 3; g++ {
		for i := 0; i < 2; i++ {
			fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"g%d\",NAME=\"%d\",URI=\"g%d-%d.m3u8\"\n", g, i, g, i)
		}
	}
	b.WriteString("#EXT-X-STREAM-INF:BANDWIDTH=1,AUDIO=\"g1\",CODECS=\" avc1.4d401e , MP4A.40.2 ,ec-3,,\"\nv1.m3u8\n")
	b.WriteString("#EXT-X-STREAM-INF:BANDWIDTH=2,AUDIO=\"g1\",CODECS=\"avc1.4d401e,opus\"\nv2.m3u8\n")
	b.WriteString("#EXT-X-STREAM-INF:BANDWIDTH=3,AUDIO=\"g0\",CODECS=\"avc1.4d401e\"\nv3.m3u8\n")
	p, err := ParseHLS(nil, []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"g0": "", "g1": "MP4A.40.2,ec-3", "g2": ""}
	n := 0
	for _, tr := range p.Options() {
		if tr.Kind != KindAudio {
			continue
		}
		n++
		if tr.Codecs != want[tr.Group] {
			t.Errorf("rendition %s of %s: codecs %q, want %q", tr.ID, tr.Group, tr.Codecs, want[tr.Group])
		}
	}
	if n != 6 {
		t.Errorf("%d audio renditions, want 6", n)
	}
}
