package media

import (
	"errors"
	"testing"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
)

// Audio pages (SITE-MEDIA-SPEC §3.13).
func TestAudioPage(t *testing.T) {
	site := downexv1.MediaOrigin_MEDIA_ORIGIN_SITE
	for _, c := range []struct {
		info        *downexv1.MediaInfo
		audio       bool
		ext, label  string
		hint, items string
	}{
		{&downexv1.MediaInfo{Origin: site, Container: "m4a", Site: "SoundCloud", Title: "Track"}, true, ".m4a", "SoundCloud audio",
			"This page has no video: its audio is saved as .m4a.", "Track.m4a"},
		{&downexv1.MediaInfo{Origin: site, Container: "MP3"}, true, ".mp3", "Audio", "This page has no video: its audio is saved as .mp3.", "video.mp3"},
		{&downexv1.MediaInfo{Origin: site, Container: "mp4", Site: "YouTube", Title: "Talk"}, false, ".mp4", "YouTube video",
			"This page has no video: its audio is saved as .mp4.", "Talk.mp4"},
		// A stream's container never makes an audio page.
		{&downexv1.MediaInfo{Container: "m4a"}, false, ".mp4", "Video", "This page has no video: its audio is saved as .mp4.", "video.mp4"},
	} {
		if got := AudioPage(c.info); got != c.audio {
			t.Errorf("AudioPage(%v) = %v", c.info, got)
		}
		if got := SiteExtension(c.info); got != c.ext {
			t.Errorf("SiteExtension(%v) = %q", c.info, got)
		}
		if got := SiteVideoLabel(c.info); got != c.label {
			t.Errorf("SiteVideoLabel(%v) = %q", c.info, got)
		}
		if got := AudioPageHint(c.info); got != c.hint {
			t.Errorf("AudioPageHint(%v) = %q", c.info, got)
		}
		if got := ItemName(c.info); got != c.items {
			t.Errorf("ItemName(%v) = %q", c.info, got)
		}
	}
	if CodecName("mp3") != "MP3" {
		t.Errorf("CodecName(mp3) = %q", CodecName("mp3"))
	}
	refused := func(msg string) error {
		return connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
	}
	for msg, want := range map[string]string{
		"media_unsupported: media: unsupported operation: the audio is Opus or Vorbis, which Downex cannot save without converting it": SiteTexts["audio_opus"],
		"media_unsupported: media: unsupported operation: no AAC or MP3 audio on this page":                                            SiteTexts["audio_none"],
		"media_unsupported: media: unsupported operation: no MP4 video on this page":                                                   SiteTexts["media_unsupported"],
	} {
		if got := SiteErrorText(refused(msg)); got != want {
			t.Errorf("SiteErrorText(%q) = %q, want %q", msg, got, want)
		}
	}
}

// An audio page's refusals name their cause, and its items are tracks (SITE-MEDIA-SPEC §3.12,
// §3.13).
func TestAudioPageTexts(t *testing.T) {
	refused := func(msg string) error {
		return connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
	}
	for msg, want := range map[string]string{
		"media_unsupported: media: unsupported operation: the audio is a raw AAC stream, which Downex cannot save without converting it": SiteTexts["audio_raw"],
		"media_unsupported: media: unsupported operation: only a preview of this track is public, not the whole track":                   SiteTexts["audio_preview"],
		"media_unsupported: media: unsupported operation: the page is a playlist; open a single track of it":                             "Open a single track of this playlist.",
		"media_unsupported: media: unsupported operation: the page is a playlist; open a single video of it":                             "Open a single video of this playlist.",
	} {
		if got := SiteErrorText(refused(msg)); got != want {
			t.Errorf("SiteErrorText(%q) = %q, want %q", msg, got, want)
		}
	}
	site := downexv1.MediaOrigin_MEDIA_ORIGIN_SITE
	items := []*downexv1.MediaItem{{Id: "a", Title: "Turning"}, {Id: "b", Title: "Other", DurationMs: 61_000}, {Id: "c"}}
	audio := &downexv1.MediaInfo{Origin: site, Container: "mp3", Items: items, EntryId: "a", ItemsLeftOut: 3}
	video := &downexv1.MediaInfo{Origin: site, Container: "mp4", Items: items, EntryId: "a"}
	for _, c := range []struct {
		info               *downexv1.MediaInfo
		noun, count, label string
		fileType           string
	}{
		{audio, "Track", "Track 1 of 3", "Track 1", "MP3"},
		{video, "Video", "Video 1 of 3", "Video 1", "MP4"},
	} {
		if got := ItemNoun(c.info); got != c.noun {
			t.Errorf("ItemNoun = %q, want %q", got, c.noun)
		}
		if got := ItemsCountText(c.info); got != c.count {
			t.Errorf("ItemsCountText = %q, want %q", got, c.count)
		}
		if got := ItemOptions(c.info)[0].Label; got != c.label {
			t.Errorf("ItemOptions label = %q, want %q", got, c.label)
		}
		if got := SiteFileType(c.info); got != c.fileType {
			t.Errorf("SiteFileType = %q, want %q", got, c.fileType)
		}
	}
	none := &downexv1.MediaInfo{Origin: site, Container: "m4a", Items: items}
	if got := ItemsCountText(none); got != "3 tracks" {
		t.Errorf("ItemsCountText without a chosen item = %q", got)
	}
	for _, c := range []struct {
		n     int32
		audio bool
		want  string
	}{
		{1, true, "1 other item not listed: a link or audio Downex cannot save."},
		{3, true, "3 other items not listed: links or audio Downex cannot save."},
		{3, false, "3 other items not listed: photos, links or videos Downex cannot save."},
	} {
		if got := ItemsLeftOutText(c.n, c.audio); got != c.want {
			t.Errorf("ItemsLeftOutText(%d, %v) = %q", c.n, c.audio, got)
		}
	}
}
