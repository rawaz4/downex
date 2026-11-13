//go:build windows

package win

import (
	"fmt"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/dwrite"
)

// unbounded is the box width of a layout without MaxWidth while it is measured, and the box
// height of every layout: wide enough for any line the UI shows.
const unbounded = 1 << 20

// textLayout is a kit.TextLayout: a DirectWrite layout of the text (or, after a middle
// ellipsis, of what is shown of it) and its measurements.
type textLayout struct {
	res    *Resources
	text   string
	layout *dwrite.TextLayout
	m      kit.TextMetrics
	aa     kit.Antialias
	// A middle ellipsis replaces the UTF-16 units [cutFrom, cutTo) of the text with one
	// "…" at cutFrom of the shown text; cut is false otherwise.
	cut            bool
	cutFrom, cutTo int
	n16            int // UTF-16 length of the text
	shownN16       int // UTF-16 length of what the layout holds
	// glued are the UTF-16 indexes of the text before which the layout holds a WORD JOINER
	// (U+2060), sorted: after a "/" that a word follows, where Chromium never breaks a line
	// and DirectWrite would ("github.com/" | "denoland/deno."). Only a wrapping layout has
	// them, never one with a middle ellipsis (that is one line).
	glued []int
}

// LayoutText implements kit.TextMeasurer.
func (r *Resources) LayoutText(text string, style kit.TextStyle, opts kit.TextOptions) (kit.TextLayout, error) {
	l, err := r.layoutText(text, style, opts)
	if err != nil {
		return nil, err
	}
	return l, nil
}

func (r *Resources) layoutText(text string, style kit.TextStyle, opts kit.TextOptions) (*textLayout, error) {
	l := &textLayout{res: r}
	if err := l.lay(text, style, opts); err != nil {
		l.release()
		return nil, err
	}
	r.live.layouts++
	return l, nil
}

// Relayout implements kit.TextRelayouter: the layout holds text in style with opts in place
// of what it held, so that a label whose text changes every tick (a speed, a percentage)
// makes no new layout object. A failure releases the layout.
func (l *textLayout) Relayout(text string, style kit.TextStyle, opts kit.TextOptions) error {
	if l.layout == nil {
		return errInvalid("relayout of a released text layout")
	}
	if err := l.lay(text, style, opts); err != nil {
		l.release()
		l.res.live.layouts--
		return err
	}
	return nil
}

// lay lays out text in l, replacing what l held. On an error the caller releases l.
func (l *textLayout) lay(text string, style kit.TextStyle, opts kit.TextOptions) error {
	r := l.res
	if err := r.usable(); err != nil {
		return err
	}
	s, err := normStyle(style)
	if err != nil {
		return err
	}
	if !(opts.MaxWidth >= 0) || !finite(opts.MaxWidth) {
		return errInvalid("text box width %v", opts.MaxWidth)
	}
	opts.MaxLines = max(opts.MaxLines, 0)
	f, err := r.format(s)
	if err != nil {
		return err
	}
	l.text, l.aa = text, opts.Antialias
	l.cut, l.cutFrom, l.cutTo = false, 0, 0
	u, n := r.utf16Of(text)
	l.n16, l.shownN16 = n, n
	l.glued = l.glued[:0]
	if opts.MaxWidth > 0 && (opts.Wrap == kit.TextWrapWords || opts.Wrap == kit.TextWrapAnywhere) {
		u, n = l.glue(u, n)
	}
	dir := opts.Direction
	if dir == kit.DirAuto {
		dir = kit.FirstStrong(text)
	}
	middle := false
	if opts.Truncate == kit.TruncateMiddle {
		// The middle ellipsis is for one line; elsewhere it acts as the end one.
		middle = opts.MaxWidth > 0 && opts.Wrap == kit.TextWrapNone && !hasHardBreak(text)
		if middle {
			opts.Truncate = kit.TruncateNone
		} else {
			opts.Truncate = kit.TruncateEnd
		}
	}
	if err := l.build(f, u, n, opts, dir); err != nil {
		return err
	}
	if middle && l.m.Advance > opts.MaxWidth+0.01 {
		return l.cutMiddle(f, opts, dir)
	}
	return nil
}

// glue is u (n units and its NUL) with a WORD JOINER after every "/" that a word follows,
// recorded in l.glued: Chromium's line breaking (the WebView window's) has no break
// opportunity there, so an address such as github.com/denoland/deno moves to the next line
// whole, while DirectWrite's (UAX #14, where a solidus allows a break after it) breaks inside
// it. A break after the slash stays where white space or a line break follows it. Text
// without such a slash is returned as it is.
func (l *textLayout) glue(u []uint16, n int) ([]uint16, int) {
	for i := 0; i+1 < n; i++ {
		if u[i] == '/' && !breakAfter(u[i+1]) {
			l.glued = append(l.glued, i+1)
		}
	}
	if len(l.glued) == 0 {
		return u, n
	}
	out := make([]uint16, 0, n+len(l.glued)+1)
	prev := 0
	for _, at := range l.glued {
		out = append(out, u[prev:at]...)
		out = append(out, wordJoiner)
		prev = at
	}
	out = append(out, u[prev:n]...)
	m := len(out)
	l.shownN16 = m
	return append(out, 0), m
}

// wordJoiner is U+2060 WORD JOINER: no line break on either side of it, and no width.
const wordJoiner = 0x2060

// breakAfter reports whether the UTF-16 unit c, following a "/", leaves a line break after
// the slash: white space or a hard line break.
func breakAfter(c uint16) bool {
	switch c {
	case ' ', 0x09, 0x0A, 0x0D, 0x0B, 0x0C, 0x85, 0x2028, 0x2029, 0x3000:
		return true
	}
	return c >= 0x2000 && c <= 0x200B && c != 0x2007 // the spaces that break (not the figure space), and ZWSP
}

// build lays out n units of u (u[n] is 0) in f with opts.
func (l *textLayout) build(f *textFormat, u []uint16, n int, opts kit.TextOptions, dir kit.Direction) error {
	r := l.res
	com.Release(&l.layout)
	w := opts.MaxWidth
	free := w == 0
	if free {
		w = unbounded
	}
	tl, hr := r.dw.CreateTextLayout(&u[0], uint32(n), f.format, w, unbounded)
	if hr.Failed() {
		return fmt.Errorf("win: %w", com.Check("IDWriteFactory::CreateTextLayout", hr))
	}
	l.layout = tl
	wrap := uint32(dwrite.WORD_WRAPPING_NO_WRAP)
	if !free {
		switch opts.Wrap {
		case kit.TextWrapWords:
			wrap = dwrite.WORD_WRAPPING_WHOLE_WORD
		case kit.TextWrapAnywhere:
			wrap = dwrite.WORD_WRAPPING_EMERGENCY_BREAK
		case kit.TextWrapCharacters:
			wrap = dwrite.WORD_WRAPPING_CHARACTER
		}
	}
	tl.SetWordWrapping(wrap)
	rd := uint32(dwrite.READING_DIRECTION_LEFT_TO_RIGHT)
	if dir == kit.DirRTL {
		rd = dwrite.READING_DIRECTION_RIGHT_TO_LEFT
	}
	tl.SetReadingDirection(rd)
	align := uint32(dwrite.TEXT_ALIGNMENT_LEADING)
	switch opts.Align {
	case kit.TextAlignCenter:
		align = dwrite.TEXT_ALIGNMENT_CENTER
	case kit.TextAlignEnd:
		align = dwrite.TEXT_ALIGNMENT_TRAILING
	}
	if n > 0 {
		if f.tabular {
			tl.SetTypography(r.tnum, 0, uint32(n))
		}
		if f.spacing != 0 {
			if l1, hr := com.Query[dwrite.TextLayout1](&tl.Object, &dwrite.IID_IDWriteTextLayout1); !hr.Failed() && l1 != nil {
				l1.SetCharacterSpacing(0, f.spacing, 0, 0, uint32(n))
				l1.Release()
			}
		}
	}
	trim := !free && (opts.Truncate != kit.TruncateNone || opts.MaxLines > 0)
	if trim {
		if sign := r.ellipsisSign(f); sign != nil {
			t := dwrite.Trimming{Granularity: dwrite.TRIMMING_GRANULARITY_CHARACTER}
			tl.SetTrimming(&t, sign)
		}
	}

	var tm dwrite.TextMetrics
	if hr := tl.GetMetrics(&tm); hr.Failed() {
		return fmt.Errorf("win: %w", com.Check("IDWriteTextLayout::GetMetrics", hr))
	}
	lines, err := l.lineMetrics()
	if err != nil {
		return err
	}
	if free {
		// The box is as wide as the text, so that alignment and right-to-left text start
		// at its edges.
		tl.SetMaxWidth(tm.Width)
	} else if opts.Align != kit.TextAlignStart && wrap == dwrite.WORD_WRAPPING_NO_WRAP && tm.WidthIncludingTrailingWhitespace > w {
		// A line wider than its box starts at its start edge whatever the alignment (CSS).
		align = dwrite.TEXT_ALIGNMENT_LEADING
	}
	tl.SetTextAlignment(align)

	visible := len(lines)
	height := tm.Height
	truncated := false
	if opts.MaxLines > 0 && len(lines) > opts.MaxLines && !free {
		visible = opts.MaxLines
		height = 0
		for _, lm := range lines[:visible] {
			height += lm.Height
		}
		// DirectWrite trims what does not fit the box's height and ends the last line kept
		// with the ellipsis.
		tl.SetMaxHeight(height + 0.5)
		truncated = true
		if lines, err = l.lineMetrics(); err != nil {
			return err
		}
	}
	for _, lm := range lines[:min(visible, len(lines))] {
		if lm.IsTrimmed != 0 {
			truncated = true
		}
	}
	if hr := tl.GetMetrics(&tm); hr.Failed() {
		return fmt.Errorf("win: %w", com.Check("IDWriteTextLayout::GetMetrics", hr))
	}
	m := kit.TextMetrics{
		Width:     tm.Width,
		Advance:   tm.WidthIncludingTrailingWhitespace,
		Height:    height,
		Lines:     max(visible, 1),
		Truncated: truncated,
		Direction: kit.DirLTR,
	}
	if dir == kit.DirRTL {
		m.Direction = kit.DirRTL
	}
	if visible < len(lines) || truncated {
		// After trimming DirectWrite reports the kept text's width.
		m.Width = min(m.Width, w)
		m.Advance = min(m.Advance, w)
	}
	if len(lines) > 0 {
		m.Baseline = lines[0].Baseline
	} else {
		m.Baseline = f.metrics.Baseline
	}
	switch {
	case free || opts.Wrap == kit.TextWrapNone:
		m.MinWidth = m.Width
	case opts.Wrap == kit.TextWrapWords:
		if mw, hr := tl.DetermineMinWidth(); !hr.Failed() {
			m.MinWidth = mw
		}
	default:
		m.MinWidth = l.widestCluster()
	}
	l.m = m
	return nil
}

// lineMetrics reads the layout's lines into the shared scratch buffer.
func (l *textLayout) lineMetrics() ([]dwrite.LineMetrics, error) {
	r := l.res
	if cap(r.lines) < 4 {
		r.lines = make([]dwrite.LineMetrics, 16)
	}
	buf := r.lines[:cap(r.lines)]
	n, hr := l.layout.GetLineMetrics(&buf[0], uint32(len(buf)))
	if isNotSufficient(hr) {
		r.lines = make([]dwrite.LineMetrics, n)
		buf = r.lines
		n, hr = l.layout.GetLineMetrics(&buf[0], uint32(len(buf)))
	}
	if hr.Failed() {
		return nil, fmt.Errorf("win: %w", com.Check("IDWriteTextLayout::GetLineMetrics", hr))
	}
	return buf[:n], nil
}

// clusterMetrics reads the layout's clusters into the shared scratch buffer.
func (l *textLayout) clusterMetrics() []dwrite.ClusterMetrics {
	r := l.res
	if cap(r.clusters) < 16 {
		r.clusters = make([]dwrite.ClusterMetrics, 64)
	}
	buf := r.clusters[:cap(r.clusters)]
	n, hr := l.layout.GetClusterMetrics(&buf[0], uint32(len(buf)))
	if isNotSufficient(hr) {
		r.clusters = make([]dwrite.ClusterMetrics, n)
		buf = r.clusters
		n, hr = l.layout.GetClusterMetrics(&buf[0], uint32(len(buf)))
	}
	if hr.Failed() {
		return nil
	}
	return buf[:n]
}

func (l *textLayout) widestCluster() float32 {
	var w float32
	for _, c := range l.clusterMetrics() {
		if c.Flags&dwrite.ClusterWhitespace == 0 {
			w = max(w, c.Width)
		}
	}
	return w
}

func hasHardBreak(s string) bool {
	for _, c := range s {
		switch c {
		case '\n', '\r', ' ', ' ', '\u0085', '\v', '\f':
			return true
		}
	}
	return false
}

// Metrics implements kit.TextLayout.
func (l *textLayout) Metrics() kit.TextMetrics {
	if l.layout == nil {
		return kit.TextMetrics{}
	}
	return l.m
}

// Release implements kit.TextLayout.
func (l *textLayout) Release() {
	if l.layout == nil {
		return
	}
	l.release()
	l.res.live.layouts--
}

// release frees the layout's DirectWrite object.
func (l *textLayout) release() {
	com.Release(&l.layout)
	l.m = kit.TextMetrics{}
}

// shown maps a UTF-16 index of the text to one of the layout's text, and back: across the
// middle ellipsis (cut) or the word joiners (glued; a caret at a joiner's place goes after
// it, before the character it keeps on the slash's line).
func (l *textLayout) shown(i int) int {
	if len(l.glued) > 0 {
		k := 0
		for k < len(l.glued) && l.glued[k] <= i {
			k++
		}
		return i + k
	}
	if !l.cut || i <= l.cutFrom {
		return i
	}
	if i < l.cutTo {
		return l.cutFrom // inside what the ellipsis stands for: the caret before it
	}
	return i - l.cutTo + l.cutFrom + 1
}

func (l *textLayout) source(i int) int {
	if len(l.glued) > 0 {
		k := 0
		for k < len(l.glued) && l.glued[k]+k < i {
			k++
		}
		return i - k
	}
	if !l.cut || i <= l.cutFrom {
		return i
	}
	if i == l.cutFrom+1 {
		return l.cutTo
	}
	return i - l.cutFrom - 1 + l.cutTo
}

// HitTest implements kit.TextLayout.
func (l *textLayout) HitTest(p kit.Point) kit.TextHit {
	if l.layout == nil || !finitePt(p) {
		return kit.TextHit{}
	}
	var m dwrite.HitTestMetrics
	trailing, inside, hr := l.layout.HitTestPoint(p.X, p.Y, &m)
	if hr.Failed() {
		return kit.TextHit{}
	}
	pos := int(m.TextPosition)
	switch {
	case l.cut && pos == l.cutFrom:
		// On the ellipsis: the start of the text it stands for.
	case m.IsTrimmed != 0:
		// On DirectWrite's trimming sign: the same.
	case trailing:
		pos += int(m.Length)
	}
	return kit.TextHit{Pos: utf8Offset(l.text, l.source(pos)), Inside: inside}
}

// Caret implements kit.TextLayout.
func (l *textLayout) Caret(pos int) (kit.Point, kit.DIP) {
	if l.layout == nil {
		return kit.Point{}, 0
	}
	i := l.shown(utf16Len(l.text, pos))
	var m dwrite.HitTestMetrics
	trailing := false
	if i >= l.shownN16 && l.shownN16 > 0 {
		i, trailing = l.shownN16-1, true
	}
	x, _, hr := l.layout.HitTestTextPosition(uint32(i), trailing, &m)
	if hr.Failed() {
		return kit.Point{}, l.m.Height
	}
	return kit.Point{X: x, Y: m.Top}, m.Height
}

// AppendRangeRects implements kit.TextLayout.
func (l *textLayout) AppendRangeRects(dst []kit.Rect, start, end int) []kit.Rect {
	if l.layout == nil || end <= start {
		return dst
	}
	a, b := l.shown(utf16Len(l.text, start)), utf16Len(l.text, end)
	bs := l.shown(b)
	if l.cut && b > l.cutFrom && b < l.cutTo {
		bs = l.cutFrom + 1 // a range ending in the hidden text covers the ellipsis
	}
	if bs <= a {
		return dst
	}
	r := l.res
	if cap(r.hits) < 8 {
		r.hits = make([]dwrite.HitTestMetrics, 16)
	}
	buf := r.hits[:cap(r.hits)]
	n, hr := l.layout.HitTestTextRange(uint32(a), uint32(bs-a), 0, 0, &buf[0], uint32(len(buf)))
	if isNotSufficient(hr) {
		r.hits = make([]dwrite.HitTestMetrics, n)
		buf = r.hits
		n, hr = l.layout.HitTestTextRange(uint32(a), uint32(bs-a), 0, 0, &buf[0], uint32(len(buf)))
	}
	if hr.Failed() {
		return dst
	}
	for _, h := range buf[:n] {
		dst = append(dst, kit.R(h.Left, h.Top, h.Left+h.Width, h.Top+h.Height))
	}
	return dst
}

var (
	_ kit.TextLayout     = (*textLayout)(nil)
	_ kit.TextRelayouter = (*textLayout)(nil)
)
