package sitemedia

import (
	"cmp"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"

	"downex/internal/media"
)

// Offer is what a page offers for download (docs/SITE-MEDIA-SPEC.md §3.11). Only what
// can be saved as one MP4 file is offered; nothing else is listed.
type Offer struct {
	// Format is FormatHTTP when the page has direct MP4 formats: Tracks lists them and
	// Select picks from them. It is FormatHLS or FormatDASH for the manifest fallback:
	// the caller probes ManifestURL with Headers (mediajob.ProbeMP4 with ManifestTracks:
	// fragmented MP4 and convertible MPEG-TS tracks), whose tracks, keys and default pick
	// are then the offer, and refuses a probed container other than mp4, or a
	// presentation without video, as media_unsupported.
	Format string
	// Container is always ContainerMP4.
	Container string
	// Tracks (FormatHTTP) are one MP4 video per quality class (classVideos), best class
	// first, and every MP4 video without a picture after them, then the MP4 audio
	// tracks, best first; at most MaxOffered.
	Tracks []media.Track
	// DefaultVideo is the first video track; DefaultAudio the first audio track when
	// the default video is video-only, else "".
	DefaultVideo, DefaultAudio string
	// ManifestURL and Headers (FormatHLS, FormatDASH) are the manifest to probe and the
	// headers of one of its formats (a Cookie only for the manifest's own host).
	ManifestURL string
	Headers     http.Header

	// more are the usable MP4 videos the one-per-class rule (or the cap) left out: Select
	// still finds them by key or ID (a pick of an earlier offer, or an ID the user
	// typed), and Listed then shows the chosen one. class is the quality class of every
	// video of Tracks and more, by ID.
	more  []media.Track
	class map[string]int
}

// Offers lists what a parsed page offers (docs/SITE-MEDIA-SPEC.md §3.11). pageHost is
// the host of the page URL that was resolved (a port is ignored): format and manifest
// URLs on localhost or on a loopback, link-local or unspecified address are usable
// only when they are on that same host (docs/SITE-MEDIA-SPEC.md §6.1).
//
// The offer is one direct MP4 video per quality class (a video-only format only when
// an MP4 audio exists to pair it with; classVideos) with the MP4 audios; without any,
// the manifest that most of the page's video formats share. A page with neither is
// refused: encrypted (media.ErrEncrypted) when it had DRM-protected formats,
// ErrAuthRequired when yt-dlp says the video needs an account, else
// media.ErrUnsupported ("no MP4 video").
func Offers(info *Info, pageHost string) (*Offer, error) {
	if info == nil {
		return nil, fmt.Errorf("%w: no page to offer", media.ErrMalformed)
	}
	host := normalizeHost(pageHost)
	var muxed, videoOnly []classVideo
	var audios []*Format
	for i := range info.Formats {
		f := &info.Formats[i]
		if !f.direct() || !allowedURL(f.URL, host) {
			continue
		}
		switch {
		case pairableVideo(f):
			videoOnly = append(videoOnly, classVideo{f: f, order: i})
		case pairableAudio(f):
			audios = append(audios, f)
		case muxedMP4(f):
			muxed = append(muxed, classVideo{f: f, order: i})
		}
	}
	usable := muxed
	if len(audios) > 0 {
		usable = append(usable, videoOnly...)
	}
	videos, more := classVideos(usable, info.ExtractorKey == youtubeKey)
	slices.SortFunc(audios, compareAudio)
	// At most MaxOffered tracks: the longer of the two lists loses its worst. Classed
	// videos are at most 9, so only audios and videos without a picture go; a video
	// that goes stays selectable.
	for len(videos)+len(audios) > MaxOffered {
		if len(audios) > len(videos) {
			audios = audios[:len(audios)-1]
			continue
		}
		more = append(more, videos[len(videos)-1])
		videos = videos[:len(videos)-1]
	}
	if len(videos) > 0 {
		off := &Offer{Format: FormatHTTP, Container: ContainerMP4, Tracks: make([]media.Track, 0, len(videos)+len(audios)),
			class: make(map[string]int, len(videos)+len(more))}
		for _, v := range videos {
			off.Tracks = append(off.Tracks, track(v.f, v.class))
			off.class[v.f.ID] = v.class
		}
		for _, f := range audios {
			off.Tracks = append(off.Tracks, track(f, 0))
		}
		for _, v := range more {
			off.more = append(off.more, track(v.f, v.class))
			off.class[v.f.ID] = v.class
		}
		off.DefaultVideo = videos[0].f.ID
		if videos[0].f.Kind == media.KindVideo {
			off.DefaultAudio = audios[0].ID
		}
		return off, nil
	}
	if off := manifestOffer(info, host); off != nil {
		return off, nil
	}
	switch {
	case info.Protected > 0 || info.HasDRM:
		return nil, fmt.Errorf("%w: the video is DRM-protected", media.ErrEncrypted)
	case info.Availability == "needs_auth":
		return nil, fmt.Errorf("%w: the video needs a signed-in account", ErrAuthRequired)
	case info.Availability == "premium_only", info.Availability == "subscriber_only":
		return nil, fmt.Errorf("%w: the video is for members only", ErrAuthRequired)
	}
	return nil, fmt.Errorf("%w: no MP4 video on this page", media.ErrUnsupported)
}

// manifestOffer is the manifest fallback: the manifest URL that the most video or
// muxed formats of HLS or DASH share (the first on a tie), with the headers of its
// first format; nil when there is none. Audio-only formats are not counted: audio
// alone is never a download.
func manifestOffer(info *Info, host string) *Offer {
	counts := make(map[string]int)
	var order []*Format
	for i := range info.Formats {
		f := &info.Formats[i]
		if f.direct() || f.Kind == media.KindAudio || !allowedURL(f.ManifestURL, host) {
			continue
		}
		if counts[f.ManifestURL] == 0 {
			order = append(order, f)
		}
		counts[f.ManifestURL]++
	}
	var best *Format
	for _, f := range order {
		if best == nil || counts[f.ManifestURL] > counts[best.ManifestURL] {
			best = f
		}
	}
	if best == nil {
		return nil
	}
	off := &Offer{Format: FormatHLS, Container: ContainerMP4, ManifestURL: best.ManifestURL, Headers: best.Headers.Clone()}
	if best.Protocol == protoDASH {
		off.Format = FormatDASH
	}
	return off
}

// ManifestTracks is the offer of every probed manifest (docs/VIDEO-DETECT-SPEC.md §3.3)
// and of the manifest fallback (docs/SITE-MEDIA-SPEC.md §3.11), the offer function of
// mediajob.ProbeMP4, which then plans the offered tracks, refuses those it cannot save
// (AC-3 in MPEG-TS, WebM, ...) and asks once more without them. Of a probed manifest's
// tracks it keeps the video, muxed and audio tracks whose codecs are all MP4 ones
// (H.264, AV1 or HEVC video; AAC, AC-3 or E-AC-3 audio; a track that names no codec is
// kept, its data decides) and one video per quality class (media.QualityClass of its
// picture; tracks without a picture have no class and are all kept, after the
// classes), chosen and ordered as direct formats are (SITE-MEDIA-SPEC amendment
// 2026-10-23): the best class first; within a class video-only before muxed, the higher
// frame rate, H.264 before HEVC before AV1 (before an unnamed codec), the higher
// bandwidth, the larger picture, the larger size. Then the audios, DEFAULT=YES first,
// then by bandwidth; ties keep the manifest's order. HDR is not known of manifest
// tracks. At most MaxOffered tracks are kept, the worst of the longer list dropped.
// tracks is not changed; the names are media.Track's (media.QualityName of the
// picture).
func ManifestTracks(tracks []media.Track) []media.Track {
	return manifestTracks(tracks, nil)
}

// ManifestTracksFor is the offer function of a probe that names a video: ManifestTracks,
// except that the video named by key (else by id, as mediajob finds a chosen track)
// stands in for the video of its class when the class rule leaves it out, so that a
// variant the list does not show can still be chosen (`downex media --video ID`, a
// stored pick). A named track that ManifestTracks never offers (another codec, audio)
// changes nothing. With neither id nor key it is ManifestTracks.
func ManifestTracksFor(id, key string) func([]media.Track) []media.Track {
	if id == "" && key == "" {
		return ManifestTracks
	}
	named := func(t media.Track) bool {
		if key != "" {
			return t.Key == key
		}
		return t.ID == id
	}
	return func(tracks []media.Track) []media.Track { return manifestTracks(tracks, named) }
}

// manifestTracks is ManifestTracks with the video that named accepts (nil: none) kept
// in place of the one of its class.
func manifestTracks(tracks []media.Track, named func(media.Track) bool) []media.Track {
	var videos, audios []media.Track
	for _, t := range tracks {
		switch {
		case !mp4Codecs(t):
		case t.Kind == media.KindVideo, t.Kind == media.KindMuxed:
			videos = append(videos, t)
		case t.Kind == media.KindAudio:
			audios = append(audios, t)
		}
	}
	slices.SortStableFunc(videos, compareManifestVideo)
	chosen, chosenClass := -1, -1
	if named != nil {
		if chosen = slices.IndexFunc(videos, named); chosen >= 0 {
			chosenClass = media.QualityClass(videos[chosen].Width, videos[chosen].Height)
		}
	}
	seen := make(map[int]bool)
	out := make([]media.Track, 0, len(videos)+len(audios))
	for _, t := range videos {
		c := media.QualityClass(t.Width, t.Height)
		switch {
		case c == 0:
		case seen[c]:
			continue
		case c == chosenClass:
			t = videos[chosen]
		}
		seen[c] = true
		out = append(out, t)
	}
	slices.SortStableFunc(audios, func(a, b media.Track) int {
		if a.Default != b.Default {
			return boolFirst(a.Default)
		}
		return cmp.Compare(b.Bandwidth, a.Bandwidth)
	})
	nv := len(out)
	out = append(out, audios...)
	for len(out) > MaxOffered {
		if na := len(out) - nv; na > nv {
			out = slices.Delete(out, len(out)-1, len(out))
			continue
		}
		at := nv - 1
		if named != nil && named(out[at]) && at > 0 {
			at-- // the named video stays
		}
		out = slices.Delete(out, at, at+1)
		nv--
	}
	return out
}

// mp4Codecs reports whether every codec a manifest track names is an MP4 one of its
// kind: video or audio families for video and muxed tracks, audio families for audio.
func mp4Codecs(t media.Track) bool {
	for c := range strings.SplitSeq(t.Codecs, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		if t.Kind == media.KindAudio && !mp4Audio(c) || !mp4Audio(c) && !mp4Video(c) {
			return false
		}
	}
	return true
}

// trackVideoFamily is the family of a manifest track's video codec ("" when it names
// none).
func trackVideoFamily(t media.Track) string {
	for c := range strings.SplitSeq(t.Codecs, ",") {
		if mp4Video(c) {
			return codecFamily(c)
		}
	}
	return ""
}

// compareManifestVideo orders manifest video tracks best first (ManifestTracks): the
// quality class; video-only before muxed; frame rate, rounded; codec (codecRank);
// bandwidth; picture area; size (a measured one).
func compareManifestVideo(a, b media.Track) int {
	if c := cmp.Compare(media.QualityClass(b.Width, b.Height), media.QualityClass(a.Width, a.Height)); c != 0 {
		return c
	}
	if am, bm := a.Kind == media.KindMuxed, b.Kind == media.KindMuxed; am != bm {
		return boolFirst(!am)
	}
	if c := cmp.Compare(math.Round(b.FrameRate), math.Round(a.FrameRate)); c != 0 {
		return c
	}
	if c := cmp.Compare(codecRank(trackVideoFamily(a)), codecRank(trackVideoFamily(b))); c != 0 {
		return c
	}
	if c := cmp.Compare(b.Bandwidth, a.Bandwidth); c != 0 {
		return c
	}
	if c := cmp.Compare(int64(b.Width)*int64(b.Height), int64(a.Width)*int64(a.Height)); c != 0 {
		return c
	}
	return cmp.Compare(b.Size, a.Size)
}

// codecRank ranks the video codec families for the one video of a quality class:
// H.264, which plays everywhere, then HEVC, then AV1, then an unnamed codec.
func codecRank(family string) int {
	switch family {
	case familyH264:
		return 0
	case familyHEVC:
		return 1
	case familyAV1:
		return 2
	}
	return 3
}

// pairableVideo reports a video-only fragmented MP4 of H.264, AV1 or HEVC.
func pairableVideo(f *Format) bool {
	return f.Kind == media.KindVideo && f.fragmented() && f.Ext == "mp4" && mp4Video(f.VCodec)
}

// pairableAudio reports an audio-only fragmented MP4 (m4a) of AAC, AC-3 or E-AC-3.
func pairableAudio(f *Format) bool {
	return f.Kind == media.KindAudio && f.fragmented() && mp4Audio(f.ACodec)
}

// muxedMP4 reports an MP4 with video and audio whose codecs are MP4 ones. A codec
// yt-dlp does not name (a site that only says "mp4") does not stop it: the file is
// saved as it is or remuxed without looking at its codecs.
func muxedMP4(f *Format) bool {
	return f.Kind == media.KindMuxed && f.Ext == "mp4" &&
		(f.VCodec == "" || mp4Video(f.VCodec)) && (f.ACodec == "" || mp4Audio(f.ACodec))
}

// youtubeKey is YouTube's extractor key: its formats' notes are YouTube's own quality
// labels ("2160p60 HDR"), which name the class (formatClass).
const youtubeKey = "Youtube"

// classVideo is a usable video format with its quality class (formatClass) and its
// place in Info.Formats (yt-dlp's order, worst first).
type classVideo struct {
	f     *Format
	class int
	order int
}

// formatClass is the quality class of a video format: for YouTube (youtube)
// media.LabeledClass of its format_note ("2160p", "1440p60", "1080p60 HDR" name the
// class, unless the label only restates the raw height of a picture of another class),
// else media.QualityClass of its picture; 0 when neither is known.
func formatClass(f *Format, youtube bool) int {
	if youtube {
		return media.LabeledClass(f.Note, f.Width, f.Height)
	}
	return media.QualityClass(f.Width, f.Height)
}

// classVideos sorts the usable videos best first (compareClassVideo) and splits them
// into the first of each quality class, which is offered, and the rest (docs/
// SITE-MEDIA-SPEC.md amendment 2026-10-23). Formats without a known picture have no
// class (0): none of them is left out for another, and they are offered after the
// classes.
func classVideos(videos []classVideo, youtube bool) (offered, rest []classVideo) {
	for i := range videos {
		videos[i].class = formatClass(videos[i].f, youtube)
	}
	slices.SortFunc(videos, compareClassVideo)
	seen := make(map[int]bool)
	for _, v := range videos {
		if v.class > 0 && seen[v.class] {
			rest = append(rest, v)
			continue
		}
		seen[v.class] = true
		offered = append(offered, v)
	}
	return offered, rest
}

// compareClassVideo orders videos best first, which makes the first of each class the
// one offered and the first offered the default video (docs/SITE-MEDIA-SPEC.md §3.11,
// amendment 2026-10-23): the class; then, within a class, video-only before muxed
// (muxed formats are offered only for a class without a video-only one); SDR before
// HDR (HDR only when the class has nothing else); the frame rate, rounded; H.264, which
// plays everywhere, before HEVC before AV1 (codecRank); bit rate; picture area; size;
// the format yt-dlp lists later (it lists them worst first).
func compareClassVideo(av, bv classVideo) int {
	a, b := av.f, bv.f
	if c := cmp.Compare(bv.class, av.class); c != 0 {
		return c
	}
	if am, bm := a.Kind == media.KindMuxed, b.Kind == media.KindMuxed; am != bm {
		return boolFirst(!am)
	}
	if a.HDR != b.HDR {
		return boolFirst(!a.HDR)
	}
	if c := cmp.Compare(math.Round(b.FPS), math.Round(a.FPS)); c != 0 {
		return c
	}
	if c := cmp.Compare(codecRank(codecFamily(a.VCodec)), codecRank(codecFamily(b.VCodec))); c != 0 {
		return c
	}
	if c := cmp.Compare(videoRate(b), videoRate(a)); c != 0 {
		return c
	}
	if c := cmp.Compare(int64(b.Width)*int64(b.Height), int64(a.Width)*int64(a.Height)); c != 0 {
		return c
	}
	if c := cmp.Compare(b.Size+b.ApproxSize, a.Size+a.ApproxSize); c != 0 {
		return c
	}
	return cmp.Compare(bv.order, av.order)
}

// compareAudio orders audios best first, which makes the first the default audio
// (docs/SITE-MEDIA-SPEC.md §3.11): the original language (language preference, then
// "original" in the note); not DRC; bit rate; sampling rate; format ID ascending.
func compareAudio(a, b *Format) int {
	if c := cmp.Compare(b.LanguagePreference, a.LanguagePreference); c != 0 {
		return c
	}
	if ao, bo := original(a), original(b); ao != bo {
		return boolFirst(ao)
	}
	if ad, bd := drc(a), drc(b); ad != bd {
		return boolFirst(!ad)
	}
	if c := cmp.Compare(audioRate(b), audioRate(a)); c != 0 {
		return c
	}
	if c := cmp.Compare(b.ASR, a.ASR); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// boolFirst orders a before b when first holds, b before a otherwise.
func boolFirst(first bool) int {
	if first {
		return -1
	}
	return 1
}

func original(f *Format) bool { return strings.Contains(strings.ToLower(f.Note), "original") }

func drc(f *Format) bool { return strings.HasSuffix(f.ID, "-drc") || strings.Contains(f.Note, "DRC") }

// videoRate and audioRate are a format's bit rate in kbit/s: tbr, else its video or
// audio rate.
func videoRate(f *Format) float64 { return cmp.Or(f.TBR, f.VBR+f.ABR) }

func audioRate(f *Format) float64 { return cmp.Or(f.ABR, f.TBR) }

// track is the offered track of a format; class is a video's quality class
// (formatClass), which names it.
func track(f *Format, class int) media.Track {
	t := media.Track{
		ID:        f.ID,
		Kind:      f.Kind,
		Bandwidth: int64(math.Round(videoRate(f) * 1000)),
		Width:     f.Width,
		Height:    f.Height,
		FrameRate: f.FPS,
		Language:  f.Language,
		Key:       f.Key,
		Ext:       f.Ext,
	}
	switch f.Kind {
	case media.KindAudio:
		t.Codecs = f.ACodec
		t.Name = f.Note
	case media.KindVideo:
		t.Codecs = f.VCodec
		t.Name = media.ClassName(class, f.FPS, f.HDR)
	default:
		t.Codecs = strings.Join(slices.DeleteFunc([]string{f.VCodec, f.ACodec}, func(s string) bool { return s == "" }), ", ")
		t.Name = media.ClassName(class, f.FPS, f.HDR)
	}
	if f.Size > 0 {
		t.Size = f.Size
	} else if f.ApproxSize > 0 {
		t.Size, t.SizeApprox = f.ApproxSize, true
	}
	return t
}

// allowedURL applies the address rule of docs/SITE-MEDIA-SPEC.md §6.1 to a format or
// manifest URL: a host that is localhost, a loopback, link-local or unspecified
// address, or a numeric host that is not a plain IP literal, is allowed only when it
// is the page's own host.
func allowedURL(raw, pageHost string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := normalizeHost(u.Hostname())
	switch {
	case host == "":
		return false
	case !localHost(host):
		return true
	}
	return host == pageHost
}

// normalizeHost lower-cases a host and drops its port, IPv6 brackets and final dot.
func normalizeHost(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// localHost reports a host that names this machine or its link: localhost and its
// subdomains, loopback, link-local and unspecified addresses (IPv4-mapped ones too),
// and all-numeric hosts in the forms some resolvers read as IPv4 ("127.1",
// "0x7f000001", "2130706433").
func localHost(h string) bool {
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if addr, err := netip.ParseAddr(h); err == nil {
		a := addr.Unmap()
		return a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsUnspecified()
	}
	return numericHost(h)
}

// numericHost reports 1 to 4 dot-separated parts, each decimal or 0x-hexadecimal.
func numericHost(h string) bool {
	parts := strings.Split(h, ".")
	if len(parts) > 4 {
		return false
	}
	for _, p := range parts {
		digits := strings.TrimPrefix(p, "0x")
		if digits == "" {
			return false
		}
		hex := len(digits) != len(p)
		for i := 0; i < len(digits); i++ {
			c := digits[i]
			if !(c >= '0' && c <= '9') && !(hex && (c >= 'a' && c <= 'f')) {
				return false
			}
		}
	}
	return true
}
