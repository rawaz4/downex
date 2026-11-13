package media

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestParseAttrList(t *testing.T) {
	cases := []struct {
		in   string
		want []attr
	}{
		{`BANDWIDTH=1280000,AVERAGE-BANDWIDTH=1000000`, []attr{{"BANDWIDTH", "1280000", false}, {"AVERAGE-BANDWIDTH", "1000000", false}}},
		// Quoted strings keep commas and equals signs.
		{`CODECS="avc1.4d401f,mp4a.40.2",NAME="a=b"`, []attr{{"CODECS", "avc1.4d401f,mp4a.40.2", true}, {"NAME", "a=b", true}}},
		{`URI=""`, []attr{{"URI", "", true}}},
		{`RESOLUTION=1920x1080,FRAME-RATE=59.940,IV=0x0A0b`, []attr{{"RESOLUTION", "1920x1080", false}, {"FRAME-RATE", "59.940", false}, {"IV", "0x0A0b", false}}},
		{`METHOD=NONE`, []attr{{"METHOD", "NONE", false}}},
		// Spaces around pairs and a trailing comma are tolerated.
		{` TYPE=AUDIO , GROUP-ID="g", `, []attr{{"TYPE", "AUDIO", false}, {"GROUP-ID", "g", true}}},
		{``, nil},
	}
	for _, c := range cases {
		got, err := parseAttrList(c.in)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(attrList(c.want)) {
			t.Errorf("parseAttrList(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{
		`=1`, `A`, `A=`, `A=,B=1`, `a=1`, `A=1,A=2`, `A="x`, `A=x"y"`, `A=1 B=2`, `A="x"B=1`, `A=1,,B=2`, `A_B=1`, `A=1 2`,
	} {
		if _, err := parseAttrList(in); !errors.Is(err, ErrMalformed) {
			t.Errorf("parseAttrList(%q) error = %v, want ErrMalformed", in, err)
		}
	}
}

func TestAttributeValueTypes(t *testing.T) {
	if v, err := parseDecimalInt("18446744073709551615"); err != nil || v != 1<<64-1 {
		t.Errorf("max decimal-integer = %d, %v", v, err)
	}
	for _, s := range []string{"", "-1", "+1", "1.0", "18446744073709551616", "1_000", "0x10", " 1"} {
		if _, err := parseDecimalInt(s); err == nil {
			t.Errorf("parseDecimalInt(%q) accepted", s)
		}
	}
	for s, want := range map[string]float64{"29.970": 29.97, "60": 60, "0.5": 0.5, "10.": 10, ".25": 0.25} {
		if v, err := parseDecimalFloat(s); err != nil || v != want {
			t.Errorf("parseDecimalFloat(%q) = %v, %v", s, v, err)
		}
	}
	for _, s := range []string{"", ".", "-1.5", "1e3", "inf", "NaN", "0x1p3", "1.2.3", "1,5"} {
		if _, err := parseDecimalFloat(s); err == nil {
			t.Errorf("parseDecimalFloat(%q) accepted", s)
		}
	}
	for s, want := range map[string]time.Duration{
		"10": 10 * time.Second, "9.009": 9009 * time.Millisecond, "0.0000000019": 1, "0": 0, "9223372036.854775807": maxDuration,
	} {
		if v, err := parseSeconds(s); err != nil || v != want {
			t.Errorf("parseSeconds(%q) = %v, %v; want %v", s, v, err, want)
		}
	}
	for _, s := range []string{"9223372036.854775808", "9223372037", "-1", "1e1"} {
		if _, err := parseSeconds(s); err == nil {
			t.Errorf("parseSeconds(%q) accepted", s)
		}
	}
	if w, h, err := parseResolution("3840x2160"); err != nil || w != 3840 || h != 2160 {
		t.Errorf("parseResolution = %d, %d, %v", w, h, err)
	}
	for _, s := range []string{"1920X1080", "1920x", "x1080", "1920x1080x1", "99999999999x1", "-1x1"} {
		if _, _, err := parseResolution(s); err == nil {
			t.Errorf("parseResolution(%q) accepted", s)
		}
	}
	for s, want := range map[string]bool{"0x0": true, "0XfF": true, "0x": false, "00": false, "0xg": false, "x0": false} {
		if got := isHexSequence(s); got != want {
			t.Errorf("isHexSequence(%q) = %v", s, got)
		}
	}
	if r, err := parseDASHRange(" 0-861 "); err != nil || *r != [2]int64{0, 861} {
		t.Errorf("parseDASHRange = %v, %v", r, err)
	}
	for _, s := range []string{"", "5", "5-", "-5", "5-4", "a-b", "0-9223372036854775808", "1-2-3"} {
		if _, err := parseDASHRange(s); err == nil {
			t.Errorf("parseDASHRange(%q) accepted", s)
		}
	}
	if r, err := inclusiveRange(1, 9223372036854775806); err != nil || r[1] != 9223372036854775806 {
		t.Errorf("inclusiveRange at the edge = %v, %v", r, err)
	}
	if _, err := inclusiveRange(2, 9223372036854775807); err == nil {
		t.Error("inclusiveRange overflow accepted")
	}
}
