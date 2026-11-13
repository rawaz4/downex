package mac

import (
	"testing"

	"downex/ui/kit"
)

func TestFldValue(t *testing.T) {
	for _, c := range []struct {
		in    string
		multi bool
		want  string
	}{
		{"budget-plan.xlsx", false, "budget-plan.xlsx"},
		{"a\r\nb\nc\rd", false, "abcd"},
		{"a\r\nb\nc\rd", true, "a\nb\nc\nd"},
		{"a\x00b", false, "ab"},
		{"a\x00\r\n", true, "a\n"},
		{"\r\r\n", true, "\n\n"},
		// Invalid UTF-8 never reaches AppKit: each bad byte is U+FFFD, as a UTF-16 conversion
		// (Windows' field) and a JSON round trip (today's WebKit field) make it.
		{"bad\xffname.bin", false, "bad\ufffdname.bin"},
		{"\xed\xa0\x80x", true, "\ufffd\ufffd\ufffdx"}, // an encoded lone surrogate
		{"\xff\r\n\xfe", true, "\ufffd\n\ufffd"},
	} {
		if got := fldValue(c.in, c.multi); got != c.want {
			t.Errorf("fldValue(%q, %v) = %q, want %q", c.in, c.multi, got, c.want)
		}
	}
}

func TestFldUTF8(t *testing.T) {
	for in, want := range map[string]string{"": "", "File name": "File name", "a\xffb\xfe": "a\ufffdb\ufffd", "日本": "日本"} {
		if got := fldUTF8(in); got != want {
			t.Errorf("fldUTF8(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFldBaseline(t *testing.T) {
	// A 13 px SF Pro field (ascent 12, descent 4 as the text engine rounds them) in an 18 px
	// tall text area at y 10: (18 - 16) / 2 + 12 = 13 below the top.
	if got := fldBaseline(kit.R(0, 10, 100, 28), 12, 4, 2); got != 23 {
		t.Errorf("baseline %v, want 23", got)
	}
	// Half pixels go to the nearest device pixel: at 1x 22.25 is 22, at 2x 22.5.
	r := kit.R(0, 10, 100, 28.5)
	if got := fldBaseline(r, 12, 4, 1); got != 23 {
		t.Errorf("1x baseline %v, want 23", got)
	}
	if got := fldBaseline(r, 12, 4, 2); got != 23.5 {
		t.Errorf("2x baseline %v, want 23.5", got)
	}
}

func TestFldAlign(t *testing.T) {
	o := kit.TextFieldOptions{}
	if fldAlign(o, kit.DirLTR) != fldAlignLeft || fldAlign(o, kit.DirRTL) != fldAlignRight {
		t.Error("start follows the direction")
	}
	o.Align = kit.TextAlignEnd
	if fldAlign(o, kit.DirLTR) != fldAlignRight || fldAlign(o, kit.DirRTL) != fldAlignLeft {
		t.Error("end follows the direction")
	}
	o.Align = kit.TextAlignCenter
	if fldAlign(o, kit.DirRTL) != fldAlignCenter {
		t.Error("centre")
	}
}

func TestFldRoundAndShown(t *testing.T) {
	if got := fldRound(kit.R(10.2, 20.3, 50.74, 40.26), 2); got != kit.R(10, 20.5, 50.5, 40.5) {
		t.Errorf("round %v", got)
	}
	if fldShown(kit.Rect{}) || !fldShown(kit.R(0, 0, 1, 1)) {
		t.Error("shown")
	}
	nan := float32(0)
	nan = nan / nan
	if fldShown(kit.R(0, 0, nan, 1)) {
		t.Error("NaN shows")
	}
}
