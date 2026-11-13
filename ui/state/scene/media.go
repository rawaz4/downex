package scene

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	imedia "downex/internal/media"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/media"
	"downex/ui/core/text"
	"downex/ui/core/validate"
)

func standardName(width, height int32, fps float64, hdr bool) string {
	return imedia.ClassName(imedia.QualityClass(int(width), int(height)), fps, hdr)
}

type track = downexv1.MediaTrack

var videoLadder = []struct {
	width, height int32
	bandwidth     int64
	codecs        string
}{
	{640, 360, 800_000, "avc1.4d401e"},
	{1280, 720, 2_800_000, "avc1.4d401f"},
	{1920, 1080, 5_500_000, "avc1.640028"},
}

var audioSet = []struct {
	language, name string
	bandwidth      int64
	codecs, dash   string
}{
	{"en", "English", 128_000, "mp4a.40.2", "audio-en"},
	{"es", "Español", 128_000, "mp4a.40.2", "audio-es"},
	{"en", "Commentary", 64_000, "mp4a.40.5", "audio-en-commentary"},
}

// fragmentMs: HLS media playlists commonly use 6 s, DASH 4 s segments.
func fragmentMs(format string) float64 {
	if format == "dash" {
		return 4000
	}
	return 6000
}

var (
	genericName = regexp.MustCompile(`(?i)^(master|manifest|index|playlist|stream|main|audio|video)$`)
	manifestExt = regexp.MustCompile(`(?i)\.(m3u8|mpd)$`)
)

func pathParts(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// titleOf is the title a manifest's address suggests: the folder that holds it.
func titleOf(u jsURL) string {
	parts := pathParts(u.pathname)
	file := ""
	if len(parts) > 0 {
		file = manifestExt.ReplaceAllString(parts[len(parts)-1], "")
	}
	title := file
	if len(parts) >= 2 && genericName.MatchString(file) {
		title = parts[len(parts)-2]
	}
	if dec, ok := text.DecodeURIComponent(title); ok {
		title = dec
	}
	if title == "" {
		return "stream"
	}
	return title
}

// probeStream is what the daemon's probe would find (MEDIA-SPEC §6.3), or its refusal.
func probeStream(manifestURL string) (*downexv1.MediaInfo, error) {
	if reason, ok := validate.CheckURL(manifestURL); !ok {
		return nil, apiErr(connect.CodeInvalidArgument, "manifest_url "+reason)
	}
	u, _ := parseJSURL(manifestURL)
	format := ""
	switch lp := strings.ToLower(u.pathname); {
	case strings.HasSuffix(lp, ".m3u8"):
		format = "hls"
	case strings.HasSuffix(lp, ".mpd"):
		format = "dash"
	}
	if format == "" {
		return nil, apiErr(connect.CodeInvalidArgument, "manifest_url: not an HLS (.m3u8) or DASH (.mpd) manifest")
	}
	marker := strings.ToLower(manifestURL)
	switch {
	case strings.Contains(marker, "404"):
		return nil, apiErr(connect.CodeFailedPrecondition, "manifest: the server responded 404 Not Found")
	case strings.Contains(marker, "drm") || strings.Contains(marker, "encrypted"):
		return nil, apiErr(connect.CodeInvalidArgument, "encrypted: the stream is encrypted or DRM-protected")
	case strings.Contains(marker, "live"):
		return nil, apiErr(connect.CodeInvalidArgument, "live: the presentation is live and still growing")
	}
	h := hashOf(manifestURL)
	durationMs := float64(3+h%88)*60_000 + float64(h%59)*1000
	audioOnly := strings.Contains(marker, "audio") || strings.Contains(marker, "podcast")
	muxed := format == "hls" && !audioOnly && strings.Contains(marker, "muxed")
	var tracks []*track
	if !audioOnly {
		for i, v := range videoLadder {
			id := "v" + strconv.Itoa(i)
			if format != "hls" {
				id = "video-" + strconv.Itoa(int(v.height)) + "p"
			}
			fps := 30.0
			if i == 0 {
				fps = 25
			}
			t := &track{Id: id, Kind: "video", Width: v.width, Height: v.height, Bandwidth: v.bandwidth, Codecs: v.codecs, FrameRate: fps,
				// downexd names every variant by its quality class (one per class).
				Name: standardName(v.width, v.height, fps, false)}
			if muxed {
				t.Kind, t.Bandwidth, t.Codecs = "muxed", v.bandwidth+128_000, v.codecs+",mp4a.40.2"
			}
			tracks = append(tracks, t)
		}
	}
	if !muxed {
		for i, a := range audioSet {
			id := "r" + strconv.Itoa(i)
			if format != "hls" {
				id = a.dash
			}
			tracks = append(tracks, &track{Id: id, Kind: "audio", Bandwidth: a.bandwidth, Codecs: a.codecs, Language: a.language, Name: a.name})
		}
	}
	info := &downexv1.MediaInfo{
		ManifestUrl: manifestURL, Format: format, Tracks: tracks, DurationMs: int64(durationMs),
		Container: "mp4", Title: titleOf(u), Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_MANIFEST,
	}
	return withPick(info, media.DefaultPick(info)), nil
}

var siteLadder = []struct {
	height   int32
	fps      float64
	avc, av1 float64
	ids      [2]string
}{
	{2160, 60, 0, 18_000, [2]string{"", "701"}},
	{2160, 30, 0, 12_000, [2]string{"", "401"}},
	{1440, 60, 0, 9_000, [2]string{"", "700"}},
	{1440, 30, 0, 6_000, [2]string{"", "400"}},
	{1080, 60, 6_000, 3_800, [2]string{"299", "699"}},
	{1080, 30, 4_400, 2_100, [2]string{"137", "399"}},
	{720, 60, 3_300, 1_900, [2]string{"298", "698"}},
	{720, 30, 2_200, 1_200, [2]string{"136", "398"}},
	{480, 30, 1_100, 650, [2]string{"135", "397"}},
	{360, 30, 650, 400, [2]string{"134", "396"}},
	{240, 30, 300, 200, [2]string{"133", "395"}},
	{144, 30, 120, 90, [2]string{"160", "394"}},
}

var youTubeHost = regexp.MustCompile(`(^|\.)(youtube\.com|youtu\.be|youtube-nocookie\.com)$`)

// siteOf is the site's name for display: YouTube, else the host without "www.".
func siteOf(u jsURL) string {
	host := strings.ToLower(u.hostname)
	if youTubeHost.MatchString(host) {
		return "YouTube"
	}
	return strings.TrimPrefix(host, "www.")
}

// pageTitle is the title a page suggests: a YouTube id, else its last path segment, else
// its host.
func pageTitle(u jsURL) string {
	if v, ok := u.param("v"); ok && v != "" {
		return "Video " + v
	}
	parts := pathParts(u.pathname)
	last := ""
	if len(parts) > 0 {
		last = parts[len(parts)-1]
	}
	if dec, ok := text.DecodeURIComponent(last); ok {
		if dec != "" {
			return dec
		}
		return u.hostname
	}
	if last != "" {
		return last
	}
	return u.hostname
}

// FixtureTitles are a fixture video's titles: the owner's case, one that wraps,
// right-to-left ones, none.
var FixtureTitles = map[string]string{
	"plain": "Set Up Staging and Production with Dokploy",
	"long":  "Set Up Staging and Production with Dokploy: Docker Compose, Traefik, Preview Deployments, Automatic Backups and Zero-Downtime Rollbacks on One VPS (Full Course, 2026 Edition)",
	"ku":    "سلێمانی تا بەغداد بۆ بینینی The Odyssey و IMAX",
	"ar":    "أطيب كباب في السليمانية_ أكلات شعبية وخبز التنور الحار",
	"none":  "",
}

type ladderStep struct {
	height int32
	fps    float64
	codec  string
	kbit   float64
	hdr    bool
	id     string
}

// fixtureLadders are real YouTube pages' offers by their number of MP4 qualities.
var fixtureLadders = map[int][]ladderStep{
	1: {{360, 30, "avc", 650, false, "134"}},
	3: {{720, 30, "avc", 2200, false, "136"}, {480, 30, "avc", 1100, false, "135"}, {360, 30, "avc", 650, false, "134"}},
	6: {
		{1080, 50, "avc", 5200, false, "299"}, {720, 50, "avc", 3000, false, "298"}, {480, 25, "avc", 1100, false, "135"},
		{360, 25, "avc", 650, false, "134"}, {240, 25, "avc", 300, false, "133"}, {144, 25, "avc", 120, false, "160"},
	},
	8: {
		{2160, 60, "av1", 21_000, true, "701"}, {1440, 60, "av1", 9500, true, "700"}, {1080, 60, "avc", 6000, false, "299"},
		{720, 60, "avc", 3300, false, "298"}, {480, 30, "avc", 1100, false, "135"}, {360, 30, "avc", 650, false, "134"},
		{240, 30, "avc", 300, false, "133"}, {144, 30, "avc", 120, false, "160"},
	},
}

type fixtureVideo struct {
	pageURL          string
	qualities, audio int
	title            string
	durationMs       float64
}

// fixtureOffer is a fixture video's offer, as downexd lists a page's (SITE-MEDIA-SPEC §3.11).
func fixtureOffer(v fixtureVideo) (*downexv1.MediaInfo, error) {
	ladder, ok := fixtureLadders[v.qualities]
	if !ok {
		return nil, fixtureError("no offer with " + strconv.Itoa(v.qualities) + " qualities (1, 3, 6 or 8)")
	}
	durationMs := v.durationMs
	if durationMs == 0 {
		durationMs = 991_000
	}
	bytes := func(kbit float64) int64 { return int64(round(kbit * 1000 * durationMs / 1000 / 8)) }
	var tracks []*track
	for _, s := range ladder {
		width := int32(round(float64(s.height) * 16 / 9))
		codecs := "avc1.640028"
		if s.codec != "avc" {
			codecs = "av01.0.12M.10"
		}
		tracks = append(tracks, &track{Id: s.id, Kind: "video", Width: width, Height: s.height, FrameRate: s.fps, Bandwidth: int64(s.kbit * 1000),
			Codecs: codecs, Name: standardName(width, s.height, s.fps, s.hdr), Size: bytes(s.kbit), SizeApprox: s.codec == "av1", Ext: "mp4"})
	}
	audios := []*track{
		{Id: "140", Kind: "audio", Bandwidth: 129_000, Codecs: "mp4a.40.2", Language: "en", Name: "English original", Size: bytes(129), Ext: "m4a"},
		{Id: "140-1", Kind: "audio", Bandwidth: 129_000, Codecs: "mp4a.40.2", Language: "es", Name: "Español", Size: bytes(129), Ext: "m4a"},
		{Id: "139", Kind: "audio", Bandwidth: 49_000, Codecs: "mp4a.40.5", Language: "en", Name: "English original, low", Size: bytes(49), Ext: "m4a"},
	}
	switch {
	case v.audio >= 3:
		tracks = append(tracks, audios...)
	case v.audio == 2:
		tracks = append(tracks, audios[0], audios[2])
	default:
		tracks = append(tracks, audios[0])
	}
	info := &downexv1.MediaInfo{Format: "http", Tracks: tracks, DurationMs: int64(durationMs), Container: "mp4", Title: v.title,
		Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, Site: "YouTube", PageUrl: v.pageURL}
	pick := media.DefaultPick(info)
	info.SelectedVideo, info.SelectedAudio = pick.Video, media.PickedAudio(info, pick)
	return info, nil
}

var (
	reSignin   = regexp.MustCompile(`signin|\bage\b|age-restricted`)
	rePlaylist = regexp.MustCompile(`/playlist\b`)
)

// siteOffer is what yt-dlp would find on a page (formats already turned into downexd's
// offer), or its refusal.
func siteOffer(pageURL string) (*downexv1.MediaInfo, error) {
	u, _ := parseJSURL(pageURL)
	// A layout fixture's page: "qualities=<1|3|6|8>", with "audio=<1-3>" and "title=<…>".
	if fixed, ok := u.param("qualities"); ok {
		q, _ := strconv.Atoi(fixed)
		audio := 2
		if a, ok := u.param("audio"); ok {
			audio, _ = strconv.Atoi(a)
		}
		key, ok := u.param("title")
		if !ok {
			key = "plain"
		}
		title, ok := FixtureTitles[key]
		if !ok {
			title = FixtureTitles["plain"]
		}
		return fixtureOffer(fixtureVideo{pageURL: pageURL, qualities: q, audio: audio, title: title})
	}
	marker := strings.ToLower(pageURL)
	switch {
	case strings.Contains(marker, "unsupported"):
		return nil, apiErr(connect.CodeInvalidArgument, "site_unsupported: no video found on this page")
	case strings.Contains(marker, "broken"):
		return nil, apiErr(connect.CodeFailedPrecondition, "site_error: yt-dlp could not read this page: HTTP Error 500")
	case strings.Contains(marker, "private") || strings.Contains(marker, "unavailable"):
		return nil, apiErr(connect.CodeFailedPrecondition, "site_unavailable: this video is not available: Private video")
	case reSignin.MatchString(marker):
		return nil, apiErr(connect.CodeFailedPrecondition, "auth_required: the site asks to sign in: Sign in to confirm your age")
	case strings.Contains(marker, "drm") || strings.Contains(marker, "encrypted"):
		return nil, apiErr(connect.CodeInvalidArgument, "encrypted: the video is DRM-protected")
	case strings.Contains(marker, "live"):
		return nil, apiErr(connect.CodeInvalidArgument, "live: this is a live stream")
	case rePlaylist.MatchString(u.pathname):
		return nil, apiErr(connect.CodeInvalidArgument, "media_unsupported: a playlist: open a single video")
	case strings.Contains(marker, "podcast") || strings.Contains(marker, "audio-only"):
		return nil, apiErr(connect.CodeInvalidArgument, "media_unsupported: no MP4 video on this page")
	}
	h := hashOf(pageURL)
	durationMs := float64(2+h%40)*60_000 + float64(h%57)*1000
	seconds := durationMs / 1000
	maxHeight := [5]int32{2160, 1440, 1080, 1080, 720}[h%5]
	sixty := h%2 == 0
	film := strings.Contains(marker, "film") || strings.Contains(marker, "movie")
	bytes := func(kbit float64) int64 { return int64(round(kbit * 1000 * seconds / 8)) }
	var formats []*track
	for _, q := range siteLadder {
		if q.height > maxHeight || q.fps > 30 && !sixty {
			continue
		}
		width := int32(round(float64(q.height) * 16 / 9))
		// A film's picture is as wide and lower: 3840×1634 for a 2160 step.
		height := q.height
		if film {
			height = int32(round(float64(width)/2.35/2) * 2)
		}
		name := standardName(width, height, q.fps, false)
		if q.avc > 0 {
			formats = append(formats, &track{Id: q.ids[0], Kind: "video", Width: width, Height: height, FrameRate: q.fps, Bandwidth: int64(q.avc * 1000), Codecs: "avc1.640028", Name: name, Size: bytes(q.avc), Ext: "mp4"})
		}
		// The site estimates the size of its largest AV1 qualities.
		formats = append(formats, &track{Id: q.ids[1], Kind: "video", Width: width, Height: height, FrameRate: q.fps, Bandwidth: int64(q.av1 * 1000), Codecs: "av01.0.08M.08", Name: name, Size: bytes(q.av1), SizeApprox: q.height > 1080, Ext: "mp4"})
	}
	muxedHeight := int32(360)
	if film {
		muxedHeight = 272
	}
	formats = append(formats, &track{Id: "18", Kind: "muxed", Width: 640, Height: muxedHeight, FrameRate: 30, Bandwidth: 760_000, Codecs: "avc1.42001E, mp4a.40.2", Name: standardName(640, muxedHeight, 30, false), Size: bytes(760), SizeApprox: true, Ext: "mp4"})
	// downexd keeps one quality per class: video only, SDR, the higher frame rate, H.264, bitrate.
	videos := media.OnePerClass(formats, "")
	var audios []*track
	if strings.Contains(marker, "dub") {
		audios = []*track{
			{Id: "140-0", Kind: "audio", Bandwidth: 129_000, Codecs: "mp4a.40.2", Language: "en", Name: "English original", Size: bytes(129), Ext: "m4a"},
			{Id: "140-1", Kind: "audio", Bandwidth: 129_000, Codecs: "mp4a.40.2", Language: "es", Name: "Español", Size: bytes(129), Ext: "m4a"},
			{Id: "139-0", Kind: "audio", Bandwidth: 49_000, Codecs: "mp4a.40.5", Language: "en", Name: "English original, low", Size: bytes(49), Ext: "m4a"},
		}
	} else {
		audios = []*track{
			{Id: "140", Kind: "audio", Bandwidth: 129_000, Codecs: "mp4a.40.2", Name: "medium", Size: bytes(129), Ext: "m4a"},
			{Id: "139", Kind: "audio", Bandwidth: 49_000, Codecs: "mp4a.40.5", Name: "low", Size: bytes(49), Ext: "m4a"},
		}
	}
	info := &downexv1.MediaInfo{Format: "http", Tracks: append(videos, audios...), DurationMs: int64(durationMs), Container: "mp4",
		Title: pageTitle(u), Origin: downexv1.MediaOrigin_MEDIA_ORIGIN_SITE, Site: siteOf(u), PageUrl: pageURL}
	pick := media.DefaultPick(info)
	// A probe counts no parts: they are planned when the download runs.
	info.SelectedVideo, info.SelectedAudio = pick.Video, media.PickedAudio(info, pick)
	return info, nil
}

// withPick is the stream with a choice of tracks: its selection and fragment count (a web
// page's video: its parts, partBytes of each chosen track). info is not changed.
func withPick(info *downexv1.MediaInfo, pick media.TrackPick) *downexv1.MediaInfo {
	out := cloneMedia(info)
	audio := media.PickedAudio(info, pick)
	var chosen []*track
	for _, t := range []*track{media.TrackByID(info, pick.Video), media.TrackByID(info, audio)} {
		if t != nil {
			chosen = append(chosen, t)
		}
	}
	var total float64
	if media.IsSite(info) {
		for _, t := range chosen {
			total += math.Max(1, math.Ceil(float64(t.GetSize())/partBytes))
		}
	} else {
		total = math.Ceil(float64(info.GetDurationMs())/fragmentMs(info.GetFormat())) * float64(len(chosen))
	}
	out.SelectedVideo, out.SelectedAudio, out.FragmentsTotal, out.FragmentsDone = pick.Video, audio, int32(total), 0
	return out
}

// choosePick resolves requested track ids as the daemon does: empty = its default pick; a
// muxed video track carries its own sound, so no audio track is used with it.
func choosePick(info *downexv1.MediaInfo, video, audio string) (media.TrackPick, error) {
	v := media.TrackByID(info, video)
	if video != "" && (v == nil || v.GetKind() != "video" && v.GetKind() != "muxed") {
		return media.TrackPick{}, apiErr(connect.CodeInvalidArgument, `video_track: the stream has no video track "`+video+`"`)
	}
	if audio != "" && media.TrackByID(info, audio).GetKind() != "audio" {
		return media.TrackPick{}, apiErr(connect.CodeInvalidArgument, `audio_track: the stream has no audio track "`+audio+`"`)
	}
	def := media.DefaultPick(info)
	if video == "" {
		video = def.Video
	}
	base := media.WithVideo(info, def, video)
	pick := media.TrackPick{Video: base.Video, Audio: audio}
	if audio == "" {
		pick.Audio = base.Audio
	}
	return media.TrackPick{Video: pick.Video, Audio: media.PickedAudio(info, pick)}, nil
}

// mediaBytes is the bytes the chosen tracks take: a web page's from their declared sizes,
// a stream's from their bitrate (varied a little per stream).
func mediaBytes(info *downexv1.MediaInfo) float64 {
	pick := media.TrackPick{Video: info.GetSelectedVideo(), Audio: info.GetSelectedAudio()}
	if media.IsSite(info) {
		return math.Max(1, float64(media.PickSizeOf(info, pick).Bytes))
	}
	bytes := float64(media.EstimatedBytes(info, pick))
	jitter := 0.85 + float64(hashOf(info.GetManifestUrl())%20)/100
	return math.Max(1, round(math.Max(0, bytes)*jitter))
}

// mediaFilename is the name the daemon gives a stream or a page's video: the requested
// name or the title, with ".mp4".
func mediaFilename(base string) string {
	b := text.TrimJS(base)
	if b == "" {
		b = "stream"
	}
	name := sanitizeFilename(b)
	if categories.ExtensionOf(name) == "mp4" {
		return name
	}
	return name + ".mp4"
}

func cloneMedia(m *downexv1.MediaInfo) *downexv1.MediaInfo {
	out := &downexv1.MediaInfo{}
	*out = downexv1.MediaInfo{
		ManifestUrl: m.GetManifestUrl(), Format: m.GetFormat(), Tracks: m.GetTracks(), SelectedVideo: m.GetSelectedVideo(),
		SelectedAudio: m.GetSelectedAudio(), DurationMs: m.GetDurationMs(), FragmentsTotal: m.GetFragmentsTotal(),
		FragmentsDone: m.GetFragmentsDone(), Container: m.GetContainer(), Title: m.GetTitle(), Origin: m.GetOrigin(),
		Site: m.GetSite(), PageUrl: m.GetPageUrl(),
	}
	return out
}

// fixtureError is a layout fixture that cannot be set up.
type fixtureError string

func (e fixtureError) Error() string { return "fixture: " + string(e) }
