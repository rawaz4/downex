// Package format is the window's display formatting, ported from ui/frontend/src/lib/format.ts.
// Byte quantities use decimal units (1 KB = 1,000 B, 1 MB = 1,000,000 B), as other download
// managers and file managers show them. Every function is pure but for the locale and time
// zone, a package setting (SetLocale) as format.ts's setFormatLocale, so tests can pin them.
//
// Numbers are float64 as the TypeScript numbers they port: NaN, the infinities and -0 keep
// their meaning ("unknown", "—").
package format

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"downex/ui/core/locale"
	"downex/ui/core/text"
)

// Dash is shown for an unknown value.
const Dash = "—"

var (
	current     atomic.Pointer[locale.Formatter]
	defaultOnce sync.Once
	defaultF    *locale.Formatter // the formatter of no SetLocale (Locale's first answer)
)

// SetLocale sets the formatter every function formats numbers and dates with (format.ts's
// setFormatLocale: the window sets the user's language and the system's time zone at start).
func SetLocale(f locale.Formatter) {
	if f != nil {
		current.Store(&f)
	}
}

// Locale is the formatter SetLocale set (en-US in the system's zone until then, as
// format.ts's default).
func Locale() locale.Formatter {
	if f := current.Load(); f != nil {
		return *f
	}
	defaultOnce.Do(func() {
		f, err := locale.New("en-US", "")
		if err != nil {
			f, _ = locale.NewFallback("en-US", "")
		}
		defaultF = &f
		current.CompareAndSwap(nil, &f)
	})
	return *current.Load()
}

// IsDefault reports whether the functions format with the default formatter: no SetLocale
// chose another (or the one chosen was put back, as a test's cleanup does with Locale's
// answer). A test harness uses it to set a locale of its own without overriding a test's.
func IsDefault() bool {
	f := Locale()
	return defaultF != nil && sameFormatter(f, *defaultF)
}

func sameFormatter(a, b locale.Formatter) (same bool) {
	defer func() {
		if recover() != nil { // a formatter type that cannot be compared: not the same
			same = false
		}
	}()
	return a == b
}

var byteUnits = [...]string{"B", "KB", "MB", "GB", "TB", "PB", "EB"}

// unitStep is one step of byteUnits: 1 KB = 1,000 B.
const unitStep = 1000

// Count formats a count in the locale ("10,000"), rounded to a whole number.
func Count(n float64) string { return Locale().Number(n, 0) }

// Bytes formats a byte count: 1500 → "1.5 KB"; values below 100 units keep one decimal. A
// value switches to the next unit from 1000, and one that would round to 1000 shows in the
// next unit (999,999 → "1.0 MB"), so columns never show four integer digits.
// Negative = unknown → "—".
func Bytes(bytes float64) string {
	if math.IsNaN(bytes) || math.IsInf(bytes, 0) || bytes < 0 {
		return Dash
	}
	if bytes < 1000 {
		return jsString(jsRound(bytes)) + " B"
	}
	unit := 0
	value := bytes
	for value >= 1000 && unit < len(byteUnits)-1 {
		value /= unitStep
		unit++
	}
	digits := 0
	if value < 100 {
		digits = 1
	}
	// 99.95 KB rounds to 100: no decimal from 100, as for any other value from 100
	if digits == 1 && roundTo(value, 1) >= 100 {
		digits = 0
	}
	// 999.96 KB would print as "1000 KB": promote to the next unit instead.
	if roundTo(value, digits) >= 1000 && unit < len(byteUnits)-1 {
		value /= unitStep
		unit++
		digits = 1
	}
	return Locale().Number(roundTo(value, digits), digits) + " " + byteUnits[unit]
}

func roundTo(value float64, digits int) float64 {
	f := math.Pow(10, float64(digits))
	return jsRound(value*f) / f
}

// Speed formats bytes per second ("1.5 KB/s"); unknown or not moving is "0 B/s".
func Speed(bytesPerSecond float64) string {
	if math.IsNaN(bytesPerSecond) || math.IsInf(bytesPerSecond, 0) || bytesPerSecond <= 0 {
		return "0 B/s"
	}
	return Bytes(bytesPerSecond) + "/s"
}

// Eta formats seconds: "45s", "3m 05s", "2h 07m", "3d 4h". NaN/negative → "—", Infinity → "∞".
func Eta(seconds float64) string {
	if math.IsNaN(seconds) || seconds < 0 {
		return Dash
	}
	if math.IsInf(seconds, 1) {
		return "∞"
	}
	s := math.Ceil(seconds)
	if s < 1 {
		return "<1s"
	}
	if s < 60 {
		return jsString(s) + "s"
	}
	if s < 3600 {
		m := math.Floor(s / 60)
		return jsString(m) + "m " + pad2(math.Mod(s, 60)) + "s"
	}
	if s < 86400 {
		h := math.Floor(s / 3600)
		return jsString(h) + "h " + pad2(math.Floor(math.Mod(s, 3600)/60)) + "m"
	}
	d := math.Floor(s / 86400)
	if d > 99 {
		return ">99d"
	}
	return jsString(d) + "d " + jsString(math.Floor(math.Mod(s, 86400)/3600)) + "h"
}

func pad2(n float64) string {
	if n < 10 {
		return "0" + jsString(n)
	}
	return jsString(n)
}

// Bitrate formats bits per second: "800 kbit/s", "5.2 Mbit/s" (decimal units, as bitrates are
// quoted); "" when unknown.
func Bitrate(bitsPerSecond float64) string {
	if math.IsNaN(bitsPerSecond) || math.IsInf(bitsPerSecond, 0) || bitsPerSecond <= 0 {
		return ""
	}
	if bitsPerSecond < 1000 {
		return jsString(jsRound(bitsPerSecond)) + " bit/s"
	}
	if bitsPerSecond < 999_500 {
		return Locale().Number(jsRound(bitsPerSecond/1000), 0) + " kbit/s"
	}
	mbit := bitsPerSecond / 1_000_000
	digits := 0
	if mbit < 100 {
		digits = 1
	}
	return Locale().Number(roundTo(mbit, digits), digits) + " Mbit/s"
}

// Duration formats milliseconds of playback: "45:07" or "1:02:03"; "—" when unknown.
func Duration(ms float64) string {
	if math.IsNaN(ms) || math.IsInf(ms, 0) || ms <= 0 {
		return Dash
	}
	s := jsRound(ms / 1000)
	h := math.Floor(s / 3600)
	m := math.Floor(math.Mod(s, 3600) / 60)
	rest := pad2(math.Mod(s, 60))
	if h > 0 {
		return jsString(h) + ":" + pad2(m) + ":" + rest
	}
	return jsString(m) + ":" + rest
}

// Percent formats a fraction 0..1 as a whole percentage, floored so a download never shows
// 100% before it has completed; "" when the fraction is not a number.
func Percent(fraction float64) string {
	if math.IsNaN(fraction) || math.IsInf(fraction, 0) {
		return ""
	}
	pct := math.Floor(math.Min(1, math.Max(0, fraction)) * 100)
	return jsString(pct) + "%"
}

// Date is a compact date for list columns: the time of day for today, "Oct 3" this year,
// otherwise "Oct 3, 2024". 0/negative → "—". now is the current time in epoch milliseconds.
func Date(ms, now float64) string {
	if !(ms > 0) {
		return Dash
	}
	l := Locale()
	if l.Date(ms, locale.YMD) == l.Date(now, locale.YMD) {
		return l.Date(ms, locale.Time)
	}
	if l.Year(ms) == l.Year(now) {
		return l.Date(ms, locale.MonthDay)
	}
	return l.Date(ms, locale.Date)
}

// DateTime is a full timestamp ("Oct 20, 2026, 09:05:07"); 0/negative → "—".
func DateTime(ms float64) string {
	if !(ms > 0) {
		return Dash
	}
	return Locale().Date(ms, locale.Full)
}

// Clock is a time of day on the locale's clock, for "Done ≈ 11:59": "11:59" where the locale
// counts 24 hours, "11:59 AM" where it counts 12. 0, negative or not a number → "—".
func Clock(epochMs float64) string {
	if !(epochMs > 0) || math.IsInf(epochMs, 0) {
		return Dash
	}
	return Locale().Date(epochMs, locale.Clock)
}

// DriveLabel names the volume of a download folder, for the status bar's free space
// ("D: 230 GB free"): "D:" for a Windows drive, "Network folder" for a UNC path
// (\\server\share), else the folder's last component ("Downloads"; "/" for the root). "" when
// there is no folder.
func DriveLabel(dir string) string {
	d := text.TrimJS(dir)
	if d == "" {
		return ""
	}
	if len(d) >= 2 && isASCIILetter(d[0]) && d[1] == ':' && (len(d) == 2 || d[2] == '\\' || d[2] == '/') {
		return strings.ToUpper(d[:1]) + ":"
	}
	if len(d) >= 3 && isSlash(d[0]) && isSlash(d[1]) && !isSlash(d[2]) {
		return "Network folder"
	}
	parts := strings.FieldsFunc(d, func(r rune) bool { return r == '\\' || r == '/' })
	if len(parts) == 0 {
		return "/"
	}
	return parts[len(parts)-1]
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isSlash(c byte) bool       { return c == '\\' || c == '/' }

// RateUnit is a unit of the speed limit pickers.
type RateUnit string

// The units of the speed limit pickers (decimal: 1 KB/s = 1,000 bytes per second).
const (
	KBps RateUnit = "KB/s"
	MBps RateUnit = "MB/s"
)

// RateUnits lists the units of the speed limit pickers in their order.
func RateUnits() []RateUnit { return []RateUnit{KBps, MBps} }

func (u RateUnit) factor() float64 {
	if u == KBps {
		return 1000
	}
	return 1000 * 1000
}

// MaxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER, the largest rate ParseRate returns.
const MaxSafeInteger = 1<<53 - 1

// ParseRate parses a user-entered rate: a number in unit, or with a unit typed after it, which
// wins ("2 MB", "500K", "1.5 MiB/s"; K, M, G = 1,000, 1,000,000, 1,000,000,000 bytes per
// second, KiB, MiB, GiB = 1024, 1024², 1024³). Empty = 0 (unlimited). ok is false when the
// text is invalid. loaded is the limit the fields were filled from: while their text is still
// what SplitRate showed for it, it is kept exactly (a limit set in MiB/s before shows rounded
// in MB/s and a save does not change it); 0 when none.
func ParseRate(value string, unit RateUnit, loaded float64) (bytes int64, ok bool) {
	t := strings.Replace(text.TrimJS(value), ",", ".", 1)
	if t == "" {
		return 0, true
	}
	if loaded > 0 {
		shown, shownUnit := SplitRate(loaded)
		if shownUnit == unit && shown == t {
			return int64(loaded), true
		}
	}
	m, matched := matchRate(t)
	if !matched {
		return 0, false
	}
	// "5Mi" (a binary prefix without its B) is not a unit
	if m.binary && !m.b {
		return 0, false
	}
	factor := unit.factor()
	switch {
	case m.prefix != 0 && m.binary:
		factor = map[byte]float64{'k': 1024, 'm': 1024 * 1024, 'g': 1024 * 1024 * 1024}[m.prefix]
	case m.prefix != 0:
		factor = map[byte]float64{'k': 1e3, 'm': 1e6, 'g': 1e9}[m.prefix]
	case m.b:
		factor = 1
	}
	typed, err := strconv.ParseFloat(m.number, 64)
	if err != nil && !math.IsInf(typed, 0) {
		return 0, false
	}
	n := jsRound(typed * factor)
	if math.IsInf(n, 0) || math.Abs(n) > MaxSafeInteger {
		return 0, false
	}
	// "0.4 B/s" is not 0 (no limit): a speed above 0 that rounds to no bytes is invalid
	if typed > 0 && n == 0 {
		return 0, false
	}
	return int64(n), true
}

type rateMatch struct {
	number string
	prefix byte // 'k', 'm', 'g' or 0
	binary bool // the "i" of KiB, MiB, GiB
	b      bool // the "B" of a unit
}

// matchRate is format.ts's RATE_TEXT, /^(\d+(?:\.\d+)?)\s*(?:([kmg])(i)?)?(b)?(?:\s*\/\s*s)?$/i,
// with JavaScript's \d (ASCII digits), \s (JavaScript white space) and ASCII-only case folding.
func matchRate(s string) (rateMatch, bool) {
	var m rateMatch
	i := 0
	digits := func() int {
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		return i - start
	}
	if digits() == 0 {
		return m, false
	}
	if i+1 < len(s) && s[i] == '.' && s[i+1] >= '0' && s[i+1] <= '9' {
		i++
		digits()
	}
	m.number = s[:i]
	rest := s[i:]
	rest = text.TrimJSStart(rest)
	if rest != "" {
		switch c := lower(rest[0]); c {
		case 'k', 'm', 'g':
			m.prefix = c
			rest = rest[1:]
			if rest != "" && lower(rest[0]) == 'i' {
				m.binary = true
				rest = rest[1:]
			}
		}
	}
	if rest != "" && lower(rest[0]) == 'b' {
		m.b = true
		rest = rest[1:]
	}
	if rest == "" {
		return m, true
	}
	// (?:\s*\/\s*s)?$
	r := text.TrimJSStart(rest)
	if r == "" || r[0] != '/' {
		return m, false
	}
	r = text.TrimJSStart(r[1:])
	if len(r) != 1 || lower(r[0]) != 's' {
		return m, false
	}
	return m, true
}

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// SplitRate splits bytes/s into an editable value and unit for the dialogs: KB/s with at most
// two decimals, MB/s once that would read 1000 or more (from 999,995 B/s, never "1000" KB/s).
// 0 → "" (unlimited) in MB/s.
func SplitRate(bytesPerSecond float64) (value string, unit RateUnit) {
	if !(bytesPerSecond > 0) {
		return "", MBps
	}
	kb := trimNumber(bytesPerSecond / KBps.factor())
	if n, err := strconv.ParseFloat(kb, 64); err == nil && n < 1000 {
		return kb, KBps
	}
	return trimNumber(bytesPerSecond / MBps.factor()), MBps
}

func trimNumber(n float64) string { return jsString(jsRound(n*100) / 100) }

// HostOf is the host of a URL for compact display (with its port when it has one); the input
// when it does not parse.
func HostOf(rawURL string) string {
	u, err := text.ParseURL(rawURL)
	if err != nil {
		return rawURL
	}
	return u.Host()
}
