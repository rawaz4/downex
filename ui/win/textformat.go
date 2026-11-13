//go:build windows

package win

import (
	"fmt"
	"math"
	"strings"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/dwrite"

	"golang.org/x/sys/windows"
)

// The platform's generic families (kit.TextStyle.Family).
const (
	uiFamily   = "Segoe UI"
	monoFamily = "Consolas"
)

// textFormat is one TextStyle made into a DirectWrite text format: the first installed
// family of the style's list at the style's size, weight and italic, with the line box of CSS
// half-leading set on it. Layouts copy what they need, so a format may be released while
// layouts made from it live.
//
// Weights and optical sizes are what Chromium on Windows, today's WebView2 window, makes of
// them for a system font. CSS font-weight picks one font of the family by DirectWrite's font
// matching (Skia's font manager asks IDWriteFontFamily::GetFirstMatchingFont); a variable
// font's named instances are the fonts of its family, so Segoe UI Variable Text at 500 is
// its Semibold (wght 600) and the 650 page title Display's Bold (wght 700). Neither
// font-weight, font-optical-sizing nor font-variation-settings sets a variation axis, so
// each instance keeps its own 'wght' and 'opsz'; TextStyle.OpticalSize is ignored. Measured
// in headless Edge 156 (the WebView2 runtime's build) on Windows 11 at device scales 1, 1.25
// and 1.5: "All downloads" (Display, 20 px, -0.02em, tabular) lays out 125.70 px at 650 and
// 700 alike and 120.59 at 500 and 600, the widths of those named instances here to 0.01 px,
// whatever font-optical-sizing or font-variation-settings say.
type textFormat struct {
	format   *dwrite.TextFormat
	ellipsis *dwrite.InlineObject // made on first truncation
	face     *fontFace
	family   string
	metrics  kit.FontMetrics
	spacing  float32 // letter spacing in DIPs, after every character
	tabular  bool
	ellW     float32
	ellWOK   bool
}

func (f *textFormat) release() {
	com.Release(&f.ellipsis)
	com.Release(&f.format)
}

// faceKey picks a font of a family: its weight, rounded, and italic.
type faceKey struct {
	family string
	weight uint32
	italic bool
}

// fontFace is what a format needs of the font that sets its text: the design metrics of the
// family's font that matches the weight and style.
type fontFace struct {
	m dwrite.FontMetrics
}

func (f *fontFace) release() {}

// normStyle checks s and puts it in the form that keys the cache: weight in 1..1000 (0 is
// 400), sizes finite.
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
	s.OpticalSize = 0 // ignored, as Chromium ignores it for system fonts (textFormat)
	s.LineHeight = max(s.LineHeight, 0)
	return s, nil
}

// format is the cached text format of s (normalised).
func (r *Resources) format(s kit.TextStyle) (*textFormat, error) {
	if f, ok := r.formats[s]; ok {
		return f, nil
	}
	if err := r.text(); err != nil {
		return nil, err
	}
	if len(r.formats) >= 512 { // styles that keep changing (an animated size): start over
		for k, f := range r.formats {
			f.release()
			delete(r.formats, k)
		}
	}
	family := r.resolveFamily(s.Family)
	style := uint32(dwrite.FONT_STYLE_NORMAL)
	if s.Italic {
		style = dwrite.FONT_STYLE_ITALIC
	}
	weight := uint32(math.Round(float64(s.Weight)))
	name, err := windows.UTF16PtrFromString(family)
	if err != nil {
		return nil, errInvalid("font family %q", family)
	}
	face := r.face(faceKey{family, weight, s.Italic}, name)
	tf, hr := r.dw.CreateTextFormat(name, weight, style, dwrite.FONT_STRETCH_NORMAL, s.Size, r.locale)
	if hr.Failed() {
		return nil, fmt.Errorf("win: font %q %v px: %w", family, s.Size, com.Check("IDWriteFactory::CreateTextFormat", hr))
	}
	f := &textFormat{format: tf, family: family, tabular: s.Tabular, spacing: s.LetterSpacing * s.Size, face: face}

	// The line box (CSS half-leading): the ascent and descent sit centred in it.
	upem := float32(face.m.DesignUnitsPerEm)
	if upem == 0 {
		upem = 2048
	}
	k := s.Size / upem
	asc, desc, gap := float32(face.m.Ascent)*k, float32(face.m.Descent)*k, float32(face.m.LineGap)*k
	lineH := asc + desc + max(gap, 0)
	if s.LineHeight > 0 {
		lineH = s.LineHeight * s.Size
	}
	base := (lineH-(asc+desc))/2 + asc
	if hr := tf.SetLineSpacing(dwrite.LINE_SPACING_METHOD_UNIFORM, lineH, base); hr.Failed() {
		f.release()
		return nil, fmt.Errorf("win: %w", com.Check("IDWriteTextFormat::SetLineSpacing", hr))
	}
	f.metrics = kit.FontMetrics{
		Ascent: asc, Descent: desc, LineGap: gap,
		LineHeight: lineH, Baseline: base,
		CapHeight: float32(face.m.CapHeight) * k, XHeight: float32(face.m.XHeight) * k,
		UnderlineOffset: -float32(face.m.UnderlinePosition) * k, UnderlineThickness: float32(face.m.UnderlineThickness) * k,
	}
	r.formats[s] = f
	return f, nil
}

// face is the metrics of family's font that matches k's weight and style.
func (r *Resources) face(k faceKey, name *uint16) *fontFace {
	if f, ok := r.faces[k]; ok {
		return f
	}
	f := &fontFace{m: dwrite.FontMetrics{DesignUnitsPerEm: 2048, Ascent: 2210, Descent: 514, CapHeight: 1434, XHeight: 1024,
		UnderlinePosition: -205, UnderlineThickness: 102}} // Segoe UI's, if the lookup fails
	defer func() { r.faces[k] = f }()
	idx, ok, hr := r.fonts.FindFamilyName(name)
	if hr.Failed() || !ok {
		return f
	}
	fam, hr := r.fonts.GetFontFamily(idx)
	if hr.Failed() {
		return f
	}
	defer fam.Release()
	style := uint32(dwrite.FONT_STYLE_NORMAL)
	if k.italic {
		style = dwrite.FONT_STYLE_ITALIC
	}
	font, hr := fam.GetFirstMatchingFont(k.weight, dwrite.FONT_STRETCH_NORMAL, style)
	if hr.Failed() {
		return f
	}
	defer font.Release()
	font.GetMetrics(&f.m)
	return f
}

// resolveFamily is the first family of the CSS list css that is installed, with the generic
// names mapped to the platform's (system-ui and sans-serif to Segoe UI, monospace and
// ui-monospace to Consolas); Segoe UI when none is.
func (r *Resources) resolveFamily(css string) string {
	if f, ok := r.families[css]; ok {
		return f
	}
	if r.text() != nil {
		return uiFamily
	}
	found := uiFamily
	for _, name := range parseFamilies(css) {
		switch strings.ToLower(name) {
		case "system-ui", "sans-serif", "-apple-system", "ui-sans-serif":
			name = uiFamily
		case "monospace", "ui-monospace":
			name = monoFamily
		case "serif", "ui-serif", "cursive", "fantasy", "math", "emoji", "fangsong", "ui-rounded":
			continue
		}
		p, err := windows.UTF16PtrFromString(name)
		if err != nil {
			continue
		}
		if _, ok, hr := r.fonts.FindFamilyName(p); !hr.Failed() && ok {
			found = name
			break
		}
	}
	if len(r.families) < 64 {
		r.families[css] = found
	}
	return found
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

// FontMetrics implements kit.TextMeasurer.
func (r *Resources) FontMetrics(style kit.TextStyle) (kit.FontMetrics, error) {
	if err := r.usable(); err != nil {
		return kit.FontMetrics{}, err
	}
	s, err := normStyle(style)
	if err != nil {
		return kit.FontMetrics{}, err
	}
	f, err := r.format(s)
	if err != nil {
		return kit.FontMetrics{}, err
	}
	return f.metrics, nil
}

// ellipsisSign is the format's "…" trimming sign.
func (r *Resources) ellipsisSign(f *textFormat) *dwrite.InlineObject {
	if f.ellipsis == nil {
		f.ellipsis, _ = r.dw.CreateEllipsisTrimmingSign(f.format)
	}
	return f.ellipsis
}
