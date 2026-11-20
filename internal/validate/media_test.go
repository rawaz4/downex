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

func TestEntryID(t *testing.T) {
	for _, id := range []string{"", "CarouselItem1", "2026/01/27 ü", strings.Repeat("x", MaxEntryIDBytes)} {
		if err := EntryID(id); err != nil {
			t.Errorf("EntryID(%q) = %v", id, err)
		}
	}
	for _, id := range []string{"a\nb", "del\x7f", "\xff", strings.Repeat("x", MaxEntryIDBytes+1)} {
		if err := EntryID(id); err == nil {
			t.Errorf("EntryID(%q) accepted", id)
		}
	}
}

func TestClipMs(t *testing.T) {
	for _, c := range []struct {
		start, end int64
		want       string
	}{
		{0, 0, ""},
		{80_000, 165_000, ""},
		{80_000, 0, ""},
		{0, MaxClipMs, ""},
		{-1, 1000, "times must be between 0 and 168 hours"},
		{0, MaxClipMs + 1, "times must be between 0 and 168 hours"},
		{5000, 5000, "the part must start before it ends"},
		{6000, 5000, "the part must start before it ends"},
		{5000, 5999, "the part must be at least 1 second long"},
	} {
		err := ClipMs(c.start, c.end)
		if got := ""; err != nil {
			got = err.Error()
			if got != c.want {
				t.Errorf("ClipMs(%d, %d) = %q, want %q", c.start, c.end, got, c.want)
			}
		} else if c.want != "" {
			t.Errorf("ClipMs(%d, %d) accepted, want %q", c.start, c.end, c.want)
		}
	}
}

func TestChapters(t *testing.T) {
	long := strings.Repeat("é", 150) // 300 bytes
	got := Chapters([]Chapter{
		{StartMs: 80_000, EndMs: 165_000, Title: "Two\nlines"},
		{StartMs: 0, EndMs: 80_000, Title: long},
		{StartMs: 100_000, EndMs: 120_000, Title: "overlaps"},
		{StartMs: 165_000, EndMs: 165_000, Title: "empty"},
		{StartMs: -1, EndMs: 10, Title: "negative"},
		{StartMs: 165_000, EndMs: 755_500, Title: "to the end"}, // within 1 s of the end
		{StartMs: 700_000, EndMs: 800_000, Title: "past the end"},
	}, 754_600)
	want := []string{strings.Repeat("é", 100), "Two lines", "to the end"}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, c := range got {
		if c.Title != want[i] {
			t.Errorf("chapter %d: %q, want %q", i, c.Title, want[i])
		}
	}
	if Chapters(make([]Chapter, MaxChapters+1), 0) != nil {
		t.Error("too many chapters were kept")
	}
}
