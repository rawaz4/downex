//go:build windows

// Package dwrite binds the parts of DirectWrite (dwrite.h, dwrite_1.h, dwrite_3.h) that
// ui/win lays out text with. Types are the interfaces without their IDWrite prefix; each
// method's slot is written in its body and checked against the SDK headers by TestSlots.
// See package com for the calling rules.
package dwrite

import (
	"syscall"
	"unsafe"

	"downex/ui/win/internal/com"

	"golang.org/x/sys/windows"
)

// The IIDs used, checked against the headers by TestSlots.
var (
	IID_IDWriteFactory     = com.MustGUID("b859ee5a-d838-4b5b-a2e8-1adc7d93db48")
	IID_IDWriteTextFormat3 = com.MustGUID("6d3b5641-e550-430d-a85b-b7bf48a93427")
	IID_IDWriteTextLayout1 = com.MustGUID("9064d822-80a7-465c-a986-df65f78b8feb")
	IID_IDWriteFontFace5   = com.MustGUID("98eff3a5-b667-479a-b145-e2fa5b9fdc29")
	IID_IDWriteFactory6    = com.MustGUID("f3744d80-21f7-42eb-b35d-995bc72fc223")
)

// Enumerations (dwrite.h).
const (
	FACTORY_TYPE_SHARED = 0

	FONT_STYLE_NORMAL  = 0
	FONT_STYLE_OBLIQUE = 1
	FONT_STYLE_ITALIC  = 2

	FONT_STRETCH_NORMAL = 5

	READING_DIRECTION_LEFT_TO_RIGHT = 0
	READING_DIRECTION_RIGHT_TO_LEFT = 1

	TEXT_ALIGNMENT_LEADING  = 0
	TEXT_ALIGNMENT_TRAILING = 1
	TEXT_ALIGNMENT_CENTER   = 2

	WORD_WRAPPING_WRAP            = 0
	WORD_WRAPPING_NO_WRAP         = 1
	WORD_WRAPPING_EMERGENCY_BREAK = 2
	WORD_WRAPPING_WHOLE_WORD      = 3
	WORD_WRAPPING_CHARACTER       = 4

	LINE_SPACING_METHOD_DEFAULT = 0
	LINE_SPACING_METHOD_UNIFORM = 1

	TRIMMING_GRANULARITY_NONE      = 0
	TRIMMING_GRANULARITY_CHARACTER = 1

	FONT_FAMILY_MODEL_TYPOGRAPHIC          = 0
	FONT_FAMILY_MODEL_WEIGHT_STRETCH_STYLE = 1

	INFORMATIONAL_STRING_TYPOGRAPHIC_FAMILY_NAMES = 13
)

// FourCC packs an OpenType tag ("wght", "tnum") as DirectWrite's DWRITE_MAKE_OPENTYPE_TAG does.
func FourCC(s string) uint32 {
	return uint32(s[0]) | uint32(s[1])<<8 | uint32(s[2])<<16 | uint32(s[3])<<24
}

// Structures, laid out as the headers declare them.
type (
	FontMetrics struct {
		DesignUnitsPerEm       uint16
		Ascent, Descent        uint16
		LineGap                int16
		CapHeight, XHeight     uint16
		UnderlinePosition      int16
		UnderlineThickness     uint16
		StrikethroughPosition  int16
		StrikethroughThickness uint16
	}
	TextMetrics struct {
		Left, Top, Width, WidthIncludingTrailingWhitespace, Height float32
		LayoutWidth, LayoutHeight                                  float32
		MaxBidiReorderingDepth, LineCount                          uint32
	}
	LineMetrics struct {
		Length, TrailingWhitespaceLength, NewlineLength uint32
		Height, Baseline                                float32
		IsTrimmed                                       int32
	}
	ClusterMetrics struct {
		Width  float32
		Length uint16
		Flags  uint16 // canWrapLineAfter, isWhitespace, isNewline, isSoftHyphen, isRightToLeft
	}
	HitTestMetrics struct {
		TextPosition, Length     uint32
		Left, Top, Width, Height float32
		BidiLevel                uint32
		IsText, IsTrimmed        int32
	}
	Trimming struct {
		Granularity, Delimiter, DelimiterCount uint32
	}
	FontAxisValue struct {
		Tag   uint32
		Value float32
	}
)

// The DWRITE_CLUSTER_METRICS flag bits.
const (
	ClusterCanWrapAfter = 1 << 0
	ClusterWhitespace   = 1 << 1
	ClusterNewline      = 1 << 2
	ClusterSoftHyphen   = 1 << 3
	ClusterRightToLeft  = 1 << 4
)

var (
	modDWrite               = windows.NewLazySystemDLL("dwrite.dll")
	procDWriteCreateFactory = modDWrite.NewProc("DWriteCreateFactory")
)

// CreateFactory makes (or shares) the process's DirectWrite factory.
func CreateFactory() (*Factory, error) {
	if err := procDWriteCreateFactory.Find(); err != nil {
		return nil, err
	}
	var f *Factory
	r, _, _ := syscall.SyscallN(procDWriteCreateFactory.Addr(), FACTORY_TYPE_SHARED,
		uintptr(unsafe.Pointer(&IID_IDWriteFactory)), uintptr(unsafe.Pointer(&f)))
	if err := com.Check("DWriteCreateFactory", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return f, nil
}

// Factory is IDWriteFactory.
type Factory struct{ com.Object }

// GetSystemFontCollection is the installed fonts.
func (f *Factory) GetSystemFontCollection() (*FontCollection, com.HRESULT) {
	var c *FontCollection
	r, _, _ := syscall.SyscallN(f.Method(3), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(&c)), 0)
	return c, com.HRESULT(r)
}

// CreateTextFormat makes a text format of the system family named by family (UTF-16, NUL
// ended) at size DIPs.
func (f *Factory) CreateTextFormat(family *uint16, weight, style, stretch uint32, size float32, locale *uint16) (*TextFormat, com.HRESULT) {
	var tf *TextFormat
	r, _, _ := syscall.SyscallN(f.Method(15), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(family)), 0, uintptr(weight),
		uintptr(style), uintptr(stretch), com.F(size), uintptr(unsafe.Pointer(locale)), uintptr(unsafe.Pointer(&tf)))
	return tf, com.HRESULT(r)
}

// CreateTypography makes an empty set of OpenType features.
func (f *Factory) CreateTypography() (*Typography, com.HRESULT) {
	var t *Typography
	r, _, _ := syscall.SyscallN(f.Method(16), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(&t)))
	return t, com.HRESULT(r)
}

// CreateTextLayout lays out n UTF-16 units of text in format, in a box maxW × maxH.
func (f *Factory) CreateTextLayout(text *uint16, n uint32, format *TextFormat, maxW, maxH float32) (*TextLayout, com.HRESULT) {
	var l *TextLayout
	r, _, _ := syscall.SyscallN(f.Method(18), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(text)), uintptr(n),
		uintptr(unsafe.Pointer(format)), com.F(maxW), com.F(maxH), uintptr(unsafe.Pointer(&l)))
	return l, com.HRESULT(r)
}

// CreateEllipsisTrimmingSign makes the "…" trimming sign in format's font.
func (f *Factory) CreateEllipsisTrimmingSign(format *TextFormat) (*InlineObject, com.HRESULT) {
	var o *InlineObject
	r, _, _ := syscall.SyscallN(f.Method(20), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(format)), uintptr(unsafe.Pointer(&o)))
	return o, com.HRESULT(r)
}

// Factory6 is IDWriteFactory6 (Windows 10 1903): the typographic font model, in which a
// variable font is one family with all its axes ("Segoe UI Variable" with opsz and wght).
type Factory6 struct{ Factory }

// GetSystemFontCollection is the installed fonts in the family model asked for.
func (f *Factory6) GetSystemFontCollection(includeDownloadable bool, model uint32) (*FontCollection, com.HRESULT) {
	var c *FontCollection
	r, _, _ := syscall.SyscallN(f.Method(51), uintptr(unsafe.Pointer(f)), com.Bool(includeDownloadable), uintptr(model),
		uintptr(unsafe.Pointer(&c)))
	return c, com.HRESULT(r)
}

// CreateTextFormat makes a text format of family in coll whose fonts are picked and set by
// the axis values.
func (f *Factory6) CreateTextFormat(family *uint16, coll *FontCollection, axes *FontAxisValue, n uint32, size float32, locale *uint16) (*TextFormat3, com.HRESULT) {
	var tf *TextFormat3
	r, _, _ := syscall.SyscallN(f.Method(54), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(family)), uintptr(unsafe.Pointer(coll)),
		uintptr(unsafe.Pointer(axes)), uintptr(n), com.F(size), uintptr(unsafe.Pointer(locale)), uintptr(unsafe.Pointer(&tf)))
	return tf, com.HRESULT(r)
}

// FontCollection is IDWriteFontCollection.
type FontCollection struct{ com.Object }

// GetFontFamily is the family at index.
func (c *FontCollection) GetFontFamily(index uint32) (*FontFamily, com.HRESULT) {
	var f *FontFamily
	r, _, _ := syscall.SyscallN(c.Method(4), uintptr(unsafe.Pointer(c)), uintptr(index), uintptr(unsafe.Pointer(&f)))
	return f, com.HRESULT(r)
}

// FindFamilyName looks a family up by name (UTF-16, NUL ended).
func (c *FontCollection) FindFamilyName(name *uint16) (index uint32, exists bool, hr com.HRESULT) {
	var ex int32
	r, _, _ := syscall.SyscallN(c.Method(5), uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&index)),
		uintptr(unsafe.Pointer(&ex)))
	return index, ex != 0, com.HRESULT(r)
}

// FontList is IDWriteFontList.
type FontList struct{ com.Object }

// FontFamily is IDWriteFontFamily.
type FontFamily struct{ FontList }

// GetFirstMatchingFont is the family's font nearest to weight, stretch and style.
func (f *FontFamily) GetFirstMatchingFont(weight, stretch, style uint32) (*Font, com.HRESULT) {
	var font *Font
	r, _, _ := syscall.SyscallN(f.Method(7), uintptr(unsafe.Pointer(f)), uintptr(weight), uintptr(stretch), uintptr(style),
		uintptr(unsafe.Pointer(&font)))
	return font, com.HRESULT(r)
}

// Font is IDWriteFont.
type Font struct{ com.Object }

// GetMetrics is the font's metrics in design units.
func (f *Font) GetMetrics(m *FontMetrics) {
	syscall.SyscallN(f.Method(11), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(m)))
}

// GetInformationalStrings is one of the font's name strings, in every language it has.
func (f *Font) GetInformationalStrings(id uint32) (*LocalizedStrings, bool, com.HRESULT) {
	var ls *LocalizedStrings
	var ex int32
	r, _, _ := syscall.SyscallN(f.Method(9), uintptr(unsafe.Pointer(f)), uintptr(id), uintptr(unsafe.Pointer(&ls)), uintptr(unsafe.Pointer(&ex)))
	return ls, ex != 0, com.HRESULT(r)
}

// CreateFontFace makes the font's face.
func (f *Font) CreateFontFace() (*FontFace, com.HRESULT) {
	var ff *FontFace
	r, _, _ := syscall.SyscallN(f.Method(13), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(&ff)))
	return ff, com.HRESULT(r)
}

// LocalizedStrings is IDWriteLocalizedStrings: one string in several languages.
type LocalizedStrings struct{ com.Object }

// GetCount is the number of languages.
func (l *LocalizedStrings) GetCount() uint32 {
	r, _, _ := syscall.SyscallN(l.Method(3), uintptr(unsafe.Pointer(l)))
	return uint32(r)
}

// GetStringLength is the length of the string at index in UTF-16 units, without its NUL.
func (l *LocalizedStrings) GetStringLength(index uint32) (uint32, com.HRESULT) {
	var n uint32
	r, _, _ := syscall.SyscallN(l.Method(7), uintptr(unsafe.Pointer(l)), uintptr(index), uintptr(unsafe.Pointer(&n)))
	return n, com.HRESULT(r)
}

// GetString copies the string at index, NUL ended, into buf.
func (l *LocalizedStrings) GetString(index uint32, buf []uint16) com.HRESULT {
	if len(buf) == 0 {
		return com.E_INVALIDARG
	}
	r, _, _ := syscall.SyscallN(l.Method(8), uintptr(unsafe.Pointer(l)), uintptr(index), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return com.HRESULT(r)
}

// FontFace is IDWriteFontFace.
type FontFace struct{ com.Object }

// FontFace5 is IDWriteFontFace5 (Windows 10 1803): a face's variable-font axes.
type FontFace5 struct{ FontFace }

// GetFontAxisValueCount is the number of the face's axes.
func (f *FontFace5) GetFontAxisValueCount() uint32 {
	r, _, _ := syscall.SyscallN(f.Method(53), uintptr(unsafe.Pointer(f)))
	return uint32(r)
}

// GetFontAxisValues writes the face's axis values (count of them) to values.
func (f *FontFace5) GetFontAxisValues(values *FontAxisValue, count uint32) com.HRESULT {
	r, _, _ := syscall.SyscallN(f.Method(54), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(values)), uintptr(count))
	return com.HRESULT(r)
}

// Typography is IDWriteTypography.
type Typography struct{ com.Object }

// AddFontFeature adds the OpenType feature tag with parameter (1 turns it on).
func (t *Typography) AddFontFeature(tag, parameter uint32) com.HRESULT {
	r, _, _ := syscall.SyscallN(t.Method(3), uintptr(unsafe.Pointer(t)), com.Pack2U(tag, parameter))
	return com.HRESULT(r)
}

// InlineObject is IDWriteInlineObject (here: the ellipsis trimming sign).
type InlineObject struct{ com.Object }

// TextFormat is IDWriteTextFormat; a TextLayout is one too, and its setters then apply to
// the layout.
type TextFormat struct{ com.Object }

// SetTextAlignment sets where lines sit in the box.
func (f *TextFormat) SetTextAlignment(a uint32) com.HRESULT {
	r, _, _ := syscall.SyscallN(f.Method(3), uintptr(unsafe.Pointer(f)), uintptr(a))
	return com.HRESULT(r)
}

// SetWordWrapping sets where lines may break.
func (f *TextFormat) SetWordWrapping(w uint32) com.HRESULT {
	r, _, _ := syscall.SyscallN(f.Method(5), uintptr(unsafe.Pointer(f)), uintptr(w))
	return com.HRESULT(r)
}

// SetReadingDirection sets the paragraph's base direction.
func (f *TextFormat) SetReadingDirection(d uint32) com.HRESULT {
	r, _, _ := syscall.SyscallN(f.Method(6), uintptr(unsafe.Pointer(f)), uintptr(d))
	return com.HRESULT(r)
}

// SetTrimming sets how overflowing text is cut; sign may be nil.
func (f *TextFormat) SetTrimming(t *Trimming, sign *InlineObject) com.HRESULT {
	r, _, _ := syscall.SyscallN(f.Method(9), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(sign)))
	return com.HRESULT(r)
}

// SetLineSpacing sets the line height and the baseline's distance from a line's top.
func (f *TextFormat) SetLineSpacing(method uint32, lineSpacing, baseline float32) com.HRESULT {
	r, _, _ := syscall.SyscallN(f.Method(10), uintptr(unsafe.Pointer(f)), uintptr(method), com.F(lineSpacing), com.F(baseline))
	return com.HRESULT(r)
}

// TextFormat3 is IDWriteTextFormat3 (Windows 10 1709): variable-font axes.
type TextFormat3 struct{ TextFormat }

// SetFontAxisValues sets the values of count axes; the fonts that have them use them.
func (f *TextFormat3) SetFontAxisValues(values *FontAxisValue, count uint32) com.HRESULT {
	r, _, _ := syscall.SyscallN(f.Method(38), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(values)), uintptr(count))
	return com.HRESULT(r)
}

// TextLayout is IDWriteTextLayout.
type TextLayout struct{ TextFormat }

// SetMaxWidth sets the box's width.
func (l *TextLayout) SetMaxWidth(w float32) com.HRESULT {
	r, _, _ := syscall.SyscallN(l.Method(28), uintptr(unsafe.Pointer(l)), com.F(w))
	return com.HRESULT(r)
}

// SetMaxHeight sets the box's height.
func (l *TextLayout) SetMaxHeight(h float32) com.HRESULT {
	r, _, _ := syscall.SyscallN(l.Method(29), uintptr(unsafe.Pointer(l)), com.F(h))
	return com.HRESULT(r)
}

// SetTypography sets the OpenType features of n units from start.
func (l *TextLayout) SetTypography(t *Typography, start, n uint32) com.HRESULT {
	r, _, _ := syscall.SyscallN(l.Method(40), uintptr(unsafe.Pointer(l)), uintptr(unsafe.Pointer(t)), com.Pack2U(start, n))
	return com.HRESULT(r)
}

// GetLineMetrics writes up to max lines' metrics to lines and returns how many lines there
// are; E_NOT_SUFFICIENT_BUFFER when max is too small (the count is still set).
func (l *TextLayout) GetLineMetrics(lines *LineMetrics, max uint32) (uint32, com.HRESULT) {
	var n uint32
	r, _, _ := syscall.SyscallN(l.Method(59), uintptr(unsafe.Pointer(l)), uintptr(unsafe.Pointer(lines)), uintptr(max),
		uintptr(unsafe.Pointer(&n)))
	return n, com.HRESULT(r)
}

// GetMetrics is the layout's measurements.
func (l *TextLayout) GetMetrics(m *TextMetrics) com.HRESULT {
	r, _, _ := syscall.SyscallN(l.Method(60), uintptr(unsafe.Pointer(l)), uintptr(unsafe.Pointer(m)))
	return com.HRESULT(r)
}

// GetClusterMetrics writes up to max clusters' metrics and returns how many there are
// (E_NOT_SUFFICIENT_BUFFER when max is too small).
func (l *TextLayout) GetClusterMetrics(clusters *ClusterMetrics, max uint32) (uint32, com.HRESULT) {
	var n uint32
	r, _, _ := syscall.SyscallN(l.Method(62), uintptr(unsafe.Pointer(l)), uintptr(unsafe.Pointer(clusters)), uintptr(max),
		uintptr(unsafe.Pointer(&n)))
	return n, com.HRESULT(r)
}

// DetermineMinWidth is the narrowest box the text fits without breaking inside words.
func (l *TextLayout) DetermineMinWidth() (float32, com.HRESULT) {
	var w float32
	r, _, _ := syscall.SyscallN(l.Method(63), uintptr(unsafe.Pointer(l)), uintptr(unsafe.Pointer(&w)))
	return w, com.HRESULT(r)
}

// HitTestPoint finds the text at (x, y) in box coordinates.
func (l *TextLayout) HitTestPoint(x, y float32, m *HitTestMetrics) (trailing, inside bool, hr com.HRESULT) {
	var tr, in int32
	r, _, _ := syscall.SyscallN(l.Method(64), uintptr(unsafe.Pointer(l)), com.F(x), com.F(y), uintptr(unsafe.Pointer(&tr)),
		uintptr(unsafe.Pointer(&in)), uintptr(unsafe.Pointer(m)))
	return tr != 0, in != 0, com.HRESULT(r)
}

// HitTestTextPosition is where the caret at pos (UTF-16) goes, on its leading or trailing
// edge.
func (l *TextLayout) HitTestTextPosition(pos uint32, trailing bool, m *HitTestMetrics) (x, y float32, hr com.HRESULT) {
	r, _, _ := syscall.SyscallN(l.Method(65), uintptr(unsafe.Pointer(l)), uintptr(pos), com.Bool(trailing), uintptr(unsafe.Pointer(&x)),
		uintptr(unsafe.Pointer(&y)), uintptr(unsafe.Pointer(m)))
	return x, y, com.HRESULT(r)
}

// HitTestTextRange writes up to max rectangles covering n units from pos and returns how
// many there are (E_NOT_SUFFICIENT_BUFFER when max is too small).
func (l *TextLayout) HitTestTextRange(pos, n uint32, originX, originY float32, m *HitTestMetrics, max uint32) (uint32, com.HRESULT) {
	var count uint32
	r, _, _ := syscall.SyscallN(l.Method(66), uintptr(unsafe.Pointer(l)), uintptr(pos), uintptr(n), com.F(originX), com.F(originY),
		uintptr(unsafe.Pointer(m)), uintptr(max), uintptr(unsafe.Pointer(&count)))
	return count, com.HRESULT(r)
}

// TextLayout1 is IDWriteTextLayout1 (Windows 8): letter spacing.
type TextLayout1 struct{ TextLayout }

// SetCharacterSpacing adds leading and trailing space to every character of n units from
// start (CSS letter-spacing is trailing).
func (l *TextLayout1) SetCharacterSpacing(leading, trailing, minAdvance float32, start, n uint32) com.HRESULT {
	r, _, _ := syscall.SyscallN(l.Method(69), uintptr(unsafe.Pointer(l)), com.F(leading), com.F(trailing), com.F(minAdvance),
		com.Pack2U(start, n))
	return com.HRESULT(r)
}
