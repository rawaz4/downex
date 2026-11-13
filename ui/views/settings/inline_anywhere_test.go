package settings

// [mac webkit] overflow-wrap: anywhere under WebKit's look; the Chromium look keeps the word.

import (
	"strings"
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
	"downex/ui/kit/kittest"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

func layOutInline(t *testing.T, webkit bool, width kit.DIP, spans ...span) *inline {
	t.Helper()
	in := newInline(fontSteps, 1.5, spans...)
	in.Style.Width = kit.Px(width)
	choice := &widgets.Choice{Choice: tokens.ChoiceLight}
	root := widgets.NewScope(choice, in)
	root.Style = kit.Style{Display: kit.DisplayBlock}
	clock := kittest.NewClock()
	win := kit.NewWindow(root, kit.WindowOptions{Title: "t", Palette: func(env kit.Environment) kit.Palette { return choice.Tokens(env).Palette() }, Now: clock.Now, Scheduler: clock})
	loop, _ := hostcaptest.NewLoop(win, clock, kit.Size{W: 600, H: 400}, kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1, Text: 1}, WebKit: webkit})
	loop.Settle(30)
	return in
}

func TestInlineOverflowWrapAnywhereWebKit(t *testing.T) {
	path := "Downex.app/Contents/Resources/extension/chrome-mv3"
	spans := []span{
		{text: "pick the ", font: fontSteps, ink: views.InkInk2},
		{text: path, font: fontMono, ink: views.InkInk, code: true},
		{text: " folder that came with Downex.", font: fontSteps, ink: views.InkInk2},
	}
	const w = 150
	// Chromium: the code word before the hyphen stays whole on its own line and overflows
	in := layOutInline(t, false, w, spans...)
	box := in.WindowBounds()
	over := false
	for _, p := range in.pieces {
		if strings.Contains(p.text, "/") && p.x+p.w > box.Max.X+0.01 {
			over = true
		}
	}
	if !over || len(in.pieces) != len(in.src) {
		t.Fatalf("Chromium look: the word was cut or fits: %s", InlinePiecesOf(in))
	}

	if in.lineH != 18.75 {
		t.Errorf("Chromium look: line %v, want 12.5 × 1.5", in.lineH)
	}

	in = layOutInline(t, true, w, spans...)
	if in.lineH != 18 {
		t.Errorf("WebKit look: line %v, want 12.5 × 1.5 truncated to 18", in.lineH)
	}
	box = in.WindowBounds()
	var code strings.Builder
	lines := map[int]bool{}
	for _, p := range in.pieces {
		if p.x+p.w > box.Max.X+0.01 {
			t.Errorf("WebKit look: %q ends at %.2f past %.2f", p.text, p.x+p.w, box.Max.X)
		}
		if in.spans[p.span].code {
			code.WriteString(p.text)
			lines[p.line] = true
		}
	}
	if code.String() != path || len(lines) < 3 {
		t.Fatalf("WebKit look: code %q over %d lines: %s", code.String(), len(lines), InlinePiecesOf(in))
	}
	// the code starts a line (the space before it is the ordinary break), and each cut part
	// but the last fills its line to within one character
	var first piece
	for _, p := range in.pieces {
		if in.spans[p.span].code {
			first = p
			break
		}
	}
	if first.x != box.Min.X+codePad || first.line != 1 {
		t.Errorf("WebKit look: the code starts at %.2f line %d", first.x, first.line)
	}
	if !first.first || first.last {
		t.Errorf("WebKit look: first part first %v last %v", first.first, first.last)
	}
}

// InlinePiecesOf is InlinePieces for an inline.
func InlinePiecesOf(in *inline) string {
	var b strings.Builder
	for _, p := range in.pieces {
		b.WriteString("[" + p.text + "] ")
	}
	return b.String()
}
