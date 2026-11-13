package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowStateRoundTrip(t *testing.T) {
	path := WindowStatePath(filepath.Join(t.TempDir(), "Downex", ThemeFileName))
	if filepath.Base(path) != "ui-window.json" {
		t.Fatalf("window state file %q, want today's ui-window.json", path)
	}
	if _, ok := LoadWindowState(path); ok {
		t.Fatal("a missing file reads as a state")
	}
	want := WindowState{Width: 1840, Height: 1040, Maximised: true}
	if err := SaveWindowState(path, want); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadWindowState(path)
	if !ok || got != want {
		t.Fatalf("loaded %+v, %v; want %+v", got, ok, want)
	}
	// Today's window reads the same JSON.
	if b, err := os.ReadFile(path); err != nil || string(b) != "{\"width\":1840,\"height\":1040,\"maximised\":true}\n" {
		t.Errorf("file = %q, %v", b, err)
	}
	// A preview keeps a file of its own wherever it says.
	own := filepath.Join(t.TempDir(), "ui-window-native-preview.json")
	if err := SaveWindowState(own, WindowState{Width: 1000, Height: 700}); err != nil {
		t.Fatal(err)
	}
	if got, ok := LoadWindowState(path); !ok || got != want {
		t.Errorf("the product's file changed: %+v, %v", got, ok)
	}
}

func TestWindowStateRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"malformed": `{"width":`,
		"zero":      `{"width":0,"height":700}`,
		"negative":  `{"width":1200,"height":-1}`,
		"huge":      `{"width":99999,"height":700}`,
		"oversized": `{"width":1200,"height":700}` + string(make([]byte, maxWindowStateFile)),
	} {
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if s, ok := LoadWindowState(path); ok {
			t.Errorf("%s: read as %+v", name, s)
		}
	}
	if err := SaveWindowState(filepath.Join(dir, "w.json"), WindowState{}); err == nil {
		t.Error("saved an empty size")
	}
	if err := SaveWindowState("", WindowState{Width: 1, Height: 1}); err == nil {
		t.Error("saved without a path")
	}
	if WindowStatePath("") != "" {
		t.Error("a path without a theme folder")
	}
	if _, ok := LoadWindowState(""); ok {
		t.Error("no path reads as a state")
	}
}

func TestWindowStateStartSizeKeepsTheMinimum(t *testing.T) {
	w, h := WindowState{Width: 500, Height: 900}.StartSize(MainMinWidth, MainMinHeight)
	if w != 900 || h != 900 {
		t.Fatalf("start size %dx%d, want 900x900", w, h)
	}
}

func TestClosingState(t *testing.T) {
	prev := WindowState{Width: 1500, Height: 950}
	cases := []struct {
		name                 string
		prev                 WindowState
		hasPrev              bool
		maximised, minimised bool
		want                 WindowState
	}{
		{"normal stores its size", prev, true, false, false, WindowState{Width: 1840, Height: 1040}},
		{"maximised keeps the last normal size", prev, true, true, false, WindowState{Width: 1500, Height: 950, Maximised: true}},
		{"maximised without a stored size keeps the default", WindowState{}, false, true, false, WindowState{Width: 1200, Height: 760, Maximised: true}},
		{"minimised keeps what was stored", WindowState{Width: 1500, Height: 950, Maximised: true}, true, false, true, WindowState{Width: 1500, Height: 950, Maximised: true}},
	}
	for _, c := range cases {
		got := ClosingState(c.prev, c.hasPrev, MainWidth, MainHeight, 1840, 1040, c.maximised, c.minimised)
		if got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// What a closing window stores from its placement: its restored size in whole DIPs (the
// last normal size while maximised or minimised, which GetWindowPlacement keeps), never
// below the minimum, and maximised when it is or when it was before it was minimised.
func TestPlacedState(t *testing.T) {
	for _, c := range []struct {
		name      string
		w, h      float32
		maximised bool
		want      WindowState
	}{
		{"normal", 1840, 1040, false, WindowState{Width: 1840, Height: 1040}},
		{"125%: 1251 x 813 px", 1251 / 1.25, 813 / 1.25, false, WindowState{Width: 1001, Height: 650}},
		{"maximised keeps the normal size", 1500, 950, true, WindowState{Width: 1500, Height: 950, Maximised: true}},
		{"never below the minimum", 640, 480, false, WindowState{Width: 900, Height: 600}},
	} {
		if got := PlacedState(c.w, c.h, c.maximised, MainMinWidth, MainMinHeight); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}
