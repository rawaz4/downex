package palette

import (
	"unicode/utf16"

	"downex/ui/core/text"
)

// The palette matches text as today's window does in JavaScript: lower case is
// String.prototype.toLowerCase (full case mapping, final sigma included), white space is
// JavaScript's \s, and positions are UTF-16 code units, so scores and highlights are the same
// numbers.

// isJSSpace is JavaScript's \s, jsTrim String.prototype.trim, jsLower
// String.prototype.toLowerCase.
var (
	isJSSpace = text.IsJSSpace
	jsTrim    = text.TrimJS
	jsLower   = text.LowerJS
)

// units is s in UTF-16 code units, as a JavaScript string indexes it.
func units(s string) []uint16 { return utf16.Encode([]rune(s)) }

// indexFrom is String.prototype.indexOf(sub, from) over UTF-16 code units.
func indexFrom(s, sub []uint16, from int) int {
	for i := max(from, 0); i+len(sub) <= len(s); i++ {
		match := true
		for k, c := range sub {
			if s[i+k] != c {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
