package locale

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// fallbackFormatter writes en-US forms (what Intl writes for "en-US" with ICU 72's data) in
// any locale, in the requested time zone: the formatter for platforms without a system one.
type fallbackFormatter struct {
	tag, zone string
	loc       *time.Location
}

// NewFallback returns the pure Go formatter: en-US numbers and dates for any tag, in the IANA
// zone (Go's zone database; "" is the system's zone). An unknown zone is an error.
func NewFallback(tag, timeZone string) (Formatter, error) {
	if err := checkTag(tag); err != nil {
		return nil, err
	}
	loc := time.Local
	if timeZone != "" {
		l, err := time.LoadLocation(timeZone)
		if err != nil {
			return nil, fmt.Errorf("locale: invalid time zone %q: %w", timeZone, err)
		}
		loc = l
	}
	return &fallbackFormatter{tag: tag, zone: timeZone, loc: loc}, nil
}

func (f *fallbackFormatter) Tag() string      { return f.tag }
func (f *fallbackFormatter) TimeZone() string { return f.zone }

func (f *fallbackFormatter) Number(v float64, digits int) string {
	return fallbackNumber(v, max(0, min(digits, 20)))
}

func (f *fallbackFormatter) Date(ms float64, style Style) string {
	t, ok := timeClip(ms)
	if !ok {
		return ""
	}
	at := time.UnixMilli(int64(t)).In(f.loc)
	switch style {
	case Time:
		return at.Format("15:04")
	case Clock:
		return at.Format("3:04 PM")
	case MonthDay:
		return at.Format("Jan 2")
	case Date:
		return at.Format("Jan 2, ") + yearText(at.Year())
	case YMD:
		return at.Format("01/02/") + yearText(at.Year())
	case Full:
		return at.Format("Jan 2, ") + yearText(at.Year()) + at.Format(", 15:04:05")
	}
	return ""
}

func (f *fallbackFormatter) Year(ms float64) string {
	t, ok := timeClip(ms)
	if !ok {
		return ""
	}
	return yearText(time.UnixMilli(int64(t)).In(f.loc).Year())
}

// yearText is ICU's "y": the year without padding; years before 1 count back from 1 BC.
func yearText(y int) string {
	if y <= 0 {
		y = 1 - y
	}
	return strconv.Itoa(y)
}

// fallbackNumber is en-US Intl.NumberFormat with exactly digits fraction digits: half-expand
// rounding of the shortest decimal for v, "," between groups of three, "." before the
// fraction, "-" for negatives (-0 too).
func fallbackNumber(v float64, digits int) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "∞"
	case math.IsInf(v, -1):
		return "-∞"
	}
	neg := math.Signbit(v)
	intPart, frac := roundHalfExpand(strconv.FormatFloat(math.Abs(v), 'f', -1, 64), digits)
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if digits > 0 {
		b.WriteByte('.')
		b.WriteString(frac)
	}
	return b.String()
}

// roundHalfExpand rounds a plain decimal ("123.456") to digits fraction digits, half away
// from zero, and returns its integer digits and exactly digits fraction digits.
func roundHalfExpand(dec string, digits int) (intPart, frac string) {
	intPart, frac, _ = strings.Cut(dec, ".")
	if len(frac) <= digits {
		return intPart, frac + strings.Repeat("0", digits-len(frac))
	}
	up := frac[digits] >= '5'
	all := []byte(intPart + frac[:digits])
	if up {
		i := len(all) - 1
		for ; i >= 0; i-- {
			if all[i] == '9' {
				all[i] = '0'
				continue
			}
			all[i]++
			break
		}
		if i < 0 {
			all = append([]byte{'1'}, all...)
		}
	}
	n := len(all) - digits
	return strings.TrimLeft(string(all[:n]), "0") + zeroIfEmpty(string(all[:n])), string(all[n:])
}

func zeroIfEmpty(digits string) string {
	if strings.Trim(digits, "0") == "" {
		return "0"
	}
	return ""
}
