package transport

import (
	"net/http"
	"testing"
	"time"
)

func TestParseContentRange(t *testing.T) {
	valid := []struct {
		in   string
		want ContentRange
	}{
		{"bytes 0-499/1234", ContentRange{0, 499, 1234}},
		{"bytes 500-999/1234", ContentRange{500, 999, 1234}},
		{"bytes 1233-1233/1234", ContentRange{1233, 1233, 1234}},
		{"bytes 0-499/*", ContentRange{0, 499, -1}},
		{"bytes */1234", ContentRange{-1, -1, 1234}},
		{"bytes */0", ContentRange{-1, -1, 0}},
		{"BYTES 0-0/1", ContentRange{0, 0, 1}},
		{"  bytes\t0-9/10  ", ContentRange{0, 9, 10}},
		{"bytes 0 - 9 / 10", ContentRange{0, 9, 10}},
		{"bytes=0-9/10", ContentRange{0, 9, 10}},
		{"bytes 0-9223372036854775806/9223372036854775807", ContentRange{0, 9223372036854775806, 9223372036854775807}},
	}
	for _, tc := range valid {
		got, err := ParseContentRange(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseContentRange(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
	invalid := []string{
		"", "bytes", "bytes ", "bytes0-9/10", "items 0-9/10", "bytes 0-9", "bytes 9-0/10",
		"bytes 0-10/10", "bytes 0-9/abc", "bytes -1-9/10", "bytes +0-9/10", "bytes 0-+9/10",
		"bytes 0-9/-10", "bytes */*", "bytes */", "bytes 0-/10", "bytes -9/10", "bytes 0x0-9/10",
		"bytes 0-9223372036854775808/*", "bytes 0-9/10/11", "bytes 0-9,20-29/100",
	}
	for _, in := range invalid {
		if got, err := ParseContentRange(in); err == nil {
			t.Errorf("ParseContentRange(%q) = %+v, want error", in, got)
		}
	}
}

func TestResponseContentRange(t *testing.T) {
	tests := []struct {
		status int
		value  string
		want   *ContentRange
	}{
		{http.StatusPartialContent, "bytes 0-9/10", &ContentRange{0, 9, 10}},
		{http.StatusPartialContent, "bytes */10", nil},
		{http.StatusRequestedRangeNotSatisfiable, "bytes */10", &ContentRange{-1, -1, 10}},
		{http.StatusRequestedRangeNotSatisfiable, "bytes 0-9/10", nil},
		{http.StatusOK, "bytes 0-9/10", nil},
		{http.StatusPartialContent, "garbage", nil},
		{http.StatusPartialContent, "", nil},
	}
	for _, tc := range tests {
		got := responseContentRange(tc.status, tc.value)
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("responseContentRange(%d, %q) = %+v, want %+v", tc.status, tc.value, got, tc.want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"5", 5 * time.Second},
		{" 7 ", 7 * time.Second},
		{"3600", time.Hour},
		{"3601", MaxRetryAfter},
		{"999999", MaxRetryAfter},
		{"99999999999999999999999", MaxRetryAfter},
		{"-1", 0},
		{"1.5", 0},
		{"+5", 0},
		{"soon", 0},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{now.Add(-time.Second).Format(http.TimeFormat), 0},
		{now.Add(48 * time.Hour).Format(http.TimeFormat), MaxRetryAfter},
		{now.Add(time.Minute).Format(time.RFC850), time.Minute},
		{now.Add(2 * time.Minute).Format(time.ANSIC), 2 * time.Minute},
	}
	for _, tc := range tests {
		if got := ParseRetryAfter(tc.in, now); got != tc.want {
			t.Errorf("ParseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
