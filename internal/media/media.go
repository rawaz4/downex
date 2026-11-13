// Package media turns HTTP streaming manifests into uniform fragment plans
// (docs/MEDIA-SPEC.md §1–§3): HLS playlists (RFC 8216 and its revision
// draft-pantos-hls-rfc8216bis) and static MPEG-DASH MPDs (ISO/IEC 23009-1). It also
// refuses encrypted or DRM-protected content, including fMP4 initialization segments
// (ISO/IEC 14496-12 boxes), with ErrEncrypted.
//
// The package is pure parsing: it performs no network access. Callers fetch manifests
// and segments themselves and pass the bytes in. All input is untrusted. Every limit of
// MEDIA-SPEC §3 is checked before the memory it guards is allocated, malformed input
// yields an error rather than a panic, and every error wraps one of the sentinel errors
// below. Error messages never repeat URIs, which may carry credentials.
package media

import (
	"errors"
	"fmt"
	"time"
)

// Limits on untrusted input (docs/MEDIA-SPEC.md §3).
const (
	// MaxManifestBytes bounds a playlist or MPD.
	MaxManifestBytes = 8 << 20
	// MaxSegments bounds the media segments of one playlist, SegmentTimeline,
	// SegmentList or plan.
	MaxSegments = 100_000
	// MaxVariants bounds the variant streams and the renditions of an HLS master
	// playlist, and the Periods, AdaptationSets and Representations of an MPD.
	MaxVariants = 1_000
	// MaxURIBytes bounds every URI, before and after resolution and template expansion.
	MaxURIBytes = 32 << 10
	// MaxURLBytesTotal bounds the bytes of all distinct URL strings one parse or plan
	// produces, so that MaxSegments resolved URIs near MaxURIBytes each cannot exhaust
	// memory.
	MaxURLBytesTotal = 64 << 20
	// MaxXMLDepth bounds element nesting in an MPD.
	MaxXMLDepth = 32
	// MaxXMLElements bounds the number of elements in an MPD.
	MaxXMLElements = 1 << 20
	// MaxBoxDepth bounds box nesting walked by CheckInit.
	MaxBoxDepth = 16
	// MaxBoxes bounds the number of boxes CheckInit visits.
	MaxBoxes = 1 << 20
)

// Sentinel errors. Every error returned by this package wraps exactly one of them;
// test with errors.Is.
var (
	// ErrEncrypted reports encrypted or DRM-protected content (docs/MEDIA-SPEC.md §2),
	// which Downex refuses to download.
	ErrEncrypted = errors.New("media: content is encrypted or DRM-protected")
	// ErrLive reports a live presentation: a dynamic MPD, or an HLS media playlist that
	// may still grow.
	ErrLive = errors.New("media: live presentation")
	// ErrTooLarge reports input beyond the limits of docs/MEDIA-SPEC.md §3.
	ErrTooLarge = errors.New("media: input exceeds limits")
	// ErrMalformed reports input that violates its format.
	ErrMalformed = errors.New("media: malformed input")
	// ErrUnsupported reports a valid feature this package does not implement. It wraps
	// errors.ErrUnsupported.
	ErrUnsupported = fmt.Errorf("media: %w", errors.ErrUnsupported)
	// ErrNotFound reports a requested track that the manifest does not contain, or a
	// manifest without any playable track.
	ErrNotFound = errors.New("media: track not found")
)

// mediaError is a sentinel kind plus a message; it prints as "<kind>: <message>".
type mediaError struct {
	kind error
	msg  string
}

func (e *mediaError) Error() string { return e.kind.Error() + ": " + e.msg }
func (e *mediaError) Unwrap() error { return e.kind }

// errf returns an error of the given sentinel kind.
func errf(kind error, format string, args ...any) error {
	return &mediaError{kind: kind, msg: fmt.Sprintf(format, args...)}
}

// withContext prefixes the message of err, keeping its kind. Errors of unknown kind
// become ErrMalformed.
func withContext(err error, format string, args ...any) error {
	prefix := fmt.Sprintf(format, args...)
	var me *mediaError
	if errors.As(err, &me) {
		return &mediaError{kind: me.kind, msg: prefix + ": " + me.msg}
	}
	return &mediaError{kind: ErrMalformed, msg: prefix + ": " + err.Error()}
}

// Container is the format of a plan's fragments.
type Container string

// Containers.
const (
	ContainerTS      Container = "ts"   // MPEG-2 transport stream
	ContainerFMP4    Container = "fmp4" // fragmented ISO-BMFF (incl. CMAF)
	ContainerWebM    Container = "webm" // WebM / Matroska
	ContainerUnknown Container = "unknown"
)

// Fragment is one request of a plan.
type Fragment struct {
	// URL is the absolute URL to fetch (relative only when the manifest was parsed
	// without a base URL).
	URL string
	// ByteRange is the inclusive first and last byte to fetch; nil fetches the whole
	// resource.
	ByteRange *[2]int64
	// Duration is the media duration of the fragment; 0 for initialization data and
	// when unknown.
	Duration time.Duration
	// Index is the fragment's position in Plan.Fragments.
	Index int
	// Init marks initialization data (HLS EXT-X-MAP, DASH Initialization) that the
	// following media fragments need. HLS plans repeat it only when the map changes;
	// DASH plans give it once per Period.
	Init bool
	// Discontinuity marks a timestamp or format discontinuity before this fragment
	// (HLS EXT-X-DISCONTINUITY or a skipped EXT-X-GAP segment; a new DASH Period).
	Discontinuity bool
	// IndexRange is set on a fragment that is a whole DASH SegmentBase resource: the
	// inclusive byte range of its segment index ('sidx' box, Cues for WebM). The
	// fragment can be fetched whole; a downloader that wants one request per media
	// segment must fetch and parse that index first.
	IndexRange *[2]int64
	// InitRange is set on a whole DASH SegmentBase resource whose initialization data
	// lies inside it: the inclusive byte range of that data, which can be fetched and
	// checked with CheckInit before the rest.
	InitRange *[2]int64
}

// Plan is the ordered fragment list of one selected track.
type Plan struct {
	Container Container
	Fragments []Fragment
	// Duration is the sum of the media fragment durations.
	Duration time.Duration
	// SingleResource marks a plan of PlanRanged: its fragments are consecutive byte
	// ranges of one resource (or that whole resource), stored in one file at their
	// offsets rather than as files of their own. Fragment 0 is marked Init because it
	// is fetched and checked first; it may hold media too.
	SingleResource bool
}

// MediaFragments returns the number of fragments that are not initialization data.
// Every fragment of a SingleResource plan counts: each is a part of the media resource.
func (p *Plan) MediaFragments() int {
	if p.SingleResource {
		return len(p.Fragments)
	}
	n := 0
	for i := range p.Fragments {
		if !p.Fragments[i].Init {
			n++
		}
	}
	return n
}

// finish checks the fragments (they may come from hand-built playlists or MPDs),
// numbers them and sums their durations.
func (p *Plan) finish() error {
	var total time.Duration
	for i := range p.Fragments {
		f := &p.Fragments[i]
		switch {
		case f.URL == "":
			return errf(ErrMalformed, "fragment %d has no URL", i+1)
		case len(f.URL) > MaxURIBytes:
			return errf(ErrTooLarge, "fragment %d URL is longer than %d bytes", i+1, MaxURIBytes)
		case !validRange(f.ByteRange) || !validRange(f.IndexRange) || !validRange(f.InitRange):
			return errf(ErrMalformed, "fragment %d has an invalid byte range", i+1)
		case f.Duration < 0:
			return errf(ErrMalformed, "fragment %d has a negative duration", i+1)
		}
		f.Index = i
		total = addDuration(total, f.Duration)
	}
	p.Duration = total
	return nil
}

func validRange(r *[2]int64) bool {
	return r == nil || (r[0] >= 0 && r[1] >= r[0])
}

// addDuration adds non-negative durations, saturating instead of overflowing.
func addDuration(a, b time.Duration) time.Duration {
	if b > 0 && a > maxDuration-b {
		return maxDuration
	}
	return a + b
}

const maxDuration = time.Duration(1<<63 - 1)

// Sniff identifies a container from the first bytes of a fragment: an MPEG-2 TS sync
// byte (twice, 188 bytes apart, when enough bytes are given), an ISO-BMFF top-level box
// type, or the EBML header magic of WebM.
func Sniff(prefix []byte) Container {
	switch {
	case len(prefix) >= 4 && prefix[0] == 0x1A && prefix[1] == 0x45 && prefix[2] == 0xDF && prefix[3] == 0xA3:
		return ContainerWebM
	case len(prefix) >= 8 && isTopLevelBox(string(prefix[4:8])):
		return ContainerFMP4
	case len(prefix) >= 1 && prefix[0] == 0x47 && (len(prefix) <= 188 || prefix[188] == 0x47):
		return ContainerTS
	}
	return ContainerUnknown
}

func isTopLevelBox(t string) bool {
	switch t {
	case "ftyp", "styp", "moov", "moof", "sidx", "emsg", "prft":
		return true
	}
	return false
}
