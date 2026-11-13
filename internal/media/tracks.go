package media

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"iter"
	"math"
	"strconv"
	"strings"
)

// maxTokenBytes bounds the short descriptive and numeric attributes that every track
// reads when it is classified or planned: codecs lists, MIME and content types, frame
// rates, and the numbers of inherited segment addressing. Real values are tens of
// bytes. Many tracks share one value (HLS renditions take the codecs of a variant; DASH
// Representations inherit from their AdaptationSet and Period), so without a bound one
// long value would cost its length once per track.
const maxTokenBytes = 1 << 10

// TrackKind classifies a Track.
type TrackKind string

// Track kinds.
const (
	KindVideo     TrackKind = "video"     // video only; pair it with an audio track
	KindAudio     TrackKind = "audio"     // audio only
	KindMuxed     TrackKind = "muxed"     // video and audio together, or unknown content
	KindSubtitles TrackKind = "subtitles" // subtitles or captions
)

// Track is a quality option for the picker.
type Track struct {
	// ID selects the track: an HLS track is "v<i>" (Variants[i]) or "r<i>"
	// (Renditions[i]); a DASH track is the Representation@id, the argument of PlanDASH.
	ID        string
	Kind      TrackKind
	Bandwidth int64 // bits per second; 0 when unknown
	Width     int
	Height    int
	FrameRate float64 // 0 when unknown
	Codecs    string
	Language  string
	// Name is what the pickers show: the standard quality name of a video or muxed track
	// whose picture is known (QualityName, "1080p (Full HD)"; HDR only where the source
	// says so), else an HLS rendition's NAME or a DASH Representation's Label.
	Name string
	// URL is the media playlist to fetch and plan with PlanHLS ("" for DASH).
	URL string
	// Group is an HLS rendition's GROUP-ID; AudioGroup is the AUDIO group of an HLS
	// variant, whose renditions are the audio that matches it.
	Group      string
	AudioGroup string
	// Default reports an HLS rendition with DEFAULT=YES.
	Default bool
	// Key identifies the track across fetches of the manifest where ID may not: the ID
	// of an HLS track is its position in the master playlist, which a server may list in
	// another order. It is a digest of the track's kind and attributes (without the URI,
	// which may carry session tokens), numbered among tracks of identical attributes, so
	// the keys of one playlist differ. It is "" where the ID is stable already: DASH
	// tracks (the Representation@id) and the single track of a media playlist. Site
	// tracks (docs/SITE-MEDIA-SPEC.md §3.11) carry their "s1-" key.
	Key string
	// Size is the track's size in bytes as a site declares it (0 = unknown), an estimate
	// when SizeApprox is set; Ext is the site's file type for the track, "mp4" or "m4a"
	// ("" = unknown). Site tracks (docs/SITE-MEDIA-SPEC.md §3.11) set them; manifest
	// options leave them zero, except that mediajob.ProbeMP4 sets Size of an HLS video
	// track without a BANDWIDTH (a bare media playlist's) from what it measured of its
	// segments, always with SizeApprox (the MP4 saved from them differs).
	Size       int64
	SizeApprox bool
	Ext        string
}

// Options lists the quality options of a playlist. A master playlist offers its
// variant streams and its renditions that have a URI (renditions without one are
// carried inside the variants). A media playlist is its own single option.
func (p *HLSPlaylist) Options() []Track {
	if !p.Master {
		return []Track{{ID: "v0", Kind: KindMuxed, URL: p.URL}}
	}
	// Renditions with a URI, per AUDIO group: a variant whose whole audio group
	// lives in separate playlists carries video only.
	separateAudio := make(map[string]bool)
	for _, r := range p.Renditions {
		if r.Type != "AUDIO" {
			continue
		}
		sep, seen := separateAudio[r.GroupID]
		separateAudio[r.GroupID] = (sep || !seen) && r.URI != ""
	}
	var audioCodecs map[string]string // per AUDIO group, built on first use
	keys := make(trackKeys)
	out := make([]Track, 0, len(p.Variants)+len(p.Renditions))
	for i, v := range p.Variants {
		t := Track{
			ID:         "v" + strconv.Itoa(i),
			Bandwidth:  v.Bandwidth,
			Width:      v.Width,
			Height:     v.Height,
			FrameRate:  v.FrameRate,
			Codecs:     v.Codecs,
			URL:        v.URI,
			AudioGroup: v.Audio,
			Key: keys.next("variant", strconv.FormatInt(v.Bandwidth, 10), v.Codecs,
				strconv.Itoa(v.Width), strconv.Itoa(v.Height), strconv.FormatFloat(v.FrameRate, 'g', -1, 64),
				v.Audio, v.Video, v.Subtitles, v.ClosedCaptions),
		}
		video, audio, text := codecKinds(v.Codecs)
		switch {
		case v.Audio != "" && separateAudio[v.Audio]:
			switch {
			case video, v.Width > 0 || v.Height > 0:
				t.Kind = KindVideo
			case audio:
				t.Kind = KindAudio
			default:
				t.Kind = KindMuxed // nothing says what it carries
			}
		case video && audio, v.Codecs == "":
			t.Kind = KindMuxed
		case video:
			t.Kind = KindVideo
		case audio:
			t.Kind = KindAudio
		case text:
			t.Kind = KindSubtitles
		default:
			t.Kind = KindMuxed
		}
		if t.Kind == KindVideo || t.Kind == KindMuxed {
			t.Name = QualityName(t.Width, t.Height, t.FrameRate, false)
		}
		out = append(out, t)
	}
	for i, r := range p.Renditions {
		if r.URI == "" {
			continue
		}
		t := Track{
			ID:       "r" + strconv.Itoa(i),
			Language: r.Language,
			Name:     r.Name,
			URL:      r.URI,
			Group:    r.GroupID,
			Default:  r.Default,
			Key: keys.next("rendition", r.Type, r.GroupID, r.Name, r.Language, r.AssocLanguage,
				r.Channels, r.Characteristics, r.InstreamID),
		}
		switch r.Type {
		case "AUDIO":
			t.Kind = KindAudio
			if audioCodecs == nil {
				audioCodecs = groupAudioCodecs(p.Variants)
			}
			t.Codecs = audioCodecs[r.GroupID]
		case "VIDEO":
			t.Kind = KindVideo
		case "SUBTITLES":
			t.Kind = KindSubtitles
		default:
			continue
		}
		out = append(out, t)
	}
	return out
}

// trackKeys numbers the tracks of one playlist by their attributes (Track.Key).
type trackKeys map[[sha256.Size]byte]int

// next returns the key of a track with the given attributes: "h" plus 24 hex digits of
// a digest of the attributes and of how many earlier tracks had the same ones.
func (k trackKeys) next(attrs ...string) string {
	h := sha256.New()
	for _, a := range attrs {
		// Length-prefixed, so that no two attribute lists hash alike.
		var n [binary.MaxVarintLen64]byte
		h.Write(n[:binary.PutUvarint(n[:], uint64(len(a)))])
		h.Write([]byte(a))
	}
	var base [sha256.Size]byte
	h.Sum(base[:0])
	seq := k[base]
	k[base] = seq + 1
	h.Write([]byte(strconv.Itoa(seq)))
	return "h" + hex.EncodeToString(h.Sum(nil)[:12])
}

// groupAudioCodecs maps each AUDIO group to the audio codecs that the first variant
// naming it declares. Each variant's CODECS is read at most once, however many
// renditions the group has.
func groupAudioCodecs(variants []Variant) map[string]string {
	groups := make(map[string]string)
	for i := range variants {
		v := &variants[i]
		if _, done := groups[v.Audio]; done {
			continue
		}
		var b strings.Builder
		for c := range codecList(v.Codecs) {
			if codecKind(c) == KindAudio {
				if b.Len() > 0 {
					b.WriteByte(',')
				}
				b.WriteString(c)
			}
		}
		groups[v.Audio] = b.String()
	}
	return groups
}

// codecList yields the non-empty entries of an RFC 6381 codecs list, trimmed, without
// allocating.
func codecList(codecs string) iter.Seq[string] {
	return func(yield func(string) bool) {
		for rest, more := codecs, true; more; {
			var c string
			c, rest, more = strings.Cut(rest, ",")
			if c = strings.TrimSpace(c); c != "" && !yield(c) {
				return
			}
		}
	}
}

// Options lists the Representations of an MPD, once per @id (the first Period that
// has it describes it). Image (thumbnail) Representations are left out.
func (m *MPD) Options() []Track {
	var out []Track
	seen := make(map[string]bool)
	for pi := range m.Periods {
		for ai := range m.Periods[pi].AdaptationSets {
			as := &m.Periods[pi].AdaptationSets[ai]
			for ri := range as.Representations {
				r := &as.Representations[ri]
				if seen[r.ID] {
					continue
				}
				seen[r.ID] = true
				kind, ok := dashKind(r)
				if !ok {
					continue
				}
				t := Track{
					ID:        r.ID,
					Kind:      kind,
					Bandwidth: r.Bandwidth,
					Width:     r.Width,
					Height:    r.Height,
					FrameRate: parseFrameRate(r.FrameRate),
					Codecs:    r.Codecs,
					Language:  r.Lang,
					Name:      r.Label,
				}
				if name := QualityName(t.Width, t.Height, t.FrameRate, false); name != "" && (kind == KindVideo || kind == KindMuxed) {
					t.Name = name
				}
				out = append(out, t)
			}
		}
	}
	return out
}

func dashKind(r *Representation) (TrackKind, bool) {
	video, audio, text := codecKinds(r.Codecs)
	switch {
	case video && audio:
		return KindMuxed, true
	case video:
		return KindVideo, true
	case audio:
		return KindAudio, true
	case text:
		return KindSubtitles, true
	}
	typ := strings.ToLower(r.ContentType)
	if typ == "" {
		typ, _, _ = strings.Cut(strings.ToLower(r.MimeType), "/")
	}
	mime := strings.ToLower(r.MimeType)
	switch {
	case typ == "image":
		return "", false
	case typ == "video":
		return KindVideo, true
	case typ == "audio":
		return KindAudio, true
	case typ == "text", strings.Contains(mime, "ttml"), strings.Contains(mime, "vtt"):
		return KindSubtitles, true
	}
	return KindMuxed, true
}

// parseFrameRate parses a DASH frame rate, "<n>" or "<n>/<d>" (xs:unsignedInt
// values); 0 when it is not one.
func parseFrameRate(s string) float64 {
	n, d, ok := strings.Cut(s, "/")
	num, err := parseDecimalInt(strings.TrimSpace(n))
	if err != nil || num > math.MaxUint32 {
		return 0
	}
	den := uint64(1)
	if ok {
		if den, err = parseDecimalInt(strings.TrimSpace(d)); err != nil || den == 0 || den > math.MaxUint32 {
			return 0
		}
	}
	return float64(num) / float64(den)
}

// codecKinds reports which media kinds an RFC 6381 codecs list contains.
func codecKinds(codecs string) (video, audio, text bool) {
	for c := range codecList(codecs) {
		switch codecKind(c) {
		case KindVideo:
			video = true
		case KindAudio:
			audio = true
		case KindSubtitles:
			text = true
		}
	}
	return
}

// codecKind classifies one codec by its sample entry or format identifier.
func codecKind(c string) TrackKind {
	id, _, _ := strings.Cut(strings.TrimSpace(c), ".")
	if len(id) > 8 {
		return "" // longer than every identifier below
	}
	switch strings.ToLower(id) {
	case "avc1", "avc2", "avc3", "avc4", "hvc1", "hev1", "dvh1", "dvhe", "dva1", "dvav", "av01",
		"vp08", "vp09", "vp8", "vp9", "mp4v", "vvc1", "vvi1", "theora", "mjpg":
		return KindVideo
	case "mp4a", "ac-3", "ec-3", "ac-4", "opus", "flac", "mp3", "alac", "vorbis", "dtsc", "dtse",
		"dtsh", "dtsl", "dtsx", "mha1", "mhm1", "ipcm", "fpcm", "aac":
		return KindAudio
	case "wvtt", "stpp", "tx3g", "c608", "c708", "webvtt", "vtt":
		return KindSubtitles
	}
	return ""
}

// DefaultPair picks the default selection from tracks: the best video (largest
// picture, then highest bandwidth, then frame rate) and, unless that track is muxed,
// the best audio for it (highest bandwidth, then DEFAULT=YES), preferring the audio
// group the video names. Without video it picks the best audio alone. Either result
// may be nil; non-nil results point into tracks.
func DefaultPair(tracks []Track) (video, audio *Track) {
	for i := range tracks {
		t := &tracks[i]
		if (t.Kind == KindVideo || t.Kind == KindMuxed) && (video == nil || betterVideo(t, video)) {
			video = t
		}
	}
	if video != nil && video.Kind == KindMuxed {
		return video, nil
	}
	pick := func(group string) *Track {
		var best *Track
		for i := range tracks {
			t := &tracks[i]
			if t.Kind != KindAudio || (group != "" && t.Group != group) {
				continue
			}
			if best == nil || betterAudio(t, best) {
				best = t
			}
		}
		return best
	}
	if video != nil && video.AudioGroup != "" {
		audio = pick(video.AudioGroup)
	}
	if audio == nil {
		audio = pick("")
	}
	return video, audio
}

func betterVideo(a, b *Track) bool {
	if pa, pb := int64(a.Width)*int64(a.Height), int64(b.Width)*int64(b.Height); pa != pb {
		return pa > pb
	}
	if a.Bandwidth != b.Bandwidth {
		return a.Bandwidth > b.Bandwidth
	}
	return a.FrameRate > b.FrameRate
}

func betterAudio(a, b *Track) bool {
	if a.Bandwidth != b.Bandwidth {
		return a.Bandwidth > b.Bandwidth
	}
	return a.Default && !b.Default
}
