package media

import (
	"net/http"
	"net/url"
	"time"
)

// Byte-range plans of site media (docs/SITE-MEDIA-SPEC.md §2.4). A web page resolved
// with yt-dlp yields formats that are single resources with a direct URL. Each chosen
// format is cut into consecutive byte ranges of a fixed chunk size, fetched with Range
// requests over several connections and written into one file at their offsets.

// Chunk sizes and size limits of ranged tracks (docs/SITE-MEDIA-SPEC.md §2.4).
const (
	// MinChunkBytes and MaxChunkBytes bound RangedTrack.ChunkBytes.
	MinChunkBytes = 1 << 20
	MaxChunkBytes = 64 << 20
	// DefaultChunkBytes is the chunk size when neither the format nor the host caps the
	// request length.
	DefaultChunkBytes = 16 << 20
	// MaxRangedSize bounds RangedTrack.Size.
	MaxRangedSize = 1 << 40
)

// RangedTrack is one format of a resolved web page: a single resource fetched in byte
// ranges. URL and Headers are secrets of the run (they may carry session tokens): they
// are never stored, and errors and logs never repeat them.
type RangedTrack struct {
	// ID is the site's format id; Key its identity across resolutions
	// (docs/SITE-MEDIA-SPEC.md §3.11), which finds the track again in a fresh resolution.
	ID  string
	Key string
	// Kind is KindVideo, KindAudio or KindMuxed.
	Kind TrackKind
	// Ext is the site's file type: "mp4" or "m4a".
	Ext string
	// URL is the absolute http(s) URL of the resource; it is never stored and never
	// logged beyond scheme://host/path.
	URL string
	// Headers are the request headers of this format (docs/SITE-MEDIA-SPEC.md §6.2); a
	// Cookie only for the URL's own domain.
	Headers http.Header
	// Size is the exact size in bytes; <= 0 when unknown, which makes the track one
	// whole-resource request.
	Size int64
	// ChunkBytes is the length of every range but the last, MinChunkBytes..MaxChunkBytes.
	// It is fixed for the life of the track's checkpoint, which records it.
	ChunkBytes int64
	// Expires is when the URL expires, when known (zero = unknown).
	Expires time.Time
	// Network is "", "tcp4" or "tcp6": direct connections for this track dial only that
	// address family ("" = any). Proxied connections are not affected.
	Network string
	// Unranged marks a resource the server cannot resume (yt-dlp's no_resume): one
	// connection, restarted from byte 0 when interrupted.
	Unranged bool
}

// PlanRanged plans a ranged track: its consecutive byte ranges of t.ChunkBytes (the
// last one shorter), in order, the first marked Init, so that it is fetched and checked
// before the others. A track of unknown size (Size <= 0) or an Unranged one is a single
// whole-resource fragment without a byte range, and its ChunkBytes is not used. The plan
// has SingleResource set, no durations, and the container of t.Ext (ContainerFMP4 for
// the ISO-BMFF file types "mp4" and "m4a", whether fragmented or not; else
// ContainerUnknown).
//
// PlanRanged refuses a URL that is not an absolute http(s) URL with a host, a Kind or
// Network outside their values, and a ChunkBytes outside MinChunkBytes..MaxChunkBytes
// (ErrMalformed); a URL longer than MaxURIBytes, a Size above MaxRangedSize, and more
// than MaxSegments ranges (ErrTooLarge). Errors never repeat the URL.
func PlanRanged(t RangedTrack) (*Plan, error) {
	switch t.Kind {
	case KindVideo, KindAudio, KindMuxed:
	default:
		return nil, errf(ErrMalformed, "a ranged track must be video, audio or muxed")
	}
	switch t.Network {
	case "", "tcp4", "tcp6":
	default:
		return nil, errf(ErrMalformed, "unknown network for a ranged track")
	}
	if err := checkRangedURL(t.URL); err != nil {
		return nil, err
	}
	if t.Size > MaxRangedSize {
		return nil, errf(ErrTooLarge, "track is larger than %d bytes", int64(MaxRangedSize))
	}
	p := &Plan{Container: rangedContainer(t.Ext), SingleResource: true}
	if t.Size <= 0 || t.Unranged {
		p.Fragments = []Fragment{{URL: t.URL, Init: true}}
	} else {
		if t.ChunkBytes < MinChunkBytes || t.ChunkBytes > MaxChunkBytes {
			return nil, errf(ErrMalformed, "chunk size %d is outside %d..%d bytes", t.ChunkBytes,
				int64(MinChunkBytes), int64(MaxChunkBytes))
		}
		n := (t.Size + t.ChunkBytes - 1) / t.ChunkBytes
		if n > MaxSegments {
			return nil, errf(ErrTooLarge, "track needs more than %d ranges of %d bytes", MaxSegments, t.ChunkBytes)
		}
		ranges := make([][2]int64, n)
		p.Fragments = make([]Fragment, n)
		for i := range p.Fragments {
			start := int64(i) * t.ChunkBytes
			ranges[i] = [2]int64{start, min(start+t.ChunkBytes, t.Size) - 1}
			p.Fragments[i] = Fragment{URL: t.URL, ByteRange: &ranges[i], Init: i == 0}
		}
	}
	if err := p.finish(); err != nil {
		return nil, err
	}
	return p, nil
}

// checkRangedURL accepts an absolute http(s) URL with a host.
func checkRangedURL(raw string) error {
	switch {
	case raw == "":
		return errf(ErrMalformed, "ranged track has no URL")
	case len(raw) > MaxURIBytes:
		return errf(ErrTooLarge, "ranged track URL is longer than %d bytes", MaxURIBytes)
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return errf(ErrMalformed, "ranged track URL does not parse")
	case u.Scheme != "http" && u.Scheme != "https":
		return errf(ErrMalformed, "ranged track URL is not http(s)")
	case u.Hostname() == "":
		return errf(ErrMalformed, "ranged track URL has no host")
	}
	return nil
}

// rangedContainer maps a site's file type to the container of its plan.
func rangedContainer(ext string) Container {
	switch ext {
	case "mp4", "m4a":
		return ContainerFMP4
	}
	return ContainerUnknown
}
