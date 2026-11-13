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
	"Youtube":      "YouTube",
	"Twitter":      "X",
	"TwitchVod":    "Twitch",
	"TwitchClips":  "Twitch",
	"FacebookReel": "Facebook",
}

// SiteName is the display name of a yt-dlp extractor key: the site's name for the keys
// of siteNames ("YouTube" for "Youtube", "X" for "Twitter", ...), any other key as it is.
func SiteName(extractorKey string) string {
	if name, ok := siteNames[extractorKey]; ok {
		return name
	}
	return extractorKey
}

// CodecName is the name every UI shows for a codec string such as "avc1.640028"
// (docs/SITE-MEDIA-SPEC.md §3.11): H.264, AV1, HEVC, VP9, AAC, Opus, AC-3 or E-AC-3;
// "" for any other codec.
func CodecName(codec string) string {
	id, _, _ := strings.Cut(codec, ".")
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "avc1", "avc3":
		return "H.264"
	case "av01":
		return "AV1"
	case "hvc1", "hev1":
		return "HEVC"
	case "vp09", "vp9":
		return "VP9"
	case "mp4a":
		return "AAC"
	case "opus":
		return "Opus"
	case "ac-3":
		return "AC-3"
	case "ec-3":
		return "E-AC-3"
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
