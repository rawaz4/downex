//go:build windows

package win

import (
	"strings"
	"testing"

	"downex/ui/kit"
)

// A wrapping paragraph never breaks a line after a "/" that a word follows, as Chromium's
// line breaking does not: Settings > Video sites' Licences paragraph moves
// "github.com/denoland/deno." to the next line whole, where DirectWrite alone broke it as
// "…from github.com/" | "denoland/deno.". Positions still map to the text as given.
func TestWrapKeepsAddressesWhole(t *testing.T) {
	r := newRes(t)
	const text = "yt-dlp (Unlicense; the Windows/macOS/Linux executables are GPLv3+) is downloaded from github.com/yt-dlp; Deno (MIT) from github.com/denoland/deno. Downex does not include either."
	st := kit.TextStyle{Family: "'Segoe UI Variable Text', 'Segoe UI', system-ui", Size: 12, LineHeight: 1.5}
	addr := strings.LastIndex(text, "github.com/denoland")
	after := addr + len("github.com/")
	// a box that ends just after "…from github.com/": the slash's line would end there
	one, err := r.LayoutText(text, st, kit.TextOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer one.Release()
	edge, _ := one.Caret(after)
	for _, wrap := range []kit.TextWrap{kit.TextWrapWords, kit.TextWrapAnywhere} {
		l, err := r.LayoutText(text, st, kit.TextOptions{MaxWidth: edge.X + 2, Wrap: wrap})
		if err != nil {
			t.Fatal(err)
		}
		at, _ := l.Caret(addr)
		in, _ := l.Caret(after)
		end, _ := l.Caret(strings.LastIndex(text, "deno.") + len("deno."))
		if at.Y == 0 || in.Y != at.Y || end.Y != at.Y {
			t.Errorf("wrap %d: \"github.com/\" on the line at %.1f, \"denoland\" at %.1f, \"deno.\" ending at %.1f: the address is broken", wrap, at.Y, in.Y, end.Y)
		}
		if in.X <= at.X {
			t.Errorf("wrap %d: \"denoland\" at x %.1f, before its \"github.com/\" at %.1f", wrap, in.X, at.X)
		}
		// the joiners are the layout's: hits and ranges are positions in the text as given
		if h := l.HitTest(kit.Pt(in.X+0.5, in.Y+2)); h.Pos != after && h.Pos != after+1 {
			t.Errorf("wrap %d: a hit on \"denoland\"'s d gives %d, want %d", wrap, h.Pos, after)
		}
		if rs := l.AppendRangeRects(nil, addr, after); len(rs) != 1 || rs[0].Width() <= 0 {
			t.Errorf("wrap %d: \"github.com/\" covered by %v", wrap, rs)
		}
		l.Release()
	}
	// one line, nothing to break: the text is laid out as given (no joiners)
	if tl := one.(*textLayout); len(tl.glued) != 0 || tl.shownN16 != tl.n16 {
		t.Errorf("an unbounded layout holds %d joiners", len(tl.glued))
	}
	// white space after the slash still breaks there
	l, err := r.LayoutText("see https://example.com/ "+strings.Repeat("x", 40), st, kit.TextOptions{MaxWidth: 160, Wrap: kit.TextWrapWords})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if tl := l.(*textLayout); len(tl.glued) != 2 {
		t.Errorf("joiners at %v, want after the two slashes of https:// only", tl.glued)
	}
}
