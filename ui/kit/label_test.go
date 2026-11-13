package kit_test

import (
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/kittest"
)

// TestLabelTruncation checks that a name cut by an ellipsis knows it and gives its whole
// text as the tooltip (the fit rule), and stops once it fits.
func TestLabelTruncation(t *testing.T) {
	long := "conference-keynote-2026-recording.mp4"
	name := kit.NewLabel(long, font13, kit.TextOptions{Truncate: kit.TruncateEnd})
	name.Style = kit.Style{Flex: kit.Flex1, Overflow: kit.OverflowHidden}
	size := kit.NewLabel("1.8 GB", font13, kit.TextOptions{})
	cell := box(kit.Style{Display: kit.DisplayFlex, Width: kit.Px(150), ColumnGap: kit.Px(6)}, name, size)
	w, loop := open(t, box(kit.Style{}, cell), kit.Size{W: 400, H: 100}, env100)
	if !name.Truncated() || name.TooltipText() != long {
		t.Fatalf("cut name: truncated %v, tooltip %q", name.Truncated(), name.TooltipText())
	}
	if got := name.Bounds().Width(); !approx(got, 150-6-kittest.Advance("1.8 GB", font13.Style(env100.Scale))) {
		t.Errorf("the name takes %v, want what the size leaves", got)
	}
	if lines := name.TextLayout().(*kittest.Layout).Lines(); len(lines) != 1 || lines[0][len(lines[0])-3:] != "…" {
		t.Errorf("drawn %q, want one line ending in an ellipsis", lines)
	}
	text, area, ok := w.TooltipAt(name.WindowBounds().Center())
	if !ok || text != long || area != name.WindowBounds() {
		t.Errorf("TooltipAt = %q, %v, %v", text, area, ok)
	}
	if _, _, ok := w.TooltipAt(size.WindowBounds().Center()); ok {
		t.Error("a label that fits has a tooltip")
	}
	// Wide enough: no ellipsis, no tooltip; the layouts it no longer needs are released.
	cell.Style.Width = kit.Px(400)
	cell.Relayout()
	loop.Settle(5)
	if name.Truncated() || name.TooltipText() != "" {
		t.Errorf("a name that fits: truncated %v, tooltip %q", name.Truncated(), name.TooltipText())
	}
	if live := loop.Host.Res.Live; live > 3 {
		t.Errorf("%d live layouts for two labels", live)
	}
	// An explicit tooltip wins (title=).
	size.Tooltip = "1,800,000,000 bytes"
	if text, _, _ := w.TooltipAt(size.WindowBounds().Center()); text != "1,800,000,000 bytes" {
		t.Errorf("explicit tooltip %q", text)
	}
}

// TestLabelLineClamp checks -webkit-line-clamp: two lines, the second ending in an
// ellipsis when more text is left.
func TestLabelLineClamp(t *testing.T) {
	title := kit.NewLabel("Building a download manager that is light, fast and kind to the network", font13,
		kit.TextOptions{Wrap: kit.TextWrapWords, MaxLines: 2})
	// A window's layer fills the window whatever its width (position: fixed; inset: 0),
	// so the 160 DIP box sits in a block.
	_, _ = open(t, box(kit.Style{}, box(kit.Style{Width: kit.Px(160)}, title)), kit.Size{W: 400, H: 200}, env100)
	lay := title.TextLayout().(*kittest.Layout)
	if m := lay.Metrics(); m.Lines != 2 || !m.Truncated || !title.Truncated() {
		t.Errorf("clamped title: %d lines, truncated %v", m.Lines, m.Truncated)
	}
	if got := title.Bounds().Height(); !approx(got, 2*13*1.4) {
		t.Errorf("clamped height %v, want two lines", got)
	}
}

// TestBaselineAlignment checks the page header's title and count, aligned by baseline
// (Toolbar.svelte: align-items: baseline): their baselines meet.
func TestBaselineAlignment(t *testing.T) {
	titleFont := kit.Font{Size: kit.Rem(1.25), Weight: 650, LineHeight: 1.2, LetterSpacing: -0.02}
	title := kit.NewLabel("All downloads", titleFont, kit.TextOptions{})
	count := kit.NewLabel("28", font13, kit.TextOptions{})
	head := box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignBaseline, ColumnGap: kit.Px(8)}, title, count)
	_, _ = open(t, box(kit.Style{}, head), kit.Size{W: 600, H: 100}, env100)
	base := func(l *kit.Label) kit.DIP {
		return l.WindowBounds().Min.Y + l.TextLayout().Metrics().Baseline
	}
	if b1, b2 := base(title), base(count); !approx(b1, b2) {
		t.Errorf("baselines %v and %v differ", b1, b2)
	}
	if title.Bounds().Min.Y != 0 || count.Bounds().Min.Y <= 0 {
		t.Errorf("the taller title sits at the top: title %v, count %v", title.Bounds(), count.Bounds())
	}
}

// TestLabelShrinkRules checks CSS's automatic minimum for text: a label that clips
// (overflow: hidden) shrinks below its text, one that does not keeps its text's width.
func TestLabelShrinkRules(t *testing.T) {
	text := "a-long-unbreakable-file-name.iso"
	clips := kit.NewLabel(text, font13, kit.TextOptions{Truncate: kit.TruncateEnd})
	clips.Style.Overflow = kit.OverflowHidden
	keeps := kit.NewLabel(text, font13, kit.TextOptions{})
	_, _ = open(t, box(kit.Style{},
		box(kit.Style{Display: kit.DisplayFlex, Width: kit.Px(100)}, clips),
		box(kit.Style{Display: kit.DisplayFlex, Width: kit.Px(100)}, keeps),
	), kit.Size{W: 400, H: 100}, env100)
	full := kittest.Advance(text, font13.Style(env100.Scale))
	if got := clips.Bounds().Width(); got != 100 {
		t.Errorf("the clipping label is %v wide, want the row's 100", got)
	}
	if got := keeps.Bounds().Width(); !approx(got, full) {
		t.Errorf("the label without overflow hidden is %v wide, want its text's %v", got, full)
	}
}

// TestRemFollowsTextSize checks that rem text grows with the Windows text size and keeps
// the layouts it holds to the ones in use.
func TestRemFollowsTextSize(t *testing.T) {
	l := kit.NewLabel("Settings", font13, kit.TextOptions{})
	_, loop := open(t, box(kit.Style{}, box(kit.Style{Display: kit.DisplayFlex}, l)), kit.Size{W: 400, H: 100}, env100)
	w16 := l.Bounds().Width()
	env := env100
	env.Scale.Text = 1.25 // 20 px text
	loop.Send(kit.EnvironmentEvent{Env: env})
	loop.Settle(5)
	if got := l.Bounds().Width(); !approx(got, w16*1.25) {
		t.Errorf("at 20 px text the label is %v wide, want %v", got, w16*1.25)
	}
	if loop.Host.Res.Live != 1 {
		t.Errorf("%d live layouts for one label", loop.Host.Res.Live)
	}
}
