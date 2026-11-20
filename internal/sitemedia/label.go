package sitemedia

import (
	"strings"
	"unicode/utf8"

	"downex/internal/media"
	"downex/internal/validate"
)

// siteNames are the display names of the yt-dlp extractor keys whose own spelling is not
// the site's name (docs/VIDEO-DETECT-SPEC.md §3.8); the keys match exactly.
var siteNames = map[string]string{
	"Youtube":              "YouTube",
	"Twitter":              "X",
	"TwitterBroadcast":     "X",
	"TwitchVod":            "Twitch",
	"TwitchClips":          "Twitch",
	"FacebookReel":         "Facebook",
	"FacebookPluginsVideo": "Facebook",
	"InstagramStory":       "Instagram",
	"KickClip":             "Kick",
	"KickVOD":              "Kick",
	"BiliBili":             "Bilibili",
	"NineGag":              "9GAG",
	"TelegramEmbed":        "Telegram",
	"LBRY":                 "Odysee",
	"FoxNews":              "Fox News",
	"Soundcloud":           "SoundCloud",
	// Site names of several words run together in the key (amendment 2026-11-19).
	"ApplePodcasts":       "Apple Podcasts",
	"GooglePodcasts":      "Google Podcasts",
	"AppleTrailers":       "Apple Trailers",
	"ABCNews":             "ABC News",
	"CBSNews":             "CBS News",
	"NBCNews":             "NBC News",
	"SkyNews":             "Sky News",
	"WashingtonPost":      "Washington Post",
	"NYTimes":             "New York Times",
	"BBCCoUk":             "BBC",
	"TedTalk":             "TED",
	"DailyMotion":         "Dailymotion",
	"RumbleEmbed":         "Rumble",
	"BandcampAlbum":       "Bandcamp",
	"SoundcloudPlaylist":  "SoundCloud",
	"SoundcloudSet":       "SoundCloud",
	"PinterestCollection": "Pinterest",
}

// hostNamed are the extractor keys of yt-dlp's readers of any page (no site of their
// own): such a page is named by its host (PageSiteName).
var hostNamed = map[string]bool{"Generic": true, "HTML5MediaEmbed": true}

// SiteName is the display name of a yt-dlp extractor key: the site's name for the keys
// of siteNames ("YouTube" for "Youtube", "X" for "Twitter", ...), any other key as it is.
func SiteName(extractorKey string) string {
	if name, ok := siteNames[extractorKey]; ok {
		return name
	}
	return extractorKey
}

// PageSiteName is the display name of a resolved page (docs/VIDEO-DETECT-SPEC.md §3.8,
// amendment 2026-11-18): SiteName of its extractor key, except that a page read by one
// of yt-dlp's readers of any page ("Generic", "HTML5MediaEmbed") is named by pageHost,
// lower case, without a port and without a leading "www."; the key when the host is
// empty.
func PageSiteName(extractorKey, pageHost string) string {
	if !hostNamed[extractorKey] {
		return SiteName(extractorKey)
	}
	host := strings.TrimPrefix(normalizeHost(pageHost), "www.")
	if host == "" || len(host) > 64 {
		return SiteName(extractorKey)
	}
	return host
}

// familyNames are the names of the MP4 codec families.
var familyNames = map[string]string{
	familyH264: "H.264", familyAV1: "AV1", familyHEVC: "HEVC",
	familyAAC: "AAC", familyAC3: "AC-3", familyEAC3: "E-AC-3",
}

// CodecName is the name every UI shows for a codec string such as "avc1.640028" or a
// plain name such as "h264" (docs/SITE-MEDIA-SPEC.md §3.11): H.264, AV1, HEVC, VP9, AAC,
// Opus, AC-3 or E-AC-3; "" for any other codec.
func CodecName(codec string) string {
	if f := codecFamily(codec); f != "" {
		return familyNames[f]
	}
	id, _, _ := strings.Cut(codec, ".")
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "vp09", "vp9":
		return "VP9"
	case "opus":
		return "Opus"
	case "mp3":
		return "MP3"
	}
	return ""
}

// QualityLabel describes a pick as the completion view and the CLI show it, e.g.
// "1080p (Full HD) 60fps H.264 + AAC" for a video and an audio track, or "360p H.264 +
// AAC" for a muxed one: the video's Name (its standard quality name), then the names of
// the known codecs of both tracks. Either track may be nil.
func QualityLabel(video, audio *media.Track) string {
	var parts, codecs []string
	for _, t := range []*media.Track{video, audio} {
		if t == nil {
			continue
		}
		for c := range strings.SplitSeq(t.Codecs, ",") {
			if name := CodecName(c); name != "" {
				codecs = append(codecs, name)
			}
		}
	}
	if video != nil && video.Name != "" {
		parts = append(parts, video.Name)
	}
	if len(codecs) > 0 {
		parts = append(parts, strings.Join(codecs, " + "))
	}
	return strings.Join(parts, " ")
}

// cleanText cleans a title or a format note from the page: bidi embeddings, overrides
// and isolates removed, then validate.Title (control characters become spaces, runs of
// space collapse), then cut to at most maxRunes characters. It is "" for text that is
// not valid UTF-8.
func cleanText(s string, maxRunes int) string {
	if !utf8.ValidString(s) {
		return ""
	}
	s = strings.Map(func(r rune) rune {
		if (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return -1
		}
		return r
	}, s)
	t, err := validate.Title(cutRunes(s, validate.MaxTitleRunes))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cutRunes(t, maxRunes))
}

// cutRunes returns the first n characters of s.
func cutRunes(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}
