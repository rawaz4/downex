//go:build darwin && cgo

package mac

/*
#include "textct.h"
*/
import "C"

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"downex/ui/kit"
)

// The text engine sets text as WebKit on macOS sets it in today's window (the Svelte page
// in a WKWebView), with Core Text, which WebKit itself shapes with:
//
//   - Families: the first installed family of the CSS list. system-ui, -apple-system and
//     ui-sans-serif are the system UI font (SF Pro, made as WebKit makes it: the UI font
//     type, whose optical size and tracking follow the size); ui-monospace the system's
//     monospaced font (SF Mono); sans-serif Helvetica and monospace Courier, WebKit's
//     defaults. Names that start with a dot (the system's private ones) are never matched.
//     Without Segoe UI or Cascadia installed, app.css's lists come out as SF Pro and SF
//     Mono, as in WebKit.
//   - Weights go on the variable font's 'wght' axis as they are (450 and 650 are not
//     snapped to named instances as on Windows): measured in WKWebView on macOS 26, "All
//     downloads" at 20 px and a 13 px file name lay out to 0.0001 px alike for every weight
//     from 300 to 700.
//   - Line boxes in whole CSS pixels, as WebKit's: the ascent and descent rounded (Times,
//     Helvetica and Courier with WebKit's 15 % more ascent), a line-height multiple
//     truncated to whole pixels, the half-leading above floored. 13 px at 1.4 is an 18 px
//     line with its baseline 14 px down (WKWebView: 18 and 14).
//   - Letter spacing after every character, the last included, with the common ligatures
//     off (WebKit's rule).
//   - Line breaking by WebKit's rules (BreakLines): after white space, its ASCII table
//     (after "-" and "?", before an opening bracket after punctuation; never after "/"
//     between ASCII characters), ICU's opportunities next to anything past ASCII; greedy,
//     white space hanging. Widths are measured as WebKit's line layout measures them: the
//     pieces between opportunities (and white space) each set alone, so no kerning across
//     them ("city-timelapse" is "city-" + "timelapse"); glyphs are drawn as one line.
//   - Glyphs drawn grayscale without font smoothing (-webkit-font-smoothing: antialiased,
//     app.css), colour emoji in colour, the baseline on a device pixel.
//
// Measured against an offscreen WKWebView on macOS 26 (267 cases: every type size, weight,
// the three families, Arabic and Kurdish names, emoji, wrapping at several widths): widths
// within 0.0013 px, line breaks, line heights and baselines identical; at 2x, drawn pixels
// identical to WebKit's snapshot except a colour emoji at 13 px, one device pixel higher.

// The kinds of font a family name resolves to.
const (
	kindSystem = iota // the system UI font (SF Pro)
	kindMono          // the system's monospaced font (SF Mono)
	kindNamed         // an installed family, by name
)

// family is a resolved CSS family list.
type family struct {
	kind int
	name string // kindNamed's
}

// textFormat is a normalised kit.TextStyle made into a Core Text font, with what layouts
// need of it. Layouts copy what they need, so a format may be freed while they live.
type textFormat struct {
	font    C.CTFontRef
	attrs   [2]C.CFDictionaryRef // left to right, right to left; made when first needed
	spacing float32              // letter spacing in DIPs, after every character
	metrics kit.FontMetrics
	ellW    float32
	ellWOK  bool
}

func (f *textFormat) release() {
	for i := range f.attrs {
		C.dxtRelease(C.CFTypeRef(f.attrs[i]))
		f.attrs[i] = 0
	}
	C.dxtRelease(C.CFTypeRef(f.font))
	f.font = 0
}

// attributes are the format's character attributes for a paragraph of direction rtl.
func (f *textFormat) attributes(rtl bool) C.CFDictionaryRef {
	i := 0
	if rtl {
		i = 1
	}
	if f.attrs[i] == 0 {
		r := C.int(0)
		if rtl {
			r = 1
		}
		f.attrs[i] = C.dxtAttributes(f.font, C.double(f.spacing), r)
	}
	return f.attrs[i]
}

// textEngine lays out and measures text. It belongs to one goroutine at a time, as
// kit.Resources does.
type textEngine struct {
	closed    bool
	formats   map[kit.TextStyle]*textFormat
	families  map[string]family
	installed map[string]bool
	live      int // layouts made and not released

	// scratch, reused by every layout
	u16    []uint16
	widths []C.double
	runs   []C.double
	bounds [2]C.double
}

// runScratch is the scratch for dxtRangeRuns: room for 64 runs.
func (e *textEngine) runScratch() []C.double {
	if len(e.runs) < 3*64 {
		e.runs = make([]C.double, 3*64)
	}
	return e.runs
}

func newTextEngine() (*textEngine, error) {
	e := &textEngine{
		formats:   make(map[kit.TextStyle]*textFormat),
		families:  make(map[string]family),
		installed: make(map[string]bool),
	}
	// The system font is what every list falls back to: check it is there.
	f := C.dxtSystemFont(13, 400, 0, 0, 0)
	if f == 0 {
		return nil, fmt.Errorf("mac: the system font is not available: %w", kit.ErrUnsupported)
	}
	C.dxtRelease(C.CFTypeRef(f))
	return e, nil
}

func (e *textEngine) close() {
	if e.closed {
		return
	}
	e.closed = true
	for k, f := range e.formats {
		f.release()
		delete(e.formats, k)
	}
}

func (e *textEngine) usable() error {
	if e == nil || e.closed {
		return fmt.Errorf("mac: text engine closed: %w", kit.ErrInvalid)
	}
	return nil
}

// normStyle checks s and puts it in the form that keys the cache: weight in 1..1000 (0 is
// 400), sizes finite. Unlike on Windows the optical size is kept: SF Pro has an 'opsz' axis
// that WebKit sets from the size.
func normStyle(s kit.TextStyle) (kit.TextStyle, error) {
	if !(s.Size > 0) || !finite(s.Size) {
		return s, errInvalid("font size %v", s.Size)
	}
	if !finite(s.Weight) || !finite(s.OpticalSize) || !finite(s.LetterSpacing) || !finite(s.LineHeight) {
		return s, errInvalid("text style %+v has a value that is not a finite number", s)
	}
	if s.Weight == 0 {
		s.Weight = 400
	}
	s.Weight = min(max(s.Weight, 1), 1000)
	s.OpticalSize = max(s.OpticalSize, 0)
	s.LineHeight = max(s.LineHeight, 0)
	return s, nil
}

// format is the cached format of s (normalised).
func (e *textEngine) format(s kit.TextStyle) (*textFormat, error) {
	if f, ok := e.formats[s]; ok {
		return f, nil
	}
	if len(e.formats) >= 512 { // styles that keep changing (an animated size): start over
		for k, f := range e.formats {
			f.release()
			delete(e.formats, k)
		}
	}
	fam := e.resolveFamily(s.Family)
	italic, tab := C.int(0), C.int(0)
	if s.Italic {
		italic = 1
	}
	if s.Tabular {
		tab = 1
	}
	size, weight, opsz := C.double(s.Size), C.double(s.Weight), C.double(s.OpticalSize)
	var font C.CTFontRef
	switch fam.kind {
	case kindMono:
		font = C.dxtMonoFont(size, weight, italic, opsz, tab)
	case kindNamed:
		u := utf16.Encode([]rune(fam.name))
		if len(u) > 0 {
			font = C.dxtNamedFont((*C.UniChar)(&u[0]), C.CFIndex(len(u)), size, weight, italic, opsz, tab)
		}
	}
	if font == 0 {
		font = C.dxtSystemFont(size, weight, italic, opsz, tab)
	}
	if font == 0 {
		return nil, fmt.Errorf("mac: no font for %q at %v px: %w", s.Family, s.Size, kit.ErrUnsupported)
	}
	f := &textFormat{font: font, spacing: s.LetterSpacing * s.Size}
	var cm C.dxtFontMetrics
	C.dxtMetrics(font, &cm)
	f.metrics = lineBox(cm, s)
	e.formats[s] = f
	return f, nil
}

// lineBox is the font metrics and line box WebKit gives a style whose font has the metrics
// m: ascent, descent and line gap each rounded to whole pixels (with the 15 % more ascent
// WebKit gives Times, Helvetica and Courier), a normal line as tall as the three, a
// multiple of the size truncated to whole pixels (as a LayoutUnit is), and the leading
// split with its floored half above.
func lineBox(m C.dxtFontMetrics, s kit.TextStyle) kit.FontMetrics {
	asc, desc, gap := float64(m.ascent), float64(m.descent), float64(m.leading)
	if m.adjustAscent != 0 {
		asc += math.Floor((asc+desc)*0.15 + 0.5)
	}
	a, d, g := math.Round(asc), math.Round(desc), math.Round(max(gap, 0))
	lh := a + d + g
	if s.LineHeight > 0 {
		lh = cssLineHeight(s.LineHeight, s.Size)
	}
	base := math.Floor((lh-(a+d))/2) + a
	return kit.FontMetrics{
		Ascent: kit.DIP(a), Descent: kit.DIP(d), LineGap: kit.DIP(g),
		LineHeight: kit.DIP(lh), Baseline: kit.DIP(base),
		CapHeight: kit.DIP(m.capHeight), XHeight: kit.DIP(m.xHeight),
		UnderlineOffset: -kit.DIP(m.underlinePos), UnderlineThickness: kit.DIP(m.underlineThick),
	}
}

// cssLineHeight is WebKit's line box height for line-height: k at size px: k × size in
// LayoutUnits (1/64 px, floored), then in whole pixels (truncated). 13 px × 1.4 = 18,
// 12.5 × 1.4 = 17, 15.5 × 1.2 = 18 (measured in WKWebView).
func cssLineHeight(k, size float32) float64 {
	// k and size come as float32 from decimal CSS values: 1.4 × 15 must be 21, not
	// 20.999998, so the product is rounded to 1e-4 px before it is floored.
	v := math.Round(float64(k)*float64(size)*1e4) / 1e4
	return math.Floor(math.Floor(v*64) / 64)
}

// resolveFamily is the first family of the CSS list css that is installed, with the
// generic names mapped as WebKit on macOS maps them; the system UI font when none is.
func (e *textEngine) resolveFamily(css string) family {
	if f, ok := e.families[css]; ok {
		return f
	}
	found := family{kind: kindSystem}
	for _, name := range parseFamilies(css) {
		switch strings.ToLower(name) {
		case "system-ui", "-apple-system", "ui-sans-serif", "blinkmacsystemfont":
			found = family{kind: kindSystem}
		case "ui-monospace":
			found = family{kind: kindMono}
		case "monospace":
			// WebKit's default fixed font (measured: Courier's 0.6 em advance and
			// line box)
			found = family{kind: kindMono}
			if e.isInstalled("Courier") {
				found = family{kind: kindNamed, name: "Courier"}
			}
		case "sans-serif":
			found = family{kind: kindNamed, name: "Helvetica"}
		case "serif", "ui-serif", "cursive", "fantasy", "math", "emoji", "fangsong", "ui-rounded":
			continue
		default:
			if !e.isInstalled(name) {
				continue
			}
			found = family{kind: kindNamed, name: name}
		}
		break
	}
	if len(e.families) < 64 {
		e.families[css] = found
	}
	return found
}

func (e *textEngine) isInstalled(name string) bool {
	if ok, seen := e.installed[name]; seen {
		return ok
	}
	u := utf16.Encode([]rune(name))
	ok := len(u) > 0 && C.dxtFamilyInstalled((*C.UniChar)(&u[0]), C.CFIndex(len(u))) != 0
	if len(e.installed) < 256 {
		e.installed[name] = ok
	}
	return ok
}

// parseFamilies splits a CSS font-family list: names separated by commas, each bare
// (several words joined by single spaces) or quoted with ' or ".
func parseFamilies(css string) []string {
	var out []string
	for i := 0; i < len(css); {
		for i < len(css) && (css[i] == ' ' || css[i] == ',' || css[i] == '\t' || css[i] == '\n') {
			i++
		}
		if i >= len(css) {
			break
		}
		if q := css[i]; q == '\'' || q == '"' {
			j := strings.IndexByte(css[i+1:], q)
			if j < 0 {
				out = append(out, css[i+1:])
				break
			}
			out = append(out, css[i+1:i+1+j])
			i += j + 2
			continue
		}
		j := strings.IndexByte(css[i:], ',')
		if j < 0 {
			j = len(css) - i
		}
		if name := strings.Join(strings.Fields(css[i:i+j]), " "); name != "" {
			out = append(out, name)
		}
		i += j
	}
	return out
}

// fontMetrics implements kit.TextMeasurer's FontMetrics.
func (e *textEngine) fontMetrics(style kit.TextStyle) (kit.FontMetrics, error) {
	if err := e.usable(); err != nil {
		return kit.FontMetrics{}, err
	}
	s, err := normStyle(style)
	if err != nil {
		return kit.FontMetrics{}, err
	}
	f, err := e.format(s)
	if err != nil {
		return kit.FontMetrics{}, err
	}
	return f.metrics, nil
}

// utf16Of converts s into e.u16 (reused). Invalid UTF-8 becomes U+FFFD, one per byte, as
// Go reads it.
func (e *textEngine) utf16Of(s string) []uint16 {
	b := e.u16[:0]
	for _, c := range s {
		b = utf16.AppendRune(b, c)
	}
	e.u16 = b
	return b
}

// utf16Len is the number of UTF-16 units of s[:end], end moved back to a rune start.
func utf16Len(s string, end int) int {
	end = min(max(end, 0), len(s))
	for end > 0 && end < len(s) && !utf8.RuneStart(s[end]) {
		end--
	}
	n := 0
	for _, c := range s[:end] {
		if c >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// utf8Offset is the byte offset in s of UTF-16 index i (clamped to s).
func utf8Offset(s string, i int) int {
	n := 0
	for off, c := range s {
		if n >= i {
			return off
		}
		if c >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return len(s)
}
