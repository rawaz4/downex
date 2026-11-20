// Package media is what the window shows of streaming media (MEDIA-SPEC §6) and of web pages'
// videos (SITE-MEDIA-SPEC §5), ported from ui/frontend/src/lib/media.ts: the quality picker's
// track order, labels, sizes and default pick, the facts shown for a media download, and what to
// tell the user about downexd's refusals and errors. Quality classes and names come from
// internal/media. Pure but for RetrySite, which waits.
package media

import (
	"cmp"
	"slices"
	"strings"

	downexv1 "downex/internal/rpc/downex/v1"
)

// IsMedia reports a media download (a stream or a web page's video).
func IsMedia(d *downexv1.Download) bool {
	return d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
}

// IsSite reports a web page's video (yt-dlp, or the files the browser played for it,
// SITE-MEDIA-SPEC §2.9), not an HLS/DASH manifest.
func IsSite(info *downexv1.MediaInfo) bool {
	o := info.GetOrigin()
	return o == downexv1.MediaOrigin_MEDIA_ORIGIN_SITE || o == downexv1.MediaOrigin_MEDIA_ORIGIN_PLAYED
}

// IsSiteDownload reports a media download of a web page's video.
func IsSiteDownload(d *downexv1.Download) bool { return IsMedia(d) && IsSite(d.GetMedia()) }

// SiteVideoLabel is "YouTube video"; "Video" when the site has no name. An audio page's is
// "SoundCloud audio" ("Audio"; SITE-MEDIA-SPEC §3.13).
func SiteVideoLabel(info *downexv1.MediaInfo) string {
	kind, bare := " video", "Video"
	if AudioPage(info) {
		kind, bare = " audio", "Audio"
	}
	if site := jsTrim(info.GetSite()); site != "" {
		return site + kind
	}
	return bare
}

// AudioPage reports a web page without video whose audio downexd offers (SITE-MEDIA-SPEC
// §3.13): its container is m4a or mp3 and its tracks are audio tracks. Its picker lists the
// audio tracks only, its name ends in the container's extension, and it has no Part section.
func AudioPage(info *downexv1.MediaInfo) bool {
	if !IsSite(info) {
		return false
	}
	c := jsLower(jsTrim(info.GetContainer()))
	return c == "m4a" || c == "mp3"
}

// SiteExtension is the extension of a web page's file: ".mp4", or an audio page's ".m4a" or
// ".mp3".
func SiteExtension(info *downexv1.MediaInfo) string {
	if AudioPage(info) {
		return "." + jsLower(jsTrim(info.GetContainer()))
	}
	return ".mp4"
}

// SiteFileType is the file type of a web page's file for display: "MP4", or an audio page's
// "M4A" or "MP3" (SiteExtension).
func SiteFileType(info *downexv1.MediaInfo) string {
	return strings.ToUpper(strings.TrimPrefix(SiteExtension(info), "."))
}

// AudioPageHint is what the dialogs say of an audio page (SITE-MEDIA-SPEC §3.13): "This page
// has no video: its audio is saved as .m4a."
func AudioPageHint(info *downexv1.MediaInfo) string {
	return "This page has no video: its audio is saved as " + SiteExtension(info) + "."
}

// FormatLabel is "HLS", "DASH"; another format in capitals; "" when unknown.
func FormatLabel(format string) string { return strings.ToUpper(jsTrim(format)) }

// ContainerLabel is the output container for display ("" while downexd has not decided). Every
// stream is saved as MP4 (VIDEO-DETECT-SPEC §3.9); "ts" is a download stored as MPEG-TS before
// 2026-10-23, which keeps its byte-for-byte .ts.
func ContainerLabel(container string) string {
	switch c := jsLower(jsTrim(container)); c {
	case "mp4":
		return "MP4"
	case "ts":
		return "MPEG-TS"
	default:
		return strings.ToUpper(c)
	}
}

// codecNames are the codec names every UI uses (SITE-MEDIA-SPEC §3.11), by the codec's first
// part.
var codecNames = map[string]string{
	"avc1": "H.264",
	"avc3": "H.264",
	"av01": "AV1",
	"hvc1": "HEVC",
	"hev1": "HEVC",
	"vp09": "VP9",
	"vp9":  "VP9",
	"mp4a": "AAC",
	"opus": "Opus",
	"ac-3": "AC-3",
	"ec-3": "E-AC-3",
	"mp3":  "MP3",
}

// CodecName names a codec: "avc1.640028" → "H.264", "mp4a.40.2" → "AAC"; another codec as it
// is.
func CodecName(codec string) string {
	c := jsTrim(codec)
	family, _, _ := strings.Cut(c, ".")
	if name, ok := codecNames[jsLower(family)]; ok {
		return name
	}
	return c
}

// CodecNames names a codecs list: "avc1.42001E, mp4a.40.2" → "H.264 + AAC".
func CodecNames(codecs string) string {
	var names []string
	for _, c := range strings.Split(codecs, ",") {
		if c = jsTrim(c); c != "" {
			names = append(names, CodecName(c))
		}
	}
	return strings.Join(names, " + ")
}

// IsVideoTrack reports a track that carries video: video only (paired with an audio track) or
// muxed.
func IsVideoTrack(t *downexv1.MediaTrack) bool {
	return t.GetKind() == "video" || t.GetKind() == "muxed"
}

// VideoTracks lists video tracks, one per quality class (SITE-MEDIA-SPEC amendment 2026-10-23),
// best class first. downexd lists one per class already (a web page's in its own order); an older
// downexd's longer list is reduced by the same rule (video only, SDR, frame rate, H.264, bitrate,
// picture, size), so the picker never lists more than downexd would. keep (downexd's pick,
// selected_video) stays the quality of its class. A manifest's tracks are sorted by picture,
// bandwidth, frame rate and ID first, which settles what the rule leaves equal.
func VideoTracks(tracks []*downexv1.MediaTrack, site bool, keep string) []*downexv1.MediaTrack {
	var videos []*downexv1.MediaTrack
	for _, t := range tracks {
		if IsVideoTrack(t) {
			videos = append(videos, t)
		}
	}
	if !site {
		slices.SortStableFunc(videos, func(a, b *downexv1.MediaTrack) int {
			area := func(t *downexv1.MediaTrack) int64 { return int64(t.GetWidth()) * int64(t.GetHeight()) }
			if c := cmp.Compare(area(b), area(a)); c != 0 {
				return c
			}
			if c := cmp.Compare(b.GetHeight(), a.GetHeight()); c != 0 {
				return c
			}
			if c := cmp.Compare(b.GetBandwidth(), a.GetBandwidth()); c != 0 {
				return c
			}
			if c := cmp.Compare(b.GetFrameRate(), a.GetFrameRate()); c != 0 {
				return c
			}
			return localeCompare(a.GetId(), b.GetId())
		})
	}
	return OnePerClass(videos, keep)
}

// AudioTracks lists audio tracks: a web page's in downexd's order (the original language first);
// a manifest's by language (undeclared last), then highest bandwidth first.
func AudioTracks(tracks []*downexv1.MediaTrack, site bool) []*downexv1.MediaTrack {
	var audios []*downexv1.MediaTrack
	for _, t := range tracks {
		if t.GetKind() == "audio" {
			audios = append(audios, t)
		}
	}
	if site {
		return audios
	}
	slices.SortStableFunc(audios, func(a, b *downexv1.MediaTrack) int {
		if (a.GetLanguage() == "") != (b.GetLanguage() == "") {
			if a.GetLanguage() != "" {
				return -1
			}
			return 1
		}
		if c := localeCompare(LanguageName(a.GetLanguage()), LanguageName(b.GetLanguage())); c != 0 {
			return c
		}
		if c := cmp.Compare(b.GetBandwidth(), a.GetBandwidth()); c != 0 {
			return c
		}
		if c := localeCompare(a.GetName(), b.GetName()); c != 0 {
			return c
		}
		return localeCompare(a.GetId(), b.GetId())
	})
	return audios
}

func trimRate(fps float64) string { return jsNumber(jsRound(fps*100) / 100) }

// TrackSizeText is a track's declared size: "412 MB", "~412 MB" for the site's estimate, ""
// when unknown.
func TrackSizeText(t *downexv1.MediaTrack) string {
	if t.GetSize() <= 0 {
		return ""
	}
	if t.GetSizeApprox() {
		return "~" + formatBytes(float64(t.GetSize()))
	}
	return formatBytes(float64(t.GetSize()))
}

// QualityName is a video quality's name: downexd's standard name ("4K (2160p)", "1080p (Full
// HD) 60fps", "480p"), else the standard name of its picture (an older downexd's "1634p", a
// manifest's "1920x1080"), else its name as it is; never a raw height. "" when nothing names
// it.
func QualityName(t *downexv1.MediaTrack) string { return QualityOf(t).Name }

// shownName is a quality's name, or what the track is when nothing names it.
func shownName(t *downexv1.MediaTrack) string {
	if name := QualityName(t); name != "" {
		return name
	}
	if t.GetKind() == "muxed" {
		return "Video and audio"
	}
	return "Video"
}

// VideoPictureText is the picture of a video track, the secondary detail beside its name:
// "3840×1634", "1920×1080 · 29.97 fps" (a manifest's, whose frame rate the name states only over
// 30); "" when unknown.
func VideoPictureText(t *downexv1.MediaTrack, site bool) string {
	picture := PictureText(t)
	if site || picture == "" || !(t.GetFrameRate() > 0) {
		return picture
	}
	return picture + " · " + trimRate(t.GetFrameRate()) + " fps"
}

// VideoTrackLabel is a video quality as the picker lists it: its standard name, its codecs by
// name and its bitrate, "1080p (Full HD) · H.264 · 5.0 Mbit/s" (a muxed one names both codecs:
// "480p · H.264 + AAC · 1.2 Mbit/s"). A web page's (site) gives the site's size where it has
// one: "1080p (Full HD) 60fps · H.264 · 412 MB", "360p · H.264 + AAC · ~12 MB". The picture is
// not part of it (VideoPictureText).
func VideoTrackLabel(t *downexv1.MediaTrack, site bool) string {
	return videoTrackLabel(t, site, TrackSizeText)
}

// videoTrackLabel is VideoTrackLabel with a site's size from size.
func videoTrackLabel(t *downexv1.MediaTrack, site bool, size func(*downexv1.MediaTrack) string) string {
	parts := []string{shownName(t)}
	if codecs := CodecNames(t.GetCodecs()); codecs != "" {
		parts = append(parts, codecs)
	}
	sz := ""
	if site {
		sz = size(t)
	}
	if sz == "" {
		sz = formatBitrate(float64(t.GetBandwidth()))
	}
	if sz != "" {
		parts = append(parts, sz)
	}
	return strings.Join(parts, " · ")
}

// SiteQualityLabel is a web page's quality as the picker lists it (SITE-MEDIA-SPEC §5.3), with
// the size of the file it makes: the video and the audio pick pairs it with (its audio, or the
// default one), as the browser's picker shows it: "4K (2160p) · AV1 · 860 MB", "360p · H.264 +
// AAC · ~12 MB".
func SiteQualityLabel(info *downexv1.MediaInfo, pick TrackPick, t *downexv1.MediaTrack) string {
	parts := []string{shownName(t)}
	if codecs := CodecNames(t.GetCodecs()); codecs != "" {
		parts = append(parts, codecs)
	}
	if NoSound(info, t) {
		parts = append(parts, NoSoundText)
	}
	size := PickSizeText(PickSizeOf(info, WithVideo(info, pick, t.GetId())))
	if size == "" {
		size = formatBitrate(float64(t.GetBandwidth()))
	}
	if size != "" {
		parts = append(parts, size)
	}
	return strings.Join(parts, " · ")
}

// NoSoundText is what the pickers say of a video without sound (SITE-MEDIA-SPEC §5.3,
// amendment 2026-11-18).
const NoSoundText = "No sound"

// NoSound reports a video-only track listed without any audio track: downexd lists one alone
// only when the page or stream has no sound (SITE-MEDIA-SPEC §3.11, VIDEO-DETECT-SPEC §3.3), so
// its file has none.
func NoSound(info *downexv1.MediaInfo, t *downexv1.MediaTrack) bool {
	if t.GetKind() != "video" {
		return false
	}
	for _, a := range info.GetTracks() {
		if a.GetKind() == "audio" {
			return false
		}
	}
	return true
}

// isAudioNoteWord is yt-dlp's quality words in a page's audio format note ("medium", "low,
// DRC"): not a name.
func isAudioNoteWord(w string) bool {
	switch jsLower(w) {
	case "ultralow", "low", "medium", "high", "drc":
		return true
	}
	return false
}

// AudioTrackTitle is an audio track's name: its own name with the language unless the name says
// it ("Deutsch (German)", "English original"). A page's track (site) is named by yt-dlp's format
// note, whose quality words ("medium", "low") are left out, so "medium" reads "Audio" (or its
// language); dynamic range compression is said as "· DRC".
func AudioTrackTitle(t *downexv1.MediaTrack, site bool) string {
	lang := LanguageName(t.GetLanguage())
	name := jsTrim(t.GetName())
	drc := false
	if site {
		var words []string
		for _, w := range strings.Split(name, ",") {
			if w = jsTrim(w); w != "" {
				if jsLower(w) == "drc" {
					drc = true
				}
				if !isAudioNoteWord(w) {
					words = append(words, w)
				}
			}
		}
		name = strings.Join(words, ", ")
	}
	title := name
	if title == "" {
		title = lang
	}
	if title == "" {
		title = "Audio"
	}
	if name != "" && lang != "" && !strings.Contains(jsLower(name), jsLower(lang)) {
		title = name + " (" + lang + ")"
	}
	if drc {
		return title + " · DRC"
	}
	return title
}

// AudioTrackLabel is "Commentary (English) · 128 kbit/s · mp4a.40.2". A web page's (site) names
// its codec and adds its size: "English · AAC · 129 kbit/s · 9.3 MB".
func AudioTrackLabel(t *downexv1.MediaTrack, site bool) string {
	return audioTrackLabel(t, site, TrackSizeText)
}

// audioTrackLabel is AudioTrackLabel with a site's size from size.
func audioTrackLabel(t *downexv1.MediaTrack, site bool, size func(*downexv1.MediaTrack) string) string {
	parts := []string{AudioTrackTitle(t, site)}
	if site && t.GetCodecs() != "" {
		parts = append(parts, CodecNames(t.GetCodecs()))
	}
	if rate := formatBitrate(float64(t.GetBandwidth())); rate != "" {
		parts = append(parts, rate)
	}
	if site {
		if sz := size(t); sz != "" {
			parts = append(parts, sz)
		}
	} else if t.GetCodecs() != "" {
		parts = append(parts, t.GetCodecs())
	}
	return strings.Join(parts, " · ")
}

// TrackByID is the track with id; nil for "" and for an id that is not listed.
func TrackByID(info *downexv1.MediaInfo, id string) *downexv1.MediaTrack {
	if id == "" {
		return nil
	}
	for _, t := range info.GetTracks() {
		if t.GetId() == id {
			return t
		}
	}
	return nil
}

// TrackPick is a choice of tracks: Video "" = none (none offered, or downexd's audio-only pick of
// a manifest); Audio "" = none (or in the muxed video).
type TrackPick struct {
	Video, Audio string
}

// bestAudio is the best audio track: a web page's first (downexd's default order), else the
// highest bandwidth.
func bestAudio(info *downexv1.MediaInfo) *downexv1.MediaTrack {
	var best *downexv1.MediaTrack
	for _, t := range info.GetTracks() {
		if t.GetKind() != "audio" {
			continue
		}
		if IsSite(info) {
			return t
		}
		if best == nil || t.GetBandwidth() > best.GetBandwidth() {
			best = t
		}
	}
	return best
}

// AudioOnlyOffered reports whether "no video" can be chosen. A video track "" asks downexd for
// its own pick, so it means audio only when that pick is: downexd leaves out video tracks it
// cannot save (e.g. WebM or SegmentBase DASH Representations) although the stream still lists
// them. Never for a web page: its video is always one MP4 file with a picture.
func AudioOnlyOffered(info *downexv1.MediaInfo) bool {
	if IsSite(info) {
		return false
	}
	return info.GetSelectedVideo() == "" && TrackByID(info, info.GetSelectedAudio()).GetKind() == "audio"
}

// DefaultPick is the picker's starting point: downexd's pick where it names offered tracks (a
// manifest's audio-only pick included), else the best video and, unless that is muxed, the best
// audio. info nil picks nothing.
func DefaultPick(info *downexv1.MediaInfo) TrackPick {
	if info == nil {
		return TrackPick{}
	}
	if AudioOnlyOffered(info) {
		return TrackPick{Audio: info.GetSelectedAudio()}
	}
	video := TrackByID(info, info.GetSelectedVideo())
	if video == nil || !IsVideoTrack(video) {
		video = nil
		if list := VideoTracks(info.GetTracks(), IsSite(info), info.GetSelectedVideo()); len(list) > 0 {
			video = list[0]
		}
	}
	if video.GetKind() == "muxed" {
		return TrackPick{Video: video.GetId()}
	}
	audio := TrackByID(info, info.GetSelectedAudio())
	if audio.GetKind() != "audio" {
		audio = bestAudio(info)
	}
	return TrackPick{Video: video.GetId(), Audio: audio.GetId()}
}

// NeedsAudio reports whether a pick of video needs an audio track of its own (its video is not
// muxed).
func NeedsAudio(info *downexv1.MediaInfo, video string) bool {
	if TrackByID(info, video).GetKind() == "muxed" {
		return false
	}
	return slices.ContainsFunc(info.GetTracks(), func(t *downexv1.MediaTrack) bool { return t.GetKind() == "audio" })
}

// WithVideo is pick with another video track; an audio track is chosen when the new one needs
// it.
func WithVideo(info *downexv1.MediaInfo, pick TrackPick, video string) TrackPick {
	audio := pick.Audio
	if NeedsAudio(info, video) && TrackByID(info, pick.Audio) == nil {
		audio = bestAudio(info).GetId()
	}
	return TrackPick{Video: video, Audio: audio}
}

// PickedAudio is the audio of a pick as sent to downexd: none with a muxed video.
func PickedAudio(info *downexv1.MediaInfo, pick TrackPick) string {
	if NeedsAudio(info, pick.Video) {
		return pick.Audio
	}
	return ""
}

// EstimatedBytes is the size a pick will have from the tracks' declared bandwidth and the
// duration; -1 when either is unknown. Only an estimate: declared bandwidths are peaks or
// averages.
func EstimatedBytes(info *downexv1.MediaInfo, pick TrackPick) int64 {
	if info.GetDurationMs() <= 0 {
		return -1
	}
	video := TrackByID(info, pick.Video)
	audio := TrackByID(info, PickedAudio(info, pick))
	bits := float64(video.GetBandwidth()) + float64(audio.GetBandwidth())
	if !(bits > 0) || video != nil && !(video.GetBandwidth() > 0) {
		return -1
	}
	return int64(jsRound(bits * float64(info.GetDurationMs()) / 8000))
}

// SizeKind is how sure a pick's size is.
type SizeKind uint8

const (
	// SizeUnknown: nothing says it (bytes -1).
	SizeUnknown SizeKind = iota
	// SizeExact: the sum of the chosen tracks' declared sizes.
	SizeExact
	// SizeApprox: a web page's tracks with the site's estimate among them.
	SizeApprox
	// SizeEstimate: downexd's (a manifest track measured from a few segments) or bandwidth ×
	// duration.
	SizeEstimate
)

// PickSize is the size of a pick (SITE-MEDIA-SPEC §5.3).
type PickSize struct {
	Bytes int64
	Kind  SizeKind
}

// PickSizeOf is the size of a pick: the sum of the chosen tracks' sizes when every one has one
// (exact; when any is an estimate, approx for a web page's, whose site estimated it, and
// estimate for a manifest's, which downexd measured from a few segments of a track without a
// bandwidth), else the bandwidth × duration estimate, else unknown (bytes -1).
func PickSizeOf(info *downexv1.MediaInfo, pick TrackPick) PickSize {
	var chosen []*downexv1.MediaTrack
	for _, t := range []*downexv1.MediaTrack{TrackByID(info, pick.Video), TrackByID(info, PickedAudio(info, pick))} {
		if t != nil {
			chosen = append(chosen, t)
		}
	}
	if len(chosen) > 0 && !slices.ContainsFunc(chosen, func(t *downexv1.MediaTrack) bool { return t.GetSize() <= 0 }) {
		var sum int64
		approx := false
		for _, t := range chosen {
			sum += t.GetSize()
			approx = approx || t.GetSizeApprox()
		}
		switch {
		case !approx:
			return PickSize{sum, SizeExact}
		case IsSite(info):
			return PickSize{sum, SizeApprox}
		}
		return PickSize{sum, SizeEstimate}
	}
	if estimate := EstimatedBytes(info, pick); estimate > 0 {
		return PickSize{estimate, SizeEstimate}
	}
	return PickSize{-1, SizeUnknown}
}

// PickSizeText is "412 MB", "~412 MB" (the site's estimate), "about 412 MB" (downexd's: from the
// bitrate or measured); "" when unknown.
func PickSizeText(size PickSize) string {
	switch size.Kind {
	case SizeExact:
		return formatBytes(float64(size.Bytes))
	case SizeApprox:
		return "~" + formatBytes(float64(size.Bytes))
	case SizeEstimate:
		return "about " + formatBytes(float64(size.Bytes))
	}
	return ""
}

// ChosenTracks labels the tracks a media download uses, for its details: the video's with its
// picture ("4K (2160p) · AV1 · 860 MB · 3840×1634"). A web page's sizes are the part's when it
// saves one (ClipTrackSizeText, "~").
func ChosenTracks(info *downexv1.MediaInfo) (video, audio string) {
	site := IsSite(info)
	v := TrackByID(info, info.GetSelectedVideo())
	a := TrackByID(info, info.GetSelectedAudio())
	clip, dur := InfoClip(info), info.GetDurationMs()
	size := func(t *downexv1.MediaTrack) string { return ClipTrackSizeText(t, clip, dur) }
	if v != nil {
		video = videoTrackLabel(v, site, size)
		if picture := VideoPictureText(v, site); picture != "" {
			video += " · " + picture
		}
	}
	switch {
	case a != nil:
		audio = audioTrackLabel(a, site, size)
	case v.GetKind() == "muxed":
		audio = "In the video track"
	}
	return video, audio
}

// SiteQualityText is the chosen quality of a web page's video in short: "4K (2160p) AV1 + AAC",
// "1080p (Full HD) 60fps H.264 + AAC" (a muxed quality names its own codecs); "" when no video
// track is chosen.
func SiteQualityText(info *downexv1.MediaInfo) string {
	video := TrackByID(info, info.GetSelectedVideo())
	if video == nil {
		return ""
	}
	var audio *downexv1.MediaTrack
	if video.GetKind() != "muxed" {
		audio = TrackByID(info, info.GetSelectedAudio())
	}
	var codecs []string
	if c := CodecNames(video.GetCodecs()); c != "" {
		codecs = append(codecs, c)
	}
	if audio != nil {
		c := CodecNames(audio.GetCodecs())
		if c == "" {
			c = "audio"
		}
		codecs = append(codecs, c)
	}
	var parts []string
	if name := QualityName(video); name != "" {
		parts = append(parts, name)
	}
	if c := strings.Join(codecs, " + "); c != "" {
		parts = append(parts, c)
	}
	return strings.Join(parts, " ")
}

// shortClass is the short class of a quality: "1080p (Full HD) 60fps" → "1080p", "4K (2160p)"
// → "4K".
func shortClass(t *downexv1.MediaTrack) string {
	name := QualityName(t)
	if cut := strings.Index(name, " ("); cut >= 0 {
		name = name[:cut]
	}
	return jsTrim(name)
}

// CodecSummary is the quality of a stream in plain words, for headers and the complete dialog
// (DESIGN-SYSTEM.md §4.8): "1080p · H.264 · AAC 128 kbit/s". It joins the short class, the video
// codec, and the audio codec with its bitrate; a muxed track's second codec stands for the audio
// (its bitrate is the whole track's, so none is shown). Audio only: "AAC 128 kbit/s". pick nil
// is the stream's selection. "" when nothing is known.
func CodecSummary(info *downexv1.MediaInfo, pick *TrackPick) string {
	chosen := TrackPick{Video: info.GetSelectedVideo(), Audio: info.GetSelectedAudio()}
	if pick != nil {
		chosen = *pick
	}
	video := TrackByID(info, chosen.Video)
	var parts []string
	audioText := ""
	if video != nil && IsVideoTrack(video) {
		if cls := shortClass(video); cls != "" {
			parts = append(parts, cls)
		}
		var codecs []string
		for _, c := range strings.Split(video.GetCodecs(), ",") {
			if c = jsTrim(c); c != "" {
				codecs = append(codecs, c)
			}
		}
		if len(codecs) > 0 {
			parts = append(parts, CodecName(codecs[0]))
		}
		if video.GetKind() == "muxed" && len(codecs) > 1 {
			audioText = CodecName(codecs[1])
		}
	}
	var audio *downexv1.MediaTrack
	if video.GetKind() != "muxed" {
		audio = TrackByID(info, chosen.Audio)
	}
	if audio != nil && audio.GetKind() == "audio" {
		name := ""
		if audio.GetCodecs() != "" {
			first, _, _ := strings.Cut(audio.GetCodecs(), ",")
			name = CodecName(first)
		}
		rate := formatBitrate(float64(audio.GetBandwidth()))
		if name == "" && rate != "" {
			name = "Audio"
		}
		var words []string
		for _, w := range []string{name, rate} {
			if w != "" {
				words = append(words, w)
			}
		}
		audioText = strings.Join(words, " ")
	}
	if audioText != "" {
		parts = append(parts, audioText)
	}
	return strings.Join(parts, " · ")
}

// LooksLikeManifest reports an address whose path names an HLS playlist (.m3u8) or a DASH
// manifest (.mpd).
func LooksLikeManifest(url string) bool {
	scheme, path, ok := schemeAndPath(jsTrim(url))
	if !ok || scheme != "http" && scheme != "https" {
		return false
	}
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".m3u8") || strings.HasSuffix(lower, ".mpd")
}
