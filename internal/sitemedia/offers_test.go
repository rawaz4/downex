package sitemedia

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"downex/internal/media"
)

func offerFixture(t *testing.T, name, host string) (*Info, *Offer) {
	t.Helper()
	info := parseFixture(t, name)
	off, err := Offers(info, host)
	if err != nil {
		t.Fatalf("Offers(%s): %v", name, err)
	}
	return info, off
}

func trackIDs(tracks []media.Track) (videos, audios []string) {
	for _, t := range tracks {
		if t.Kind == media.KindAudio {
			audios = append(audios, t.ID)
		} else {
			videos = append(videos, t.ID)
		}
	}
	return videos, audios
}

// The YouTube-like page offers one MP4 video per quality class, best class first, named
// by its class (YouTube's own label), the MP4 audios follow best first, and nothing of
// WebM, VP9 or Opus is listed.
func TestOffersYouTube(t *testing.T) {
	_, off := offerFixture(t, "youtube.json", "www.youtube.com")
	if off.Format != FormatHTTP || off.Container != ContainerMP4 || off.ManifestURL != "" || off.Headers != nil {
		t.Errorf("offer format %q, container %q, manifest %q", off.Format, off.Container, off.ManifestURL)
	}
	videos, audios := trackIDs(off.Tracks)
	// 2160: AV1 SDR over AV1 HDR; 1080 and 720: H.264 60 fps over 30 fps, AV1 and HEVC;
	// 360: video-only over the muxed 18; 144: H.264 over AV1. (The 480p 135 carries
	// request_data and is dropped by Parse.)
	wantVideos := []string{"401", "400", "299", "298", "134", "160"}
	wantAudios := []string{"140-0", "139-0", "140-drc", "328-1", "140-1"}
	if !reflect.DeepEqual(videos, wantVideos) {
		t.Errorf("videos\n got %v\nwant %v", videos, wantVideos)
	}
	if !reflect.DeepEqual(audios, wantAudios) {
		t.Errorf("audios\n got %v\nwant %v", audios, wantAudios)
	}
	if n := len(videos); n == 0 || off.Tracks[n-1].Kind == media.KindAudio || off.Tracks[n].Kind != media.KindAudio {
		t.Error("videos do not all precede the audios")
	}
	if off.DefaultVideo != "401" || off.DefaultAudio != "140-0" {
		t.Errorf("default %q + %q, want 401 + 140-0", off.DefaultVideo, off.DefaultAudio)
	}
	var names []string
	for _, tr := range off.Tracks {
		if tr.Ext != "mp4" && tr.Ext != "m4a" {
			t.Errorf("track %s has ext %q", tr.ID, tr.Ext)
		}
		for c := range strings.SplitSeq(tr.Codecs, ",") {
			if name := CodecName(c); name == "VP9" || name == "Opus" {
				t.Errorf("track %s offers %s", tr.ID, name)
			}
		}
		if !strings.HasPrefix(tr.Key, "s1-") {
			t.Errorf("track %s key %q", tr.ID, tr.Key)
		}
		if tr.Kind != media.KindAudio {
			names = append(names, tr.Name)
		}
	}
	wantNames := []string{"4K (2160p) 60fps", "1440p (2K) 60fps", "1080p (Full HD) 60fps", "720p (HD) 60fps", "360p", "144p"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Errorf("names\n got %q\nwant %q", names, wantNames)
	}

	byID := make(map[string]media.Track)
	for _, tr := range off.Tracks {
		byID[tr.ID] = tr
	}
	for id, want := range map[string]media.Track{
		"299": {ID: "299", Kind: media.KindVideo, Bandwidth: 6500000, Width: 1920, Height: 1080, FrameRate: 60,
			Codecs: "avc1.64002a", Name: "1080p (Full HD) 60fps", Size: 172900000, Ext: "mp4"},
		"401": {ID: "401", Kind: media.KindVideo, Bandwidth: 18000000, Width: 3840, Height: 2160, FrameRate: 60,
			Codecs: "av01.0.13M.08", Name: "4K (2160p) 60fps", Size: 478000000, Ext: "mp4"},
		"134": {ID: "134", Kind: media.KindVideo, Bandwidth: 350400, Width: 640, Height: 360, FrameRate: 30,
			Codecs: "avc1.4d401e", Name: "360p", Size: 9300000, Ext: "mp4"},
		"140-1": {ID: "140-1", Kind: media.KindAudio, Bandwidth: 129400, Codecs: "mp4a.40.2", Language: "de",
			Name: "German, medium", Size: 3443000, Ext: "m4a"},
	} {
		got := byID[id]
		want.Key = got.Key
		if !reflect.DeepEqual(got, want) {
			t.Errorf("track %s\n got %+v\nwant %+v", id, got, want)
		}
	}
	// What the class rule left out stays selectable: the HDR 2160p and the muxed 360p.
	for id, want := range map[string]string{"701": "4K (2160p) 60fps HDR", "18": "360p", "137": "1080p (Full HD)"} {
		if tr := trackByID(off, id); tr == nil || tr.Name != want || slices.ContainsFunc(off.Tracks, func(t media.Track) bool { return t.ID == id }) {
			t.Errorf("left-out track %s = %+v, want %q and not offered", id, tr, want)
		}
	}
}

// The user's 3840x1634 film (avc1 and av01 at every class, HDR at 2160 and 1440, a
// muxed 360p, WebM): exactly one row per quality class, named by the class and not by
// the film's height, H.264 wherever it exists, AV1 only where nothing else does; the
// default is the best class.
func TestOffersFilm(t *testing.T) {
	info, off := offerFixture(t, "film.json", "www.youtube.com")
	type row struct{ id, name, codec string }
	var got []row
	for _, tr := range off.Tracks {
		if tr.Kind != media.KindAudio {
			got = append(got, row{tr.ID, tr.Name, CodecName(tr.Codecs)})
		}
	}
	want := []row{
		{"401", "4K (2160p)", "AV1"},
		{"400", "1440p (2K)", "AV1"},
		{"137", "1080p (Full HD)", "H.264"},
		{"136", "720p (HD)", "H.264"},
		{"135", "480p", "H.264"}, // no format_note: named by its picture, 854x364
		{"134", "360p", "H.264"},
		{"133", "240p", "H.264"},
		{"160", "144p", "H.264"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows\n got %v\nwant %v", got, want)
	}
	if _, audios := trackIDs(off.Tracks); !reflect.DeepEqual(audios, []string{"140", "140-drc"}) {
		t.Errorf("audios %v", audios)
	}
	if off.DefaultVideo != "401" || off.DefaultAudio != "140" {
		t.Errorf("default %q + %q, want 401 + 140", off.DefaultVideo, off.DefaultAudio)
	}
	if tr := off.Tracks[0]; tr.Width != 3840 || tr.Height != 1634 || tr.FrameRate != 24 {
		t.Errorf("the picture is lost: %+v", tr)
	}

	// A key of a left-out format (the AV1 1080p of an earlier offer) still selects it,
	// and the probe then lists it in place of its class's row.
	av1 := trackByID(off, "399")
	if av1 == nil || av1.Name != "1080p (Full HD)" {
		t.Fatalf("left-out 399 = %+v", av1)
	}
	pick, err := Select(info, off, "", "", "", av1.Key)
	if err == nil {
		t.Fatalf("an audio key of a video selected %+v", pick)
	}
	if pick, err = Select(info, off, "", "", av1.Key, ""); err != nil || pick.Tracks[0].ID != "399" || pick.Tracks[1].ID != "140" {
		t.Fatalf("Select(key 399) = %+v, %v", pick, err)
	}
	var ids []string
	for _, tr := range off.Listed(pick) {
		ids = append(ids, tr.ID)
	}
	// It stands in for its class's row: still one row per class.
	if want := []string{"401", "400", "399", "136", "135", "134", "133", "160", "140", "140-drc"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("Listed = %v, want %v", ids, want)
	}
	if len(off.Tracks) != 10 || off.Tracks[2].ID != "137" {
		t.Errorf("Listed changed the offer: %v", off.Tracks)
	}
	// An offered pick lists the offer as it is; the muxed 18, chosen by its ID, stands
	// in for the 360p row.
	if def, err := Select(info, off, "", "", "", ""); err != nil || len(off.Listed(def)) != len(off.Tracks) {
		t.Errorf("Listed(default) = %v, %v", off.Listed(def), err)
	}
	muxed, err := Select(info, off, "18", "", "", "")
	if err != nil || muxed.Output != OutputCopy {
		t.Fatalf("Select(18) = %+v, %v", muxed, err)
	}
	if l := off.Listed(muxed); len(l) != 10 || l[5].ID != "18" || l[4].ID != "135" || l[6].ID != "133" {
		t.Errorf("Listed(18) = %v", l)
	}
}

// A page of muxed formats offers every MP4 one (a site that names no codec included)
// and no audio; WebM and VP9-in-MP4 are left out.
func TestOffersMuxed(t *testing.T) {
	_, off := offerFixture(t, "muxed.json", "vimeo.com")
	videos, audios := trackIDs(off.Tracks)
	if want := []string{"http-1080p", "http-720p", "dash-480p", "http-360p"}; !reflect.DeepEqual(videos, want) || audios != nil {
		t.Errorf("tracks %v + %v, want %v and no audio", videos, audios, want)
	}
	if off.Format != FormatHTTP || off.DefaultVideo != "http-1080p" || off.DefaultAudio != "" {
		t.Errorf("format %q, default %q + %q", off.Format, off.DefaultVideo, off.DefaultAudio)
	}
	if tr := off.Tracks[0]; tr.Codecs != "" || tr.Name != "1080p (Full HD)" || tr.Kind != media.KindMuxed {
		t.Errorf("unnamed-codec track = %+v", tr)
	}
}

// Without direct MP4 video the manifest most video formats share is offered, with the
// headers of its first format and a Cookie only for the manifest's own host. A video-
// only MP4 is not offered when no MP4 audio exists to pair it with.
func TestOffersManifestFallback(t *testing.T) {
	_, off := offerFixture(t, "hls.json", "example.org")
	if off.Format != FormatHLS || off.Container != ContainerMP4 || off.Tracks != nil || off.DefaultVideo != "" {
		t.Errorf("offer = %+v, want the HLS fallback", off)
	}
	if off.ManifestURL != "https://video.example.org/ext_tw_video/1/pu/pl/master.m3u8?tag=12" {
		t.Errorf("manifest %q", off.ManifestURL)
	}
	if off.Headers.Get("Referer") != "https://example.org/status/1" || !reflect.DeepEqual(off.Headers["Cookie"], []string{"guest_id=v1"}) {
		t.Errorf("headers %v", off.Headers)
	}

	dash := page(`{"format_id": "d1", "protocol": "http_dash_segments", "manifest_url": "https://cdn.example.com/a.mpd", "vcodec": "avc1", "acodec": "none"}`,
		`{"format_id": "h1", "protocol": "m3u8_native", "manifest_url": "https://cdn.example.com/b.m3u8", "vcodec": "avc1", "acodec": "none"}`,
		`{"format_id": "d2", "protocol": "http_dash_segments", "manifest_url": "https://cdn.example.com/a.mpd", "vcodec": "none", "acodec": "mp4a"}`)
	info, err := Parse(dash, testNow)
	if err != nil {
		t.Fatal(err)
	}
	// One video format each: the first manifest wins the tie; audio is not counted.
	if off, err := Offers(info, "example.com"); err != nil || off.Format != FormatDASH || off.ManifestURL != "https://cdn.example.com/a.mpd" {
		t.Errorf("Offers = %+v, %v; want the DASH manifest", off, err)
	}
}

// Pages without anything to offer are refused with the right reason.
func TestOffersRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		data []byte
		want error
	}{
		"audio only":         {fixture(t, "audio.json"), media.ErrUnsupported},
		"drm":                {fixture(t, "drm.json"), media.ErrEncrypted},
		"has drm":            {[]byte(`{"id": "x", "extractor_key": "Generic", "_has_drm": true, "formats": []}`), media.ErrEncrypted},
		"needs auth":         {[]byte(`{"id": "x", "extractor_key": "Generic", "availability": "needs_auth"}`), ErrAuthRequired},
		"members only":       {[]byte(`{"id": "x", "extractor_key": "Generic", "availability": "subscriber_only"}`), ErrAuthRequired},
		"premium only":       {[]byte(`{"id": "x", "extractor_key": "Generic", "availability": "premium_only"}`), ErrAuthRequired},
		"no formats":         {[]byte(`{"id": "x", "extractor_key": "Generic", "availability": "public"}`), media.ErrUnsupported},
		"webm only":          {page(`{"format_id": "w", "protocol": "https", "url": "https://cdn.example.com/w", "ext": "webm", "vcodec": "vp9", "acodec": "opus"}`), media.ErrUnsupported},
		"audio manifest":     {page(`{"format_id": "a", "protocol": "m3u8_native", "url": "https://cdn.example.com/a.m3u8", "vcodec": "none", "acodec": "mp4a"}`), media.ErrUnsupported},
		"video without mp4a": {page(`{"format_id": "v", "protocol": "https", "url": "https://cdn.example.com/v", "ext": "mp4", "container": "mp4_dash", "vcodec": "avc1", "acodec": "none"}`, `{"format_id": "a", "protocol": "https", "url": "https://cdn.example.com/a", "ext": "webm", "container": "webm_dash", "vcodec": "none", "acodec": "opus"}`), media.ErrUnsupported},
	} {
		info, err := Parse(c.data, testNow)
		if err != nil {
			t.Errorf("%s: Parse: %v", name, err)
			continue
		}
		if off, err := Offers(info, "example.com"); !errors.Is(err, c.want) || off != nil {
			t.Errorf("%s: Offers = %+v, %v; want %v", name, off, err, c.want)
		}
	}
	if _, err := Offers(nil, "example.com"); !errors.Is(err, media.ErrMalformed) {
		t.Errorf("Offers(nil) = %v", err)
	}
}

// Format URLs on this machine or its link are usable only when the page is on the
// same host (docs/SITE-MEDIA-SPEC.md §6.1).
func TestOffersAddressRule(t *testing.T) {
	for _, c := range []struct {
		url, page string
		want      bool
	}{
		{"https://cdn.example.com/v", "www.example.com", true},
		{"https://10.0.0.5/v", "www.example.com", true},
		{"https://203.0.113.9:8443/v", "www.example.com", true},
		{"http://127.0.0.1:8080/v", "www.example.com", false},
		{"http://127.0.0.1:8080/v", "127.0.0.1:9999", true},
		{"http://127.0.0.1:8080/v", "127.0.0.1", true},
		{"http://127.0.0.2/v", "127.0.0.1", false},
		{"http://localhost/v", "www.example.com", false},
		{"http://LOCALHOST./v", "localhost:3000", true},
		{"http://a.localhost/v", "www.example.com", false},
		{"http://[::1]/v", "www.example.com", false},
		{"http://[::1]:8080/v", "[::1]:9000", true},
		{"http://[::ffff:127.0.0.1]/v", "www.example.com", false},
		{"http://169.254.169.254/latest", "www.example.com", false},
		{"http://[fe80::1%25eth0]/v", "www.example.com", false},
		{"http://0.0.0.0/v", "www.example.com", false},
		{"http://[::]/v", "www.example.com", false},
		{"http://2130706433/v", "www.example.com", false},
		{"http://0x7f.1/v", "www.example.com", false},
		{"http://127.1/v", "www.example.com", false},
		{"http://1.example/v", "www.example.com", true},
	} {
		info, err := Parse(page(muxedFormat("f", fmt.Sprintf("%q: %q", "url", c.url))), testNow)
		if err != nil {
			t.Fatal(err)
		}
		if info.Format("f") == nil {
			t.Errorf("%s: dropped by Parse", c.url)
			continue
		}
		off, err := Offers(info, c.page)
		if got := err == nil && len(off.Tracks) == 1; got != c.want {
			t.Errorf("%s on page %s: offered = %v (%v), want %v", c.url, c.page, got, err, c.want)
		}
	}
}

// At most MaxOffered tracks are offered, the worst dropped: with one video per class,
// what goes is the worst audios.
func TestOffersCap(t *testing.T) {
	var formats []string
	for i, height := range []int{1080, 720, 480} {
		formats = append(formats, fmt.Sprintf(`{"format_id": "v%d", "protocol": "https", "url": "https://cdn.example.com/v%d", "ext": "mp4",`+
			` "container": "mp4_dash", "vcodec": "avc1.4d401f", "acodec": "none", "height": %d}`, i, i, height))
	}
	for i := range 250 {
		formats = append(formats, fmt.Sprintf(`{"format_id": "a%d", "protocol": "https", "url": "https://cdn.example.com/a%d", "ext": "m4a",`+
			` "container": "m4a_dash", "vcodec": "none", "acodec": "mp4a.40.2", "abr": %d}`, i, i, 10+i))
	}
	info, err := Parse(page(formats...), testNow)
	if err != nil {
		t.Fatal(err)
	}
	off, err := Offers(info, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	videos, audios := trackIDs(off.Tracks)
	if len(off.Tracks) != MaxOffered || len(videos) != 3 || len(audios) != MaxOffered-3 {
		t.Fatalf("%d tracks (%d videos, %d audios), want %d", len(off.Tracks), len(videos), len(audios), MaxOffered)
	}
	if audios[0] != "a249" || audios[len(audios)-1] != "a53" {
		t.Errorf("kept audios %s..%s; want the best", audios[0], audios[len(audios)-1])
	}
}

// One video per quality class: SDR before HDR (HDR only when the class has nothing
// else), then the higher frame rate, then H.264 before HEVC before AV1, then the higher
// bit rate; a muxed format only for a class without a video-only one. Formats without
// a picture are one class, offered last.
func TestOffersOnePerClass(t *testing.T) {
	video := func(id string, width, height int, extra string) string {
		return fmt.Sprintf(`{"format_id": %q, "protocol": "https", "url": "https://cdn.example.com/%s", "ext": "mp4",`+
			` "container": "mp4_dash", "vcodec": "avc1.640028", "acodec": "none", "width": %d, "height": %d, "fps": 30%s}`,
			id, id, width, height, extra)
	}
	aac := `{"format_id": "aac", "protocol": "https", "url": "https://cdn.example.com/a", "ext": "m4a", "container": "m4a_dash", "vcodec": "none", "acodec": "mp4a.40.2"}`
	for name, c := range map[string]struct {
		formats []string
		want    []string
	}{
		"hdr only": {[]string{video("hdr", 3840, 2160, `, "dynamic_range": "HDR10", "tbr": 9000`)}, []string{"hdr"}},
		"sdr over hdr": {[]string{
			video("hdr60", 3840, 2160, `, "dynamic_range": "HDR10", "fps": 60, "tbr": 9000`),
			video("sdr", 3840, 2160, `, "tbr": 10, "vcodec": "av01.0.12M.08"`),
		}, []string{"sdr"}},
		"fps over codec": {[]string{
			video("avc30", 1920, 1080, `, "tbr": 9000`),
			video("av160", 1920, 1080, `, "fps": 59.94, "tbr": 10, "vcodec": "av01.0.09M.08"`),
		}, []string{"av160"}},
		"h264 over hevc over av1": {[]string{
			video("av1", 1920, 1080, `, "tbr": 9000, "vcodec": "av01.0.08M.08"`),
			video("hevc", 1920, 1080, `, "tbr": 5000, "vcodec": "hvc1.1.6.L120.90"`),
			video("avc", 1920, 1080, `, "tbr": 10`),
		}, []string{"avc"}},
		"hevc over av1": {[]string{
			video("av1", 1920, 1080, `, "tbr": 9000, "vcodec": "av01.0.08M.08"`),
			video("hevc", 1920, 1080, `, "tbr": 5000, "vcodec": "hev1.1.6.L120.90"`),
		}, []string{"hevc"}},
		"bit rate, then size": {[]string{
			video("low", 1920, 1080, `, "tbr": 1000, "filesize": 900`),
			video("small", 1920, 1080, `, "tbr": 2000, "filesize": 100`),
			video("big", 1920, 1080, `, "tbr": 2000, "filesize": 200`),
		}, []string{"big"}},
		"video-only over muxed": {[]string{
			muxedFormat("muxed", `"width": 640, "tbr": 9000`),
			video("av1", 640, 360, `, "tbr": 10, "vcodec": "av01.0.00M.08"`),
		}, []string{"av1"}},
		"muxed for a class of its own": {[]string{
			muxedFormat("m360", `"width": 640`),
			video("v1080", 1920, 1080, ``),
		}, []string{"v1080", "m360"}},
		"classes, not heights": {[]string{
			video("1634", 3840, 1634, `, "tbr": 5`), video("2160", 3840, 2160, `, "tbr": 1`),
			video("1090", 2560, 1090, ``), video("818", 1920, 818, `, "tbr": 5`), video("1080x1920", 1080, 1920, `, "tbr": 1`),
			`{"format_id": "nopic", "ext": "mp4", "protocol": "https", "url": "https://cdn.example.com/n.mp4", "vcodec": "avc1.4d401f", "acodec": "mp4a.40.2"}`,
		}, []string{"1634", "1090", "818", "nopic"}},
		"not usable": {[]string{
			video("progressive", 1920, 1080, `, "container": "mp4", "tbr": 9000`),
			video("vp9", 1920, 1080, `, "vcodec": "vp9", "tbr": 9000`),
			video("kept", 1920, 1080, `, "tbr": 1`),
		}, []string{"kept"}},
	} {
		info, err := Parse(page(append(c.formats, aac)...), testNow)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		off, err := Offers(info, "example.com")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if videos, _ := trackIDs(off.Tracks); !reflect.DeepEqual(videos, c.want) || off.DefaultVideo != c.want[0] {
			t.Errorf("%s: videos %v (default %s), want %v", name, videos, off.DefaultVideo, c.want)
		}
	}
}

// The class comes from the picture for every site; only YouTube's own labels name it
// (another site's note "720p" for a 1080p picture is not read).
func TestOffersClassLabels(t *testing.T) {
	formats := []string{
		`{"format_id": "a", "protocol": "https", "url": "https://cdn.example.com/a", "ext": "mp4", "container": "mp4_dash", "vcodec": "avc1", "acodec": "none", "width": 1920, "height": 1080, "format_note": "720p"}`,
		`{"format_id": "b", "protocol": "https", "url": "https://cdn.example.com/b", "ext": "mp4", "container": "mp4_dash", "vcodec": "avc1", "acodec": "none", "width": 1280, "height": 720, "format_note": "720p"}`,
		`{"format_id": "s", "protocol": "https", "url": "https://cdn.example.com/s", "ext": "m4a", "container": "m4a_dash", "vcodec": "none", "acodec": "mp4a.40.2"}`,
	}
	for key, want := range map[string][]string{"Generic": {"a", "b"}, "Youtube": {"a"}} {
		data := strings.Replace(string(page(formats...)), `"Generic"`, `"`+key+`"`, 1)
		info, err := Parse([]byte(data), testNow)
		if err != nil {
			t.Fatal(err)
		}
		off, err := Offers(info, "example.com")
		if err != nil {
			t.Fatal(err)
		}
		if videos, _ := trackIDs(off.Tracks); !reflect.DeepEqual(videos, want) {
			t.Errorf("%s: videos %v, want %v", key, videos, want)
		}
	}

	// A YouTube label that only restates the raw height of a wider picture does not
	// name the class: a "480p" of 1138x480 is 720p, the class of the 1280x720 "720p".
	data := strings.Replace(string(page(
		`{"format_id": "wide", "protocol": "https", "url": "https://cdn.example.com/w", "ext": "mp4", "container": "mp4_dash", "vcodec": "avc1", "acodec": "none", "width": 1138, "height": 480, "format_note": "480p", "tbr": 900}`,
		`{"format_id": "hd", "protocol": "https", "url": "https://cdn.example.com/h", "ext": "mp4", "container": "mp4_dash", "vcodec": "avc1", "acodec": "none", "width": 1280, "height": 720, "format_note": "720p", "tbr": 1000}`,
		`{"format_id": "sd", "protocol": "https", "url": "https://cdn.example.com/s", "ext": "mp4", "container": "mp4_dash", "vcodec": "avc1", "acodec": "none", "width": 854, "height": 480, "format_note": "480p"}`,
		formats[2])), `"Generic"`, `"Youtube"`, 1)
	info, err := Parse([]byte(data), testNow)
	if err != nil {
		t.Fatal(err)
	}
	off, err := Offers(info, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, tr := range off.Tracks {
		if tr.Kind != media.KindAudio {
			rows = append(rows, tr.ID+" "+tr.Name)
		}
	}
	if want := []string{"hd 720p (HD)", "sd 480p"}; !reflect.DeepEqual(rows, want) {
		t.Errorf("raw-height label: rows %q, want %q", rows, want)
	}
	if wide := trackByID(off, "wide"); wide == nil || wide.Name != "720p (HD)" {
		t.Errorf("left-out wide = %+v", wide)
	}
}

// Formats without a picture have no class: none is left out for another, they follow
// the classes in the order of the default video, and yt-dlp's own order (it lists
// formats worst first) settles what nothing else does, so the best of them comes first.
func TestOffersNoPicture(t *testing.T) {
	nopic := func(id, extra string) string {
		return fmt.Sprintf(`{"format_id": %q, "ext": "mp4", "protocol": "https", "url": "https://cdn.example.com/%s.mp4",`+
			` "vcodec": "avc1.4d401f", "acodec": "mp4a.40.2"%s}`, id, id, extra)
	}
	for name, c := range map[string]struct {
		key     string
		formats []string
		want    []string
	}{
		"yt-dlp order": {"Generic", []string{nopic("0", ""), nopic("1", ""), nopic("2", "")}, []string{"2", "1", "0"}},
		"size": {"Facebook", []string{nopic("sd", `, "filesize_approx": 1000000`), nopic("hd", `, "filesize_approx": 5000000`)},
			[]string{"hd", "sd"}},
		"bit rate": {"Generic", []string{nopic("b", `, "tbr": 3000`), nopic("a", `, "tbr": 800`), nopic("c", `, "tbr": 1500`)},
			[]string{"b", "c", "a"}},
		"after the classes": {"Generic", []string{nopic("x", ""), muxedFormat("m360", `"width": 640`), nopic("y", "")},
			[]string{"m360", "y", "x"}},
	} {
		data := strings.Replace(string(page(c.formats...)), `"Generic"`, `"`+c.key+`"`, 1)
		info, err := Parse([]byte(data), testNow)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		off, err := Offers(info, "example.com")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if videos, _ := trackIDs(off.Tracks); !reflect.DeepEqual(videos, c.want) || off.DefaultVideo != c.want[0] {
			t.Errorf("%s: videos %v (default %s), want %v", name, videos, off.DefaultVideo, c.want)
		}
		if len(off.more) != 0 {
			t.Errorf("%s: left out %v", name, off.more)
		}
	}
}

// Videos without a picture are not reduced to one, so they are capped too: the longer
// of videos and audios loses its worst until MaxOffered remain, and a video capped away
// still selects and is then listed in place of the worst offered one.
func TestOffersCapVideos(t *testing.T) {
	var formats []string
	for i := range 150 {
		formats = append(formats, fmt.Sprintf(`{"format_id": "v%d", "protocol": "https", "url": "https://cdn.example.com/v%d", "ext": "mp4",`+
			` "container": "mp4_dash", "vcodec": "avc1.4d401f", "acodec": "none", "tbr": %d}`, i, i, 100+i))
	}
	formats = append(formats, `{"format_id": "hd", "protocol": "https", "url": "https://cdn.example.com/hd", "ext": "mp4",`+
		` "container": "mp4_dash", "vcodec": "avc1.640028", "acodec": "none", "width": 1280, "height": 720}`)
	for i := range 100 {
		formats = append(formats, fmt.Sprintf(`{"format_id": "a%d", "protocol": "https", "url": "https://cdn.example.com/a%d", "ext": "m4a",`+
			` "container": "m4a_dash", "vcodec": "none", "acodec": "mp4a.40.2", "abr": %d}`, i, i, 10+i))
	}
	info, err := Parse(page(formats...), testNow)
	if err != nil {
		t.Fatal(err)
	}
	off, err := Offers(info, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	videos, audios := trackIDs(off.Tracks)
	if len(off.Tracks) != MaxOffered || len(videos) != 100 || len(audios) != 100 {
		t.Fatalf("%d tracks (%d videos, %d audios), want 100 + 100", len(off.Tracks), len(videos), len(audios))
	}
	if videos[0] != "hd" || videos[1] != "v149" || videos[99] != "v51" || off.DefaultVideo != "hd" {
		t.Errorf("kept videos %s, %s..%s; want the classed one, then the best", videos[0], videos[1], videos[99])
	}
	pick, err := Select(info, off, "v3", "", "", "")
	if err != nil {
		t.Fatalf("Select(capped v3) = %v", err)
	}
	listed := off.Listed(pick)
	if len(listed) != MaxOffered || listed[99].ID != "v3" || listed[98].ID != "v52" || listed[100].Kind != media.KindAudio {
		t.Errorf("Listed(v3) = %d tracks, [98..100] %v", len(listed), listed[98:101])
	}
}

// The manifest fallback, and every probed manifest, offers the MP4 tracks only (no
// VP9, Opus or subtitles), one video per quality class best class first (within a class
// video-only before muxed, frame rate, H.264 before HEVC before AV1 before an unnamed
// codec, bandwidth), then the audios best first.
func TestManifestTracks(t *testing.T) {
	in := []media.Track{
		{ID: "vp9-1080", Kind: media.KindVideo, Width: 1920, Height: 1080, Bandwidth: 3_000_000, Codecs: "vp09.00.40.08"},
		{ID: "avc-360", Kind: media.KindVideo, Width: 640, Height: 360, Bandwidth: 600_000, Codecs: "avc1.4d401e"},
		{ID: "avc-720", Kind: media.KindVideo, Width: 1280, Height: 720, Bandwidth: 1_500_000, Codecs: "avc1.64001f"},
		{ID: "av1-720", Kind: media.KindVideo, Width: 1280, Height: 720, Bandwidth: 1_000_000, Codecs: "av01.0.05M.08"},
		{ID: "avc-720-low", Kind: media.KindVideo, Width: 1280, Height: 720, Bandwidth: 900_000, Codecs: "avc1.64001f"},
		{ID: "av1-720-60", Kind: media.KindVideo, Width: 1280, Height: 720, FrameRate: 59.94, Bandwidth: 2_000_000, Codecs: "av01.0.05M.08"},
		{ID: "hevc-720-60", Kind: media.KindVideo, Width: 1280, Height: 720, FrameRate: 60, Bandwidth: 1_000_000, Codecs: "hvc1.1.6.L93.90"},
		{ID: "muxed-720", Kind: media.KindMuxed, Width: 1280, Height: 720, FrameRate: 60, Bandwidth: 1_800_000, Codecs: "avc1.64001f,mp4a.40.2"},
		{ID: "wide-2160", Kind: media.KindVideo, Width: 3840, Height: 1634, Bandwidth: 8_000_000, Codecs: "av01.0.12M.08"},
		{ID: "avc-2160", Kind: media.KindVideo, Width: 3840, Height: 2160, Bandwidth: 20_000_000, Codecs: "avc1.640033"},
		{ID: "muxed-vp9", Kind: media.KindMuxed, Height: 480, Codecs: "vp09.00.30.08,opus"},
		{ID: "muxed-unnamed", Kind: media.KindMuxed, Height: 240},
		{ID: "muxed-240", Kind: media.KindMuxed, Width: 426, Height: 240, Bandwidth: 300_000, Codecs: "avc1.4d4015,mp4a.40.2"},
		{ID: "bare", Kind: media.KindMuxed, Codecs: "avc1.4d401e,mp4a.40.2"},
		{ID: "opus", Kind: media.KindAudio, Bandwidth: 160_000, Codecs: "opus"},
		{ID: "aac-lo", Kind: media.KindAudio, Bandwidth: 64_000, Codecs: "mp4a.40.5"},
		{ID: "aac-hi", Kind: media.KindAudio, Bandwidth: 128_000, Codecs: "mp4a.40.2"},
		{ID: "aac-default", Kind: media.KindAudio, Bandwidth: 96_000, Codecs: "mp4a.40.2", Default: true},
		{ID: "subs", Kind: media.KindSubtitles, Codecs: "wvtt"},
	}
	got := ManifestTracks(in)
	var ids []string
	for _, t := range got {
		ids = append(ids, t.ID)
	}
	// 2160: H.264 over AV1 (3840x1634 is the same class); 720: 60 fps video-only, HEVC
	// over AV1, before the muxed one; 240: the named codec; then the track without a
	// picture.
	want := []string{"avc-2160", "hevc-720-60", "avc-360", "muxed-240", "bare", "aac-default", "aac-hi", "aac-lo"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ManifestTracks = %v, want %v", ids, want)
	}
	if in[0].ID != "vp9-1080" || len(in) != 19 {
		t.Error("ManifestTracks changed its input")
	}
	if got := ManifestTracks(nil); len(got) != 0 {
		t.Errorf("no tracks: %v", got)
	}

	// A master whose variants declare no RESOLUTION: no class, so none is left out;
	// the higher bandwidth first.
	bare := []media.Track{
		{ID: "v0", Kind: media.KindMuxed, Bandwidth: 800_000, Codecs: "avc1.4d401e,mp4a.40.2"},
		{ID: "v1", Kind: media.KindMuxed, Bandwidth: 2_000_000, Codecs: "avc1.4d401f,mp4a.40.2"},
		{ID: "v2", Kind: media.KindMuxed, Bandwidth: 5_000_000, Codecs: "avc1.640028,mp4a.40.2"},
	}
	ids = ids[:0]
	for _, t := range ManifestTracks(bare) {
		ids = append(ids, t.ID)
	}
	if want := []string{"v2", "v1", "v0"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("no RESOLUTION: ManifestTracks = %v, want %v", ids, want)
	}
}

// A probe or download that names a video the class rule leaves out (by its key, else
// its ID, as mediajob finds it) gets it listed in place of the video of its class, so
// that it can be chosen; a track ManifestTracks never offers (VP9, audio) is not listed
// for being named.
func TestManifestTracksFor(t *testing.T) {
	in := []media.Track{
		{ID: "v0", Key: "k0", Kind: media.KindVideo, Width: 1920, Height: 1080, Bandwidth: 5_000_000, Codecs: "avc1.640028"},
		{ID: "v1", Key: "k1", Kind: media.KindVideo, Width: 1920, Height: 1080, Bandwidth: 6_000_000, Codecs: "hvc1.2.4.L123.B0"},
		{ID: "v2", Key: "k2", Kind: media.KindVideo, Width: 1280, Height: 720, Bandwidth: 1_500_000, Codecs: "avc1.64001f"},
		{ID: "v3", Key: "k3", Kind: media.KindVideo, Width: 1920, Height: 1080, Bandwidth: 9_000_000, Codecs: "vp09.00.40.08"},
		{ID: "a0", Key: "ka", Kind: media.KindAudio, Bandwidth: 128_000, Codecs: "mp4a.40.2"},
	}
	ids := func(ts []media.Track) []string {
		var out []string
		for _, t := range ts {
			out = append(out, t.ID)
		}
		return out
	}
	for _, c := range []struct {
		id, key string
		want    []string
	}{
		{"", "", []string{"v0", "v2", "a0"}},
		{"v0", "", []string{"v0", "v2", "a0"}},
		{"v1", "", []string{"v1", "v2", "a0"}},
		{"", "k1", []string{"v1", "v2", "a0"}},
		{"v0", "k1", []string{"v1", "v2", "a0"}}, // the key wins
		{"v1", "gone", []string{"v0", "v2", "a0"}},
		{"v3", "", []string{"v0", "v2", "a0"}},
		{"a0", "", []string{"v0", "v2", "a0"}},
	} {
		if got := ids(ManifestTracksFor(c.id, c.key)(in)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ManifestTracksFor(%q, %q) = %v, want %v", c.id, c.key, got, c.want)
		}
	}
	if got := ids(in); !reflect.DeepEqual(got, []string{"v0", "v1", "v2", "v3", "a0"}) {
		t.Errorf("ManifestTracksFor changed its input: %v", got)
	}
}

// ManifestTracks keeps one track per quality before anything is planned, so of two 720p
// variants the one of the higher bandwidth (AC-3 in MPEG-TS, which mediajob.ProbeMP4
// then refuses) is offered; offered again without it, it offers the AAC variant: the
// second round of ProbeMP4 (docs/VIDEO-DETECT-SPEC.md §3.3).
func TestManifestTracksSecondRound(t *testing.T) {
	ac3 := media.Track{ID: "v0", Kind: media.KindMuxed, Height: 720, Bandwidth: 3_000_000, Codecs: "avc1.64001f,ac-3"}
	aac := media.Track{ID: "v1", Kind: media.KindMuxed, Height: 720, Bandwidth: 2_000_000, Codecs: "avc1.64001f,mp4a.40.2"}
	low := media.Track{ID: "v2", Kind: media.KindMuxed, Height: 360, Bandwidth: 800_000, Codecs: "avc1.4d401e,mp4a.40.2"}
	ids := func(ts []media.Track) []string {
		var out []string
		for _, t := range ts {
			out = append(out, t.ID)
		}
		return out
	}
	if got := ids(ManifestTracks([]media.Track{ac3, aac, low})); !reflect.DeepEqual(got, []string{"v0", "v2"}) {
		t.Errorf("first round = %v", got)
	}
	if got := ids(ManifestTracks([]media.Track{aac, low})); !reflect.DeepEqual(got, []string{"v1", "v2"}) {
		t.Errorf("second round = %v", got)
	}
}
