package sitemedia

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"downex/internal/media"
)

// Audio pages (docs/SITE-MEDIA-SPEC.md §3.13): goldens trimmed from real yt-dlp output of
// public SoundCloud, Bandcamp and Mixcloud tracks (2026-11-18), with example hosts.

func TestAudioPage(t *testing.T) {
	for name, c := range map[string]struct {
		data []byte
		want bool
	}{
		"soundcloud":  {fixture(t, "soundcloud.json"), true},
		"bandcamp":    {fixture(t, "bandcamp.json"), true},
		"mixcloud":    {fixture(t, "mixcloud.json"), true},
		"youtube":     {fixture(t, "youtube.json"), false},
		"silent reel": {fixture(t, "silent.json"), false},
		"no vcodec, audio type": {page(`{"format_id": "a", "protocol": "https", "url": "https://cdn.example.com/a.mp3", "ext": "mp3"}`),
			true},
		// A file type of video, or no word on the picture, makes a video page.
		"no vcodec, video type": {page(`{"format_id": "a", "protocol": "https", "url": "https://cdn.example.com/a", "ext": "mp4", "acodec": "mp4a.40.2"}`),
			false},
		"one entry says nothing": {page(`{"format_id": "a", "protocol": "https", "url": "https://cdn.example.com/a", "ext": "m4a", "vcodec": "none", "acodec": "mp4a.40.2"}`,
			`{"format_id": "b", "protocol": "https", "url": "https://cdn.example.com/b"}`), false},
		// A dropped video format (DRM) still makes a video page.
		"a dropped video": {page(`{"format_id": "a", "protocol": "https", "url": "https://cdn.example.com/a", "ext": "m4a", "vcodec": "none", "acodec": "mp4a.40.2"}`,
			`{"format_id": "v", "protocol": "https", "url": "https://cdn.example.com/v", "ext": "mp4", "vcodec": "avc1", "acodec": "none", "has_drm": true}`), false},
		"no sound at all": {page(`{"format_id": "a", "protocol": "https", "url": "https://cdn.example.com/a", "ext": "m4a", "vcodec": "none", "acodec": "none"}`), false},
		"no formats":      {[]byte(`{"id": "x", "extractor_key": "Generic"}`), false},
	} {
		info, err := Parse(c.data, testNow)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.AudioOnly != c.want {
			t.Errorf("%s: AudioOnly = %v, want %v", name, info.AudioOnly, c.want)
		}
	}
}

func TestOffersAudio(t *testing.T) {
	aac := func(id string, abr int, extra string) string {
		return `{"format_id": "` + id + `", "protocol": "https", "url": "https://cdn.example.com/` + id + `", "ext": "m4a", "vcodec": "none", "acodec": "mp4a.40.2", "abr": ` +
			strconv.Itoa(abr) + extra + `}`
	}
	mp3 := func(id string, abr int) string {
		return `{"format_id": "` + id + `", "protocol": "https", "url": "https://cdn.example.com/` + id + `.mp3", "ext": "mp3", "vcodec": "none", "acodec": "mp3", "abr": ` + strconv.Itoa(abr) + `}`
	}
	for name, c := range map[string]struct {
		data              []byte
		format, container string
		tracks            []string
		manifest          string
	}{
		"bandcamp: its MP3":                      {fixture(t, "bandcamp.json"), FormatHTTP, ContainerMP3, []string{"mp3-128"}, ""},
		"soundcloud: its AAC stream":             {fixture(t, "soundcloud.json"), FormatHLS, ContainerM4A, nil, "https://playback.media.example/aac_96k/0001/playlist.m3u8?expires=1794979813&Signature=sig"},
		"mixcloud: the 192 kbit/s stream":        {fixture(t, "mixcloud.json"), FormatHLS, ContainerM4A, nil, "https://aod.media.example/secure/hls/0003-192K.m4a/index-a1.m3u8"},
		"direct AAC beats MP3 and an AAC stream": {fixture(t, "audio.json"), FormatHTTP, ContainerM4A, []string{"http_aac_160"}, ""},
		"a better MP3 wins":                      {page(aac("a", 128, ""), mp3("m", 320)), FormatHTTP, ContainerMP3, []string{"m"}, ""},
		"a tie goes to AAC":                      {page(mp3("m", 128), aac("a", 128, "")), FormatHTTP, ContainerM4A, []string{"a"}, ""},
		"AAC tracks best first":                  {page(aac("a64", 64, ""), aac("a160", 160, ""), aac("a96", 96, `, "container": "m4a_dash"`)), FormatHTTP, ContainerM4A, []string{"a160", "a96", "a64"}, ""},
		"opus and AAC: the AAC": {page(`{"format_id": "o", "protocol": "https", "url": "https://cdn.example.com/o", "ext": "webm", "vcodec": "none", "acodec": "opus", "abr": 160}`, aac("a", 128, "")),
			FormatHTTP, ContainerM4A, []string{"a"}, ""},
	} {
		info, err := Parse(c.data, testNow)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		off, err := Offers(info, "example.com")
		if err != nil {
			t.Fatalf("%s: Offers: %v", name, err)
		}
		_, audios := trackIDs(off.Tracks)
		if !off.Audio || off.Format != c.format || off.Container != c.container || !slices.Equal(audios, c.tracks) ||
			off.ManifestURL != c.manifest || off.DefaultVideo != "" || len(c.tracks) > 0 && off.DefaultAudio != c.tracks[0] {
			t.Errorf("%s: offer %+v", name, off)
		}
	}
}

func TestOffersAudioRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		data []byte
		want error
		text string
	}{
		"opus only": {page(`{"format_id": "o", "protocol": "https", "url": "https://cdn.example.com/o", "ext": "opus", "vcodec": "none", "acodec": "opus"}`),
			media.ErrUnsupported, "Opus or Vorbis"},
		"vorbis in ogg": {page(`{"format_id": "o", "protocol": "https", "url": "https://cdn.example.com/o", "ext": "ogg", "vcodec": "none"}`),
			media.ErrUnsupported, "Opus or Vorbis"},
		"flac only": {page(`{"format_id": "f", "protocol": "https", "url": "https://cdn.example.com/f", "ext": "flac", "vcodec": "none", "acodec": "flac"}`),
			media.ErrUnsupported, "no AAC or MP3 audio"},
		"protected AAC": {page(`{"format_id": "a", "protocol": "https", "url": "https://cdn.example.com/a", "ext": "m4a", "vcodec": "none", "acodec": "mp4a.40.2", "has_drm": true}`),
			media.ErrEncrypted, ""},
		"members only": {[]byte(`{"id": "x", "extractor_key": "Generic", "availability": "subscriber_only", "formats": [{"format_id": "o", "protocol": "https", "url": "https://cdn.example.com/o", "ext": "opus", "vcodec": "none", "acodec": "opus"}]}`),
			ErrAuthRequired, ""},
	} {
		info, err := Parse(c.data, testNow)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		off, err := Offers(info, "example.com")
		if !errors.Is(err, c.want) || off != nil || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%s: Offers = %+v, %v; want %v %q", name, off, err, c.want, c.text)
		}
	}
}

func TestSelectAudio(t *testing.T) {
	for _, c := range []struct {
		fixture, host, id, output, container string
	}{
		{"bandcamp.json", "example.com", "mp3-128", OutputCopy, ContainerMP3},
		{"audio.json", "soundcloud.com", "http_aac_160", OutputRemux, ContainerM4A}, // m4a_dash: joined
	} {
		info, off := offerFixture(t, c.fixture, c.host)
		for _, args := range [][2]string{{"", ""}, {c.id, ""}, {"", off.Tracks[0].Key}} {
			pick, err := Select(info, off, "", args[0], "", args[1])
			if err != nil {
				t.Fatalf("%s: Select(audio %q, key %q): %v", c.fixture, args[0], args[1], err)
			}
			if pick.Output != c.output || pick.Container != c.container || len(pick.Tracks) != 1 || pick.Tracks[0].ID != c.id ||
				pick.Tracks[0].Kind != media.KindAudio || pick.Tracks[0].URL == "" {
				t.Errorf("%s: pick %+v", c.fixture, pick)
			}
		}
		if got := off.Listed(&Pick{Tracks: []media.RangedTrack{{ID: c.id}}}); !slices.EqualFunc(got, off.Tracks, func(a, b media.Track) bool { return a.ID == b.ID }) {
			t.Errorf("%s: Listed = %v", c.fixture, got)
		}
		// An audio page has no video: the audio named as a video is the wrong kind, and a
		// key the page no longer has is gone.
		if _, err := Select(info, off, c.id, "", "", ""); !errors.Is(err, media.ErrMalformed) {
			t.Errorf("%s: Select(video %q) = %v", c.fixture, c.id, err)
		}
		if _, err := Select(info, off, "", "", "", "s1-gone"); !errors.Is(err, media.ErrNotFound) {
			t.Errorf("%s: Select(gone key) = %v", c.fixture, err)
		}
	}
	// A direct file without a container (Mixcloud's, when it is the best): copied as it is.
	info, err := Parse(page(`{"format_id": "http", "protocol": "https", "url": "https://cdn.example.com/a.m4a", "ext": "m4a", "vcodec": "none"}`), testNow)
	if err != nil {
		t.Fatal(err)
	}
	off, err := Offers(info, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if pick, err := Select(info, off, "", "", "", ""); err != nil || pick.Output != OutputCopy || pick.Container != ContainerM4A ||
		off.Tracks[0].Codecs != "" || off.Tracks[0].Ext != "m4a" {
		t.Errorf("pick %+v, %v of %+v", pick, err, off)
	}
}

// A stream's media playlist (a master of audio variants lists no video) is described by
// the format it was chosen from.
func TestOffersAudioStream(t *testing.T) {
	_, off := offerFixture(t, "mixcloud.json", "example.com")
	if off.ManifestURL != "https://aod.media.example/secure/hls/0003-192K.m4a/index-a1.m3u8" ||
		off.Headers.Get("Cookie") != "mx_t=example" {
		t.Fatalf("offer %+v", off)
	}
	got := off.DescribeAudio([]media.Track{{ID: "v0", Kind: media.KindMuxed, Size: 87_000_000, SizeApprox: true}})
	want := media.Track{ID: "v0", Kind: media.KindMuxed, Codecs: "mp4a.40.2", Bandwidth: 192_001, Ext: "m4a", Size: 87_000_000, SizeApprox: true}
	if len(got) != 1 || got[0] != want {
		t.Errorf("DescribeAudio = %+v", got)
	}
	// Tracks that say what they are, or several, stay as they are.
	named := []media.Track{{ID: "v0", Codecs: "mp4a.40.5", Bandwidth: 48_000, Ext: "mp4"}}
	if got := off.DescribeAudio(named); got[0] != named[0] {
		t.Errorf("DescribeAudio(named) = %+v", got)
	}
	if two := []media.Track{{ID: "a"}, {ID: "b"}}; len(off.DescribeAudio(two)) != 2 || off.DescribeAudio(two)[0].Codecs != "" {
		t.Error("DescribeAudio changed a list of two")
	}
	_, bc := offerFixture(t, "bandcamp.json", "example.com")
	if one := []media.Track{{ID: "x"}}; bc.DescribeAudio(one)[0] != one[0] {
		t.Error("DescribeAudio of a direct offer")
	}
}

func TestManifestAudioTracks(t *testing.T) {
	in := []media.Track{
		{ID: "v0", Kind: media.KindMuxed},                                        // a media playlist's one track
		{ID: "a1", Kind: media.KindAudio, Codecs: "mp4a.40.2", Bandwidth: 128e3}, // an AAC rendition
		{ID: "v1", Kind: media.KindVideo, Codecs: "avc1.64001f", Width: 1280, Height: 720},
		{ID: "v2", Kind: media.KindMuxed, Codecs: "mp4a.40.2", Height: 360},
		{ID: "a2", Kind: media.KindAudio, Codecs: "opus"},
		{ID: "v3", Kind: media.KindMuxed, Codecs: "avc1.4d401f,mp4a.40.2"},
		{ID: "a3", Kind: media.KindAudio, Codecs: "ec-3", Bandwidth: 256e3},
	}
	got := ManifestAudioTracks(in)
	var ids []string
	for _, tr := range got {
		ids = append(ids, tr.ID)
	}
	if want := []string{"a3", "a1", "v0"}; !slices.Equal(ids, want) {
		t.Errorf("ManifestAudioTracks = %v, want %v", ids, want)
	}
	if in[0].ID != "v0" || in[1].ID != "a1" {
		t.Error("the input was changed")
	}
}
