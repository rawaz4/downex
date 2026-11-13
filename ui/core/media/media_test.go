package media

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format"
)

// The cases of ui/frontend/src/lib/media.test.ts. Its cases of display.ts (statusText,
// progressSummary, rowDescription, mediaBadge) belong to the display port.

const MiB = 1 << 20

func init() {
	// Intl.DisplayNames(['en']) for the tags the fixtures use.
	SetLanguageNamer(func(tag string) (string, bool) {
		name, ok := map[string]string{"en": "English", "de": "German"}[tag]
		return name, ok
	})
}

func track(id, kind string, edit ...func(*downexv1.MediaTrack)) *downexv1.MediaTrack {
	t := &downexv1.MediaTrack{Id: id, Kind: kind}
	for _, e := range edit {
		e(t)
	}
	return t
}

func video(w, h int32, fps float64, bw int64, codecs string) func(*downexv1.MediaTrack) {
	return func(t *downexv1.MediaTrack) {
		t.Width, t.Height, t.FrameRate, t.Bandwidth, t.Codecs = w, h, fps, bw, codecs
	}
}

func audio(bw int64, codecs, lang, name string) func(*downexv1.MediaTrack) {
	return func(t *downexv1.MediaTrack) { t.Bandwidth, t.Codecs, t.Language, t.Name = bw, codecs, lang, name }
}

func named(name string) func(*downexv1.MediaTrack) {
	return func(t *downexv1.MediaTrack) { t.Name = name }
}
func sized(size int64, approx bool) func(*downexv1.MediaTrack) {
	return func(t *downexv1.MediaTrack) { t.Size, t.SizeApprox = size, approx }
}

// mediaInfo is testing.ts makeMediaInfo.
func mediaInfo() *downexv1.MediaInfo {
	return &downexv1.MediaInfo{
		ManifestUrl: "https://media.example.org/talk/master.m3u8",
		Format:      "hls",
		Tracks: []*downexv1.MediaTrack{
			track("v0", "video", video(640, 360, 25, 800_000, "avc1.4d401e")),
			track("v1", "video", video(1920, 1080, 29.97, 5_000_000, "avc1.640028")),
			track("v2", "muxed", video(960, 540, 0, 1_200_000, "avc1.4d401f,mp4a.40.2")),
			track("r0", "audio", audio(128_000, "mp4a.40.2", "en", "English")),
			track("r1", "audio", audio(96_000, "mp4a.40.2", "de", "Deutsch")),
		},
		SelectedVideo:  "v1",
		SelectedAudio:  "r0",
		DurationMs:     600_000,
		FragmentsTotal: 200,
		Container:      "mp4",
		Title:          "Keynote",
		Origin:         downexv1.MediaOrigin_MEDIA_ORIGIN_MANIFEST,
	}
}

// siteInfo is testing.ts makeSiteMediaInfo.
func siteInfo() *downexv1.MediaInfo {
	m := mediaInfo()
	m.ManifestUrl, m.Format, m.FragmentsTotal, m.Title = "", "http", 0, "A talk"
	m.Origin, m.Site, m.PageUrl = downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, "YouTube", "https://www.youtube.com/watch?v=aqz-KE-bpKQ"
	m.SelectedVideo, m.SelectedAudio = "299", "140"
	m.Tracks = []*downexv1.MediaTrack{
		track("299", "video", video(1920, 1080, 60, 6_000_000, "avc1.64002a"), named("1080p (Full HD) 60fps"), sized(450*MiB, false)),
		track("136", "video", video(1280, 720, 30, 1_500_000, "avc1.4d401f"), named("720p (HD)"), sized(110*MiB, false)),
		track("397", "video", video(854, 480, 30, 2_000_000, "av01.0.04M.08"), named("480p"), sized(150*MiB, true)),
		track("18", "muxed", video(640, 360, 30, 600_000, "avc1.42001E, mp4a.40.2"), named("360p")),
		track("140", "audio", audio(129_000, "mp4a.40.2", "en", "English original"), sized(10*MiB, false)),
		track("140-1", "audio", audio(129_000, "mp4a.40.2", "de", "Deutsch"), sized(10*MiB, false)),
	}
	return m
}

func with(info *downexv1.MediaInfo, edit func(*downexv1.MediaInfo)) *downexv1.MediaInfo {
	c := proto.CloneOf(info)
	edit(c)
	return c
}

func byID(info *downexv1.MediaInfo, id string) *downexv1.MediaTrack { return TrackByID(info, id) }

func trackWith(t *downexv1.MediaTrack, edit func(*downexv1.MediaTrack)) *downexv1.MediaTrack {
	c := proto.CloneOf(t)
	edit(c)
	return c
}

func ids(ts []*downexv1.MediaTrack) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t.GetId())
	}
	return out
}

func eq(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

func TestTrackOrderAndLabels(t *testing.T) {
	info := mediaInfo()
	t.Run("sorts video by picture, then bandwidth; audio by language, then bandwidth", func(t *testing.T) {
		if got := ids(VideoTracks(info.Tracks, false, "")); !slices.Equal(got, []string{"v1", "v2", "v0"}) {
			t.Errorf("video = %v", got)
		}
		tracks := append(slices.Clone(info.Tracks),
			trackWith(byID(info, "r0"), func(x *downexv1.MediaTrack) { x.Id, x.Bandwidth, x.Name = "r2", 256_000, "English (high)" }),
			trackWith(byID(info, "r0"), func(x *downexv1.MediaTrack) { x.Id, x.Language, x.Name = "r3", "", "Unlabelled" }))
		// English before German (by the language's name), higher bandwidth first; undeclared last
		if got := ids(AudioTracks(tracks, false)); !slices.Equal(got, []string{"r2", "r0", "r1", "r3"}) {
			t.Errorf("audio = %v", got)
		}
	})
	t.Run("names video by its standard quality, codecs and bitrate, the picture apart", func(t *testing.T) {
		eq(t, "v1", VideoTrackLabel(byID(info, "v1"), false), "1080p (Full HD) · H.264 · 5.0 Mbit/s")
		// 960×540 is nearer to 480 than to 720; a muxed track names its sound's codec too.
		eq(t, "v2", VideoTrackLabel(byID(info, "v2"), false), "480p · H.264 + AAC · 1.2 Mbit/s")
		bare := func(id string) *downexv1.MediaTrack {
			return trackWith(byID(info, id), func(x *downexv1.MediaTrack) { x.Width, x.Height, x.FrameRate, x.Bandwidth, x.Codecs = 0, 0, 0, 0, "" })
		}
		eq(t, "bare video", VideoTrackLabel(bare("v0"), false), "Video")
		eq(t, "bare muxed", VideoTrackLabel(bare("v2"), false), "Video and audio")
		if got := VideoTrackLabel(trackWith(byID(info, "v0"), func(x *downexv1.MediaTrack) { x.Width, x.Height = 0, 720 }), false); !strings.Contains(got, "720p (HD)") {
			t.Errorf("height only = %q", got)
		}
		// A manifest's own label is replaced by the standard name of its picture.
		eq(t, "manifest label", VideoTrackLabel(trackWith(byID(info, "v1"), func(x *downexv1.MediaTrack) { x.Name = "1920x1080" }), false), "1080p (Full HD) · H.264 · 5.0 Mbit/s")
		eq(t, "film", VideoTrackLabel(trackWith(byID(info, "v1"), func(x *downexv1.MediaTrack) { x.Width, x.Height, x.FrameRate, x.Name = 3840, 1634, 60, "" }), false), "4K (2160p) 60fps · H.264 · 5.0 Mbit/s")
		eq(t, "picture", VideoPictureText(byID(info, "v1"), false), "1920×1080 · 29.97 fps")
		eq(t, "site picture", VideoPictureText(byID(info, "v1"), true), "1920×1080")
		eq(t, "no rate", VideoPictureText(trackWith(byID(info, "v1"), func(x *downexv1.MediaTrack) { x.FrameRate = 0 }), false), "1920×1080")
		eq(t, "no width", VideoPictureText(trackWith(byID(info, "v1"), func(x *downexv1.MediaTrack) { x.Width = 0 }), false), "")
	})
	t.Run("lists one manifest quality per class: video only, SDR, frame rate, H.264, bitrate", func(t *testing.T) {
		tracks := []*downexv1.MediaTrack{
			track("hevc", "video", video(1920, 1080, 30, 4_000_000, "hvc1.1.6.L120.90")),
			track("avc", "video", video(1920, 1080, 30, 6_000_000, "avc1.640028")),
			track("wide", "video", video(1920, 800, 30, 3_000_000, "avc1.640028")),
			track("mux", "muxed", video(1280, 720, 30, 3_000_000, "avc1.4d401f,mp4a.40.2")),
			track("hd", "video", video(1280, 720, 30, 2_500_000, "avc1.4d401f")),
			track("a", "audio", audio(128_000, "mp4a.40.2", "", "")),
		}
		for _, c := range []struct {
			keep string
			want []string
		}{{"", []string{"avc", "hd"}}, {"wide", []string{"wide", "hd"}}, {"mux", []string{"avc", "mux"}}} {
			// The daemon's pick stays the quality of its class.
			if got := ids(VideoTracks(tracks, false, c.keep)); !slices.Equal(got, c.want) {
				t.Errorf("keep %q = %v", c.keep, got)
			}
		}
	})
	t.Run("describes audio by name and language and bitrate", func(t *testing.T) {
		r0, r1 := byID(info, "r0"), byID(info, "r1")
		eq(t, "r0", AudioTrackLabel(r0, false), "English · 128 kbit/s · mp4a.40.2")
		eq(t, "commentary", AudioTrackLabel(trackWith(r0, func(x *downexv1.MediaTrack) { x.Name = "Commentary" }), false), "Commentary (English) · 128 kbit/s · mp4a.40.2")
		eq(t, "r1", AudioTrackLabel(trackWith(r1, func(x *downexv1.MediaTrack) { x.Name = "" }), false), "German · 96 kbit/s · mp4a.40.2")
		eq(t, "bare", AudioTrackLabel(trackWith(r1, func(x *downexv1.MediaTrack) { x.Name, x.Language, x.Bandwidth, x.Codecs = "", "", 0, "" }), false), "Audio")
		// a page's track: yt-dlp's quality words in the format note are not a name
		eq(t, "medium", AudioTrackTitle(&downexv1.MediaTrack{Name: "medium"}, true), "Audio")
		eq(t, "drc", AudioTrackTitle(&downexv1.MediaTrack{Name: "low, DRC", Language: "en"}, true), "English · DRC")
		eq(t, "original", AudioTrackTitle(&downexv1.MediaTrack{Name: "English original (default), medium", Language: "en"}, true), "English original (default)")
		eq(t, "site label", AudioTrackLabel(trackWith(r0, func(x *downexv1.MediaTrack) {
			x.Name, x.Language, x.Codecs, x.Bandwidth = "medium", "", "mp4a.40.2", 129_000
		}), true), "Audio · AAC · 129 kbit/s")
		// a manifest's rendition keeps its own name
		eq(t, "manifest medium", AudioTrackTitle(&downexv1.MediaTrack{Name: "medium"}, false), "medium")
	})
	t.Run("formats bitrates, lengths, formats and containers", func(t *testing.T) {
		for _, c := range []struct {
			bps  float64
			want string
		}{{0, ""}, {640, "640 bit/s"}, {96_000, "96 kbit/s"}, {999_499, "999 kbit/s"}, {999_600, "1.0 Mbit/s"}, {14_250_000, "14.3 Mbit/s"}, {120_000_000, "120 Mbit/s"}} {
			eq(t, "bitrate", format.Bitrate(c.bps), c.want)
		}
		eq(t, "no length", format.Duration(0), "—")
		eq(t, "length", format.Duration(65_400), "1:05")
		eq(t, "long", format.Duration(5_400_250), "1:30:00")
		eq(t, "hls", FormatLabel("hls"), "HLS")
		eq(t, "no format", FormatLabel(""), "")
		eq(t, "ts", ContainerLabel("ts"), "MPEG-TS")
		eq(t, "mp4", ContainerLabel("mp4"), "MP4")
		eq(t, "no container", ContainerLabel(""), "")
	})
}

func TestPickingTracks(t *testing.T) {
	info := mediaInfo()
	pick := func(v, a string) TrackPick { return TrackPick{Video: v, Audio: a} }
	t.Run("starts from the daemon's pick, else the best tracks", func(t *testing.T) {
		for _, c := range []struct {
			info *downexv1.MediaInfo
			want TrackPick
		}{
			{info, pick("v1", "r0")},
			{with(info, func(m *downexv1.MediaInfo) { m.SelectedVideo, m.SelectedAudio = "", "" }), pick("v1", "r0")},
			// a pick naming a track that is not offered falls back
			{with(info, func(m *downexv1.MediaInfo) { m.SelectedVideo, m.SelectedAudio = "r1", "v0" }), pick("v1", "r0")},
			{with(info, func(m *downexv1.MediaInfo) { m.SelectedVideo, m.SelectedAudio = "v2", "r1" }), pick("v2", "")},
			{with(info, func(m *downexv1.MediaInfo) {
				m.Tracks = slices.DeleteFunc(m.Tracks, func(x *downexv1.MediaTrack) bool { return x.Kind != "audio" })
			}), pick("", "r0")},
			{nil, pick("", "")},
		} {
			if got := DefaultPick(c.info); got != c.want {
				t.Errorf("DefaultPick = %+v, want %+v", got, c.want)
			}
		}
	})
	t.Run("keeps the daemon's audio-only pick, which leaves out videos it cannot save", func(t *testing.T) {
		audioOnly := with(info, func(m *downexv1.MediaInfo) { m.SelectedVideo, m.SelectedAudio = "", "r1" })
		if got := DefaultPick(audioOnly); got != pick("", "r1") || !AudioOnlyOffered(audioOnly) {
			t.Errorf("audio only = %+v", got)
		}
		// "" asks the daemon for its own pick: "no video" is that only when its pick has none
		for _, m := range []*downexv1.MediaInfo{
			info,
			with(info, func(m *downexv1.MediaInfo) { m.SelectedVideo, m.SelectedAudio = "", "" }),
			with(info, func(m *downexv1.MediaInfo) { m.SelectedVideo, m.SelectedAudio = "", "v0" }),
		} {
			if AudioOnlyOffered(m) {
				t.Errorf("audio only offered for %q/%q", m.SelectedVideo, m.SelectedAudio)
			}
		}
		if !NeedsAudio(info, "") || PickedAudio(info, pick("", "r1")) != "r1" {
			t.Error("audio of an audio-only pick")
		}
		if got := WithVideo(audioOnly, pick("v2", "r1"), ""); got != pick("", "r1") {
			t.Errorf("with no video = %+v", got)
		}
		if got := WithVideo(audioOnly, pick("v2", ""), ""); got != pick("", "r0") {
			t.Errorf("with no video, no audio = %+v", got)
		}
		// 96 kbit/s × 600 s / 8
		if got := EstimatedBytes(info, pick("", "r1")); got != 7_200_000 {
			t.Errorf("estimate = %d", got)
		}
	})
	t.Run("needs no audio track with a muxed video, and picks one again after it", func(t *testing.T) {
		if !NeedsAudio(info, "v1") || NeedsAudio(info, "v2") {
			t.Error("NeedsAudio")
		}
		if PickedAudio(info, pick("v2", "r1")) != "" || PickedAudio(info, pick("v0", "r1")) != "r1" {
			t.Error("PickedAudio")
		}
		if got := WithVideo(info, pick("v1", "r1"), "v2"); got != pick("v2", "r1") { // kept for when the user goes back
			t.Errorf("to muxed = %+v", got)
		}
		if got := WithVideo(info, pick("v2", ""), "v0"); got != pick("v0", "r0") {
			t.Errorf("from muxed = %+v", got)
		}
		noAudio := with(info, func(m *downexv1.MediaInfo) {
			m.Tracks = slices.DeleteFunc(m.Tracks, func(x *downexv1.MediaTrack) bool { return x.Kind == "audio" })
		})
		if NeedsAudio(noAudio, "v1") {
			t.Error("needs audio without audio tracks")
		}
	})
	t.Run("estimates the size from bandwidth and length", func(t *testing.T) {
		for _, c := range []struct {
			info *downexv1.MediaInfo
			pick TrackPick
			want int64
		}{
			// (5,000,000 + 128,000) bit/s × 600 s / 8
			{info, pick("v1", "r0"), 384_600_000},
			{info, pick("v2", "r0"), 90_000_000}, // muxed: its own sound
			{with(info, func(m *downexv1.MediaInfo) { m.DurationMs = 0 }), pick("v1", "r0"), -1},
			{info, pick("", ""), -1},
		} {
			if got := EstimatedBytes(c.info, c.pick); got != c.want {
				t.Errorf("EstimatedBytes(%+v) = %d, want %d", c.pick, got, c.want)
			}
		}
	})
	t.Run("labels a download's chosen tracks", func(t *testing.T) {
		v, a := ChosenTracks(info)
		eq(t, "video", v, "1080p (Full HD) · H.264 · 5.0 Mbit/s · 1920×1080 · 29.97 fps")
		eq(t, "audio", a, AudioTrackLabel(byID(info, "r0"), false))
		_, a = ChosenTracks(with(info, func(m *downexv1.MediaInfo) { m.SelectedVideo, m.SelectedAudio = "v2", "" }))
		eq(t, "muxed audio", a, "In the video track")
	})
}

func mediaDownload(id int64, state downexv1.DownloadState, received int64, speed float64, info *downexv1.MediaInfo) *downexv1.Download {
	return &downexv1.Download{Id: id, Url: "https://media.example.org/talk/master.m3u8", Filename: "Keynote.mp4", Size: -1, Received: received, State: state, SpeedBps: speed, Category: "Video", Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, Media: info}
}

func TestProgressOfAMediaDownload(t *testing.T) {
	downloading := downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING
	fragments := func(done, total int32) *downexv1.MediaInfo {
		return with(mediaInfo(), func(m *downexv1.MediaInfo) { m.FragmentsDone, m.FragmentsTotal = done, total })
	}
	d := mediaDownload(1, downloading, 100*MiB, 10*MiB, fragments(50, 200))
	if got := format.ProgressFraction(d); got != 0.25 {
		t.Errorf("progress = %v", got)
	}
	if got := format.EtaSeconds(d); math.Abs(got-30) > 0.005 { // 400 MiB in all, 300 MiB left at 10 MiB/s
		t.Errorf("eta = %v", got)
	}
	if got := format.EtaSeconds(mediaDownload(1, downloading, 100*MiB, 10*MiB, fragments(0, 200))); !math.IsNaN(got) {
		t.Errorf("eta without fragments = %v", got)
	}
	done := proto.CloneOf(d)
	done.State = downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED
	if got := format.ProgressFraction(done); got != 1 {
		t.Errorf("completed = %v", got)
	}
	if got := format.ProgressFraction(mediaDownload(1, downloading, 100*MiB, 10*MiB, fragments(0, 0))); !math.IsNaN(got) { // size -1
		t.Errorf("no fragments = %v", got)
	}
	// a web page's video is measured by its bytes, as the progress window does
	site := &downexv1.Download{Id: 1, State: downloading, Size: 460 * MiB, Received: 115 * MiB, SpeedBps: 23 * MiB, Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA,
		Media: with(siteInfo(), func(m *downexv1.MediaInfo) { m.FragmentsTotal, m.FragmentsDone = 46, 11 })}
	if got := format.ProgressFraction(site); got != 0.25 {
		t.Errorf("site progress = %v", got)
	}
	if got := format.EtaSeconds(site); math.Abs(got-15) > 0.005 { // 345 MiB left at 23 MiB/s
		t.Errorf("site eta = %v", got)
	}
	unsized := proto.CloneOf(site)
	unsized.Size = -1
	if got := format.ProgressFraction(unsized); !math.IsNaN(got) {
		t.Errorf("unsized = %v", got)
	}
}

func TestRefusals(t *testing.T) {
	for _, c := range []struct {
		err  error
		want Refusal
	}{
		{connect.NewError(connect.CodeInvalidArgument, errors.New("encrypted: the stream is encrypted or DRM-protected")), Encrypted},
		{errors.New("invalid_argument: media: content is encrypted or DRM-protected"), Encrypted},
		{errors.New("invalid_argument: live: the presentation is live"), Live},
		{errors.New("invalid_argument: manifest_url must be http(s)"), NotRefused},
		{errors.New("unavailable: live connection lost"), NotRefused},
	} {
		if got := MediaRefusal(c.err); got != c.want {
			t.Errorf("MediaRefusal(%v) = %v", c.err, got)
		}
	}
	eq(t, "encrypted", MediaErrorText(errors.New("invalid_argument: encrypted"), false), RefusalText(Encrypted))
	eq(t, "other", MediaErrorText(errors.New("failed_precondition: manifest: 404"), false), "manifest: 404")
	for in, want := range map[string]bool{
		"https://cdn.example.com/v/master.m3u8?token=1": true,
		" https://cdn.example.com/v/Manifest.MPD ":      true,
		"https://cdn.example.com/v/video.mp4":           false,
		"https://cdn.example.com/get?file=a.m3u8":       false,
		"ftp://cdn.example.com/a.m3u8":                  false,
		"not a url":                                     false,
	} {
		if got := LooksLikeManifest(in); got != want {
			t.Errorf("LooksLikeManifest(%q) = %v", in, got)
		}
	}
}

func TestVideoPagesPicker(t *testing.T) {
	site, info := siteInfo(), mediaInfo()
	tr := func(id string) *downexv1.MediaTrack { return byID(site, id) }
	t.Run("tells a web page from a manifest", func(t *testing.T) {
		if !IsSite(site) || IsSite(info) || IsSite(nil) {
			t.Error("IsSite")
		}
		if !IsSiteDownload(&downexv1.Download{Kind: downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA, Media: site}) || IsSiteDownload(mediaDownload(2, 0, 0, 0, info)) {
			t.Error("IsSiteDownload")
		}
		eq(t, "label", SiteVideoLabel(site), "YouTube video")
		eq(t, "no name", SiteVideoLabel(&downexv1.MediaInfo{Site: " "}), "Video")
	})
	t.Run("names the codecs the same way in every UI", func(t *testing.T) {
		for in, want := range map[string]string{
			"avc1.640028": "H.264", "avc3.4d401f": "H.264", "av01.0.08M.08": "AV1", "hvc1.2.4.L153": "HEVC", "hev1.1.6.L93": "HEVC",
			"vp09.00.40.08": "VP9", "mp4a.40.2": "AAC", "opus": "Opus", "ac-3": "AC-3", "ec-3": "E-AC-3", "flac": "flac",
		} {
			eq(t, "CodecName("+in+")", CodecName(in), want)
		}
		eq(t, "names", CodecNames("avc1.42001E, mp4a.40.2"), "H.264 + AAC")
		eq(t, "none", CodecNames(""), "")
	})
	t.Run("keeps downexd's one quality per class, never offering audio only", func(t *testing.T) {
		if got := ids(VideoTracks(site.Tracks, true, "")); !slices.Equal(got, []string{"299", "136", "397", "18"}) {
			t.Errorf("site = %v", got)
		}
		// the order downexd chose is not re-sorted by bitrate or size
		if got := ids(VideoTracks(site.Tracks, false, "")); !slices.Equal(got, []string{"299", "136", "397", "18"}) {
			t.Errorf("as a manifest = %v", got)
		}
		if AudioOnlyOffered(with(site, func(m *downexv1.MediaInfo) { m.SelectedVideo, m.SelectedAudio = "", "140" })) {
			t.Error("audio only offered for a page")
		}
		if got := DefaultPick(site); got != (TrackPick{"299", "140"}) {
			t.Errorf("default = %+v", got)
		}
		// no pick from downexd: the first quality and the first audio (its default order)
		if got := DefaultPick(with(site, func(m *downexv1.MediaInfo) { m.SelectedVideo, m.SelectedAudio = "", "" })); got != (TrackPick{"299", "140"}) {
			t.Errorf("no pick = %+v", got)
		}
		if got := WithVideo(site, TrackPick{"18", ""}, "136"); got != (TrackPick{"136", "140"}) {
			t.Errorf("with 136 = %+v", got)
		}
	})
	t.Run("labels each quality with its name, codec and the site's size", func(t *testing.T) {
		eq(t, "299", VideoTrackLabel(tr("299"), true), "1080p (Full HD) 60fps · H.264 · 472 MB")
		eq(t, "397", VideoTrackLabel(tr("397"), true), "480p · AV1 · ~157 MB")
		eq(t, "18", VideoTrackLabel(tr("18"), true), "360p · H.264 + AAC · 600 kbit/s") // no size: its bitrate
		eq(t, "unnamed", VideoTrackLabel(trackWith(tr("299"), func(x *downexv1.MediaTrack) { x.Name = "" }), true), "1080p (Full HD) 60fps · H.264 · 472 MB")
		eq(t, "140", AudioTrackLabel(tr("140"), true), "English original · AAC · 129 kbit/s · 10.5 MB")
		v, a := ChosenTracks(site)
		eq(t, "chosen video", v, "1080p (Full HD) 60fps · H.264 · 472 MB · 1920×1080")
		eq(t, "chosen audio", a, "English original · AAC · 129 kbit/s · 10.5 MB")
		eq(t, "quality", SiteQualityText(site), "1080p (Full HD) 60fps H.264 + AAC")
		eq(t, "muxed quality", SiteQualityText(with(site, func(m *downexv1.MediaInfo) { m.SelectedVideo, m.SelectedAudio = "18", "" })), "360p H.264 + AAC")
		eq(t, "no quality", SiteQualityText(with(site, func(m *downexv1.MediaInfo) { m.SelectedVideo = "" })), "")
	})
	t.Run("names a quality by downexd's standard name, else by its picture, never by its height", func(t *testing.T) {
		for _, c := range []struct {
			name   string
			w, h   int32
			fps    float64
			result string
		}{
			{"4K (2160p) 60fps HDR", 3840, 1634, 60, "4K (2160p) 60fps HDR"},
			// An older downexd named qualities by their height: the picture names them now.
			{"1634p", 3840, 1634, 24, "4K (2160p)"},
			{"2160p HDR", 3840, 2160, 30, "4K (2160p) HDR"},
			{"1080p60", 1920, 1080, 60, "1080p (Full HD) 60fps"},
			{"", 0, 2160, 30, "4K (2160p)"},
			{"", 0, 0, 0, ""},
		} {
			eq(t, "QualityName("+c.name+")", QualityName(&downexv1.MediaTrack{Name: c.name, Width: c.w, Height: c.h, FrameRate: c.fps}), c.result)
		}
	})
	t.Run("reduces an older downexd's every-quality list of the user's 3840×1634 film to one per class", func(t *testing.T) {
		old := with(siteInfo(), func(m *downexv1.MediaInfo) {
			m.SelectedVideo = "401"
			m.Tracks = []*downexv1.MediaTrack{
				track("401", "video", video(3840, 1634, 24, 0, "av01.0.12M.08"), named("1634p"), sized(860*MiB, false)),
				track("701", "video", video(3840, 1634, 24, 0, "av01.0.12M.10"), named("1634p HDR"), sized(990*MiB, false)),
				track("137", "video", video(1920, 818, 24, 0, "avc1.640028"), named("818p"), sized(200*MiB, false)),
				track("399", "video", video(1920, 818, 24, 0, "av01.0.08M.08"), named("818p"), sized(150*MiB, false)),
				track("134", "video", video(640, 272, 24, 0, "avc1.4d4015"), named("272p"), sized(30*MiB, false)),
				track("18", "muxed", video(640, 272, 24, 0, "avc1.42001E, mp4a.40.2"), named("272p"), sized(40*MiB, false)),
				track("140", "audio", audio(129_000, "mp4a.40.2", "en", "English original"), sized(10*MiB, false)),
			}
		})
		var got [][2]string
		for _, v := range VideoTracks(old.Tracks, true, old.SelectedVideo) {
			got = append(got, [2]string{v.Id, SiteQualityLabel(old, DefaultPick(old), v)})
		}
		want := [][2]string{{"401", "4K (2160p) · AV1 · 912 MB"}, {"137", "1080p (Full HD) · H.264 · 220 MB"}, {"134", "360p · H.264 · 41.9 MB"}}
		if !slices.Equal(got, want) {
			t.Errorf("film = %q", got)
		}
	})
	t.Run("lists each quality with the size of the file it makes, as the browser picker does", func(t *testing.T) {
		p := TrackPick{"299", "140"}
		// The video with the audio it is paired with: 450 + 10 MiB, not the video alone.
		eq(t, "299", SiteQualityLabel(site, p, tr("299")), "1080p (Full HD) 60fps · H.264 · 482 MB")
		eq(t, "397", SiteQualityLabel(site, p, tr("397")), "480p · AV1 · ~168 MB")
		// A muxed quality carries its own sound: its own size (here bitrate × length).
		eq(t, "18", SiteQualityLabel(site, p, tr("18")), "360p · H.264 + AAC · about 45.0 MB")
		// Without a length or sizes: the bitrate, as the track label says it.
		eq(t, "no length", SiteQualityLabel(with(site, func(m *downexv1.MediaInfo) { m.DurationMs = 0 }), p, tr("18")), "360p · H.264 + AAC · 600 kbit/s")
	})
	t.Run("adds the declared sizes when every chosen track has one, else estimates", func(t *testing.T) {
		for _, c := range []struct {
			info *downexv1.MediaInfo
			pick TrackPick
			want PickSize
		}{
			{site, TrackPick{"299", "140"}, PickSize{460 * MiB, SizeExact}},
			{site, TrackPick{"397", "140"}, PickSize{160 * MiB, SizeApprox}},
			// the muxed quality has no size: bitrate × length
			{site, TrackPick{"18", "140"}, PickSize{45_000_000, SizeEstimate}},
			{with(site, func(m *downexv1.MediaInfo) { m.DurationMs = 0 }), TrackPick{"18", ""}, PickSize{-1, SizeUnknown}},
			// a manifest's tracks have no sizes: the M5 estimate, as before
			{info, TrackPick{"v1", "r0"}, PickSize{384_600_000, SizeEstimate}},
			{&downexv1.MediaInfo{Tracks: []*downexv1.MediaTrack{track("a", "audio", sized(5, false))}}, TrackPick{"", "a"}, PickSize{5, SizeExact}},
		} {
			if got := PickSizeOf(c.info, c.pick); got != c.want {
				t.Errorf("PickSizeOf(%+v) = %+v, want %+v", c.pick, got, c.want)
			}
		}
		eq(t, "exact", PickSizeText(PickSize{460 * MiB, SizeExact}), "482 MB")
		eq(t, "approx", PickSizeText(PickSize{160 * MiB, SizeApprox}), "~168 MB")
		eq(t, "estimate", PickSizeText(PickSize{45_000_000, SizeEstimate}), "about 45.0 MB")
		eq(t, "unknown", PickSizeText(PickSize{-1, SizeUnknown}), "")
	})
	t.Run("says about for the size downexd measured of a manifest track without a bandwidth", func(t *testing.T) {
		// A bare media playlist: no BANDWIDTH, its picture and size measured from its segments.
		bare := with(mediaInfo(), func(m *downexv1.MediaInfo) {
			m.DurationMs, m.SelectedVideo, m.SelectedAudio = 3_907_000, "v0", ""
			m.Tracks = []*downexv1.MediaTrack{track("v0", "muxed", video(1920, 1080, 25, 0, "avc1.640028,mp4a.40.2"), named("1080p (Full HD)"), sized(2150*MiB, true))}
		})
		p := TrackPick{"v0", ""}
		if got := PickSizeOf(bare, p); got != (PickSize{2150 * MiB, SizeEstimate}) {
			t.Errorf("bare = %+v", got)
		}
		eq(t, "bare text", PickSizeText(PickSizeOf(bare, p)), "about 2.3 GB")
		// Every segment a byte range: the exact size.
		exact := with(bare, func(m *downexv1.MediaInfo) { m.Tracks[0].Size, m.Tracks[0].SizeApprox = 700*MiB, false })
		eq(t, "exact text", PickSizeText(PickSizeOf(exact, p)), "734 MB")
		// A web page's own estimate keeps its "~".
		page := with(bare, func(m *downexv1.MediaInfo) { m.Origin = downexv1.MediaOrigin_MEDIA_ORIGIN_SITE })
		if got := PickSizeOf(page, p); got != (PickSize{2150 * MiB, SizeApprox}) {
			t.Errorf("page = %+v", got)
		}
	})
}

func apiErr(code connect.Code, message string) error {
	return connect.NewError(code, errors.New(message))
}

func TestVideoPageErrors(t *testing.T) {
	t.Run("splits the stable code off a message", func(t *testing.T) {
		if c, r := SplitCode("site_error: yt-dlp could not read this page: HTTP 500"); c != "site_error" || r != "yt-dlp could not read this page: HTTP 500" {
			t.Errorf("split = %q %q", c, r)
		}
		if c, r := SplitCode("no code here"); c != "" || r != "no code here" {
			t.Errorf("no code = %q %q", c, r)
		}
	})
	t.Run("says every code as §5.5 does, with the reason where it helps", func(t *testing.T) {
		pre, inv := connect.CodeFailedPrecondition, connect.CodeInvalidArgument
		for _, c := range []struct {
			err  error
			want string
		}{
			{apiErr(inv, "site_unsupported: no video found on this page"), SiteTexts["site_unsupported"]},
			{apiErr(pre, "site_unavailable: this video is not available: private video"), "This video is not available (private, removed, blocked in your country, or the site asks to try again later). Private video."},
			{apiErr(pre, "auth_required: the site asks to sign in"), SiteTexts["auth_required"]},
			{apiErr(inv, "live: this is a live stream"), "This is a live stream. Downex downloads only finished videos."},
			{apiErr(inv, "encrypted: DRM"), "This video is DRM-protected. Downex does not download protected content."},
			{apiErr(inv, "media_unsupported: no MP4 video on this page"), "No MP4 video on this page."},
			{apiErr(inv, "media_unsupported: a playlist"), "Open a single video of this playlist."},
			{apiErr(inv, "media_invalid: no format 299"), "The chosen quality is no longer offered. Add the video again."},
			{apiErr(pre, "site_error: yt-dlp could not read this page: HTTP Error 500"), "yt-dlp could not read this page: HTTP Error 500."},
			{apiErr(pre, "site_error: yt-dlp could not read this page"), "yt-dlp could not read this page."},
			{apiErr(pre, "tools_unavailable: yt-dlp and Deno are not set up: this Downex daemon does not download tools"), "Downex could not set up yt-dlp/Deno: this Downex daemon does not download tools."},
			{apiErr(pre, "site_media_off: video downloads from web pages are turned off (Settings → Video sites)"), "Video downloads from web pages are turned off (Settings → Video sites)."},
			{apiErr(pre, "tools_installing: downloading yt-dlp and Deno (34%)"), "Downloading yt-dlp and Deno (34%)…"},
			{apiErr(pre, "site_resolving: still getting the video info"), "Getting video info…"},
			{apiErr(connect.CodeUnimplemented, "not implemented"), "Update Downex to download videos."},
			{apiErr(connect.CodeUnavailable, "downexd is not running"), "downexd is not running"},
			// a reason that names a program keeps its spelling
			{apiErr(pre, "site_unavailable: this video is not available: yt-dlp failed"), SiteTexts["site_unavailable"] + " yt-dlp failed."},
		} {
			eq(t, "SiteErrorText("+c.err.Error()+")", SiteErrorText(c.err), c.want)
		}
		// the origin picks the texts
		eq(t, "site live", MediaErrorText(errors.New("invalid_argument: live: this is a live stream"), true), SiteTexts["live"])
		eq(t, "manifest live", MediaErrorText(errors.New("invalid_argument: live: this is a live stream"), false), RefusalText(Live))
		if !NeedsToolsSettings(errors.New("failed_precondition: tools_unavailable: x")) || !NeedsToolsSettings(errors.New("failed_precondition: site_media_off: x")) ||
			NeedsToolsSettings(errors.New("failed_precondition: site_error: x")) {
			t.Error("NeedsToolsSettings")
		}
	})
	t.Run("recognises the answers that ask to try again", func(t *testing.T) {
		if r := SiteRetryOf(errors.New("failed_precondition: tools_installing: downloading yt-dlp and Deno (34%)")); r != (SiteRetry{ToolsInstalling, 0.34}) {
			t.Errorf("34%% = %+v", r)
		}
		if r := SiteRetryOf(errors.New("failed_precondition: tools_installing: downloading yt-dlp and Deno")); r.Kind != ToolsInstalling || !math.IsNaN(r.Fraction) {
			t.Errorf("no percent = %+v", r)
		}
		if r := SiteRetryOf(errors.New("failed_precondition: site_resolving: still getting the video info")); r.Kind != SiteResolving {
			t.Errorf("resolving = %+v", r)
		}
		for _, e := range []error{errors.New("failed_precondition: site_error: x"), errors.New("invalid_argument: site_resolving: x")} {
			if r := SiteRetryOf(e); r.Kind != NoRetry {
				t.Errorf("SiteRetryOf(%v) = %+v", e, r)
			}
		}
	})
	t.Run("hints at a failed download and knows when retrying cannot help", func(t *testing.T) {
		eq(t, "hint", SiteErrorHint("site_unavailable"), SiteTexts["site_unavailable"])
		eq(t, "network", SiteErrorHint("network"), "")
		for _, code := range []string{"encrypted", "live", "media_unsupported", "media_invalid", "site_unsupported"} {
			if !CannotRetry(code) {
				t.Errorf("CannotRetry(%q) = false", code)
			}
		}
		for _, code := range []string{"site_unavailable", "site_error", "auth_required", "tools_unavailable", "network", ""} {
			if CannotRetry(code) {
				t.Errorf("CannotRetry(%q) = true", code)
			}
		}
	})
}

// fakeClock is a clock the test moves: After's channels fire when Advance passes their time.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []fakeTimer
	waiting chan struct{}
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0), waiting: make(chan struct{}, 1000)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{c.now.Add(d), ch})
	c.waiting <- struct{}{}
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.timers[:0]
	for _, t := range c.timers {
		if !t.at.After(c.now) {
			t.ch <- c.now
		} else {
			kept = append(kept, t)
		}
	}
	c.timers = kept
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s", what)
	}
}

func TestRetrySite(t *testing.T) {
	t.Run("asks again at most every 2 s until an answer, joining the work that runs", func(t *testing.T) {
		clock := newFakeClock()
		answers := []error{
			errors.New("failed_precondition: tools_installing: downloading yt-dlp and Deno (10%)"),
			errors.New("failed_precondition: site_resolving: x"),
			nil,
		}
		calls := make(chan struct{}, 10)
		var mu sync.Mutex
		var waits []string
		done := make(chan int, 1)
		go func() {
			v, err := RetrySite(context.Background(), func(context.Context) (int, error) {
				calls <- struct{}{}
				next := answers[0]
				answers = answers[1:]
				if next != nil {
					return 0, next
				}
				return 42, nil
			}, RetryOptions{Clock: clock, OnWait: func(r SiteRetry) { mu.Lock(); waits = append(waits, r.Kind.String()); mu.Unlock() }})
			if err != nil {
				t.Error(err)
			}
			done <- v
		}()
		waitFor(t, calls, "first call")
		waitFor(t, clock.waiting, "first wait")
		clock.Advance(SiteRetryInterval - time.Millisecond)
		select {
		case <-calls:
			t.Fatal("asked again before 2 s")
		case <-time.After(50 * time.Millisecond):
		}
		clock.Advance(time.Millisecond)
		waitFor(t, calls, "second call")
		waitFor(t, clock.waiting, "second wait")
		clock.Advance(SiteRetryInterval)
		waitFor(t, calls, "third call")
		if v := <-done; v != 42 {
			t.Errorf("answer = %d", v)
		}
		mu.Lock()
		defer mu.Unlock()
		if !slices.Equal(waits, []string{"tools_installing", "site_resolving"}) {
			t.Errorf("waits = %v", waits)
		}
	})
	t.Run("passes other failures on at once, and stops when told to", func(t *testing.T) {
		clock := newFakeClock()
		_, err := RetrySite(context.Background(), func(context.Context) (int, error) {
			return 0, errors.New("failed_precondition: site_error: x")
		}, RetryOptions{Clock: clock})
		if err == nil || !strings.Contains(err.Error(), "site_error") {
			t.Errorf("site_error = %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		result := make(chan error, 1)
		go func() {
			_, err := RetrySite(ctx, func(context.Context) (int, error) {
				calls++
				return 0, errors.New("failed_precondition: site_resolving: x")
			}, RetryOptions{Clock: clock})
			result <- err
		}()
		waitFor(t, clock.waiting, "a wait")
		clock.Advance(500 * time.Millisecond)
		cancel()
		err = <-result
		if connect.CodeOf(err) != connect.CodeCanceled {
			t.Errorf("stopped = %v", err)
		}
		clock.Advance(10 * time.Second)
		if calls != 1 {
			t.Errorf("%d calls", calls)
		}
	})
	t.Run("gives up after its limit with the last answer", func(t *testing.T) {
		clock := newFakeClock()
		calls := 0
		result := make(chan error, 1)
		go func() {
			_, err := RetrySite(context.Background(), func(context.Context) (int, error) {
				calls++
				return 0, errors.New("failed_precondition: site_resolving: x")
			}, RetryOptions{Clock: clock})
			result <- err
		}()
		for elapsed := time.Duration(0); elapsed < 17*time.Minute; elapsed += SiteRetryInterval {
			select {
			case err := <-result:
				if err == nil || err.Error() != "failed_precondition: site_resolving: x" {
					t.Errorf("last answer = %v", err)
				}
				if calls <= 400 {
					t.Errorf("%d calls", calls)
				}
				return
			case <-clock.waiting:
				clock.Advance(SiteRetryInterval)
			case <-time.After(5 * time.Second):
				t.Fatal("stuck")
			}
		}
		t.Fatal("did not give up")
	})
}

func TestCodecSummary(t *testing.T) {
	site, hls := siteInfo(), mediaInfo()
	p := func(v, a string) *TrackPick { return &TrackPick{v, a} }
	eq(t, "site", CodecSummary(site, nil), "1080p · H.264 · AAC 129 kbit/s")
	eq(t, "site 397", CodecSummary(site, p("397", "140")), "480p · AV1 · AAC 129 kbit/s")
	eq(t, "hls", CodecSummary(hls, nil), "1080p · H.264 · AAC 128 kbit/s")
	eq(t, "hls v0", CodecSummary(hls, p("v0", "r1")), "360p · H.264 · AAC 96 kbit/s")
	// a muxed track's second codec is its audio, without a bitrate of its own
	eq(t, "muxed", CodecSummary(site, p("18", "140")), "360p · H.264 · AAC")
	// 960×540 is in the 480p class (the class rule, never the raw height)
	eq(t, "540", CodecSummary(hls, p("v2", "")), "480p · H.264 · AAC")
	uhd := track("u", "video", video(3840, 2160, 60, 0, "av01.0.12M.08"), named("4K (2160p) 60fps"))
	opus := track("a", "audio", audio(160_000, "opus", "", ""))
	info := with(hls, func(m *downexv1.MediaInfo) {
		m.Tracks, m.SelectedVideo, m.SelectedAudio = []*downexv1.MediaTrack{uhd, opus}, "u", "a"
	})
	eq(t, "4K", CodecSummary(info, nil), "4K · AV1 · Opus 160 kbit/s")
	eq(t, "audio only", CodecSummary(info, p("", "a")), "Opus 160 kbit/s")
	bare := with(hls, func(m *downexv1.MediaInfo) {
		m.Tracks, m.SelectedVideo, m.SelectedAudio = []*downexv1.MediaTrack{track("x", "audio")}, "", "x"
	})
	eq(t, "bare", CodecSummary(bare, nil), "")
}

func TestLanguageName(t *testing.T) {
	eq(t, "en", LanguageName("en"), "English")
	eq(t, "unknown", LanguageName("x-unknown"), "x-unknown")
	eq(t, "empty", LanguageName(""), "")
	SetLanguageNamer(nil)
	defer SetLanguageNamer(func(tag string) (string, bool) {
		name, ok := map[string]string{"en": "English", "de": "German"}[tag]
		return name, ok
	})
	eq(t, "no namer", LanguageName("en"), "en")
}
