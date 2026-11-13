package validate

import (
	"downex/ui/core/categories"
	"downex/ui/core/format"
	"downex/ui/core/text"
)

// What validate uses of ui/core/format, ui/core/text and ui/core/categories: the window's
// JavaScript rules (trim, \s, toLowerCase, Math.round, String(number), new URL) and its rate
// fields.

var (
	isJSSpace = text.IsJSSpace
	jsTrim    = text.TrimJS
	jsLower   = text.LowerJS
	jsRound   = format.Round
	jsNumber  = format.Num
)

// RateUnit is the unit of a speed field ("KB/s" or "MB/s").
type RateUnit = format.RateUnit

func formatCount(n int) string { return format.Count(float64(n)) }

// parseRate reads a typed speed in unit; loaded is the limit the field was filled from (0:
// none), kept exactly while its text is unchanged.
func parseRate(value string, unit RateUnit, loaded int64) (int64, bool) {
	return format.ParseRate(value, unit, float64(loaded))
}

func splitRate(bytesPerSecond int64) (string, RateUnit) {
	return format.SplitRate(float64(bytesPerSecond))
}

func normalizeExtension(ext string) string { return categories.NormalizeExtension(ext) }

// webURL is what the checks read of an address: its scheme (lower case, without ":"), its host
// ("" for none) and its pathname.
type webURL struct {
	scheme, host, path string
}

// parseURL is `new URL(text)`; ok is false where it throws.
func parseURL(s string) (webURL, bool) {
	u, err := text.ParseURL(s)
	if err != nil {
		return webURL{}, false
	}
	return webURL{scheme: u.Scheme(), host: u.Hostname(), path: u.Pathname()}, true
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return s != ""
}
