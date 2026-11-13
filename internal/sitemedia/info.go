package sitemedia

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"downex/internal/media"
)

// Limits on yt-dlp's output and on what is offered (docs/SITE-MEDIA-SPEC.md §3.10,
// §3.11).
const (
	// MaxOutputBytes bounds the JSON that Parse reads (yt-dlp's stdout).
	MaxOutputBytes = 32 << 20
	// MaxFormats bounds the formats of one page; more make the output invalid.
	MaxFormats = 2_000
	// MaxOffered bounds the tracks of an offer, videos and audios together.
	MaxOffered = 200
)

// Values of Offer.Format and Offer.Container (= model.Media.Format and Container).
const (
	// FormatHTTP: direct formats, each a single resource fetched in byte ranges.
	FormatHTTP = "http"
	// FormatHLS and FormatDASH: the manifest fallback, an HLS playlist or a DASH MPD
	// downloaded as in docs/MEDIA-SPEC.md.
	FormatHLS  = "hls"
	FormatDASH = "dash"
	// ContainerMP4 is the container of every site download.
	ContainerMP4 = "mp4"
)

// Values of Pick.Output (the same strings as mediajob.OutputRemux and OutputCopy).
const (
	// OutputRemux joins the tracks (a video and an audio, or one fragmented muxed
	// track) into a progressive MP4.
	OutputRemux = "remux"
	// OutputCopy saves the one muxed MP4 track as it is.
	OutputCopy = "copy"
)

// Protocols of yt-dlp formats that Parse keeps (docs/SITE-MEDIA-SPEC.md §3.10).
const (
	protoHTTPS = "https"
	protoHTTP  = "http"
	protoHLS   = "m3u8_native"
	protoDASH  = "http_dash_segments"
)

// Info is one page as yt-dlp resolved it: the validated fields of its JSON output and
// its formats (docs/SITE-MEDIA-SPEC.md §3.10). Formats carry URLs, headers and cookies
// that are secrets of the run: an Info lives in memory only, and errors and logs never
// repeat them.
type Info struct {
	// ID is the site's id of the video; ExtractorKey yt-dlp's name of the extractor
	// (e.g. "Youtube"), and Site its display name (e.g. "YouTube").
	ID           string
	ExtractorKey string
	Site         string
	// Title is the cleaned title, or the ID when the page has none.
	Title string
	// Duration is 0 when unknown.
	Duration time.Duration
	// HasDRM reports yt-dlp's _has_drm; Protected counts the formats dropped because
	// they are DRM-protected. Either makes a page without usable formats encrypted.
	HasDRM    bool
	Protected int
	// Availability is yt-dlp's availability ("needs_auth", "premium_only", ... or ""
	// when unknown).
	Availability string
	// Formats are the formats that passed validation, in yt-dlp's order (worst first),
	// each format ID once.
	Formats []Format
}

// Format is one validated yt-dlp format (docs/SITE-MEDIA-SPEC.md §3.10).
type Format struct {
	// ID is the format_id, unique within the page; Key the format's identity across
	// resolutions (Key).
	ID  string
	Key string
	// Kind is KindVideo (video only), KindAudio (audio only) or KindMuxed (both, or a
	// manifest format whose content is unknown).
	Kind media.TrackKind
	// Ext is the file type ("mp4", "m4a", "webm", ...; "" when absent or invalid).
	Ext string
	// Protocol is "https" or "http" (a direct format), "m3u8_native" or
	// "http_dash_segments" (a format of a manifest).
	Protocol string
	// URL is the resource of a direct format. ManifestURL is the playlist or MPD of a
	// manifest format (yt-dlp's manifest_url, else its url); "" for direct formats.
	URL         string
	ManifestURL string
	// VCodec and ACodec are the codecs as yt-dlp names them ("" when none or unknown).
	VCodec, ACodec string
	Width, Height  int
	FPS            float64 // 0 when unknown
	// TBR, VBR and ABR are the total, video and audio bit rates in kbit/s (0 =
	// unknown); ASR is the audio sampling rate in Hz.
	TBR, VBR, ABR float64
	ASR           int
	// Size is the exact size in bytes (0 = unknown); ApproxSize the site's estimate,
	// set only when Size is unknown.
	Size       int64
	ApproxSize int64
	// Container is yt-dlp's container ("mp4_dash", "m4a_dash", ... or "").
	Container string
	// HDR reports a dynamic range other than SDR.
	HDR bool
	// Language is a BCP 47-like tag ("" when absent or invalid); LanguagePreference
	// ranks the languages of the page (-1 when unknown, the original language highest).
	Language           string
	LanguagePreference int
	// Note is the cleaned format_note ("medium, DRC", "English original (default)", ...).
	Note string
	// Headers are the request headers of the format after the allow-list of
	// docs/SITE-MEDIA-SPEC.md §6.2, with a Cookie built from the format's cookies for
	// URL (direct formats) or ManifestURL (manifest formats) only.
	Headers http.Header
	// ChunkSize is downloader_options.http_chunk_size in bytes (0 = none; see
	// ChunkBytes).
	ChunkSize int64
	// AvailableAt is the earliest time the format may be requested when that was still
	// ahead at parse time (zero = at once).
	AvailableAt time.Time
	// Unranged reports yt-dlp's no_resume: the server cannot resume the resource.
	Unranged bool
	// Hints from the query of a direct format's URL: Expires is when the URL expires
	// (zero = unknown), Network the address family its ip parameter binds it to (""
	// | "tcp4" | "tcp6"), LMT its lmt parameter ("" = none), part of the Key.
	Expires time.Time
	Network string
	LMT     string
}

// Format returns the format with id, or nil.
func (i *Info) Format(id string) *Format {
	for k := range i.Formats {
		if i.Formats[k].ID == id {
			return &i.Formats[k]
		}
	}
	return nil
}

// direct reports a format fetched as a single resource.
func (f *Format) direct() bool { return f.Protocol == protoHTTPS || f.Protocol == protoHTTP }

// fragmented reports a format in yt-dlp's DASH container: a fragmented MP4 (a
// "*_dash" container with the file type mp4 or m4a).
func (f *Format) fragmented() bool {
	return strings.HasSuffix(f.Container, "_dash") && (f.Ext == "mp4" || f.Ext == "m4a")
}

// The video codec families Downex saves as MP4, and the audio codec families it pairs
// with them (docs/SITE-MEDIA-SPEC.md §3.11).
const (
	familyH264 = "h264"
	familyAV1  = "av1"
	familyHEVC = "hevc"
	familyAAC  = "aac"
	familyAC3  = "ac3"
	familyEAC3 = "eac3"
)

// codecFamily returns the family of a codec string such as "avc1.640028" or
// "mp4a.40.2" by its sample entry, "" for any other codec. Only the MP4 families are
// known; VP9, Opus and the rest are not saved and need no family.
func codecFamily(codec string) string {
	id, _, _ := strings.Cut(codec, ".")
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "avc1", "avc3":
		return familyH264
	case "av01":
		return familyAV1
	case "hvc1", "hev1":
		return familyHEVC
	case "mp4a":
		return familyAAC
	case "ac-3":
		return familyAC3
	case "ec-3":
		return familyEAC3
	}
	return ""
}

// mp4Video and mp4Audio report a codec of a family Downex saves as MP4 video or audio.
func mp4Video(codec string) bool {
	return slices.Contains([]string{familyH264, familyAV1, familyHEVC}, codecFamily(codec))
}

func mp4Audio(codec string) bool {
	return slices.Contains([]string{familyAAC, familyAC3, familyEAC3}, codecFamily(codec))
}

// Video and audio file types, which classify a format whose codecs are both unknown
// (docs/SITE-MEDIA-SPEC.md §3.10).
var (
	videoExts = []string{"mp4", "webm", "mov", "mkv", "m4v", "flv", "3gp", "ts"}
	audioExts = []string{"m4a", "mp3", "ogg", "opus", "aac", "flac", "wav"}
)
