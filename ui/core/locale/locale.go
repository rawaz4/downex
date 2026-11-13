// Package locale formats numbers and instants the way today's window does through
// JavaScript's Intl.NumberFormat and Intl.DateTimeFormat in WebView2 (ui/frontend/src/lib/
// format.ts): fixed fraction digits with half-expand rounding, and the six date and time
// styles format.ts asks for, in a BCP 47 locale and an IANA time zone.
//
// On Windows (amd64 and 386) the formats come from the system's ICU (icu.dll, part of
// Windows since Windows 10 1903) through the same steps V8 takes: the locale's best pattern
// for the option skeleton, the requested hour cycle, a proleptic Gregorian calendar, and
// U+202F before AM/PM read as a space. On macOS (with cgo) they come from the system's ICU
// (libicucore) through the steps JavaScriptCore takes, as today's Mac window (WebKit's Intl)
// shows them, and the user's locale is WebKit's navigator.language (icu_darwin.go,
// user_darwin.go). Elsewhere, and where the system's ICU cannot be used, a pure Go formatter
// writes en-US forms in the requested time zone.
package locale

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Style is one of the date and time formats of format.ts (its dateFormat kinds).
type Style uint8

const (
	// Time is a time of day on a 24-hour clock with two-digit hours ("09:05"):
	// {hour: '2-digit', minute: '2-digit', hourCycle: 'h23'}.
	Time Style = iota
	// Clock is a time of day on the locale's own clock ("11:59", "11:59 AM"):
	// {hour: 'numeric', minute: '2-digit'}.
	Clock
	// MonthDay is a day of this year ("Oct 3"): {month: 'short', day: 'numeric'}.
	MonthDay
	// Date is a full date ("Oct 3, 2024"): {year: 'numeric', month: 'short', day: 'numeric'}.
	Date
	// YMD is the calendar day in digits, for telling days apart:
	// {year: 'numeric', month: '2-digit', day: '2-digit'}.
	YMD
	// Full is a date with the time of day to the second ("Oct 20, 2026, 09:05:07"):
	// {year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
	// second: '2-digit', hourCycle: 'h23'}.
	Full

	styleCount
)

// String names the style as format.ts does.
func (s Style) String() string {
	switch s {
	case Time:
		return "time"
	case Clock:
		return "clock"
	case MonthDay:
		return "monthDay"
	case Date:
		return "date"
	case YMD:
		return "ymd"
	case Full:
		return "full"
	}
	return fmt.Sprintf("Style(%d)", uint8(s))
}

// skeleton is the ICU skeleton V8 builds from the style's options, and whether the options
// set hourCycle 'h23'.
func (s Style) skeleton() (skeleton string, h23 bool) {
	switch s {
	case Time:
		return "HHmm", true
	case Clock:
		return "jmm", false
	case MonthDay:
		return "MMMd", false
	case Date:
		return "yMMMd", false
	case YMD:
		return "yMMdd", false
	case Full:
		return "yMMMdHHmmss", true
	}
	return "", false
}

// MaxTime is the largest distance from the epoch, in milliseconds, that an instant may have
// (ECMAScript's time range, ±8.64e15 ms).
const MaxTime = 8.64e15

// Formatter formats in one locale and time zone. Its methods are safe for concurrent use.
type Formatter interface {
	// Tag is the BCP 47 tag the formatter was made for.
	Tag() string
	// TimeZone is the IANA zone it formats instants in; "" for the system's zone.
	TimeZone() string
	// Number formats v with exactly digits fraction digits, rounding half away from zero
	// (Intl.NumberFormat with minimumFractionDigits = maximumFractionDigits = digits).
	// NaN and the infinities are written as Intl writes them.
	Number(v float64, digits int) string
	// Date formats the instant ms (milliseconds since the epoch, fractions dropped) in the
	// style. It returns "" when ms is NaN or outside ±MaxTime, where Intl throws.
	Date(ms float64, style Style) string
	// Year is the year part of the YMD style for the instant ms ("" when ms is not a time).
	Year(ms float64) string
}

// ErrUnsupported means the system formatter is not available on this platform or system.
var ErrUnsupported = errors.New("locale: no system formatter")

// New returns a formatter for the BCP 47 tag and IANA time zone ("" for the system's zone)
// backed by the system's ICU where there is one, else the pure Go formatter. An invalid tag
// or an unknown zone is an error, as Intl's RangeError.
func New(tag, timeZone string) (Formatter, error) {
	if err := checkTag(tag); err != nil {
		return nil, err
	}
	f, err := newSystem(tag, timeZone)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, ErrUnsupported) {
		return nil, err
	}
	return NewFallback(tag, timeZone)
}

// checkTag accepts what Intl accepts in practice: letters, digits and hyphens in subtags of
// 1 to 8 characters, starting with a 2 or 3 letter language (or "und"). Underscores ("de_DE")
// are read as hyphens by neither Intl nor this.
func checkTag(tag string) error {
	if tag == "" {
		return fmt.Errorf("locale: empty language tag")
	}
	for i, sub := range strings.Split(tag, "-") {
		if len(sub) < 1 || len(sub) > 8 {
			return fmt.Errorf("locale: invalid language tag %q", tag)
		}
		for _, c := range sub {
			alpha := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
			if !alpha && !(c >= '0' && c <= '9') {
				return fmt.Errorf("locale: invalid language tag %q", tag)
			}
			if i == 0 && !alpha {
				return fmt.Errorf("locale: invalid language tag %q", tag)
			}
		}
		if i == 0 && (len(sub) < 2 || len(sub) == 4) {
			return fmt.Errorf("locale: invalid language tag %q", tag)
		}
	}
	return nil
}

// timeClip is ECMAScript's TimeClip: NaN outside the time range, else ms without its fraction.
func timeClip(ms float64) (float64, bool) {
	if math.IsNaN(ms) || math.Abs(ms) > MaxTime {
		return 0, false
	}
	return math.Trunc(ms) + 0, true // + 0 turns -0 into +0
}

// spaceBeforeDayPeriod is V8's revert of ICU 72's U+202F between a time and AM/PM: date
// outputs read it as an ordinary space (crbug.com/1414292).
func spaceBeforeDayPeriod(s string) string {
	return strings.ReplaceAll(s, "\u202f", " ")
}

// withHourCycle rewrites the hour fields of an ICU pattern to the 0-23 hour ('H') outside
// quoted literals, as V8 does for hourCycle 'h23'.
func withHourCycle(pattern string) string {
	var b strings.Builder
	b.Grow(len(pattern))
	literal := false
	for _, c := range pattern {
		switch {
		case c == '\'':
			literal = !literal
			b.WriteRune(c)
		case !literal && (c == 'h' || c == 'H' || c == 'k' || c == 'K'):
			b.WriteByte('H')
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// splitExtensions parts a tag into its language, script, region and variants, and the
// extensions and private use from the first singleton on ("-u-nu-latn", "-x-…").
func splitExtensions(tag string) (base, ext string) {
	parts := strings.Split(tag, "-")
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) == 1 {
			return strings.Join(parts[:i], "-"), "-" + strings.Join(parts[i:], "-")
		}
	}
	return tag, ""
}

// canonicalCase writes a tag's subtags in their canonical case: the language and variants in
// lower case, a script in title case, a region in upper case ("EN-us" → "en-US").
func canonicalCase(base string) string {
	parts := strings.Split(base, "-")
	for i, p := range parts {
		switch {
		case i == 0:
			parts[i] = strings.ToLower(p)
		case len(p) == 4 && isAlpha(p):
			parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
		case len(p) == 2 && isAlpha(p) || len(p) == 3 && isDigits(p):
			parts[i] = strings.ToUpper(p)
		default:
			parts[i] = strings.ToLower(p)
		}
	}
	return strings.Join(parts, "-")
}

func isAlpha(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// bestAvailable is ECMA-402's BestAvailableLocale, as V8 resolves a requested tag: the tag
// without its extensions, shortened from the end (with a singleton before the last subtag)
// until a locale the formatter has data for; ok is false when none matches.
func bestAvailable(base string, available func(string) bool) (string, bool) {
	candidate := base
	for {
		if available(candidate) {
			return candidate, true
		}
		pos := strings.LastIndexByte(candidate, '-')
		if pos < 0 {
			return "", false
		}
		if pos >= 2 && candidate[pos-2] == '-' {
			pos -= 2
		}
		candidate = candidate[:pos]
	}
}

// latnByDefault lists the locales whose default numbering system is Latin digits in CLDR 48
// (the data of the ICU 78 in today's WebView2 window) but Arabic-Indic digits in the CLDR 42
// of Windows' ICU 72: ar and ar-001. Of ICU 72's 889 locales they are the only ones whose
// default numbering system differs (compared with Node 24's ICU 78.2, which agrees with the
// window on every formatter golden).
func latnByDefault(resolved string) bool { return resolved == "ar" || resolved == "ar-001" }

// hasUnicodeKey reports whether the extensions set a Unicode locale key ("nu" in "-u-nu-arab").
func hasUnicodeKey(ext, key string) bool {
	parts := strings.Split(strings.ToLower(ext), "-")
	in := false
	for _, p := range parts {
		switch {
		case len(p) == 1:
			in = p == "u"
		case in && p == key:
			return true
		}
	}
	return false
}

// withLatinDigits adds the key "nu-latn" to a tag's extensions: into its Unicode extension
// when it has one, else as one of its own before the others.
func withLatinDigits(ext string) string {
	if i := strings.Index(strings.ToLower(ext), "-u-"); i >= 0 {
		return ext[:i+3] + "nu-latn-" + ext[i+3:]
	}
	return "-u-nu-latn" + ext
}

// LanguageName names a BCP 47 language tag in English as today's window does with
// Intl.DisplayNames(['en'], {type: 'language', fallback: 'code'}): "en" → "English", "pt-BR" →
// "Portuguese (Brazil)", "zh-Hans" → "Chinese (Simplified)", an unknown language by its code
// ("xx"). ok is false for a tag that is not a Unicode language identifier (where Intl throws)
// and where the system has no names (no ICU): the caller shows the tag. It has the shape of
// ui/core/media's LanguageNamer.
func LanguageName(tag string) (name string, ok bool) {
	if !isLanguageID(tag) {
		return "", false
	}
	id, err := icuLocale(canonicalCase(tag))
	if err != nil {
		return "", false
	}
	return systemLanguageName(id)
}

// isLanguageID reports a Unicode language identifier: a language (2 to 3 or 5 to 8 letters,
// or "root"), then an optional script (4 letters), an optional region (2 letters or 3
// digits) and variants (5 to 8 letters or digits, or a digit and 3 more); no extensions.
func isLanguageID(tag string) bool {
	parts := strings.Split(tag, "-")
	lang := parts[0]
	if !isAlpha(lang) || !(len(lang) >= 2 && len(lang) <= 3 || len(lang) >= 5 && len(lang) <= 8) {
		return false
	}
	i := 1
	if i < len(parts) && len(parts[i]) == 4 && isAlpha(parts[i]) {
		i++
	}
	if i < len(parts) && (len(parts[i]) == 2 && isAlpha(parts[i]) || len(parts[i]) == 3 && isDigits(parts[i])) {
		i++
	}
	for ; i < len(parts); i++ {
		v := parts[i]
		alnum := isAlnum(v)
		if !(alnum && len(v) >= 5 && len(v) <= 8 || alnum && len(v) == 4 && v[0] >= '0' && v[0] <= '9') {
			return false
		}
	}
	return true
}

func isAlnum(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return s != ""
}
