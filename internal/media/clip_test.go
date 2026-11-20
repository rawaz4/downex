package media

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func ms(n int64) time.Duration { return time.Duration(n) * time.Millisecond }

func TestParseClipTime(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"80":        80 * time.Second,
		"0":         0,
		" 1:20 ":    80 * time.Second,
		"01:20":     80 * time.Second,
		"80:00":     80 * time.Minute,
		"1:02:03":   time.Hour + 2*time.Minute + 3*time.Second,
		"1:20.5":    ms(80500),
		"1:20.25":   ms(80250),
		"1:20.125":  ms(80125),
		"12.5":      ms(12500),
		"1m20s":     80 * time.Second,
		"1h2m3s":    time.Hour + 2*time.Minute + 3*time.Second,
		"2h":        2 * time.Hour,
		"45s":       45 * time.Second,
		"1M20S":     80 * time.Second,
		"1m20.5s":   ms(80500),
		"168:00:00": MaxClip,
	} {
		if got, err := ParseClipTime(in); err != nil || got != want {
			t.Errorf("ParseClipTime(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "  ", "abc", "1:2", "1:60", "1:02:3", "1:60:00", "1:20.5000", "1:20.", "-5", "1::20",
		"1:02:03:04", "168:00:01", "1s2m", "1m1m", "m20s", "1h60m", "1x", "1:2a", "+1",
		strings.Repeat("9", 40),
	} {
		if got, err := ParseClipTime(in); err == nil {
			t.Errorf("ParseClipTime(%q) = %v, want an error", in, got)
		}
	}
}

func TestFormatClipTime(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                               "0:00",
		80 * time.Second:                "1:20",
		ms(80500):                       "1:20.5",
		ms(80050):                       "1:20",
		12*time.Minute + 34*time.Second: "12:34",
		time.Hour + 2*time.Minute + 3*time.Second: "1:02:03",
		-time.Second: "0:00",
	} {
		if got := FormatClipTime(d); got != want {
			t.Errorf("FormatClipTime(%v) = %q, want %q", d, got, want)
		}
	}
	// Every shown time parses back to itself (to the tenth).
	for _, d := range []time.Duration{0, ms(80500), 3*time.Hour + ms(5_100), 59*time.Minute + 59*time.Second} {
		got, err := ParseClipTime(FormatClipTime(d))
		if err != nil || got != d {
			t.Errorf("round trip of %v: %v, %v", d, got, err)
		}
	}
	if got := FormatClipRange(Clip{Start: 80 * time.Second, End: 165 * time.Second}, 0); got != "1:20 – 2:45" {
		t.Errorf("FormatClipRange = %q", got)
	}
	if got := FormatClipRange(Clip{Start: 80 * time.Second}, 754*time.Second); got != "1:20 – 12:34" {
		t.Errorf("FormatClipRange open = %q", got)
	}
}

func TestClipValidate(t *testing.T) {
	dur := 754 * time.Second // 12:34
	for _, c := range []struct {
		clip   Clip
		dur    time.Duration
		reason string
	}{
		{Clip{}, dur, ""},
		{Clip{Start: 80 * time.Second, End: 165 * time.Second}, dur, ""},
		{Clip{Start: 80 * time.Second}, dur, ""},
		{Clip{Start: 80 * time.Second}, 0, ""},
		{Clip{Start: 10 * time.Second, End: 755 * time.Second}, dur, ""}, // within 1 s past the end
		{Clip{Start: 10 * time.Second, End: 756 * time.Second}, dur, "the part ends after the video (12:34)"},
		{Clip{Start: 165 * time.Second, End: 80 * time.Second}, dur, ClipReasonOrder},
		{Clip{Start: 80 * time.Second, End: 80 * time.Second}, dur, ClipReasonOrder},
		{Clip{Start: 80 * time.Second, End: ms(80900)}, dur, ClipReasonShort},
		{Clip{Start: -time.Second, End: time.Second}, dur, ClipReasonBounds},
		{Clip{Start: time.Second, End: MaxClip + time.Second}, 0, ClipReasonBounds},
		{Clip{Start: dur}, dur, ClipReasonOrder},
		{Clip{Start: dur - ms(500)}, dur, ClipReasonShort},
	} {
		err := c.clip.Validate(c.dur)
		var ce *ClipError
		switch {
		case c.reason == "" && err != nil:
			t.Errorf("%+v: %v", c.clip, err)
		case c.reason != "" && (!errors.As(err, &ce) || ce.Reason != c.reason || !errors.Is(err, ErrClip)):
			t.Errorf("%+v: %v, want reason %q", c.clip, err, c.reason)
		}
	}
}

func TestClipNormalize(t *testing.T) {
	dur := 754 * time.Second
	for _, c := range []struct{ in, want Clip }{
		{Clip{End: dur}, Clip{}},
		{Clip{End: dur + ms(900)}, Clip{}},
		{Clip{Start: 0, End: 0}, Clip{}},
		{Clip{Start: time.Second, End: dur}, Clip{Start: time.Second}},
		{Clip{End: dur + 2*time.Second}, Clip{End: dur + 2*time.Second}}, // refused by Validate
		{Clip{Start: time.Second, End: 9 * time.Second}, Clip{Start: time.Second, End: 9 * time.Second}},
	} {
		if got := c.in.Normalize(dur); got != c.want {
			t.Errorf("Normalize(%+v) = %+v, want %+v", c.in, got, c.want)
		}
	}
	if got := (Clip{End: 9 * time.Second}).Normalize(0); got.End != 9*time.Second {
		t.Errorf("unknown duration changed the clip: %+v", got)
	}
}

func TestClipName(t *testing.T) {
	clip := Clip{Start: 80 * time.Second, End: 165 * time.Second}
	for _, c := range []struct {
		name string
		clip Clip
		want string
	}{
		{"Talk.mp4", clip, "Talk (01m20s-02m45s).mp4"},
		{"Talk.mp4", Clip{Start: ms(80900), End: ms(164100)}, "Talk (01m20s-02m45s).mp4"}, // floor and ceil
		{"Talk.mp4", Clip{Start: time.Hour + 2*time.Minute + 3*time.Second}, "Talk (1h02m03s-end).mp4"},
		{"Talk (00m10s-00m20s).mp4", clip, "Talk (01m20s-02m45s).mp4"},
		{"Talk (1h02m03s-end).mp4", Clip{}, "Talk.mp4"},
		{"Talk (Live).mp4", clip, "Talk (Live) (01m20s-02m45s).mp4"},
		{"Talk", clip, "Talk (01m20s-02m45s)"},
		{"Talk.mp4", Clip{}, "Talk.mp4"},
		{"a.b c.mp4", clip, "a.b c (01m20s-02m45s).mp4"},
	} {
		if got := ClipName(c.name, c.clip); got != c.want {
			t.Errorf("ClipName(%q, %+v) = %q, want %q", c.name, c.clip, got, c.want)
		}
	}
	// The longest suffix fits the budget the daemon reserves.
	if s := ClipSuffix(Clip{Start: MaxClip - time.Second, End: MaxClip}); len(s) > ClipSuffixMaxBytes {
		t.Errorf("suffix %q is %d bytes, more than %d", s, len(s), ClipSuffixMaxBytes)
	}
	if got := ClipSuffix(Clip{Start: MaxClip - time.Second, End: MaxClip}); got != " (167h59m59s-168h00m00s)" {
		t.Errorf("ClipSuffix = %q", got)
	}
}
