package display_test

import (
	"testing"

	"downex/ui/core/display"
)

// display.ts has no vitest file of its own: its tables are the goldens' cases (golden_test.go,
// every input the window's tests gave it) and these, from text.test.ts and progress.test.ts.

var rlo = string(rune(0x202e))

func TestJoinPathEscapesForDisplay(t *testing.T) {
	// text.test.ts: escapes paths shown in the details panel
	for _, c := range [][3]string{
		{"/home/you/Downloads", "invoice\u202efdp.exe", "/home/you/Downloads/invoice⟨U+202E⟩fdp.exe"},
		{`C:\Users\` + rlo + "x", "", `C:\Users\⟨U+202E⟩x`},
		// progress.test.ts
		{"/d", "x\u202egpj.exe", "/d/x⟨U+202E⟩gpj.exe"},
	} {
		if got := display.JoinPath(c[0], c[1]); got != c[2] {
			t.Errorf("JoinPath(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestRawJoinPathUsesTheFoldersSeparator(t *testing.T) {
	// progress.test.ts: joins the saved path with the folder's separator, escaped only for display
	for _, c := range [][3]string{
		{`C:\Users\you\Downloads`, "a.zip", `C:\Users\you\Downloads\a.zip`},
		{"/home/you/", "a.zip", "/home/you/a.zip"},
		{"/home/you", "", "/home/you"},
		{"/d", "x\u202egpj.exe", "/d/x\u202egpj.exe"},
	} {
		if got := display.RawJoinPath(c[0], c[1]); got != c[2] {
			t.Errorf("RawJoinPath(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}
