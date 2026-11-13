package media

import (
	"math"
	"strconv"
	"strings"
)

// Standard quality names (docs/SITE-MEDIA-SPEC.md, amendment 2026-10-23): every UI
// names a video by its quality class, a step of the 16:9 ladder, never by its raw
// height. This file is the one place of that rule in Go; the extension
// (ext/src/lib) and the desktop (ui/frontend/src/lib) show the names the daemon gives
// and repeat the rule (lib/quality.ts, one file in both) only where they must name a
// picture themselves, tested with this package's tables; quality_sync_test.go holds the
// copies and the tables together.

// qualityLadder is the ladder of quality classes, lowest first.
var qualityLadder = [...]int{144, 240, 360, 480, 720, 1080, 1440, 2160, 4320}

// QualityClass returns the quality class of a picture: the ladder step nearest to its
// longer side scaled to 16:9, that is max(height, round(width × 9 / 16)) for a
// landscape (or square) picture and max(width, round(height × 9 / 16)) for a portrait
// one, halves rounded up and a tie between two steps going to the higher. So a
// 3840x1634 film is 2160 (4K), a 1080x1920 portrait video 1080 and a 1440x1080 one
// 1080. With only one side known it is that side (a height) or the width scaled to
// 16:9; 0 when neither is known.
func QualityClass(width, height int) int {
	width, height = max(width, 0), max(height, 0)
	var side int
	switch {
	case width == 0:
		side = height
	case height == 0:
		side = scale169(width)
	case width >= height:
		side = max(height, scale169(width))
	default:
		side = max(width, scale169(height))
	}
	if side == 0 {
		return 0
	}
	for i, step := range qualityLadder[:len(qualityLadder)-1] {
		next := qualityLadder[i+1]
		if side < next {
			if side-step < next-side {
				return step
			}
			return next
		}
	}
	return qualityLadder[len(qualityLadder)-1]
}

// scale169 is round(n × 9 / 16), halves up, in integers (n ≤ 16,384 in practice; larger
// values cannot overflow below 2⁵⁹).
func scale169(n int) int {
	if n > math.MaxInt/16 {
		return math.MaxInt / 16
	}
	return (n*9 + 8) / 16
}

// isQualityClass reports a ladder step.
func isQualityClass(n int) bool {
	for _, step := range qualityLadder {
		if n == step {
			return true
		}
	}
	return false
}

// ClassName names a quality class: "8K (4320p)", "4K (2160p)", "1440p (2K)",
// "1080p (Full HD)", "720p (HD)", else "<class>p", followed by " <fps>fps" when the frame
// rate rounds to more than 30 (59.94 → " 60fps") and " HDR" for a high dynamic range,
// e.g. "4K (2160p) 60fps HDR". It is "" for 0 and for anything that is not a ladder step.
func ClassName(class int, fps float64, hdr bool) string {
	if !isQualityClass(class) {
		return ""
	}
	name := classLabels[class]
	if name == "" {
		name = strconv.Itoa(class) + "p"
	}
	if r := math.Round(fps); r > 30 && !math.IsInf(r, 0) {
		name += " " + strconv.FormatFloat(r, 'f', -1, 64) + "fps"
	}
	if hdr {
		name += " HDR"
	}
	return name
}

// QualityName names a picture: ClassName of its QualityClass, e.g. "4K (2160p)" for
// 3840x1634 or "1080p (Full HD) 60fps" for 1920x1080 at 60 frames per second; "" when
// neither side is known. The picture's own size stays secondary detail (a tooltip, the
// details), never the name.
func QualityName(width, height int, fps float64, hdr bool) string {
	return ClassName(QualityClass(width, height), fps, hdr)
}

// LabeledClass is the quality class of a video that a site labels itself (yt-dlp's
// format_note for YouTube): the class its label names (LabelClass), unless the label
// only restates the picture's raw height while the picture is of another class (a
// "480p" of 1138x480 is 720p by the rule, as TrackName shows an older daemon's raw
// heights), else QualityClass of the picture.
func LabeledClass(label string, width, height int) int {
	picture := QualityClass(width, height)
	if c := LabelClass(label); c > 0 && (c != height || c == picture) {
		return c
	}
	return picture
}

// classLabels are the names of the classes that have a common name of their own.
var classLabels = map[int]string{
	4320: "8K (4320p)",
	2160: "4K (2160p)",
	1440: "1440p (2K)",
	1080: "1080p (Full HD)",
	720:  "720p (HD)",
}

// ParseName takes apart a name of ClassName's form ("4K (2160p) 60fps HDR"): its class,
// the frame rate it states (0 when it states none) and HDR. ok is false for anything
// else, such as an older daemon's "1634p", "1080p60" and "2160p60 HDR" or a manifest's
// "1920x1080". Spaces around the name are ignored.
func ParseName(name string) (class, fps int, hdr, ok bool) {
	name = strings.TrimSpace(name)
	name, hdr = strings.CutSuffix(name, " HDR")
	if base, rate, found := cutLast(name, " "); found && strings.HasSuffix(rate, "fps") {
		digits := strings.TrimSuffix(rate, "fps")
		if len(digits) < 2 || digits[0] == '0' || !allDigits(digits, 3) {
			return 0, 0, false, false
		}
		fps, _ = strconv.Atoi(digits)
		name = base
	}
	for c, label := range classLabels {
		if name == label {
			return c, fps, hdr, true
		}
	}
	if digits, found := strings.CutSuffix(name, "p"); found && allDigits(digits, 3) {
		if c, _ := strconv.Atoi(digits); isQualityClass(c) && classLabels[c] == "" && digits[0] != '0' {
			return c, fps, hdr, true
		}
	}
	return 0, 0, false, false
}

// cutLast is strings.Cut at the last sep.
func cutLast(s, sep string) (before, after string, found bool) {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}

// TrackName is the name every UI shows for a video or muxed track: the CLI's here, the
// extension's and the desktop's trackQuality in lib/quality.ts. It is name when that is
// a standard name (ParseName), unless it is an older daemon's raw height (a class that
// is the picture's height while the picture is of another class: "480p" for 1138x480);
// else, when the picture is known, QualityName of it with the track's frame rate, HDR
// when the old name says so ("2160p60 HDR"); else name as it is, without spaces around
// it ("" stays "").
func TrackName(name string, width, height int, fps float64) string {
	class, _, _, ok := ParseName(name)
	picture := QualityClass(width, height)
	if ok && (class != height || class == picture) {
		return strings.TrimSpace(name)
	}
	if picture == 0 {
		return strings.TrimSpace(name)
	}
	return ClassName(picture, fps, hasWord(name, "HDR"))
}

// hasWord reports word in s as a whole word, case ignored.
func hasWord(s, word string) bool {
	s, word = strings.ToUpper(s), strings.ToUpper(word)
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(word)
		if (start == 0 || !wordByte(s[start-1])) && (end == len(s) || !wordByte(s[end])) {
			return true
		}
		i = start + 1
	}
}

// wordByte reports an ASCII letter, digit or underscore.
func wordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// LabelClass returns the class a site's own quality label names, such as yt-dlp's
// format_note for YouTube ("2160p", "1440p60", "1080p60 HDR", "1080p Premium"): its
// first word, "<n>p" with an optional frame rate, when n is a ladder step (case
// ignored); 0 for anything else ("1634p", "medium", "DASH video", "").
func LabelClass(label string) int {
	word, _, _ := strings.Cut(strings.TrimLeft(label, " ,"), " ")
	word, _, _ = strings.Cut(word, ",")
	digits, rate, ok := strings.Cut(strings.ToLower(word), "p")
	if !ok || !allDigits(digits, 4) || (rate != "" && !allDigits(rate, 3)) {
		return 0
	}
	n, err := strconv.Atoi(digits)
	if err != nil || !isQualityClass(n) {
		return 0
	}
	return n
}

// allDigits reports 1 to most ASCII digits.
func allDigits(s string, most int) bool {
	if s == "" || len(s) > most {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
