package kit_test

import (
	"strings"
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/kittest"
)

// The embeddings FileNameText and NewFileName add (written as runes: no invisible
// characters in the source).
const (
	tLRE = string(rune(0x202A))
	tRLE = string(rune(0x202B))
	tPDF = string(rune(0x202C))
	tLRM = string(rune(0x200E))
	tRLM = string(rune(0x200F))
)

// SplitExtension is text.ts's splitExtension, with its tests (text.test.ts).
func TestSplitExtension(t *testing.T) {
	for _, c := range []struct{ name, stem, ext string }{
		// splits off a trailing Latin extension
		{"وزە بۆ کردنی کارێک ؟.mp4", "وزە بۆ کردنی کارێک ؟", ".mp4"},
		{"Win.10.Aio.19045.7807.Multi.iso", "Win.10.Aio.19045.7807.Multi", ".iso"},
		{"archive.tar.gz", "archive.tar", ".gz"},
		// keeps names without such an extension whole
		{"README", "README", ""},
		{".gitignore", ".gitignore", ""},
		{"notes.", "notes.", ""},
		{"ڕاپۆرت.پدف", "ڕاپۆرت.پدف", ""},
		{"file.name with space", "file.name with space", ""},
		// the regex's edges: 1 to 10 ASCII letters and digits, a stem of at least one character
		{"a.b", "a", ".b"},
		{"x.ABCDEFGHIJ", "x", ".ABCDEFGHIJ"},
		{"x.ABCDEFGHIJK", "x.ABCDEFGHIJK", ""},
		{"x.mp_4", "x.mp_4", ""},
		{"x.mp4 ", "x.mp4 ", ""},
		{"x.é", "x.é", ""},
		{"..mp4", ".", ".mp4"},
		{"", "", ""},
		{"line\nbreak.txt", "line\nbreak", ".txt"}, // the s flag: the stem may hold any character
	} {
		stem, ext := kit.SplitExtension(c.name)
		if stem != c.stem || ext != c.ext {
			t.Errorf("SplitExtension(%q) = %q, %q; want %q, %q", c.name, stem, ext, c.stem, c.ext)
		}
		if stem+ext != c.name {
			t.Errorf("SplitExtension(%q) lost or reordered characters: %q + %q", c.name, stem, ext)
		}
	}
	// never loses or reorders characters
	for _, name := range []string{"a.b", "invoice⟨U+202E⟩fdp.exe", "2015_12_17 لە سوید.mp4"} {
		if stem, ext := kit.SplitExtension(name); stem+ext != name {
			t.Errorf("%q split as %q + %q", name, stem, ext)
		}
	}
}

// HasRightToLeft is FileName.svelte's RTL class: its edges, and the scripts around it.
func TestHasRightToLeft(t *testing.T) {
	for _, r := range []rune{0x0590, 0x05D0, 0x0627, 0x06A9, 0x0700, 0x0780, 0x07C0, 0x08FF, 0xFB1D, 0xFDFF, 0xFE70, 0xFEFF} {
		if !kit.HasRightToLeft("a" + string(r)) {
			t.Errorf("U+%04X is not right-to-left", r)
		}
	}
	for _, s := range []string{"", "README.md", "会议记录.dat", "한국어", "📦 setup.exe", string(rune(0x058F)), string(rune(0x0900)),
		string(rune(0xFB1C)), string(rune(0xFE00)), string(rune(0xFF00)), "⟨U+202E⟩"} {
		if kit.HasRightToLeft(s) {
			t.Errorf("%q counts as right-to-left", s)
		}
	}
}

// FileNameText is FileName.svelte's markup: the extension of a right-to-left name in its
// own left-to-right run, other names as they are (FileName.test.ts).
func TestFileNameText(t *testing.T) {
	for in, want := range map[string]string{
		"وزە بۆ کردنی کارێک ؟.mp4":        "وزە بۆ کردنی کارێک ؟" + tRLM + tLRE + ".mp4" + tPDF,
		"2015_12_17 لە سوید.mp4":          "2015_12_17 لە سوید" + tRLM + tLRE + ".mp4" + tPDF,
		"INSIDE Kurdistan ڕاپۆرت.mkv":     "INSIDE Kurdistan ڕاپۆرت" + tLRM + tLRE + ".mkv" + tPDF,
		"Win.10.Aio.19045.7807.Multi.iso": "Win.10.Aio.19045.7807.Multi.iso",
		"ڕاپۆرت":                          "ڕاپۆرت",
		"ڕاپۆرت.پدف":                      "ڕاپۆرت.پدف",
		"README":                          "README",
		"会议记录-2026年第三季度.dat":              "会议记录-2026年第三季度.dat",
	} {
		if got := kit.FileNameText(in); got != want {
			t.Errorf("FileNameText(%q) = %+q, want %+q", in, got, want)
		}
	}
}

// A file-name label lays out the name with its embeddings, by its direction: as the
// paragraph (dir=auto), or embedded by its first strong letter in a paragraph of a set
// direction (<bdi>). Its text, tooltip and accessible name stay the name.
func TestFileNameLabel(t *testing.T) {
	name := "وزە بۆ کردنی کارێک ؟.mp4"
	row := kit.NewFileName(name, font13, kit.TextOptions{Truncate: kit.TruncateEnd})
	title := kit.NewFileName(name, font13, kit.TextOptions{Direction: kit.DirLTR, Wrap: kit.TextWrapAnywhere, MaxLines: 2})
	latin := kit.NewFileName("Win.10.Aio.19045.7807.Multi.iso", font13, kit.TextOptions{Truncate: kit.TruncateEnd})
	narrow := box(kit.Style{Width: kit.Px(60), Display: kit.DisplayFlex, Axis: kit.AxisColumn}, row, latin)
	root := box(kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn}, narrow, title)
	w, loop := open(t, root, kit.Size{W: 400, H: 200}, env100)
	shown := func(l *kit.Label) string {
		tl, ok := l.TextLayout().(*kittest.Layout)
		if !ok {
			t.Fatalf("%q has no layout", l.Text())
		}
		return tl.Text
	}
	stem, ext := kit.SplitExtension(name)
	if got, want := shown(row), stem+tRLM+tLRE+ext+tPDF; got != want {
		t.Errorf("the row lays out %+q, want %+q", got, want)
	}
	if got, want := shown(title), tRLE+stem+tRLM+tLRE+ext+tPDF+tPDF; got != want {
		t.Errorf("the title lays out %+q, want %+q", got, want)
	}
	if got := shown(latin); got != "Win.10.Aio.19045.7807.Multi.iso" {
		t.Errorf("a Latin name lays out %+q", got)
	}
	if row.Text() != name || !row.Truncated() || row.TooltipText() != name {
		t.Errorf("text %q, truncated %v, tooltip %q", row.Text(), row.Truncated(), row.TooltipText())
	}
	tree := w.Semantics()
	if n, ok := tree.Node(row.NodeID()); !ok || n.Name != name {
		t.Errorf("accessible name %q", n.Name)
	}
	// SetText and SetOptions follow: a new name, and a direction that isolates it.
	row.SetText("ڕاپۆرت.mp4")
	loop.Step()
	if got := shown(row); got != "ڕاپۆرت"+tRLM+tLRE+".mp4"+tPDF {
		t.Errorf("after SetText the row lays out %+q", got)
	}
	o := row.Options()
	o.Direction = kit.DirRTL
	row.SetOptions(o)
	loop.Step()
	if got := shown(row); got != tRLE+"ڕاپۆرت"+tRLM+tLRE+".mp4"+tPDF+tPDF {
		t.Errorf("with a set direction the row lays out %+q", got)
	}
	// A name whose first strong letter is Latin is embedded left to right, also when it
	// starts with digits and spaces; one without strong letters too.
	for in, want := range map[string]string{
		"INSIDE Kurdistan ڕاپۆرت.mkv": tLRE + "INSIDE Kurdistan ڕاپۆرت" + tLRM + tLRE + ".mkv" + tPDF + tPDF,
		"2015 لە سوید.mp4":            tRLE + "2015 لە سوید" + tRLM + tLRE + ".mp4" + tPDF + tPDF,
		"2015-12-17":                  tLRE + "2015-12-17" + tPDF,
	} {
		row.SetText(in)
		loop.Step()
		if got := shown(row); got != want {
			t.Errorf("%q in a set direction lays out %+q, want %+q", in, got, want)
		}
	}
	if strings.ContainsAny(row.Text()+row.TooltipText(), tLRE+tRLE+tPDF+tLRM+tRLM) {
		t.Error("the embeddings reached the label's text or tooltip")
	}
}
