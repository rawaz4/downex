package validate

import (
	"strings"
	"testing"
)

func TestTitle(t *testing.T) {
	for in, want := range map[string]string{
		"":                                 "",
		"  Big Buck Bunny  ":               "Big Buck Bunny",
		"Line one\nline\ttwo\r\n":          "Line one line two",
		"Ünïcode – 映画":                     "Ünïcode – 映画",
		"nul\x00and\x7fdel\u0085next":      "nul and del next",
		strings.Repeat("é", MaxTitleRunes): strings.Repeat("é", MaxTitleRunes),
	} {
		if got, err := Title(in); err != nil || got != want {
			t.Errorf("Title(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{strings.Repeat("a", MaxTitleRunes+1), "bad \xff utf-8"} {
		if got, err := Title(in); err == nil {
			t.Errorf("Title(%q) = %q, want an error", in, got)
		}
	}
}

func TestTrackID(t *testing.T) {
	for _, id := range []string{"", "v0", "r12", "video=2500000", "1080p ü", strings.Repeat("x", MaxTrackIDBytes)} {
		if err := TrackID(id); err != nil {
			t.Errorf("TrackID(%q) = %v", id, err)
		}
	}
	for _, id := range []string{"a\nb", "tab\there", "\xff", strings.Repeat("x", MaxTrackIDBytes+1)} {
		if err := TrackID(id); err == nil {
			t.Errorf("TrackID(%q) accepted", id)
		}
	}
}
