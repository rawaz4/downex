package sitemedia

import (
	"strings"
	"testing"

	"downex/internal/media"
)

func TestCodecName(t *testing.T) {
	for codec, want := range map[string]string{
		"avc1.640028": "H.264", "avc3.4d401f": "H.264", "AVC1": "H.264",
		"av01.0.08M.08": "AV1", "hvc1.1.6.L120.90": "HEVC", "hev1.2.4": "HEVC",
		"vp09.00.40.08": "VP9", "vp9": "VP9", "mp4a.40.2": "AAC", "opus": "Opus",
		"ac-3": "AC-3", "ec-3": "E-AC-3", " mp4a.40.5": "AAC",
		"": "", "none": "", "h264": "", "mp3": "",
	} {
		if got := CodecName(codec); got != want {
			t.Errorf("CodecName(%q) = %q, want %q", codec, got, want)
		}
	}
}

// Display names: the extractor keys whose own spelling is not the site's name are
// renamed (docs/VIDEO-DETECT-SPEC.md §3.8); any other key is shown as it is.
func TestSiteName(t *testing.T) {
	for key, want := range map[string]string{
		"Youtube": "YouTube", "Twitter": "X", "TwitchVod": "Twitch", "TwitchClips": "Twitch", "FacebookReel": "Facebook",
		"Vimeo": "Vimeo", "Facebook": "Facebook", "Instagram": "Instagram", "TikTok": "TikTok", "Dailymotion": "Dailymotion",
		"Reddit": "Reddit", "Streamable": "Streamable", "YoutubeTab": "YoutubeTab", "Generic": "Generic",
		"youtube": "youtube", "twitter": "twitter", "": "",
	} {
		if got := SiteName(key); got != want {
			t.Errorf("SiteName(%q) = %q, want %q", key, got, want)
		}
	}
}

// A video's quality class (docs/SITE-MEDIA-SPEC.md amendment 2026-10-23): YouTube's own
// label in format_note when it is a ladder step, else the picture's class; other sites'
// notes are not read.
func TestFormatClass(t *testing.T) {
	for _, c := range []struct {
		note          string
		width, height int
		youtube       bool
		want          int
	}{
		{"2160p", 3840, 1634, true, 2160},
		{"", 3840, 1634, true, 2160},
		{"1634p", 3840, 1634, true, 2160}, // not a ladder label: the picture
		{"1440p60 HDR", 2560, 1090, true, 1440},
		{"1080p Premium", 1920, 1080, true, 1080},
		{"720p", 1920, 1080, true, 720}, // YouTube's label wins over the picture
		{"720p", 1920, 1080, false, 1080},
		{"DASH video", 854, 364, true, 480},
		{"144p", 0, 0, true, 144},
		{"medium", 0, 0, true, 0},
		{"", 1080, 1920, false, 1080},
	} {
		f := &Format{Note: c.note, Width: c.width, Height: c.height}
		if got := formatClass(f, c.youtube); got != c.want {
			t.Errorf("formatClass(%q, %dx%d, youtube %v) = %d, want %d", c.note, c.width, c.height, c.youtube, got, c.want)
		}
	}
}

func TestQualityLabel(t *testing.T) {
	video := &media.Track{Kind: media.KindVideo, Name: "1080p (Full HD) 60fps", Codecs: "avc1.64002a"}
	audio := &media.Track{Kind: media.KindAudio, Name: "medium", Codecs: "mp4a.40.2"}
	muxed := &media.Track{Kind: media.KindMuxed, Name: "360p", Codecs: "avc1.42001E, mp4a.40.2"}
	unnamed := &media.Track{Kind: media.KindMuxed, Name: "1080p (Full HD)"}
	for _, c := range []struct {
		video, audio *media.Track
		want         string
	}{
		{video, audio, "1080p (Full HD) 60fps H.264 + AAC"},
		{muxed, nil, "360p H.264 + AAC"},
		{unnamed, nil, "1080p (Full HD)"},
		{nil, audio, "AAC"},
		{nil, nil, ""},
	} {
		if got := QualityLabel(c.video, c.audio); got != c.want {
			t.Errorf("QualityLabel = %q, want %q", got, c.want)
		}
	}
}

// Titles and notes lose bidi embeddings, overrides and isolates and control
// characters, and are cut to their length in characters.
func TestCleanText(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"  plain  text ", 10, "plain text"},
		{"a‮b⁦c⁩d", 10, "abcd"},
		{"rtl ‏ mark", 20, "rtl ‏ mark"},
		{"line\nbreak\ttab\x7f", 50, "line break tab"},
		{strings.Repeat("é", 10), 4, "éééé"},
		{"\xff\xfe", 10, ""},
	} {
		if got := cleanText(c.in, c.max); got != c.want {
			t.Errorf("cleanText(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}
