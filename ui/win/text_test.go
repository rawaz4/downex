//go:build windows

package win

import (
	"errors"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"downex/ui/kit"
)

// Fonts as app.css names them.
const (
	fontUI      = `'Segoe UI Variable Text', 'Segoe UI', system-ui, -apple-system, 'Noto Sans', Ubuntu, Cantarell, sans-serif`
	fontDisplay = `'Segoe UI Variable Display', 'Segoe UI Variable Text', 'Segoe UI', system-ui, -apple-system, 'Noto Sans', sans-serif`
	fontMono    = `'Cascadia Mono', 'Cascadia Code', Consolas, ui-monospace, 'SF Mono', Menlo, 'DejaVu Sans Mono', monospace`
)

func newRes(t testing.TB) *Resources {
	t.Helper()
	r, err := NewResources()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if r.live.layouts != 0 || r.live.paths != 0 || r.live.images != 0 {
			t.Errorf("leaked: %d layouts, %d paths, %d images", r.live.layouts, r.live.paths, r.live.images)
		}
		r.Close()
	})
	return r
}

func layout(t testing.TB, r *Resources, text string, s kit.TextStyle, o kit.TextOptions) *textLayout {
	t.Helper()
	l, err := r.layoutText(text, s, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Release)
	return l
}

// Weights pick the family's font as Chromium on Windows does (textFormat): Segoe UI
// Variable's named instances, so 400 and 450 lay out as Regular, 500 to 600 as Semibold and
// 650 and 700 as Bold. The widths are headless Edge 156's on Windows 11 (the WebView2
// runtime's build; getBoundingClientRect of the same CSS, identical at device scales 1, 1.25
// and 1.5): the page title (Display, 20 px, -0.02em, tabular) and a row name (Text, 13 px,
// tabular).
func TestWeightsMatchChromium(t *testing.T) {
	r := newRes(t)
	edge := []struct {
		weight      float32
		title, name float32
	}{
		{300, 106.1094, 146.8281},
		{350, 110.3750, 150.1563},
		{400, 117.4219, 153.5781},
		{450, 117.4219, 153.5781},
		{500, 120.5938, 157.9531},
		{550, 120.5938, 157.9531},
		{600, 120.5938, 157.9531},
		{650, 125.7031, 163.3438},
		{700, 125.7031, 163.3438},
	}
	for _, e := range edge {
		title := kit.TextStyle{Family: fontDisplay, Size: 20, Weight: e.weight, LetterSpacing: -0.02, Tabular: true}
		name := kit.TextStyle{Family: fontUI, Size: 13, Weight: e.weight, Tabular: true}
		gt := layout(t, r, "All downloads", title, kit.TextOptions{}).Metrics().Width
		gn := layout(t, r, "os-image-26.04-amd64.iso", name, kit.TextOptions{}).Metrics().Width
		t.Logf("weight %v: title %.3f px (Edge %.3f), name %.3f px (Edge %.3f)", e.weight, gt, e.title, gn, e.name)
		// Edge lays out in 1/64 px; 0.1 px is a fraction of a pixel and far below the
		// 1.5 px between neighbouring named instances.
		if math.Abs(float64(gt-e.title)) > 0.1 || math.Abs(float64(gn-e.name)) > 0.1 {
			t.Errorf("weight %v: title %.3f, name %.3f px; Edge %.3f, %.3f", e.weight, gt, gn, e.title, e.name)
		}
		title.Tabular = false
		if prop := layout(t, r, "All downloads", title, kit.TextOptions{}).Metrics().Width; prop != gt {
			t.Errorf("weight %v: tabular figures changed a text without digits (%.2f, proportional %.2f): kerning was dropped", e.weight, gt, prop)
		}
	}
	f, _ := r.format(mustNorm(t, kit.TextStyle{Family: fontDisplay, Size: 20, Weight: 650}))
	if f.family != "Segoe UI Variable Display" {
		t.Errorf("format: family %q", f.family)
	}
}

// The texts of the parity gate lay out as wide as Chromium lays them out: headless Edge 156's
// getBoundingClientRect widths of the same CSS (identical at device scales 1, 1.25 and 1.5)
// within 0.05 px, Arabic and Kurdish names from the system's fallback included. The ink
// widths against the 0.9.2 shots are TestReferenceTextWidths.
func TestChromiumTextWidths(t *testing.T) {
	r := newRes(t)
	title := kit.TextStyle{Family: fontDisplay, Size: 20, Weight: 650, LetterSpacing: -0.02, LineHeight: 1.2, Tabular: true, OpticalSize: 20}
	name := kit.TextStyle{Family: fontUI, Size: 13, Weight: 500, LineHeight: 1.4, Tabular: true, OpticalSize: 13}
	item := kit.TextStyle{Family: fontUI, Size: 13, Weight: 400, LineHeight: 1.4, Tabular: true, OpticalSize: 13}
	for _, c := range []struct {
		text string
		st   kit.TextStyle
		edge float32
	}{
		{"All downloads", title, 125.7031},
		{"os-image-26.04-amd64.iso", name, 157.9531},
		{"telemetry-183.dat", name, 105.7813},
		{"city-timelapse-4k.mp4", name, 132.0469},
		{"annual-report-2025.pdf", name, 139.1563},
		{"Documents", item, 65.3750},
		{"Programs", item, 54.9219},
		{"تقرير-الميزانية-٢٠٢٦.xlsx", name, 128.9844},
		{"ڕاپۆرتی-ساڵانە-٢٠٢٥.pdf", name, 128.1875},
	} {
		got := layout(t, r, c.text, c.st, kit.TextOptions{}).Metrics().Width
		t.Logf("%-28q %.3f px, Edge %.3f (%+.3f%%)", c.text, got, c.edge, 100*(got-c.edge)/c.edge)
		if math.Abs(float64(got-c.edge)) > 0.05 {
			t.Errorf("%q measures %.3f px, Edge %.3f", c.text, got, c.edge)
		}
	}
}

// OpticalSize is ignored, as Chromium ignores font-optical-sizing and 'opsz' for system
// fonts: each named instance keeps its own (Display's and Text's still differ).
func TestOpticalSizeIgnored(t *testing.T) {
	r := newRes(t)
	w := func(family string, opsz float32) float32 {
		st := kit.TextStyle{Family: family, Size: 20, Weight: 650, OpticalSize: opsz}
		return layout(t, r, "All downloads", st, kit.TextOptions{}).Metrics().Width
	}
	display, text := w(fontDisplay, 0), w(fontUI, 0)
	if text-display < 1 {
		t.Fatalf("Display %.2f and Text %.2f should differ by their optical sizes", display, text)
	}
	for _, opsz := range []float32{10.5, 20, 36} {
		if got := w(fontDisplay, opsz); got != display {
			t.Errorf("Display at opsz %v = %.2f, without = %.2f", opsz, got, display)
		}
		if got := w(fontUI, opsz); got != text {
			t.Errorf("Text at opsz %v = %.2f, without = %.2f", opsz, got, text)
		}
	}
	if got := w(fontMono, 20); got <= 0 {
		t.Error("Cascadia Mono with an optical size measured nothing")
	}
}

func mustNorm(t testing.TB, s kit.TextStyle) kit.TextStyle {
	t.Helper()
	n, err := normStyle(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
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
	r := newRes(t)
	for css, want := range map[string]string{
		fontUI:                           "Segoe UI Variable Text",
		fontMono:                         "Cascadia Mono",
		"'No Such Font', monospace":      "Consolas",
		"'No Such Font', system-ui":      "Segoe UI",
		"":                               "Segoe UI",
		"'No Such Font', serif, cursive": "Segoe UI",
	} {
		if got := r.resolveFamily(css); got != want {
			t.Errorf("resolveFamily(%q) = %q, want %q", css, got, want)
		}
	}
}

func TestBadStyles(t *testing.T) {
	r := newRes(t)
	for _, s := range []kit.TextStyle{{Size: 0}, {Size: -1}, {Size: float32(math.NaN())}, {Size: float32(math.Inf(1))}, {Size: 13, Weight: float32(math.NaN())}} {
		if _, err := r.LayoutText("x", s, kit.TextOptions{}); !errors.Is(err, kit.ErrInvalid) {
			t.Errorf("LayoutText with %+v: %v, want ErrInvalid", s, err)
		}
		if _, err := r.FontMetrics(s); !errors.Is(err, kit.ErrInvalid) {
			t.Errorf("FontMetrics with %+v: %v, want ErrInvalid", s, err)
		}
	}
	if _, err := r.LayoutText("x", kit.TextStyle{Size: 13}, kit.TextOptions{MaxWidth: -1}); !errors.Is(err, kit.ErrInvalid) {
		t.Errorf("negative MaxWidth: %v", err)
	}
}

var body = kit.TextStyle{Family: fontUI, Size: 13, LineHeight: 1.4, Tabular: true}

func TestLineBox(t *testing.T) {
	r := newRes(t)
	fm, err := r.FontMetrics(body)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("13 px body: %+v", fm)
	if math.Abs(float64(fm.LineHeight-18.2)) > 0.01 {
		t.Errorf("line height %.3f, want 13 × 1.4 = 18.2", fm.LineHeight)
	}
	if want := (fm.LineHeight-(fm.Ascent+fm.Descent))/2 + fm.Ascent; math.Abs(float64(fm.Baseline-want)) > 0.01 {
		t.Errorf("baseline %.3f, half-leading gives %.3f", fm.Baseline, want)
	}
	l := layout(t, r, "Hg", body, kit.TextOptions{})
	m := l.Metrics()
	if math.Abs(float64(m.Height-fm.LineHeight)) > 0.01 || math.Abs(float64(m.Baseline-fm.Baseline)) > 0.01 || m.Lines != 1 {
		t.Errorf("layout %+v against font metrics %+v", m, fm)
	}
	normal, _ := r.FontMetrics(kit.TextStyle{Family: fontUI, Size: 13})
	if want := normal.Ascent + normal.Descent + normal.LineGap; math.Abs(float64(normal.LineHeight-want)) > 0.01 {
		t.Errorf("normal line height %.3f, want ascent+descent+gap %.3f", normal.LineHeight, want)
	}
}

// Tabular figures: every digit has one width.
func TestTabular(t *testing.T) {
	r := newRes(t)
	ones := layout(t, r, "1111", body, kit.TextOptions{}).Metrics().Width
	eights := layout(t, r, "8888", body, kit.TextOptions{}).Metrics().Width
	if math.Abs(float64(ones-eights)) > 0.01 {
		t.Errorf("tabular 1111 = %.2f, 8888 = %.2f", ones, eights)
	}
	prop := body
	prop.Tabular = false
	ones = layout(t, r, "1111", prop, kit.TextOptions{}).Metrics().Width
	eights = layout(t, r, "8888", prop, kit.TextOptions{}).Metrics().Width
	if ones >= eights {
		t.Errorf("proportional 1111 = %.2f should be narrower than 8888 = %.2f", ones, eights)
	}
}

// CSS letter-spacing adds the same space after every character.
func TestLetterSpacing(t *testing.T) {
	r := newRes(t)
	caps := kit.TextStyle{Family: fontUI, Size: 10.5, Weight: 600}
	plain := layout(t, r, "QUEUES", caps, kit.TextOptions{}).Metrics().Advance
	caps.LetterSpacing = 0.07
	spaced := layout(t, r, "QUEUES", caps, kit.TextOptions{}).Metrics().Advance
	if want := plain + 6*0.07*10.5; math.Abs(float64(spaced-want)) > 0.05 {
		t.Errorf("letter-spaced %.3f, want %.3f + 6 × 0.735 = %.3f", spaced, plain, want)
	}
}

func TestTruncateEnd(t *testing.T) {
	r := newRes(t)
	text := "conference-keynote-recording-2026-final-version.mp4"
	full := layout(t, r, text, body, kit.TextOptions{}).Metrics()
	l := layout(t, r, text, body, kit.TextOptions{MaxWidth: 120, Truncate: kit.TruncateEnd})
	m := l.Metrics()
	if !m.Truncated || m.Width > 120.01 || m.Lines != 1 {
		t.Errorf("end-truncated %+v (full %+v)", m, full)
	}
	fits := layout(t, r, "short.mp4", body, kit.TextOptions{MaxWidth: 120, Truncate: kit.TruncateEnd}).Metrics()
	if fits.Truncated {
		t.Errorf("a text that fits is marked truncated: %+v", fits)
	}
}

func TestTruncateMiddle(t *testing.T) {
	r := newRes(t)
	text := "https://media.example.org/videos/2026/city-timelapse-4k-final.mp4"
	l := layout(t, r, text, body, kit.TextOptions{MaxWidth: 180, Truncate: kit.TruncateMiddle})
	m := l.Metrics()
	if !m.Truncated || m.Advance > 180.01 || !l.cut {
		t.Fatalf("middle-truncated %+v cut=%v", m, l.cut)
	}
	from, to := utf8Offset(text, l.cutFrom), utf8Offset(text, l.cutTo)
	head, tail := text[:from], text[to:]
	if !strings.HasPrefix(text, head) || !strings.HasSuffix(text, tail) || len(head) < 10 || len(tail) < 10 {
		t.Fatalf("kept %q … %q", head, tail)
	}
	t.Logf("shown: %q…%q (%.1f px)", head, tail, m.Advance)
	// A point on the ellipsis hits the start of what it stands for; the caret inside the
	// hidden text sits before the ellipsis.
	caret, _ := l.Caret(from)
	hidden, _ := l.Caret(from + 3)
	if caret != hidden {
		t.Errorf("caret at %d = %v, inside the hidden text %v", from, caret, hidden)
	}
	if hit := l.HitTest(kit.Pt(caret.X+2, 5)); hit.Pos != from {
		t.Errorf("hit on the ellipsis = %d, want %d", hit.Pos, from)
	}
	end, _ := l.Caret(len(text))
	if math.Abs(float64(end.X-m.Advance)) > 0.5 {
		t.Errorf("caret at the end %.2f, advance %.2f", end.X, m.Advance)
	}
	if hit := l.HitTest(kit.Pt(m.Advance+20, 5)); hit.Pos != len(text) || hit.Inside {
		t.Errorf("hit beyond the end = %+v", hit)
	}
	if fits := layout(t, r, "short", body, kit.TextOptions{MaxWidth: 180, Truncate: kit.TruncateMiddle}); fits.cut || fits.Metrics().Truncated {
		t.Error("a text that fits was cut")
	}
}

func TestWrapAndClamp(t *testing.T) {
	r := newRes(t)
	text := "The download stopped because the server closed the connection before the last part arrived."
	words := layout(t, r, text, body, kit.TextOptions{MaxWidth: 200, Wrap: kit.TextWrapWords}).Metrics()
	if words.Lines < 3 || words.Width > 200.01 || words.Truncated {
		t.Errorf("wrapped %+v", words)
	}
	if words.MinWidth <= 0 || words.MinWidth > 90 {
		t.Errorf("min-content %.1f: the longest word (connection) is about 70 px", words.MinWidth)
	}
	clamped := layout(t, r, text, body, kit.TextOptions{MaxWidth: 200, Wrap: kit.TextWrapWords, MaxLines: 2}).Metrics()
	if clamped.Lines != 2 || !clamped.Truncated || math.Abs(float64(clamped.Height-2*18.2)) > 0.05 {
		t.Errorf("clamped to 2 lines: %+v", clamped)
	}
	long := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	anywhere := layout(t, r, long, body, kit.TextOptions{MaxWidth: 100, Wrap: kit.TextWrapAnywhere}).Metrics()
	if anywhere.Lines < 2 || anywhere.Width > 100.01 {
		t.Errorf("overflow-wrap: anywhere %+v", anywhere)
	}
	whole := layout(t, r, long, body, kit.TextOptions{MaxWidth: 100, Wrap: kit.TextWrapWords}).Metrics()
	if whole.Lines != 1 || whole.Width <= 100 {
		t.Errorf("a word longer than the line overflows it in normal wrapping: %+v", whole)
	}
	if anywhere.MinWidth <= 0 || anywhere.MinWidth > 12 {
		t.Errorf("min-content of anywhere-wrapping is one letter: %.2f", anywhere.MinWidth)
	}
	empty := layout(t, r, "", body, kit.TextOptions{}).Metrics()
	if empty.Lines != 1 || empty.Width != 0 || math.Abs(float64(empty.Height-18.2)) > 0.01 {
		t.Errorf("empty text %+v", empty)
	}
}

func TestDirection(t *testing.T) {
	for text, want := range map[string]kit.Direction{
		"hello": kit.DirLTR,
		"تقرير-الميزانية-٢٠٢٦.xlsx": kit.DirRTL,
		"123 שלום":     kit.DirRTL,
		"2026 report":  kit.DirLTR,
		"⁧שלום⁩ hello": kit.DirLTR, // an isolate's content does not count
		"":             kit.DirLTR,
		"…":            kit.DirLTR,
	} {
		if got := kit.FirstStrong(text); got != want {
			t.Errorf("FirstStrong(%q) = %v, want %v", text, got, want)
		}
	}
	r := newRes(t)
	l := layout(t, r, "تقرير-الميزانية-٢٠٢٦.xlsx", body, kit.TextOptions{MaxWidth: 300})
	if l.Metrics().Direction != kit.DirRTL {
		t.Errorf("resolved direction %v", l.Metrics().Direction)
	}
	// Right to left, the start of the text is at the right edge of the box.
	start, _ := l.Caret(0)
	if start.X < 250 {
		t.Errorf("caret at the start of an RTL paragraph at x=%.1f, want near 300", start.X)
	}
}

// Positions are UTF-8 byte offsets on cluster boundaries, also through surrogate pairs and
// combining marks.
func TestPositions(t *testing.T) {
	r := newRes(t)
	text := "a😀é́b" // a, an astral emoji, e-acute + a combining acute, b
	l := layout(t, r, text, body, kit.TextOptions{})
	var xs []float32
	for pos := 0; pos <= len(text); pos++ {
		top, h := l.Caret(pos)
		if h <= 0 {
			t.Fatalf("caret at %d has height %v", pos, h)
		}
		xs = append(xs, top.X)
	}
	// Inside the emoji's four bytes the caret stays before it.
	if xs[2] != xs[1] || xs[3] != xs[1] || xs[4] != xs[1] {
		t.Errorf("caret inside the emoji moved: %v", xs[:6])
	}
	for _, p := range []kit.Point{{X: 1, Y: 5}, {X: l.Metrics().Width / 2, Y: 5}, {X: l.Metrics().Width - 1, Y: 5}} {
		hit := l.HitTest(p)
		if !utf8.RuneStart(text[min(hit.Pos, len(text)-1)]) && hit.Pos != len(text) {
			t.Errorf("hit %v at %d, not a character boundary", p, hit.Pos)
		}
	}
	rects := l.AppendRangeRects(nil, 0, len(text))
	if len(rects) != 1 || math.Abs(float64(rects[0].Width()-l.Metrics().Width)) > 0.5 {
		t.Errorf("range rects of the whole text: %v, width %.2f", rects, l.Metrics().Width)
	}
	if got := l.AppendRangeRects(nil, 3, 3); len(got) != 0 {
		t.Errorf("empty range: %v", got)
	}
	l.Release()
	l.Release() // idempotent
	if l.Metrics() != (kit.TextMetrics{}) || len(l.AppendRangeRects(nil, 0, 4)) != 0 {
		t.Error("a released layout still measures")
	}
}

func TestInvalidUTF8(t *testing.T) {
	r := newRes(t)
	l := layout(t, r, "a\xffb", body, kit.TextOptions{})
	if l.Metrics().Width <= 0 {
		t.Fatal("invalid UTF-8 measured nothing")
	}
	if hit := l.HitTest(kit.Pt(l.Metrics().Width+5, 5)); hit.Pos != 3 {
		t.Errorf("end of text = %d, want 3", hit.Pos)
	}
}

// Relayout lays other text out in a layout in place (a label's live speed): what it measures
// and hit-tests is what a new layout of that text gives, with or without a middle ellipsis,
// the layout stays one live layout, it allocates nothing once the buffers have grown, and a
// failure releases it.
func TestRelayout(t *testing.T) {
	r := newRes(t)
	l := layout(t, r, "1.2 MB/s", body, kit.TextOptions{})
	cases := []struct {
		text string
		opts kit.TextOptions
	}{
		{"13.4 MB/s", kit.TextOptions{}},
		{"980 KB/s", kit.TextOptions{Align: kit.TextAlignEnd}},
		{"conference-keynote-2026-recording.mp4", kit.TextOptions{MaxWidth: 120, Truncate: kit.TruncateMiddle}},
		{"وزە بۆ کردنی کارێک ؟.mp4", kit.TextOptions{}},
		{"", kit.TextOptions{}},
	}
	for _, c := range cases {
		if err := l.Relayout(c.text, body, c.opts); err != nil {
			t.Fatalf("Relayout(%q): %v", c.text, err)
		}
		fresh := layout(t, r, c.text, body, c.opts)
		if l.Metrics() != fresh.Metrics() || l.cut != fresh.cut || l.shownN16 != fresh.shownN16 || l.text != c.text {
			t.Errorf("%q: relaid %+v (cut %v), new %+v (cut %v)", c.text, l.Metrics(), l.cut, fresh.Metrics(), fresh.cut)
		}
		p := kit.Pt(fresh.Metrics().Width*0.6, 5)
		if l.HitTest(p) != fresh.HitTest(p) {
			t.Errorf("%q: hit %v, new layout's %v", c.text, l.HitTest(p), fresh.HitTest(p))
		}
		fresh.Release()
	}
	if r.live.layouts != 1 {
		t.Errorf("%d live layouts, want the one relaid", r.live.layouts)
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
	// A failure (a size that is no number) releases the layout; a released one refuses.
	if err := l.Relayout("x", kit.TextStyle{Family: fontUI, Size: float32(math.NaN())}, kit.TextOptions{}); !errors.Is(err, kit.ErrInvalid) {
		t.Fatalf("Relayout with a bad size: %v", err)
	}
	if l.Metrics() != (kit.TextMetrics{}) || r.live.layouts != 0 {
		t.Errorf("after a failed relayout: metrics %+v, %d live layouts", l.Metrics(), r.live.layouts)
	}
	if err := l.Relayout("x", body, kit.TextOptions{}); !errors.Is(err, kit.ErrInvalid) || r.live.layouts != 0 {
		t.Errorf("Relayout of a released layout: %v, %d live", err, r.live.layouts)
	}
	l.Release() // still idempotent
	if r.live.layouts != 0 {
		t.Errorf("%d live layouts after Release", r.live.layouts)
	}
}
