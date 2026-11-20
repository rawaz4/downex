package media

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Parts of a video (clips, docs/MEDIA-SPEC.md §8). A clip is the span [Start, End) of
// a presentation in presentation time from its start; End 0 means to the end. It is
// chosen when a stream or site video is added or confirmed, and only what it needs is
// downloaded.

// Bounds of a clip (docs/MEDIA-SPEC.md §8 C1).
const (
	// MaxClip bounds Start and End.
	MaxClip = 168 * time.Hour
	// MinClipLen is the shortest part.
	MinClipLen = time.Second
	// clipEndSlack: an End at most this far past a known duration is taken as the end
	// (docs/MEDIA-SPEC.md §8 C1, docs/SITE-MEDIA-SPEC.md §5.3).
	clipEndSlack = time.Second
)

// The reasons of a refused clip (docs/MEDIA-SPEC.md §8 C2). internal/validate.ClipMs
// gives the same texts for the bounds it checks.
const (
	ClipReasonOrder  = "the part must start before it ends"
	ClipReasonShort  = "the part must be at least 1 second long"
	ClipReasonBounds = "times must be between 0 and 168 hours"
	// ClipReasonPastEnd is formatted with the duration (FormatClipTime).
	ClipReasonPastEnd = "the part ends after the video (%s)"
)

// ErrClip reports a clip that cannot be saved; every such error is a *ClipError.
var ErrClip = errors.New("media: invalid part")

// ClipError is a refused clip with its reason (one of the ClipReason texts).
type ClipError struct{ Reason string }

func (e *ClipError) Error() string { return ErrClip.Error() + ": " + e.Reason }
func (e *ClipError) Unwrap() error { return ErrClip }

// Clip is a part of a presentation: [Start, End) in presentation time; End 0 = to the
// end. The zero Clip is the whole presentation.
type Clip struct {
	Start time.Duration `json:"start,omitempty"`
	End   time.Duration `json:"end,omitempty"`
}

// IsZero reports the whole presentation.
func (c Clip) IsZero() bool { return c.Start == 0 && c.End == 0 }

// Normalize returns c for a presentation of the given duration (0 = unknown): an End
// at most 1 s past the duration, or at or past it, becomes 0 (to the end), and a clip
// from 0 to the end is the zero Clip (no part, docs/MEDIA-SPEC.md §8 C1).
func (c Clip) Normalize(duration time.Duration) Clip {
	if duration > 0 && c.End >= duration && c.End <= duration+clipEndSlack {
		c.End = 0
	}
	if c.Start == 0 && c.End == 0 {
		return Clip{}
	}
	return c
}

// Validate checks c against the bounds of docs/MEDIA-SPEC.md §8 C1/C2 and, when the
// duration is known (> 0), against it. The error is a *ClipError (ErrClip). The zero
// Clip is valid. Call it on a normalized clip.
func (c Clip) Validate(duration time.Duration) error {
	switch {
	case c.IsZero():
		return nil
	case c.Start < 0 || c.End < 0 || c.Start > MaxClip || c.End > MaxClip:
		return &ClipError{Reason: ClipReasonBounds}
	case c.End != 0 && c.Start >= c.End:
		return &ClipError{Reason: ClipReasonOrder}
	case c.End != 0 && c.End-c.Start < MinClipLen:
		return &ClipError{Reason: ClipReasonShort}
	}
	if duration > 0 {
		switch {
		case c.End > duration+clipEndSlack:
			return &ClipError{Reason: fmt.Sprintf(ClipReasonPastEnd, FormatClipTime(duration))}
		case c.End == 0 && c.Start >= duration:
			return &ClipError{Reason: ClipReasonOrder}
		case c.End == 0 && duration-c.Start < MinClipLen:
			return &ClipError{Reason: ClipReasonShort}
		}
	}
	return nil
}

// Contains reports whether presentation time t lies in the clip.
func (c Clip) Contains(t time.Duration) bool {
	return t >= c.Start && (c.End == 0 || t < c.End)
}

var errClipTime = errors.New("media: not a time")

// ParseClipTime parses a time of a clip (docs/MEDIA-SPEC.md §8, B2): "80" (seconds),
// "1:20", "01:20", "1:02:03", "1:20.5" (at most 3 fraction digits), "1m20s", "1h2m3s".
// Surrounding spaces are ignored. Minutes and seconds after a larger field are below
// 60. The result is between 0 and MaxClip.
func ParseClipTime(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 32 {
		return 0, errClipTime
	}
	var d time.Duration
	var err error
	switch {
	case strings.ContainsRune(s, ':'):
		d, err = parseColonTime(s)
	case strings.ContainsAny(s, "hmsHMS"):
		d, err = parseUnitTime(strings.ToLower(s))
	default:
		d, err = parseClipSeconds(s, false)
	}
	if err != nil {
		return 0, err
	}
	if d < 0 || d > MaxClip {
		return 0, errClipTime
	}
	return d, nil
}

// parseColonTime parses "m:ss", "h:mm:ss", each with an optional fraction on the
// seconds.
func parseColonTime(s string) (time.Duration, error) {
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, errClipTime
	}
	var d time.Duration
	for i, p := range parts {
		last := i == len(parts)-1
		if i > 0 && (len(p) < 2 || p[0] < '0' || p[0] > '9' || p[1] < '0' || p[1] > '9' || !last && len(p) != 2) {
			return 0, errClipTime // mm and ss are two digits
		}
		if last {
			sec, err := parseClipSeconds(p, i > 0)
			if err != nil {
				return 0, err
			}
			d += sec
			break
		}
		v, err := parseInt(p)
		if err != nil || i > 0 && v >= 60 {
			return 0, errClipTime
		}
		unit := time.Minute
		if len(parts) == 3 && i == 0 {
			unit = time.Hour
		}
		d += time.Duration(v) * unit
	}
	return d, nil
}

// parseUnitTime parses "1h2m3s", "1m20s", "45s", "2h", with an optional fraction on the
// seconds.
func parseUnitTime(s string) (time.Duration, error) {
	var d time.Duration
	seen := ""
	for s != "" {
		i := strings.IndexAny(s, "hms")
		if i <= 0 {
			return 0, errClipTime
		}
		unit := s[i]
		if strings.IndexByte(seen, unit) >= 0 || seen != "" && strings.IndexByte("hms", seen[len(seen)-1]) >= strings.IndexByte("hms", unit) {
			return 0, errClipTime // each unit once, in order
		}
		num := s[:i]
		bounded := seen != ""
		switch unit {
		case 's':
			v, err := parseClipSeconds(num, bounded)
			if err != nil {
				return 0, err
			}
			d += v
		default:
			v, err := parseInt(num)
			if err != nil || bounded && v >= 60 {
				return 0, errClipTime
			}
			if unit == 'h' {
				d += time.Duration(v) * time.Hour
			} else {
				d += time.Duration(v) * time.Minute
			}
		}
		seen += string(unit)
		s = s[i+1:]
	}
	return d, nil
}

// parseSeconds parses "80", "20", "20.5" (≤ 3 fraction digits); bounded: below 60.
func parseClipSeconds(s string, bounded bool) (time.Duration, error) {
	whole, frac, hasFrac := strings.Cut(s, ".")
	v, err := parseInt(whole)
	if err != nil || bounded && v >= 60 {
		return 0, errClipTime
	}
	d := time.Duration(v) * time.Second
	if hasFrac {
		if frac == "" || len(frac) > 3 {
			return 0, errClipTime
		}
		f, err := parseInt(frac)
		if err != nil {
			return 0, errClipTime
		}
		for range 3 - len(frac) {
			f *= 10
		}
		d += time.Duration(f) * time.Millisecond
	}
	return d, nil
}

// parseInt parses up to 9 ASCII digits.
func parseInt(s string) (int64, error) {
	if s == "" || len(s) > 9 {
		return 0, errClipTime
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, errClipTime
		}
	}
	return strconv.ParseInt(s, 10, 64)
}

// FormatClipTime shows a time of a clip: "m:ss" under an hour, "h:mm:ss" from an hour,
// with tenths only when they are not zero ("1:20.5"). Negative times show as 0:00.
func FormatClipTime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	tenths := int64(d%time.Second) / int64(100*time.Millisecond)
	total := int64(d / time.Second)
	h, m, s := total/3600, total/60%60, total%60
	var out string
	if h > 0 {
		out = fmt.Sprintf("%d:%02d:%02d", h, m, s)
	} else {
		out = fmt.Sprintf("%d:%02d", m, s)
	}
	if tenths > 0 {
		out += "." + strconv.FormatInt(tenths, 10)
	}
	return out
}

// FormatClipRange shows a clip as "1:20 – 2:45"; an open end shows the duration when
// it is known, else "end".
func FormatClipRange(c Clip, duration time.Duration) string {
	end := "end"
	switch {
	case c.End > 0:
		end = FormatClipTime(c.End)
	case duration > 0:
		end = FormatClipTime(duration)
	}
	return FormatClipTime(c.Start) + " – " + end
}

// clipSuffix matches a suffix ClipSuffix wrote at the end of a file name's stem.
var clipSuffix = regexp.MustCompile(` \((?:\d+h)?\d{2}m\d{2}s-(?:(?:\d+h)?\d{2}m\d{2}s|end)\)$`)

// ClipSuffixMaxBytes is the longest suffix ClipSuffix returns (" (168h00m00s-168h00m00s)").
const ClipSuffixMaxBytes = 24

// ClipSuffix is the part of a file name that names a clip (docs/MEDIA-SPEC.md §8 C19,
// B5): " (01m20s-02m45s)", times "MMmSSs" below an hour and "HhMMmSSs" from an hour,
// the start floored and the end ceiled to whole seconds, an open end "end". The zero
// Clip has none.
func ClipSuffix(c Clip) string {
	if c.IsZero() {
		return ""
	}
	end := "end"
	if c.End > 0 {
		end = suffixTime(int64((c.End + time.Second - 1) / time.Second))
	}
	return " (" + suffixTime(int64(c.Start/time.Second)) + "-" + end + ")"
}

func suffixTime(sec int64) string {
	h, m, s := sec/3600, sec/60%60, sec%60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	return fmt.Sprintf("%02dm%02ds", m, s)
}

// ClipName is the file name of a clip of a download named name (docs/MEDIA-SPEC.md §8
// C19): the ClipSuffix before the extension, replacing a suffix of the same pattern
// that is already there, so that a name never gets two. The zero Clip removes such a
// suffix. It is the one rule of the daemon, the CLI and the window.
func ClipName(name string, c Clip) string {
	ext := filepath.Ext(name)
	if len(ext) > 8 || strings.ContainsAny(ext, " ()") {
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	stem = clipSuffix.ReplaceAllString(stem, "")
	return stem + ClipSuffix(c) + ext
}

// StripClipSuffix removes a clip suffix from the stem of name, keeping its extension.
func StripClipSuffix(name string) string { return ClipName(name, Clip{}) }
