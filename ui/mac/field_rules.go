package mac

// [fields] The pure rules of the native text fields (field.go, field_darwin.m): untagged, so
// that they are tested on every system.

import (
	"math"
	"strings"
	"unicode/utf8"

	"downex/ui/kit"
)

// fldUTF8 is s with each byte that is not part of valid UTF-8 as U+FFFD, as Windows' UTF-16
// conversion and a JSON round trip into WebKit make it: AppKit takes no invalid text.
func fldUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return string([]rune(s))
}

// fldValue is s as a field holds it: invalid UTF-8 as U+FFFD (fldUTF8), NULs go; a
// single-line field drops line breaks (as an input's value sanitisation does), a
// multi-line one has LF for each LF, CR LF or lone CR (a textarea's value).
func fldValue(s string, multi bool) string {
	s = fldUTF8(s)
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	if !multi {
		return strings.NewReplacer("\r", "", "\n", "").Replace(s)
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// fldSnap is v (DIPs) on the nearest device pixel.
func fldSnap(v, ppd float32) float32 {
	if !(ppd > 0) {
		ppd = 1
	}
	return float32(math.Round(float64(v*ppd))) / ppd
}

// fldRound is r with each edge on the nearest device pixel, where the field's view goes:
// a view off the pixel grid would draw its text blurred.
func fldRound(r kit.Rect, ppd float32) kit.Rect {
	return kit.R(fldSnap(r.Min.X, ppd), fldSnap(r.Min.Y, ppd), fldSnap(r.Max.X, ppd), fldSnap(r.Max.Y, ppd))
}

// fldBaseline is where a single-line field's baseline goes in the text area r (window
// DIPs): the font's ascent and descent centred in the area, as a browser centres an
// input's line and as the kit draws the field's stand-in text (kit.TextField.PaintStandIn),
// on a device pixel.
func fldBaseline(r kit.Rect, ascent, descent, ppd float32) float32 {
	return fldSnap(r.Min.Y+(r.Height()-ascent-descent)/2+ascent, ppd)
}

// The alignments the Objective-C side takes.
const (
	fldAlignLeft = iota
	fldAlignCenter
	fldAlignRight
)

// fldAlign is where o puts the text in a field whose direction is dir: always an explicit
// side (never AppKit's natural alignment, which follows the system's language).
func fldAlign(o kit.TextFieldOptions, dir kit.Direction) int {
	switch {
	case o.Align == kit.TextAlignCenter:
		return fldAlignCenter
	case o.AlignsRightAt(dir):
		return fldAlignRight
	}
	return fldAlignLeft
}

// fldShown reports whether a field placed on r shows: an empty or non-finite rectangle
// hides it.
func fldShown(r kit.Rect) bool {
	ok := func(v float32) bool { return v-v == 0 }
	return !r.Empty() && ok(r.Min.X) && ok(r.Min.Y) && ok(r.Max.X) && ok(r.Max.Y)
}
