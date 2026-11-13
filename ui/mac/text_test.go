//go:build darwin && cgo

package mac

import (
	"errors"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// Fonts as app.css names them (ui/kit/tokens).
const (
	fontUI      = tokens.FontUI
	fontDisplay = tokens.FontDisplay
	fontMono    = tokens.FontMono
)

func newEngine(t testing.TB) *textEngine {
	t.Helper()
	e, err := newTextEngine()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e.live != 0 {
			t.Errorf("leaked %d layouts", e.live)
		}
		e.close()
	})
	return e
}

func lay(t testing.TB, e *textEngine, text string, s kit.TextStyle, o kit.TextOptions) *textLayout {
	t.Helper()
	tl, err := e.layoutText(text, s, o)
	if err != nil {
		t.Fatal(err)
	}
	l := tl.(*textLayout)
	t.Cleanup(l.Release)
	return l
}

// The system has neither Segoe UI nor Cascadia (the tests that name SF assume so, as the
// owner's Mac and WebKit's resolution there do).
func segoeInstalled(e *textEngine) bool {
	return e.isInstalled("Segoe UI") || e.isInstalled("Segoe UI Variable Text") || e.isInstalled("Cascadia Mono")
}

var body = kit.TextStyle{Family: fontUI, Size: 13, LineHeight: 1.4, Tabular: true}

// WKWebView's getBoundingClientRect widths on macOS 26 (the window today), the same CSS: the
// page title at every weight (Display, 20 px, -0.02em, tabular) and a row name (13 px), the
// row names, Arabic and Kurdish names from the system's fallback, emoji, and an address in
// the mono font at its 350 weight and -0.01em.
func TestWebKitWidths(t *testing.T) {
	e := newEngine(t)
	if segoeInstalled(e) {
		t.Skip("Segoe UI or Cascadia installed: WebKit uses them, the references are SF's")
	}
	for _, c := range []struct {
		w             float32
		title, name13 float64
	}{
		{300, 114.7569, 168.1312},
		{350, 115.5648, 168.6227},
		{400, 116.3726, 169.1143},
		{450, 118.0430, 171.0953},
		{500, 119.7146, 173.0778},
		{550, 121.3849, 175.0588},
		{600, 123.0553, 177.0399},
		{650, 124.7269, 179.0224},
		{700, 126.3973, 181.0034},
	} {
		title := kit.TextStyle{Family: fontDisplay, Size: 20, Weight: c.w, LetterSpacing: -0.02, LineHeight: 1.2, Tabular: true, OpticalSize: 20}
		name := kit.TextStyle{Family: fontUI, Size: 13, Weight: c.w, LineHeight: 1.4, Tabular: true, OpticalSize: 13}
		gt := lay(t, e, "All downloads", title, kit.TextOptions{}).Metrics().Width
		gn := lay(t, e, "os-image-26.04-amd64.iso", name, kit.TextOptions{}).Metrics().Width
		if math.Abs(float64(gt)-c.title) > 0.05 || math.Abs(float64(gn)-c.name13) > 0.05 {
			t.Errorf("weight %v: title %.4f, name %.4f; WebKit %.4f, %.4f", c.w, gt, gn, c.title, c.name13)
		}
	}
	item := kit.TextStyle{Family: fontUI, Size: 13, Weight: 400, LineHeight: 1.4, Tabular: true}
	semi := item
	semi.Weight = 600
	figure := kit.TextStyle{Family: fontDisplay, Size: 28, Weight: 650, LineHeight: 1.2, Tabular: true}
	tag := kit.TextStyle{Family: fontUI, Size: 10, Weight: 650, LetterSpacing: 0.04, Tabular: true}
	italic := item
	italic.Italic = true
	prop := item
	prop.Tabular = false
	mono := kit.TextStyle{Family: fontMono, Size: 12, Weight: tokens.MonoWeight, LetterSpacing: -0.01, LineHeight: 1.4, Tabular: true}
	for _, c := range []struct {
		text string
		st   kit.TextStyle
		wk   float64
	}{
		{"os-image-26.04-amd64.iso", item, 169.1143},
		{"telemetry-183.dat", item, 111.0649},
		// WebKit measures the pieces between break opportunities alone: no kerning
		// across "-t" (Core Text's whole line is 0.25 px narrower)
		{"city-timelapse-4k.mp4", item, 138.6455},
		{"annual-report-2025.pdf", item, 145.5645},
		{"Documents", item, 69.0244},
		{"تقرير-الميزانية-٢٠٢٦.xlsx", item, 135.6621},
		{"ڕاپۆرتی-ساڵانە-٢٠٢٥.pdf", item, 130.9585},
		{"a😀é́b", item, 41.3564},
		{"Hello 👋🏽 world", item, 90.4492},
		{"Straße naïve café", item, 105.1426},
		{"12:34 — 56%", item, 82.8560},
		{"1111", prop, 23.8164},
		{"8888", prop, 32.9062},
		{"Download complete", semi, 125.1416},
		{"Download complete 1,234.5", figure, 368.3875},
		{"HLS DASH", tag, 55.9031},
		{"Italic text 123", italic, 84.0430},
		{"Hello world", kit.TextStyle{Family: "'No Such Font', sans-serif", Size: 13}, 64.3018},
		{"Hello world", kit.TextStyle{Family: "monospace", Size: 13}, 85.8140},
		{"Hello world", kit.TextStyle{Family: "Menlo", Size: 13}, 86.0933},
		{"https://media.example.org/videos/2026/city-timelapse-4k-final.mp4", mono, 474.3681},
	} {
		got := lay(t, e, c.text, c.st, kit.TextOptions{}).Metrics().Width
		if math.Abs(float64(got)-c.wk) > 0.05 {
			t.Errorf("%q measures %.4f px, WebKit %.4f", c.text, got, c.wk)
		}
	}
}

func TestFamilies(t *testing.T) {
	got := parseFamilies(fontMono)
	want := []string{"Cascadia Mono", "Cascadia Code", "Consolas", "ui-monospace", "SF Mono", "Menlo", "DejaVu Sans Mono", "monospace"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("parseFamilies = %q", got)
	}
	if got := parseFamilies(` "A B" ,  plain   name ,'C'`); strings.Join(got, "|") != "A B|plain name|C" {
		t.Fatalf("parseFamilies = %q", got)
	}
	if got := parseFamilies(`'Open`); strings.Join(got, "|") != "Open" {
		t.Fatalf("parseFamilies of an unterminated quote = %q", got)
	}
	e := newEngine(t)
	if segoeInstalled(e) {
		t.Skip("Segoe UI or Cascadia installed")
	}
	for css, want := range map[string]family{
		fontUI:                           {kind: kindSystem},
		fontDisplay:                      {kind: kindSystem},
		fontMono:                         {kind: kindMono},
		"'No Such Font', ui-monospace":   {kind: kindMono},
		"'No Such Font', monospace":      {kind: kindNamed, name: "Courier"},
		"'No Such Font', system-ui":      {kind: kindSystem},
		"-apple-system":                  {kind: kindSystem},
		"":                               {kind: kindSystem},
		"'No Such Font', serif, cursive": {kind: kindSystem},
		"'No Such Font', sans-serif":     {kind: kindNamed, name: "Helvetica"},
		"Menlo, monospace":               {kind: kindNamed, name: "Menlo"},
		"'.SF NS', system-ui":            {kind: kindSystem}, // private system names are not matched
	} {
		if got := e.resolveFamily(css); got != want {
			t.Errorf("resolveFamily(%q) = %+v, want %+v", css, got, want)
		}
	}
}

func TestBadStyles(t *testing.T) {
	e := newEngine(t)
	for _, s := range []kit.TextStyle{{Size: 0}, {Size: -1}, {Size: float32(math.NaN())}, {Size: float32(math.Inf(1))}, {Size: 13, Weight: float32(math.NaN())}, {Size: 13, LetterSpacing: float32(math.Inf(-1))}} {
		if _, err := e.layoutText("x", s, kit.TextOptions{}); !errors.Is(err, kit.ErrInvalid) {
			t.Errorf("layoutText with %+v: %v, want ErrInvalid", s, err)
		}
		if _, err := e.fontMetrics(s); !errors.Is(err, kit.ErrInvalid) {
			t.Errorf("fontMetrics with %+v: %v, want ErrInvalid", s, err)
		}
	}
	for _, w := range []float32{-1, float32(math.NaN()), float32(math.Inf(1))} {
		if _, err := e.layoutText("x", kit.TextStyle{Size: 13}, kit.TextOptions{MaxWidth: w}); !errors.Is(err, kit.ErrInvalid) {
			t.Errorf("MaxWidth %v: %v", w, err)
		}
	}
}

// Line boxes in whole pixels, as WKWebView lays them out: the line-height multiple
// truncated, the ascent and descent rounded, the half-leading above floored. The numbers
// are WebKit's (the height of a block of one line and its baseline).
func TestLineBox(t *testing.T) {
	e := newEngine(t)
	if segoeInstalled(e) {
		t.Skip("Segoe UI or Cascadia installed")
	}
	for _, c := range []struct {
		family       string
		size, weight float32
		lh           float32
		height, base float32
	}{
		{fontUI, 13, 400, 1.4, 18, 14},
		{fontUI, 10, 400, 1.4, 14, 11},
		{fontUI, 10.5, 400, 1.4, 14, 11},
		{fontUI, 11.5, 400, 1.4, 16, 12},
		{fontUI, 12, 400, 1.4, 16, 12},
		{fontUI, 12.5, 400, 1.4, 17, 13},
		{fontUI, 14, 400, 1.4, 19, 15},
		{fontUI, 15.5, 400, 1.4, 21, 16},
		{fontUI, 16, 400, 1.4, 22, 17},
		{fontUI, 20, 400, 1.4, 28, 21},
		{fontUI, 28, 400, 1.4, 39, 30},
		{fontDisplay, 10.5, 650, 1.2, 12, 10},
		{fontDisplay, 13, 650, 1.2, 15, 12},
		{fontDisplay, 15.5, 650, 1.2, 18, 15},
		{fontDisplay, 20, 650, 1.2, 24, 19},
		{fontDisplay, 28, 650, 1.2, 33, 27},
		{fontMono, 12, 350, 1.4, 16, 12},
		{fontMono, 13, 350, 1.4, 18, 14},
		// normal line height: ascent + descent + gap, each rounded
		{fontUI, 13, 400, 0, 16, 13},
		{fontUI, 16, 400, 0, 18, 15},
		{fontDisplay, 20, 650, 0, 23, 19},
		{fontMono, 12, 350, 0, 15, 12},
		{"sans-serif", 13, 400, 0, 15, 12}, // Helvetica, with WebKit's 15 % more ascent
	} {
		st := kit.TextStyle{Family: c.family, Size: c.size, Weight: c.weight, LineHeight: c.lh, Tabular: true}
		fm, err := e.fontMetrics(st)
		if err != nil {
			t.Fatal(err)
		}
		m := lay(t, e, "Hg", st, kit.TextOptions{}).Metrics()
		if fm.LineHeight != c.height || fm.Baseline != c.base || m.Height != c.height || m.Baseline != c.base || m.Lines != 1 {
			t.Errorf("%v px %v ×%v: line %v base %v (layout %v, %v), WebKit %v, %v", c.size, c.weight, c.lh, fm.LineHeight, fm.Baseline, m.Height, m.Baseline, c.height, c.base)
		}
		if fm.Ascent != float32(math.Round(float64(fm.Ascent))) || fm.CapHeight <= 0 || fm.XHeight <= 0 || fm.UnderlineThickness <= 0 || fm.UnderlineOffset <= 0 {
			t.Errorf("%v px: metrics %+v", c.size, fm)
		}
	}
}

// Tabular figures: every digit has one width; a text without digits measures the same.
func TestTabular(t *testing.T) {
	e := newEngine(t)
	ones := lay(t, e, "1111", body, kit.TextOptions{}).Metrics().Width
	eights := lay(t, e, "8888", body, kit.TextOptions{}).Metrics().Width
	if math.Abs(float64(ones-eights)) > 0.01 {
		t.Errorf("tabular 1111 = %.2f, 8888 = %.2f", ones, eights)
	}
	prop := body
	prop.Tabular = false
	pones := lay(t, e, "1111", prop, kit.TextOptions{}).Metrics().Width
	peights := lay(t, e, "8888", prop, kit.TextOptions{}).Metrics().Width
	if pones >= peights {
		t.Errorf("proportional 1111 = %.2f should be narrower than 8888 = %.2f", pones, peights)
	}
	if a, b := lay(t, e, "All downloads", body, kit.TextOptions{}).Metrics().Width, lay(t, e, "All downloads", prop, kit.TextOptions{}).Metrics().Width; a != b {
		t.Errorf("tabular figures changed a text without digits: %.3f, %.3f", a, b)
	}
}

// CSS letter-spacing adds the same space after every character, the last one included
// (WebKit: QUEUES at 10.5 px 600 is 44.3942 px, 48.8042 with 0.07em).
func TestLetterSpacing(t *testing.T) {
	e := newEngine(t)
	caps := kit.TextStyle{Family: fontUI, Size: 10.5, Weight: 600}
	plain := lay(t, e, "QUEUES", caps, kit.TextOptions{}).Metrics().Advance
	caps.LetterSpacing = 0.07
	spaced := lay(t, e, "QUEUES", caps, kit.TextOptions{}).Metrics().Advance
	if want := plain + 6*0.07*10.5; math.Abs(float64(spaced-want)) > 0.01 {
		t.Errorf("letter-spaced %.3f, want %.3f + 6 × 0.735 = %.3f", spaced, plain, want)
	}
	if !segoeInstalled(e) && (math.Abs(float64(plain)-44.3942) > 0.05 || math.Abs(float64(spaced)-48.8042) > 0.05) {
		t.Errorf("QUEUES %.4f / %.4f px, WebKit 44.3942 / 48.8042", plain, spaced)
	}
}

// Wider weights are wider; bigger sizes are wider.
func TestWidthsMonotonic(t *testing.T) {
	e := newEngine(t)
	prev := float32(0)
	for w := float32(100); w <= 900; w += 50 {
		got := lay(t, e, "Downloading", kit.TextStyle{Family: fontUI, Size: 13, Weight: w}, kit.TextOptions{}).Metrics().Width
		if got < prev {
			t.Errorf("weight %v: %.3f narrower than the lighter %.3f", w, got, prev)
		}
		prev = got
	}
	prev = 0
	for s := float32(8); s <= 40; s += 0.5 {
		got := lay(t, e, "Downloading", kit.TextStyle{Family: fontUI, Size: s}, kit.TextOptions{}).Metrics().Width
		if got <= prev {
			t.Errorf("size %v: %.3f not wider than %.3f", s, got, prev)
		}
		prev = got
	}
	full := "conference-keynote-recording"
	prev = 0
	for i := 1; i <= len(full); i++ {
		got := lay(t, e, full[:i], body, kit.TextOptions{}).Metrics().Width
		if got <= prev {
			t.Errorf("%q: %.3f not wider than its prefix %.3f", full[:i], got, prev)
		}
		prev = got
	}
	it := body
	it.Italic = true
	if a, b := lay(t, e, "Italic", it, kit.TextOptions{}).Metrics().Width, lay(t, e, "Italic", body, kit.TextOptions{}).Metrics().Width; a == b || a <= 0 {
		t.Errorf("italic %.3f, upright %.3f", a, b)
	}
}

func TestTruncateEnd(t *testing.T) {
	e := newEngine(t)
	text := "conference-keynote-recording-2026-final-version.mp4"
	l := lay(t, e, text, body, kit.TextOptions{MaxWidth: 120, Truncate: kit.TruncateEnd})
	m := l.Metrics()
	if !m.Truncated || m.Width > 120.01 || m.Lines != 1 || !l.lines[0].cut || l.lines[0].cutTo != len(l.u) {
		t.Fatalf("end-truncated %+v", m)
	}
	from := utf8Offset(text, l.lines[0].cutFrom)
	if from < 10 || !strings.HasPrefix(text, text[:from]) {
		t.Errorf("kept %q", text[:from])
	}
	// a point on the ellipsis hits the start of what it hides; a caret in the hidden text
	// goes before the ellipsis
	c, _ := l.Caret(from)
	if c2, _ := l.Caret(from + 5); c2 != c {
		t.Errorf("caret in the hidden text %v, at the cut %v", c2, c)
	}
	if h := l.HitTest(kit.Pt(c.X+3, 5)); h.Pos != from || !h.Inside {
		t.Errorf("hit on the ellipsis %+v, want %d", h, from)
	}
	if rs := l.AppendRangeRects(nil, 0, len(text)); len(rs) != 1 || math.Abs(float64(rs[0].Width()-m.Width)) > 0.5 {
		t.Errorf("range of the whole text %v, width %.2f", rs, m.Width)
	}
	fits := lay(t, e, "short.mp4", body, kit.TextOptions{MaxWidth: 120, Truncate: kit.TruncateEnd}).Metrics()
	if fits.Truncated {
		t.Errorf("a text that fits is marked truncated: %+v", fits)
	}
	// right to left, the ellipsis ends the line on the left
	rtl := lay(t, e, "ڕاپۆرتی-ساڵانەی-کۆمپانیاکە-بۆ-ساڵی-٢٠٢٥.pdf", body, kit.TextOptions{MaxWidth: 100, Truncate: kit.TruncateEnd})
	if !rtl.Metrics().Truncated || rtl.Metrics().Direction != kit.DirRTL {
		t.Fatalf("RTL truncated %+v", rtl.Metrics())
	}
	ell := rtl.AppendRangeRects(nil, utf8Offset(rtl.text, rtl.lines[0].cutFrom), len(rtl.text))
	if len(ell) != 1 || ell[0].Min.X > 10 {
		t.Errorf("the RTL ellipsis is at %v, want at the left", ell)
	}
}

func TestTruncateMiddle(t *testing.T) {
	e := newEngine(t)
	text := "https://media.example.org/videos/2026/city-timelapse-4k-final.mp4"
	l := lay(t, e, text, body, kit.TextOptions{MaxWidth: 180, Truncate: kit.TruncateMiddle})
	m := l.Metrics()
	ln := l.lines[0]
	if !m.Truncated || m.Advance > 180.01 || !ln.cut {
		t.Fatalf("middle-truncated %+v", m)
	}
	from, to := utf8Offset(text, ln.cutFrom), utf8Offset(text, ln.cutTo)
	head, tail := text[:from], text[to:]
	if len(head) < 10 || len(tail) < 10 || math.Abs(float64(len(head)-len(tail))) > 6 {
		t.Fatalf("kept %q … %q", head, tail)
	}
	t.Logf("shown: %q…%q (%.1f px)", head, tail, m.Advance)
	caret, _ := l.Caret(from)
	hidden, _ := l.Caret(from + 3)
	if caret != hidden {
		t.Errorf("caret at %d = %v, inside the hidden text %v", from, caret, hidden)
	}
	if hit := l.HitTest(kit.Pt(caret.X+2, 5)); hit.Pos != from {
		t.Errorf("hit on the ellipsis = %d, want %d", hit.Pos, from)
	}
	after, _ := l.Caret(to)
	if after.X <= caret.X+3 {
		t.Errorf("caret after the cut %.2f, before it %.2f: the ellipsis is between", after.X, caret.X)
	}
	if hit := l.HitTest(kit.Pt(after.X+0.2, 5)); hit.Pos != to {
		t.Errorf("hit after the ellipsis = %d, want %d", hit.Pos, to)
	}
	end, _ := l.Caret(len(text))
	if math.Abs(float64(end.X-m.Advance)) > 0.5 {
		t.Errorf("caret at the end %.2f, advance %.2f", end.X, m.Advance)
	}
	if hit := l.HitTest(kit.Pt(m.Advance+20, 5)); hit.Pos != len(text) || hit.Inside {
		t.Errorf("hit beyond the end = %+v", hit)
	}
	// a range that ends in the hidden text covers the ellipsis
	rs := l.AppendRangeRects(nil, 0, from+2)
	if len(rs) != 1 || rs[0].Max.X < caret.X+3 {
		t.Errorf("range into the hidden text %v (ellipsis at %.1f)", rs, caret.X)
	}
	if fits := lay(t, e, "short", body, kit.TextOptions{MaxWidth: 180, Truncate: kit.TruncateMiddle}); fits.lines[0].cut || fits.Metrics().Truncated {
		t.Error("a text that fits was cut")
	}
	// with wrapping it acts as the end one
	wrapped := lay(t, e, strings.Repeat("word ", 30), body, kit.TextOptions{MaxWidth: 100, Wrap: kit.TextWrapWords, MaxLines: 2, Truncate: kit.TruncateMiddle})
	if wm := wrapped.Metrics(); wm.Lines != 2 || !wm.Truncated {
		t.Errorf("middle with wrapping %+v", wm)
	}
}

func TestWrapAndClamp(t *testing.T) {
	e := newEngine(t)
	text := "The download stopped because the server closed the connection before the last part arrived."
	wl := lay(t, e, text, body, kit.TextOptions{MaxWidth: 200, Wrap: kit.TextWrapWords})
	words := wl.Metrics()
	if words.Lines < 3 || words.Width > 200.01 || words.Truncated || words.Height != float32(words.Lines)*18 {
		t.Errorf("wrapped %+v", words)
	}
	// every line ends at a word's end
	for _, ln := range wl.lines[1:] {
		if wl.u[ln.start-1] != ' ' {
			t.Errorf("a line starts inside a word at %d", ln.start)
		}
	}
	conn := lay(t, e, "connection", body, kit.TextOptions{}).Metrics().Width
	if math.Abs(float64(words.MinWidth-conn)) > 0.01 {
		t.Errorf("min-content %.2f, the longest word (connection) %.2f", words.MinWidth, conn)
	}
	clamped := lay(t, e, text, body, kit.TextOptions{MaxWidth: 200, Wrap: kit.TextWrapWords, MaxLines: 2})
	cm := clamped.Metrics()
	if cm.Lines != 2 || !cm.Truncated || cm.Height != 36 || cm.Width > 200.01 {
		t.Errorf("clamped to 2 lines: %+v", cm)
	}
	if ln := clamped.lines[1]; !ln.cut || ln.cutTo != len(clamped.u) {
		t.Errorf("the last line kept hides the rest: %+v", ln)
	}
	// a hit below the last line kept is on it
	if h := clamped.HitTest(kit.Pt(1, 30)); h.Pos <= 0 || h.Pos >= len(text) {
		t.Errorf("hit on the second line %+v", h)
	}
	long := strings.Repeat("a", 66)
	anywhere := lay(t, e, long, body, kit.TextOptions{MaxWidth: 100, Wrap: kit.TextWrapAnywhere}).Metrics()
	if anywhere.Lines < 2 || anywhere.Width > 100.01 {
		t.Errorf("overflow-wrap: anywhere %+v", anywhere)
	}
	whole := lay(t, e, long, body, kit.TextOptions{MaxWidth: 100, Wrap: kit.TextWrapWords}).Metrics()
	if whole.Lines != 1 || whole.Width <= 100 {
		t.Errorf("a word longer than the line overflows it in normal wrapping: %+v", whole)
	}
	if anywhere.MinWidth <= 0 || anywhere.MinWidth > 12 {
		t.Errorf("min-content of anywhere-wrapping is one letter: %.2f", anywhere.MinWidth)
	}
	// a long word after short ones goes to its own line and overflows it
	mixed := lay(t, e, "a b "+long, body, kit.TextOptions{MaxWidth: 100, Wrap: kit.TextWrapWords})
	if mixed.Metrics().Lines != 2 {
		t.Errorf("short words then a long one: %d lines", mixed.Metrics().Lines)
	}
	chars := lay(t, e, "abc def", body, kit.TextOptions{MaxWidth: 15, Wrap: kit.TextWrapCharacters}).Metrics()
	if chars.Lines < 3 || chars.Width > 15.01 {
		t.Errorf("break-all %+v", chars)
	}
	empty := lay(t, e, "", body, kit.TextOptions{}).Metrics()
	if empty.Lines != 1 || empty.Width != 0 || empty.Height != 18 || empty.Baseline != 14 {
		t.Errorf("empty text %+v", empty)
	}
	// hard breaks end lines, whatever the wrap
	hard := lay(t, e, "one\ntwo\r\nthree four\n", body, kit.TextOptions{})
	if hm := hard.Metrics(); hm.Lines != 5 || hm.Height != 90 {
		t.Errorf("hard breaks: %+v", hm)
	}
	if c, _ := hard.Caret(len("one\ntwo\r\n")); c.Y != 36 || c.X > 0.01 {
		t.Errorf("caret at three's start %v", c)
	}
	// trailing spaces hang: Width without them, Advance with them
	sp := lay(t, e, "abc   ", body, kit.TextOptions{}).Metrics()
	if sp.Advance <= sp.Width+5 {
		t.Errorf("trailing spaces: %+v", sp)
	}
}

// A wrapping paragraph never breaks after a "/" that a word follows (WebKit keeps
// github.com/denoland/deno. whole); positions still map to the text as given.
func TestWrapKeepsAddressesWhole(t *testing.T) {
	e := newEngine(t)
	const text = "yt-dlp (Unlicense; the Windows/macOS/Linux executables are GPLv3+) is downloaded from github.com/yt-dlp; Deno (MIT) from github.com/denoland/deno. Downex does not include either."
	st := kit.TextStyle{Family: fontUI, Size: 12, LineHeight: 1.5}
	addr := strings.LastIndex(text, "github.com/denoland")
	after := addr + len("github.com/")
	one := lay(t, e, text, st, kit.TextOptions{})
	edge, _ := one.Caret(after)
	for _, wrap := range []kit.TextWrap{kit.TextWrapWords, kit.TextWrapAnywhere} {
		l := lay(t, e, text, st, kit.TextOptions{MaxWidth: edge.X + 2, Wrap: wrap})
		at, _ := l.Caret(addr)
		in, _ := l.Caret(after)
		end, _ := l.Caret(strings.LastIndex(text, "deno.") + len("deno."))
		if at.Y == 0 || in.Y != at.Y || end.Y != at.Y {
			t.Errorf("wrap %d: \"github.com/\" at %.1f, \"denoland\" at %.1f, \"deno.\" ending at %.1f: the address is broken", wrap, at.Y, in.Y, end.Y)
		}
		if in.X <= at.X {
			t.Errorf("wrap %d: \"denoland\" at x %.1f, before its \"github.com/\" at %.1f", wrap, in.X, at.X)
		}
		if h := l.HitTest(kit.Pt(in.X+0.5, in.Y+2)); h.Pos != after && h.Pos != after+1 {
			t.Errorf("wrap %d: a hit on \"denoland\"'s d gives %d, want %d", wrap, h.Pos, after)
		}
		if rs := l.AppendRangeRects(nil, addr, after); len(rs) != 1 || rs[0].Width() <= 0 {
			t.Errorf("wrap %d: \"github.com/\" covered by %v", wrap, rs)
		}
	}
	// white space after a slash still breaks there
	l := lay(t, e, "see https://example.com/ "+strings.Repeat("x", 40), st, kit.TextOptions{MaxWidth: 160, Wrap: kit.TextWrapWords})
	if l.Metrics().Lines != 2 || l.lines[1].start != len("see https://example.com/ ") {
		t.Errorf("lines start at %d, %d", l.lines[0].start, l.lines[1].start)
	}
}

// WebKit's line-break opportunities (BreakLines): after white space, after "-" and "?",
// before an opening bracket after punctuation, ICU's past ASCII; never inside a word or
// after "/" in ASCII text; a hyphen before a digit only after a letter or digit.
func TestBreakOpportunities(t *testing.T) {
	e := newEngine(t)
	for text, want := range map[string]string{
		"pre-release 2026-11-09 x":    "pre-|release |2026-|11-|09 |x",
		"a -5 b":                      "a |-5 |b",
		"see http://x.y/z?q=1&r=2 ok": "see |http://x.y/z?|q=1&r=2 |ok",
		"f(x) a,(b) [c]":              "f(x) |a,|(b) |[c]",
		"下载完成":                        "下|载|完|成",
		"ڕاپۆرتی ساڵانە":              "ڕاپۆرتی |ساڵانە",
	} {
		l := lay(t, e, text, body, kit.TextOptions{MaxWidth: 1000, Wrap: kit.TextWrapWords})
		var b strings.Builder
		for i, c := range l.u {
			if i > 0 && l.flags[i]&breakBefore != 0 {
				b.WriteByte('|')
			}
			b.WriteRune(rune(c))
		}
		if b.String() != want {
			t.Errorf("%q breaks as %q, want %q", text, b.String(), want)
		}
	}
}

func TestDirection(t *testing.T) {
	e := newEngine(t)
	l := lay(t, e, "تقرير-الميزانية-٢٠٢٦.xlsx", body, kit.TextOptions{MaxWidth: 300})
	if l.Metrics().Direction != kit.DirRTL {
		t.Errorf("resolved direction %v", l.Metrics().Direction)
	}
	// right to left, the start of the text is at the right edge of the box
	start, _ := l.Caret(0)
	if start.X < 290 {
		t.Errorf("caret at the start of an RTL paragraph at x=%.1f, want near 300", start.X)
	}
	if h := l.HitTest(kit.Pt(299, 5)); h.Pos != 0 {
		t.Errorf("hit at the right edge %+v, want 0", h)
	}
	ltr := lay(t, e, "تقرير", body, kit.TextOptions{MaxWidth: 300, Direction: kit.DirLTR})
	if ltr.Metrics().Direction != kit.DirLTR {
		t.Errorf("a set direction was not kept")
	}
	if s, _ := ltr.Caret(len(ltr.text)); s.X > 60 {
		t.Errorf("an RTL word in an LTR paragraph sits at the left: its end caret at %.1f", s.X)
	}
	// alignment: end puts an LTR line at the right edge, centre in the middle
	w := lay(t, e, "abc", body, kit.TextOptions{}).Metrics().Width
	for _, c := range []struct {
		align kit.TextAlign
		dir   kit.Direction
		x     float32
	}{
		{kit.TextAlignStart, kit.DirLTR, 0},
		{kit.TextAlignEnd, kit.DirLTR, 100 - w},
		{kit.TextAlignCenter, kit.DirLTR, (100 - w) / 2},
		{kit.TextAlignStart, kit.DirRTL, 100 - w},
		{kit.TextAlignEnd, kit.DirRTL, 0},
	} {
		al := lay(t, e, "abc  ", body, kit.TextOptions{MaxWidth: 100, Align: c.align, Direction: c.dir})
		rs := al.AppendRangeRects(nil, 0, 3)
		if len(rs) != 1 || math.Abs(float64(rs[0].Min.X-c.x)) > 0.01 {
			t.Errorf("align %v dir %v: text at %v, want x %.2f", c.align, c.dir, rs, c.x)
		}
	}
	// a no-wrap line wider than its box starts at its start edge whatever the alignment
	wide := lay(t, e, "a rather long line of text", body, kit.TextOptions{MaxWidth: 50, Align: kit.TextAlignEnd})
	if c, _ := wide.Caret(0); c.X != 0 {
		t.Errorf("an overflowing end-aligned line starts at %.2f", c.X)
	}
	// isolates keep a right-to-left name apart from the sentence around it
	iso := lay(t, e, "Download ⁨ڕاپۆرت 2⁩ again?", body, kit.TextOptions{})
	if iso.Metrics().Direction != kit.DirLTR {
		t.Error("a sentence with an isolated RTL name is not LTR")
	}
	q, _ := iso.Caret(strings.Index(iso.text, "again"))
	n, _ := iso.Caret(strings.Index(iso.text, "2"))
	if n.X >= q.X {
		t.Errorf("the isolated name's digit at %.1f, after \"again\" at %.1f", n.X, q.X)
	}
}

// File names: the stem, a mark and the extension in an LTR embedding (kit.FileNameText)
// show the extension at the end of a right-to-left name: its left.
func TestFileNameOrder(t *testing.T) {
	e := newEngine(t)
	name := "ڕاپۆرتی-ساڵانە-٢٠٢٥.pdf"
	s := kit.FileNameText(name)
	l := lay(t, e, s, body, kit.TextOptions{})
	ext := l.AppendRangeRects(nil, strings.Index(s, ".pdf"), strings.Index(s, ".pdf")+4)
	stem := l.AppendRangeRects(nil, 0, len("ڕاپۆرتی"))
	if len(ext) != 1 || len(stem) != 1 || ext[0].Max.X > stem[0].Min.X {
		t.Errorf("extension at %v, first word at %v: the extension should be left of the stem", ext, stem)
	}
	// ".pdf" reads left to right
	d, _ := l.Caret(strings.Index(s, ".pdf"))
	f, _ := l.Caret(strings.Index(s, "f"))
	if f.X <= d.X {
		t.Errorf(".pdf reversed: . at %.1f, f at %.1f", d.X, f.X)
	}
}

// Positions are UTF-8 byte offsets on cluster boundaries, also through surrogate pairs and
// combining marks; the range of the whole text is one rectangle although Core Text sets
// the emoji in a run of its own.
func TestPositions(t *testing.T) {
	e := newEngine(t)
	text := "a😀é́b" // a, an astral emoji, e-acute + a combining acute, b
	l := lay(t, e, text, body, kit.TextOptions{})
	var xs []float32
	for pos := 0; pos <= len(text); pos++ {
		top, h := l.Caret(pos)
		if h != 18 || top.Y != 0 {
			t.Fatalf("caret at %d: %v height %v", pos, top, h)
		}
		xs = append(xs, top.X)
	}
	if xs[2] != xs[1] || xs[3] != xs[1] || xs[4] != xs[1] {
		t.Errorf("caret inside the emoji moved: %v", xs[:6])
	}
	if xs[0] != 0 || xs[5] <= xs[1] || xs[len(text)] <= xs[5] {
		t.Errorf("carets not increasing: %v", xs)
	}
	// the combining acute stays with its e
	accent := strings.Index(text, "́")
	if xs[accent] != xs[strings.Index(text, "é")] {
		t.Errorf("caret before the combining mark moved: %v", xs)
	}
	if math.Abs(float64(xs[len(text)]-l.Metrics().Width)) > 0.01 {
		t.Errorf("end caret %.3f, width %.3f", xs[len(text)], l.Metrics().Width)
	}
	for _, p := range []kit.Point{{X: 1, Y: 5}, {X: l.Metrics().Width / 2, Y: 5}, {X: l.Metrics().Width - 1, Y: 5}} {
		hit := l.HitTest(p)
		if !hit.Inside || hit.Pos != len(text) && !utf8.RuneStart(text[hit.Pos]) {
			t.Errorf("hit %v = %+v, not on a character boundary", p, hit)
		}
	}
	rects := l.AppendRangeRects(nil, 0, len(text))
	if len(rects) != 1 || math.Abs(float64(rects[0].Width()-l.Metrics().Width)) > 0.5 || rects[0].Height() != 18 {
		t.Errorf("range rects of the whole text: %v, width %.2f", rects, l.Metrics().Width)
	}
	if got := l.AppendRangeRects(nil, 3, 3); len(got) != 0 {
		t.Errorf("empty range: %v", got)
	}
	l.Release()
	l.Release() // idempotent
	if l.Metrics() != (kit.TextMetrics{}) || len(l.AppendRangeRects(nil, 0, 4)) != 0 || l.HitTest(kit.Pt(1, 1)) != (kit.TextHit{}) {
		t.Error("a released layout still measures")
	}
}

// Hit tests and carets agree: the caret at a hit's position is where the hit was, within
// half a cluster, on every line of a wrapped paragraph.
func TestHitCaretRoundTrip(t *testing.T) {
	e := newEngine(t)
	text := "Downloads finish faster with more connections, 12 at most."
	l := lay(t, e, text, body, kit.TextOptions{MaxWidth: 150, Wrap: kit.TextWrapWords})
	if l.Metrics().Lines < 2 {
		t.Fatalf("%d lines", l.Metrics().Lines)
	}
	for pos := 0; pos <= len(text); pos++ {
		c, h := l.Caret(pos)
		hit := l.HitTest(kit.Pt(c.X+0.01, c.Y+h/2))
		hc, _ := l.Caret(hit.Pos)
		if hc != c {
			// a soft line end and the next line's start are one position
			if !(hit.Pos == pos && hc.Y != c.Y) {
				t.Errorf("pos %d: caret %v, hit %d whose caret is %v", pos, c, hit.Pos, hc)
			}
		}
	}
	// past the end of the text and above it
	if h := l.HitTest(kit.Pt(500, l.Metrics().Height-2)); h.Pos != len(text) || h.Inside {
		t.Errorf("past the end %+v", h)
	}
	if h := l.HitTest(kit.Pt(1, -10)); h.Pos != 0 || h.Inside {
		t.Errorf("above the text %+v", h)
	}
	if h := l.HitTest(kit.Pt(float32(math.NaN()), 1)); h != (kit.TextHit{}) {
		t.Errorf("NaN hit %+v", h)
	}
	// ranges over two lines give two rectangles, one per line
	rs := l.AppendRangeRects(nil, 0, len(text))
	if len(rs) != l.Metrics().Lines || rs[1].Min.Y != 18 {
		t.Errorf("rects over %d lines: %v", l.Metrics().Lines, rs)
	}
}

func TestInvalidUTF8(t *testing.T) {
	e := newEngine(t)
	l := lay(t, e, "a\xffb", body, kit.TextOptions{})
	if l.Metrics().Width <= 0 {
		t.Fatal("invalid UTF-8 measured nothing")
	}
	if hit := l.HitTest(kit.Pt(l.Metrics().Width+5, 5)); hit.Pos != 3 {
		t.Errorf("end of text = %d, want 3", hit.Pos)
	}
	if c, _ := l.Caret(2); c.X <= 0 {
		t.Errorf("caret after the U+FFFD at %.2f", c.X)
	}
}

// Relayout lays other text out in a layout in place (a label's live speed): what it measures
// and hit-tests is what a new layout of that text gives, the layout stays one live layout,
// it allocates nothing once its buffers have grown, and a failure releases it.
func TestRelayout(t *testing.T) {
	e := newEngine(t)
	l := lay(t, e, "1.2 MB/s", body, kit.TextOptions{})
	for _, c := range []struct {
		text string
		opts kit.TextOptions
	}{
		{"13.4 MB/s", kit.TextOptions{}},
		{"980 KB/s", kit.TextOptions{Align: kit.TextAlignEnd}},
		{"conference-keynote-2026-recording.mp4", kit.TextOptions{MaxWidth: 120, Truncate: kit.TruncateMiddle}},
		{"وزە بۆ کردنی کارێک ؟.mp4", kit.TextOptions{}},
		{"The server closed the connection early.", kit.TextOptions{MaxWidth: 90, Wrap: kit.TextWrapWords, MaxLines: 2}},
		{"", kit.TextOptions{}},
	} {
		if err := l.Relayout(c.text, body, c.opts); err != nil {
			t.Fatalf("Relayout(%q): %v", c.text, err)
		}
		fresh := lay(t, e, c.text, body, c.opts)
		if l.Metrics() != fresh.Metrics() || len(l.lines) != len(fresh.lines) || l.text != c.text {
			t.Errorf("%q: relaid %+v, new %+v", c.text, l.Metrics(), fresh.Metrics())
		}
		p := kit.Pt(fresh.Metrics().Width*0.6, 5)
		if l.HitTest(p) != fresh.HitTest(p) {
			t.Errorf("%q: hit %v, new layout's %v", c.text, l.HitTest(p), fresh.HitTest(p))
		}
		fresh.Release()
	}
	if e.live != 1 {
		t.Errorf("%d live layouts, want the one relaid", e.live)
	}
	texts := [...]string{"1.2 MB/s", "13.4 MB/s"}
	i := 0
	if n := testing.AllocsPerRun(100, func() {
		i++
		if err := l.Relayout(texts[i%2], body, kit.TextOptions{}); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Errorf("Relayout allocates %v times", n)
	}
	if err := l.Relayout("x", kit.TextStyle{Family: fontUI, Size: float32(math.NaN())}, kit.TextOptions{}); !errors.Is(err, kit.ErrInvalid) {
		t.Fatalf("Relayout with a bad size: %v", err)
	}
	if l.Metrics() != (kit.TextMetrics{}) || e.live != 0 {
		t.Errorf("after a failed relayout: metrics %+v, %d live layouts", l.Metrics(), e.live)
	}
	if err := l.Relayout("x", body, kit.TextOptions{}); !errors.Is(err, kit.ErrInvalid) || e.live != 0 {
		t.Errorf("Relayout of a released layout: %v, %d live", err, e.live)
	}
	l.Release()
	if e.live != 0 {
		t.Errorf("%d live layouts after Release", e.live)
	}
}

// The engine refuses work once closed.
func TestClosed(t *testing.T) {
	e, err := newTextEngine()
	if err != nil {
		t.Fatal(err)
	}
	e.close()
	e.close()
	if _, err := e.layoutText("x", body, kit.TextOptions{}); !errors.Is(err, kit.ErrInvalid) {
		t.Errorf("layout after close: %v", err)
	}
	if _, err := e.fontMetrics(body); !errors.Is(err, kit.ErrInvalid) {
		t.Errorf("metrics after close: %v", err)
	}
}

// Drawing: glyphs upright with the baseline where Metrics says, grayscale antialiasing
// (-webkit-font-smoothing: antialiased) for both kit modes, emoji in colour, nothing for a
// released layout.
func TestDraw(t *testing.T) {
	e := newEngine(t)
	black, white := kit.RGB(0), kit.RGB(0xffffff)
	for _, aa := range []kit.Antialias{kit.AntialiasSubpixel, kit.AntialiasGrayscale} {
		for _, ppd := range []float32{1, 2} {
			l := lay(t, e, "THE", body, kit.TextOptions{Antialias: aa})
			w, h := int(40*ppd), int(20*ppd)
			px := renderText(l, w, h, ppd, kit.Pt(1, 1), black, white)
			grays, top, bottom := 0, -1, -1
			rowInk := make([]int, h)
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					p := px[4*(y*w+x):]
					if p[0] != p[1] || p[1] != p[2] {
						t.Fatalf("aa %v ×%v: colour fringe at %d,%d: %v", aa, ppd, x, y, p[:4])
					}
					if p[0] < 250 {
						rowInk[y]++
						if top < 0 {
							top = y
						}
						bottom = y
						if p[0] > 5 {
							grays++
						}
					}
				}
			}
			if top < 0 || grays == 0 {
				t.Fatalf("aa %v ×%v: no antialiased ink", aa, ppd)
			}
			// capitals end on the baseline, 1 + Baseline DIPs down
			if base := (1 + l.Metrics().Baseline) * ppd; math.Abs(float64(bottom+1)-float64(base)) > 1 {
				t.Errorf("aa %v ×%v: ink ends at row %d, baseline %.1f", aa, ppd, bottom+1, base)
			}
			if rowInk[top] <= rowInk[bottom] {
				t.Errorf("aa %v ×%v: upside down? T's bar row %d ink %d, foot row %d ink %d", aa, ppd, top, rowInk[top], bottom, rowInk[bottom])
			}
		}
	}
	emoji := lay(t, e, "😀", body, kit.TextOptions{})
	px := renderText(emoji, 40, 40, 2, kit.Pt(0, 0), black, white)
	colour := 0
	for i := 0; i < len(px); i += 4 {
		if int(px[i])-int(px[i+2]) > 60 {
			colour++
		}
	}
	if colour < 20 {
		t.Errorf("the emoji drew %d coloured pixels", colour)
	}
	gone := lay(t, e, "THE", body, kit.TextOptions{})
	gone.Release()
	for i, v := range renderText(gone, 20, 20, 1, kit.Pt(0, 0), black, kit.Color{}) {
		if v != 0 {
			t.Fatalf("a released layout drew (byte %d = %d)", i, v)
		}
	}
}
