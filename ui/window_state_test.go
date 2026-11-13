package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowStateRoundTrip(t *testing.T) {
	path := windowStatePath(filepath.Join(t.TempDir(), "Downex", themeFileName))
	if _, ok := loadWindowState(path); ok {
		t.Fatal("a missing file reads as a state")
	}
	want := windowState{Width: 1840, Height: 1040, Maximised: true}
	if err := saveWindowState(path, want); err != nil {
		t.Fatal(err)
	}
	got, ok := loadWindowState(path)
	if !ok || got != want {
		t.Fatalf("loaded %+v, %v; want %+v", got, ok, want)
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
		if s, ok := loadWindowState(path); ok {
			t.Errorf("%s: read as %+v", name, s)
		}
	}
	if err := saveWindowState(filepath.Join(dir, "w.json"), windowState{}); err == nil {
		t.Error("saved an empty size")
	}
	if windowStatePath("") != "" {
		t.Error("a path without a theme folder")
	}
}

func TestWindowStateStartSizeKeepsTheMinimum(t *testing.T) {
	w, h := windowState{Width: 500, Height: 900}.startSize(900, 600)
	if w != 900 || h != 900 {
		t.Fatalf("start size %dx%d, want 900x900", w, h)
	}
}

func TestClosingState(t *testing.T) {
	prev := windowState{Width: 1500, Height: 950}
	cases := []struct {
		name                 string
		prev                 windowState
		hasPrev              bool
		maximised, minimised bool
		want                 windowState
	}{
		{"normal stores its size", prev, true, false, false, windowState{Width: 1840, Height: 1040}},
		{"maximised keeps the last normal size", prev, true, true, false, windowState{Width: 1500, Height: 950, Maximised: true}},
		{"maximised without a stored size keeps the default", windowState{}, false, true, false, windowState{Width: 1200, Height: 760, Maximised: true}},
		{"minimised keeps what was stored", windowState{Width: 1500, Height: 950, Maximised: true}, true, false, true, windowState{Width: 1500, Height: 950, Maximised: true}},
	}
	for _, c := range cases {
		got := closingState(c.prev, c.hasPrev, 1200, 760, 1840, 1040, c.maximised, c.minimised)
		if got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}
