// Package text is display safety for strings that come from the network or the daemon (file
// names, URLs, paths, error text), ported from ui/frontend/src/lib/text.ts, and the few
// JavaScript string rules (trim, URL, decodeURIComponent) the ported modules lean on. Raw
// values stay untouched for actions; these helpers are for what the user reads.
package text

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// isBidiControl reports the explicit bidirectional formatting characters: embeddings and
// overrides U+202A–U+202E, isolates U+2066–U+2069, the marks U+200E/U+200F and the Arabic
// letter mark U+061C. In a file name they can reorder what is shown ("invoice<U+202E>fdp.exe"
// reads as "invoiceexe.pdf") and so disguise the real extension.
func isBidiControl(r rune) bool {
	return r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069 || r == 0x200E || r == 0x200F || r == 0x061C
}

// HasBidiControls reports whether s holds a bidi control character.
func HasBidiControls(s string) bool {
	return strings.IndexFunc(s, isBidiControl) >= 0
}

const hexDigits = "0123456789ABCDEF"

// EscapeBidi replaces each bidi control with a visible "⟨U+202E⟩" marker.
func EscapeBidi(s string) string {
	first := strings.IndexFunc(s, isBidiControl)
	if first < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	b.WriteString(s[:first])
	for _, r := range s[first:] {
		if !isBidiControl(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteString("⟨U+")
		b.WriteByte(hexDigits[r>>12&0xF])
		b.WriteByte(hexDigits[r>>8&0xF])
		b.WriteByte(hexDigits[r>>4&0xF])
		b.WriteByte(hexDigits[r&0xF])
		b.WriteString("⟩")
	}
	return b.String()
}

// FSI and PDI are the first-strong isolate and the pop directional isolate.
const (
	FSI = "\u2068"
	PDI = "\u2069"
)

// Isolate wraps an inserted value in first-strong isolate … pop directional isolate, so a
// right-to-left name inside an English sentence (or the reverse) keeps its own order without
// reordering the words around it. The value is escaped first, so it cannot close the isolate
// early.
func Isolate(s string) string { return FSI + EscapeBidi(s) + PDI }

// SplitExtension splits a file name into its stem and a trailing Latin extension (".mp4",
// ".iso": a dot and 1 to 10 ASCII letters or digits), so a right-to-left name can show its
// extension in its own left-to-right isolate (".mp4", not "mp4."). Without such an extension,
// or without a stem before it, the whole name is the stem.
func SplitExtension(name string) (stem, ext string) {
	dot := strings.LastIndexByte(name, '.')
	if dot < 1 {
		return name, ""
	}
	e := name[dot+1:]
	if len(e) < 1 || len(e) > 10 {
		return name, ""
	}
	for i := 0; i < len(e); i++ {
		c := e[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return name, ""
		}
	}
	return name[:dot], name[dot:]
}

// IsJSSpace reports what JavaScript's String.prototype.trim and the regular expression \s
// treat as white space: WhiteSpace (tab, vertical tab, form feed, U+FEFF and the space
// separators) and LineTerminator (LF, CR, U+2028, U+2029).
func IsJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00A0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// TrimJS is JavaScript's String.prototype.trim.
func TrimJS(s string) string { return strings.TrimFunc(s, IsJSSpace) }

// TrimJSStart is JavaScript's String.prototype.trimStart.
func TrimJSStart(s string) string { return strings.TrimLeftFunc(s, IsJSSpace) }

// TrimJSEnd is JavaScript's String.prototype.trimEnd.
func TrimJSEnd(s string) string { return strings.TrimRightFunc(s, IsJSSpace) }

// DecodeURIComponent is JavaScript's decodeURIComponent: every %XX escape decoded, the
// escapes of one character forming valid UTF-8; ok is false where JavaScript throws a
// URIError (a stray "%", a bad hex digit, invalid or overlong UTF-8, a surrogate).
func DecodeURIComponent(s string) (string, bool) {
	if strings.IndexByte(s, '%') < 0 {
		return s, true
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != '%' {
			b.WriteByte(c)
			i++
			continue
		}
		first, ok := hexByte(s, i)
		if !ok {
			return "", false
		}
		i += 3
		if first < 0x80 {
			b.WriteByte(first)
			continue
		}
		n := 0
		switch {
		case first&0xE0 == 0xC0:
			n = 2
		case first&0xF0 == 0xE0:
			n = 3
		case first&0xF8 == 0xF0:
			n = 4
		default:
			return "", false
		}
		seq := []byte{first}
		for k := 1; k < n; k++ {
			if i >= len(s) || s[i] != '%' {
				return "", false
			}
			v, ok := hexByte(s, i)
			if !ok || v&0xC0 != 0x80 {
				return "", false
			}
			seq = append(seq, v)
			i += 3
		}
		if _, size := utf8.DecodeRune(seq); size != n {
			return "", false
		}
		b.Write(seq)
	}
	return b.String(), true
}

// hexByte reads the %XX escape at s[i].
func hexByte(s string, i int) (byte, bool) {
	if i+2 >= len(s) {
		return 0, false
	}
	h, ok1 := unhex(s[i+1])
	l, ok2 := unhex(s[i+2])
	return h<<4 | l, ok1 && ok2
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// LowerJS is JavaScript's String.prototype.toLowerCase: Unicode's full lower-case mapping,
// which differs from strings.ToLower in "\u0130" (→ "i\u0307") and a final capital sigma (→ "ς").
func LowerJS(s string) string {
	if !strings.ContainsAny(s, "\u0130\u03a3") {
		return strings.ToLower(s)
	}
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s) + 2)
	for i, r := range runes {
		switch r {
		case 0x0130:
			b.WriteString("i\u0307")
		case 0x03A3:
			if finalSigma(runes, i) {
				b.WriteRune(0x03C2)
			} else {
				b.WriteRune(0x03C3)
			}
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// finalSigma is Unicode's Final_Sigma condition: a cased letter before the sigma and none
// after it, case-ignorable characters skipped.
func finalSigma(runes []rune, i int) bool {
	before := false
	for j := i - 1; j >= 0; j-- {
		if caseIgnorable(runes[j]) {
			continue
		}
		before = cased(runes[j])
		break
	}
	if !before {
		return false
	}
	for j := i + 1; j < len(runes); j++ {
		if caseIgnorable(runes[j]) {
			continue
		}
		return !cased(runes[j])
	}
	return true
}

func cased(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) ||
		unicode.Is(unicode.Other_Lowercase, r) || unicode.Is(unicode.Other_Uppercase, r)
}

func caseIgnorable(r rune) bool {
	switch r {
	case '\'', '.', ':', '^', '`', 0x00A8, 0x00AD, 0x00AF, 0x00B4, 0x00B7, 0x00B8, 0x2018, 0x2019, 0x2024, 0x2027:
		return true
	}
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk)
}
